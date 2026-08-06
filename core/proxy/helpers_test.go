package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestIsHopByHop(t *testing.T) {
	tests := []struct {
		header string
		expect bool
	}{
		{"Connection", true},
		{"connection", true},   // 大小写不敏感
		{"CONNECTION", true},   // 大小写不敏感
		{"Keep-Alive", true},
		{"Proxy-Connection", true},
		{"Proxy-Authenticate", true},
		{"Proxy-Authorization", true},
		{"TE", true},
		{"Trailers", true},
		{"Transfer-Encoding", true},
		{"Upgrade", true},
		{"Content-Type", false},
		{"Authorization", false}, // 与 Proxy-Authorization 区分
		{"Host", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsHopByHop(tt.header); got != tt.expect {
			t.Errorf("IsHopByHop(%q) = %v, want %v", tt.header, got, tt.expect)
		}
	}
}

func TestFilterHopByHop(t *testing.T) {
	src := http.Header{
		"Content-Type":      {"application/json"},
		"Connection":        {"keep-alive"},
		"Transfer-Encoding": {"chunked"},
		"X-Custom":          {"a", "b"}, // 多值需完整保留
		"Authorization":     {"Bearer token"},
	}

	dst := FilterHopByHop(src)

	if dst.Get("Connection") != "" {
		t.Error("Connection should be filtered out")
	}
	if dst.Get("Transfer-Encoding") != "" {
		t.Error("Transfer-Encoding should be filtered out")
	}
	if dst.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", dst.Get("Content-Type"))
	}
	if dst.Get("Authorization") != "Bearer token" {
		t.Errorf("Authorization = %q, want Bearer token", dst.Get("Authorization"))
	}
	if got := dst["X-Custom"]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("X-Custom = %v, want [a b]", got)
	}

	// 必须是新 Header，不能污染源
	dst.Set("X-New", "1")
	if src.Get("X-New") != "" {
		t.Error("FilterHopByHop must not mutate the source header")
	}
}

func TestSetCORSHeaders(t *testing.T) {
	t.Run("回显 Origin 与请求头", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodOptions, "http://example.com/api", nil)
		r.Header.Set("Origin", "https://app.example.com")
		r.Header.Set("Access-Control-Request-Headers", "X-Trace-Id")

		h := http.Header{}
		SetCORSHeaders(h, r)

		if got := h.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
			t.Errorf("Allow-Origin = %q, want the request Origin echoed back", got)
		}
		if got := h.Get("Access-Control-Allow-Headers"); got != "X-Trace-Id" {
			t.Errorf("Allow-Headers = %q, want X-Trace-Id", got)
		}
		if got := h.Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("Allow-Credentials = %q, want true", got)
		}
		if got := h.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "OPTIONS") {
			t.Errorf("Allow-Methods = %q, want it to include OPTIONS", got)
		}
	})

	t.Run("缺省时回退为通配与默认头", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "http://example.com/api", nil)

		h := http.Header{}
		SetCORSHeaders(h, r)

		if got := h.Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("Allow-Origin = %q, want * when Origin is absent", got)
		}
		if got := h.Get("Access-Control-Allow-Headers"); got != "Content-Type, X-Lid, X-Life, Authorization" {
			t.Errorf("Allow-Headers = %q, want the default header list", got)
		}
	})
}

func TestBuildErrorResponse(t *testing.T) {
	resp := BuildErrorResponse(http.StatusBadGateway, "upstream unreachable")

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	if resp.Status != http.StatusText(http.StatusBadGateway) {
		t.Errorf("Status = %q, want %q", resp.Status, http.StatusText(http.StatusBadGateway))
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	want := `{"code":-1,"message":"upstream unreachable"}`
	if string(body) != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	// ContentLength 必须与实际 body 一致，否则客户端会挂起或截断
	if resp.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d, want %d", resp.ContentLength, len(body))
	}
}
