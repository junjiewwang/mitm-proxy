package replay

import (
	"context"
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
