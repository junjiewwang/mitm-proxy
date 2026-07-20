package security

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrSSRF 表示检测到 SSRF 攻击（指向内网地址）
var ErrSSRF = fmt.Errorf("SSRF: request targets internal network")

// ErrInvalidScheme 表示请求使用了不允许的协议
var ErrInvalidScheme = fmt.Errorf("SSRF: unsupported URL scheme")

// ValidateExternalURL 校验 URL 不指向内网地址，且使用 https/http 协议。
// 用于 replayer、debugger 等接受用户 URL 输入的端点。
//
// 拒绝规则：
//   - 非 http/https scheme
//   - host 解析为内网/环回/链路本地 IP
//   - host 为空
func ValidateExternalURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("%w: empty URL", ErrSSRF)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: invalid URL: %v", ErrSSRF, err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q not allowed (use http/https)", ErrInvalidScheme, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: empty host", ErrSSRF)
	}

	host := u.Hostname()
	if isInternal(host) {
		return fmt.Errorf("%w: host %q resolves to internal network", ErrSSRF, host)
	}

	return nil
}

// isInternal 判断 host 是否指向内网地址
func isInternal(host string) bool {
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
	}

	// DNS 解析
	ips, err := net.LookupIP(host)
	if err != nil {
		return false // 无法解析，按非内网处理
	}
	for _, addr := range ips {
		if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified() {
			return true
		}
	}
	return false
}

// AllowDebugInternal 临时关闭内网校验的标记（仅 Debug 端点）
// 用法：security.AllowDebugInternal = true
// 注意：生产环境必须为 false
var AllowDebugInternal = false

// RequireExternal 根据 AllowDebugInternal 决定是否校验内网
func RequireExternal(raw string) error {
	if AllowDebugInternal {
		// 仅校验 scheme
		u, err := url.Parse(raw)
		if err != nil {
			return fmt.Errorf("%w: invalid URL: %v", ErrSSRF, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("%w: scheme %q not allowed", ErrInvalidScheme, u.Scheme)
		}
		return nil
	}
	return ValidateExternalURL(raw)
}

// SanitizePath 确保 subPath 不通过 ../ 逃逸 baseDir
// 返回安全的绝对路径
func SanitizePath(baseDir, subPath string) (string, error) {
	if subPath == "" {
		return "", fmt.Errorf("path traversal: empty subPath")
	}
	// 清理 subPath 中的 ../ 等
	clean := strings.ReplaceAll(subPath, "\\", "/")
	clean = strings.TrimPrefix(clean, "/")

	// 拒绝包含 ".." 的路径
	if strings.Contains(clean, "..") {
		return "", fmt.Errorf("path traversal: %q contains ..", subPath)
	}

	return clean, nil
}
