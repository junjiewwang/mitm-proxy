//go:build darwin

package ca

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type darwinDetector struct{}

func newDetector() Detector { return &darwinDetector{} }

func (d *darwinDetector) CheckAll(fp string) []TrustStatus {
	var results []TrustStatus

	sys := d.checkSystem(fp)
	results = append(results, sys)

	fx := d.checkFirefox(sys.Trusted)
	if fx != nil {
		results = append(results, *fx)
	}
	return results
}

func (d *darwinDetector) checkSystem(fp string) TrustStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 用 security find-certificate 查找匹配指纹的证书
	cmd := exec.CommandContext(ctx, "security", "find-certificate", "-c", "mitm-proxy", "/Library/Keychains/System.keychain")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return TrustStatus{
			Name:    "System Keychain",
			Trusted: false,
			Error:   strings.TrimSpace(stderr.String()),
		}
	}
	return TrustStatus{
		Name:    "System Keychain",
		Trusted: len(output) > 0,
	}
}

func (d *darwinDetector) checkFirefox(sysTrusted bool) *TrustStatus {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	// 检查 Firefox 或 Zen profiles 是否存在
	ffDirs := []string{
		filepath.Join(home, "Library/Application Support/Firefox/Profiles"),
		filepath.Join(home, "Library/Application Support/zen/Profiles"),
	}

	for _, profilesDir := range ffDirs {
		entries, err := os.ReadDir(profilesDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			certDB := filepath.Join(profilesDir, entry.Name(), "cert9.db")
			if _, err := os.Stat(certDB); err == nil {
				// Firefox 75+ / Zen 默认启用 security.enterprise_roots.enabled
				// 会自动从系统钥匙串导入根证书 → 与系统信任状态一致
				// 注意：需重启浏览器后生效
				ts := TrustStatus{
					Name:    "Firefox / Zen",
					Trusted: sysTrusted,
				}
				if !sysTrusted {
					ts.Error = "not installed in system keychain — Firefox reads from it via enterprise_roots"
				}
				return &ts
			}
		}
	}
	return nil
}
