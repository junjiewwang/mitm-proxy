package replay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeProxyAddr(t *testing.T) {
	tests := []struct{ input, want string }{
		{":8081", "127.0.0.1:8081"},
		{"127.0.0.1:8081", "127.0.0.1:8081"},
		{"0.0.0.0:8081", "0.0.0.0:8081"},
		{"localhost:8081", "localhost:8081"},
		{"8081", "8081:8081"}, // 无冒号时追加默认端口
	}
	for _, tt := range tests {
		got := normalizeProxyAddr(tt.input)
		if got != tt.want {
			t.Errorf("normalizeProxyAddr(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReplayerRequestValidation(t *testing.T) {
	// 构造一个能接收请求的临时 server 作为代理后端
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()

	// NewProxyReplayer 的地址只用于 proxy Transport，无法直接用 httptest
	// 因此这里测试 Request 校验路径（无需真实代理）
	rp, err := NewProxyReplayer("127.0.0.1:19999")
	if err != nil {
		t.Fatal(err)
	}

	// 空 URL
	_, err = rp.Replay(context.Background(), Request{Method: "GET", URL: ""})
	if err == nil {
		t.Error("expected error for empty URL, got nil")
	}

	// URL 无法连接
	_, err = rp.Replay(context.Background(), Request{Method: "GET", URL: "http://127.0.0.1:1/nonexistent"})
	if err == nil {
		t.Error("expected error for unreachable URL, got nil")
	}
}

func TestNewProxyReplayerInvalidAddr(t *testing.T) {
	// url.Parse 非常宽容，几乎没有格式会报错，这里验证极端情况下的行为
	rp, err := NewProxyReplayer("127.0.0.1:1")
	if err != nil {
		t.Fatal("expected no error for valid format")
	}
	if rp == nil {
		t.Fatal("expected non-nil replayer")
	}
	// 地址与真实端口 1 肯定不通，但构造不报错
	_, err = rp.Replay(context.Background(), Request{Method: "GET", URL: "http://127.0.0.1:2/"})
	if err == nil {
		t.Log("unexpected: request to 127.0.0.1:2 succeeded")
	}
}

// TestReplaySuccess 通过进程内正向代理覆盖 Replay 的成功路径。
// 目标 URL 使用不可解析的 .invalid 域名：security.ValidateExternalURL 对
// 无法解析的 host 视为“外部”，从而绕过 SSRF 校验；实际请求被进程内代理拦截并直接回包。
func TestReplaySuccess(t *testing.T) {
	var gotMethod, gotBody string
	var gotReplayHeader bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotMethod = r.Method
		gotBody = string(b)
		gotReplayHeader = r.Header.Get("X-Replay") == "true"
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "v") // 回显，验证响应头被透传
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"replayed":true}`)
	}))
	defer proxy.Close()

	addr := proxy.Listener.Addr().String()
	rp, err := NewProxyReplayer(addr)
	if err != nil {
		t.Fatalf("NewProxyReplayer: %v", err)
	}

	resp, err := rp.Replay(context.Background(), Request{
		Method:  "POST",
		URL:     "http://target.invalid/api",
		Headers: map[string]string{"X-Custom": "v"},
		Body:    "payload",
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if resp.Body != `{"replayed":true}` {
		t.Errorf("Body = %q, want the upstream body", resp.Body)
	}
	if resp.ContentType != "application/json" {
		t.Errorf("ContentType = %q", resp.ContentType)
	}
	if resp.Headers["X-Custom"] != "v" {
		t.Errorf("upstream headers not copied: %+v", resp.Headers)
	}
	if gotMethod != "POST" || gotBody != "payload" || !gotReplayHeader {
		t.Errorf("proxy saw method=%q body=%q replayHeader=%v", gotMethod, gotBody, gotReplayHeader)
	}
}

// TestReplayInvalidURL 覆盖 http.NewRequestWithContext 失败分支
func TestReplayInvalidURL(t *testing.T) {
	rp, err := NewProxyReplayer("127.0.0.1:1")
	if err != nil {
		t.Fatalf("NewProxyReplayer: %v", err)
	}
	// 通过代理地址构造无法解析的目标（绕过 SSRF），触发 NewRequest 失败
	_, err = rp.Replay(context.Background(), Request{
		Method: "GET",
		URL:    "http://%41%41/api", // 含非法字符的 host，NewRequest 失败
	})
	if err == nil {
		t.Fatal("expected error for malformed URL")
	}
}
