package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyRequest(t *testing.T) {
	t.Run("转发请求并返回响应", func(t *testing.T) {
		var gotMethod, gotHeader string
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod = r.Method
			gotHeader = r.Header.Get("X-Custom")
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("X-Upstream", "yes")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte("upstream body"))
		}))
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodPost, "http://original.example.com/api", nil)
		req.Header.Set("X-Custom", "abc")

		resp := ProxyRequest(req, []byte("payload"), srv.URL, 5*time.Second)

		if gotMethod != http.MethodPost {
			t.Errorf("upstream saw method %q, want POST", gotMethod)
		}
		if gotHeader != "abc" {
			t.Errorf("upstream saw X-Custom = %q, want abc", gotHeader)
		}
		if string(gotBody) != "payload" {
			t.Errorf("upstream saw body %q, want payload", gotBody)
		}
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("StatusCode = %d, want 201", resp.StatusCode)
		}
		if got := resp.Header.Get("X-Upstream"); got != "yes" {
			t.Errorf("X-Upstream = %q, want yes", got)
		}

		body, _ := io.ReadAll(resp.Body)
		if string(body) != "upstream body" {
			t.Errorf("body = %q, want upstream body", body)
		}
		if resp.ContentLength != int64(len(body)) {
			t.Errorf("ContentLength = %d, want %d", resp.ContentLength, len(body))
		}
	})

	t.Run("过滤 hop-by-hop 请求头", func(t *testing.T) {
		var sawConnection string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawConnection = r.Header.Get("Proxy-Connection")
		}))
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodGet, "http://original.example.com/", nil)
		req.Header.Set("Proxy-Connection", "keep-alive")
		req.Header.Set("X-Keep", "kept")

		ProxyRequest(req, nil, srv.URL, 5*time.Second)

		if sawConnection != "" {
			t.Errorf("upstream saw Proxy-Connection = %q, want it stripped", sawConnection)
		}
	})

	t.Run("过滤 hop-by-hop 响应头", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Connection", "close")
			w.Header().Set("X-Keep", "kept")
			w.Write([]byte("ok"))
		}))
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodGet, "http://original.example.com/", nil)
		resp := ProxyRequest(req, nil, srv.URL, 5*time.Second)

		if got := resp.Header.Get("Connection"); got != "" {
			t.Errorf("Connection = %q, want it filtered from the response", got)
		}
		if got := resp.Header.Get("X-Keep"); got != "kept" {
			t.Errorf("X-Keep = %q, want it preserved", got)
		}
	})

	t.Run("空 body 不设置 Content-Length", func(t *testing.T) {
		var sawLength int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawLength = r.ContentLength
		}))
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodGet, "http://original.example.com/", nil)
		ProxyRequest(req, nil, srv.URL, 5*time.Second)

		if sawLength > 0 {
			t.Errorf("upstream saw Content-Length = %d, want 0 for an empty body", sawLength)
		}
	})

	t.Run("目标 URL 非法返回 502", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://original.example.com/", nil)
		resp := ProxyRequest(req, nil, "://malformed", 5*time.Second)

		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("StatusCode = %d, want 502", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "create upstream request failed") {
			t.Errorf("body = %q, want it to explain the failure", body)
		}
	})

	t.Run("上游不可达返回 502", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://original.example.com/", nil)
		resp := ProxyRequest(req, nil, "http://127.0.0.1:1", 5*time.Second)

		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("StatusCode = %d, want 502", resp.StatusCode)
		}
	})

	t.Run("超时返回 502", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
		}))
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodGet, "http://original.example.com/", nil)
		resp := ProxyRequest(req, nil, srv.URL, 20*time.Millisecond)

		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("StatusCode = %d, want 502 on timeout", resp.StatusCode)
		}
	})
}

func TestStreamRequest(t *testing.T) {
	t.Run("保持 body 为流不预读", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Error("the test server should support flushing")
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "chunk1")
			flusher.Flush()
			<-release // 保持连接打开，证明 StreamRequest 没有等 body 读完
			fmt.Fprint(w, "chunk2")
			flusher.Flush()
		}))
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodGet, "http://original.example.com/sse", nil)
		resp := StreamRequest(req, nil, srv.URL)
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
		}
		// chunked：长度未知
		if resp.ContentLength != -1 {
			t.Errorf("ContentLength = %d, want -1 for a streamed response", resp.ContentLength)
		}

		// 第一段应能在上游仍未结束时就读到
		buf := make([]byte, 6)
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			t.Fatalf("read first chunk: %v", err)
		}
		if string(buf) != "chunk1" {
			t.Errorf("first chunk = %q, want chunk1", buf)
		}

		close(release)
		rest, _ := io.ReadAll(resp.Body)
		if string(rest) != "chunk2" {
			t.Errorf("second chunk = %q, want chunk2", rest)
		}
	})

	t.Run("转发请求头并过滤 hop-by-hop", func(t *testing.T) {
		var gotCustom, gotConnection string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotCustom = r.Header.Get("X-Custom")
			gotConnection = r.Header.Get("Proxy-Connection")
			w.Write([]byte("ok"))
		}))
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodPost, "http://original.example.com/", nil)
		req.Header.Set("X-Custom", "abc")
		req.Header.Set("Proxy-Connection", "keep-alive")

		resp := StreamRequest(req, []byte("payload"), srv.URL)
		defer resp.Body.Close()

		if gotCustom != "abc" {
			t.Errorf("upstream saw X-Custom = %q, want abc", gotCustom)
		}
		if gotConnection != "" {
			t.Errorf("upstream saw Proxy-Connection = %q, want it stripped", gotConnection)
		}
	})

	t.Run("转发请求体", func(t *testing.T) {
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
		}))
		defer srv.Close()

		req, _ := http.NewRequest(http.MethodPost, "http://original.example.com/", bytes.NewReader([]byte("payload")))
		resp := StreamRequest(req, []byte("payload"), srv.URL)
		defer resp.Body.Close()

		if string(gotBody) != "payload" {
			t.Errorf("upstream saw body %q, want payload", gotBody)
		}
	})

	t.Run("目标 URL 非法返回 502", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://original.example.com/", nil)
		resp := StreamRequest(req, nil, "://malformed")

		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("StatusCode = %d, want 502", resp.StatusCode)
		}
	})

	t.Run("上游不可达返回 502", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://original.example.com/", nil)
		resp := StreamRequest(req, nil, "http://127.0.0.1:1")

		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("StatusCode = %d, want 502", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "upstream stream request failed") {
			t.Errorf("body = %q, want a stream-specific error message", body)
		}
	})
}
