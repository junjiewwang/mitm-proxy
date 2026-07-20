package security

import (
	"testing"
)

func TestValidateExternalURL(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		{"", true},
		{"https://example.com/api", false},
		{"https://console.cloud.tencent.com/cgi/capi", false},
		{"http://127.0.0.1:8322/test.js", true},    // 环回
		{"http://localhost:3000/", true},             // 环回
		{"http://10.0.0.1/", true},                   // 内网 A 类
		{"http://172.16.0.1/", true},                 // 内网 B 类
		{"http://192.168.1.1/", true},                // 内网 C 类
		{"http://169.254.1.1/", true},                // 链路本地
		{"file:///etc/passwd", true},                 // 非 http scheme
		{"gopher://evil.com", true},                  // 非 http scheme
		{"ftp://example.com", true},                  // 非 http scheme
		{"not-a-url", true},                          // 无效 URL
		{"https://[::1]:8080/", true},               // IPv6 环回
		{"https://0.0.0.0/", true},                  // 全零地址
	}
	for _, tt := range tests {
		err := ValidateExternalURL(tt.url)
		if (err != nil) != tt.wantErr {
			if tt.wantErr {
				t.Errorf("ValidateExternalURL(%q) should fail but passed", tt.url)
			} else {
				t.Errorf("ValidateExternalURL(%q) failed unexpectedly: %v", tt.url, err)
			}
		}
	}
}

func TestIsInternal(t *testing.T) {
	tests := []struct {
		host   string
		expect bool
	}{
		{"127.0.0.1", true},
		{"localhost", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"8.8.8.8", false},
		{"example.com", false},
		{"console.cloud.tencent.com", false},
	}
	for _, tt := range tests {
		got := isInternal(tt.host)
		if got != tt.expect {
			t.Errorf("isInternal(%q) = %v, want %v", tt.host, got, tt.expect)
		}
	}
}

func TestSanitizePath(t *testing.T) {
	tests := []struct {
		sub     string
		want    string
		wantErr bool
	}{
		{"apm.rules", "apm.rules", false},
		{"../etc/passwd", "", true},
		{"rules/../etc", "", true},
		{"..\\Windows\\system32", "", true}, // Windows 风格
		{"apm-dev.rules", "apm-dev.rules", false},
		{"/absolute/path", "absolute/path", false},
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := SanitizePath("/base", tt.sub)
		if tt.wantErr && err == nil {
			t.Errorf("SanitizePath(%q) should fail", tt.sub)
		}
		if !tt.wantErr && (err != nil || got != tt.want) {
			t.Errorf("SanitizePath(%q) = (%q, %v), want (%q, nil)", tt.sub, got, err, tt.want)
		}
	}
}

func TestRequireExternal_DebugMode(t *testing.T) {
	AllowDebugInternal = true
	defer func() { AllowDebugInternal = false }()

	// 内网 URL 在 debug 模式下应通过（仅校验 scheme）
	if err := RequireExternal("http://127.0.0.1:8322/test.js"); err != nil {
		t.Errorf("RequireExternal should pass internal URLs in debug mode: %v", err)
	}
	// 非 http scheme 仍应拒绝
	if err := RequireExternal("file:///etc/passwd"); err == nil {
		t.Error("RequireExternal should still reject non-http schemes in debug mode")
	}
}

// Benchmark 覆盖 URL 解析性能
func BenchmarkValidateExternalURL(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ValidateExternalURL("https://console.cloud.tencent.com/cgi/capi?action=delegate")
	}
}
