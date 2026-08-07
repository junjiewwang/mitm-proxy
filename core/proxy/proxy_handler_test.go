package proxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestProxy(t *testing.T, configure func(*ProxyConfig)) *ForwardProxy {
	t.Helper()
	cfg := &ProxyConfig{CertDir: t.TempDir()}
	if configure != nil {
		configure(cfg)
	}
	p, err := NewForwardProxy(cfg)
	if err != nil {
		t.Fatalf("NewForwardProxy: %v", err)
	}
	return p
}

func TestNewForwardProxyInitializesClients(t *testing.T) {
	p := newTestProxy(t, nil)
	if p.httpClient == nil {
		t.Error("httpClient not initialized")
	}
	if p.pooledClient == nil {
		t.Error("pooledClient not initialized")
	}
	if p.ConnMgr == nil {
		t.Error("ConnMgr not initialized")
	}
	if p.caCert == nil {
		t.Error("caCert not loaded/generated")
	}
}

func TestNewForwardProxyBadCertDir(t *testing.T) {
	// 一个不可写的目录路径会导致 CA 生成失败
	_, err := NewForwardProxy(&ProxyConfig{CertDir: "/proc/0/certs"})
	if err == nil {
		t.Fatal("expected error for unwritable cert dir")
	}
}

func TestServeHTTPForwardsPlainHTTP(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "hit")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "upstream-body")
	}))
	defer target.Close()

	p := newTestProxy(t, nil)
	req := httptest.NewRequest(http.MethodGet, target.URL+"/api", nil)
	w := httptest.NewRecorder()

	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "upstream-body" {
		t.Errorf("body = %q, want upstream-body", w.Body.String())
	}
	if w.Header().Get("X-Upstream") != "hit" {
		t.Errorf("upstream header not proxied: %q", w.Header().Get("X-Upstream"))
	}
}

func TestServeHTTPConnectNonHijackable(t *testing.T) {
	// 启动一个本地 TCP 监听，让隧道 dial 成功，从而走到 Hijack 失败分支
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	// 通过 UpstreamMap 把目标 host 映射到该监听端口，使 dial 成功
	p := newTestProxy(t, func(c *ProxyConfig) {
		c.UpstreamMap = map[string]string{"tunnel": ln.Addr().String()}
	})
	req := httptest.NewRequest(http.MethodConnect, "https://tunnel:443", nil)
	w := httptest.NewRecorder()

	p.ServeHTTP(w, req)

	// httptest.ResponseRecorder 不支持 Hijacking：tunnel 建立会因无法 Hijack
	// 或 dial 失败而返回 5xx，而非 panic / 200。重点是覆盖 handleConnect/handleTunnel 分支。
	if w.Code < 500 {
		t.Errorf("status = %d, want >= 500 (CONNECT tunnel not hijackable in test)", w.Code)
	}
}

func TestProcessForwardToUpstream(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "secure-body")
	}))
	defer target.Close()

	// 提取 httptest TLS server 的 host:port，通过 UpstreamMap 映射到它
	hostport := strings.TrimPrefix(target.URL, "https://")
	p := newTestProxy(t, func(c *ProxyConfig) {
		c.UpstreamMap = map[string]string{"target": hostport}
	})

	req, _ := http.NewRequest(http.MethodGet, "https://target/path?q=1", nil)
	resp := p.processForward(req, "target")

	if resp == nil {
		t.Fatal("processForward returned nil")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "secure-body" {
		t.Errorf("body = %q, want secure-body", body)
	}
}

func TestProcessForwardUpstreamFailure(t *testing.T) {
	// 映射到不可达地址 → 502
	p := newTestProxy(t, func(c *ProxyConfig) {
		c.UpstreamMap = map[string]string{"dead": "127.0.0.1:1"}
	})

	req, _ := http.NewRequest(http.MethodGet, "https://dead/path", nil)
	resp := p.processForward(req, "dead")

	if resp == nil {
		t.Fatal("processForward returned nil on failure")
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestProcessForwardHopByHopHeadersFiltered(t *testing.T) {
	sawConnection := false
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 转发不应带上 hop-by-hop 头
		sawConnection = r.Header.Get("Connection") != "" || r.Header.Get("Proxy-Connection") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	hostport := strings.TrimPrefix(target.URL, "https://")
	p := newTestProxy(t, func(c *ProxyConfig) {
		c.UpstreamMap = map[string]string{"target": hostport}
	})

	req, _ := http.NewRequest(http.MethodGet, "https://target/", nil)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Proxy-Connection", "keep-alive")
	resp := p.processForward(req, "target")
	if resp == nil {
		t.Fatal("processForward returned nil")
	}
	resp.Body.Close()
	if sawConnection {
		t.Error("hop-by-hop headers were forwarded to upstream")
	}
}

// captureRecorder 记录追踪调用
type captureRecorder struct {
	tunnels  int
	forwards int
}

func (c *captureRecorder) RecordTunnel(targetHost string) { c.tunnels++ }
func (c *captureRecorder) RecordIntercept(scriptName, ruleName string, statusCode int, elapsed time.Duration) {
}
func (c *captureRecorder) RecordForward(method, path, host string, statusCode int, elapsed time.Duration, ok bool, errMsg string) {
}

func TestProxyOptions(t *testing.T) {
	// 验证 5 个 WithX 选项正确注入依赖（通过行为观察）
	rec := &captureRecorder{}
	p, err := NewForwardProxy(
		&ProxyConfig{CertDir: t.TempDir()},
		WithInterceptMatcher(&fakeInterceptMatcher{hit: true}),
		WithUpstreamResolver(&fakeUpstreamResolver{addr: "1.2.3.4:999", ok: true}),
		WithTraceRecorder(rec),
		WithRequestMatcher(nil),
		WithRequestProcessor(nil),
	)
	if err != nil {
		t.Fatalf("NewForwardProxy with options: %v", err)
	}

	if !p.shouldIntercept("anything.com") {
		t.Error("WithInterceptMatcher not applied")
	}
	if got := p.getUpstreamAddr("other.com"); got != "1.2.3.4:999" {
		t.Errorf("WithUpstreamResolver not applied: got %q", got)
	}

	// recordTunnel/recordForward 的非空分支：TraceRecorder 已注入
	p.recordTunnel("h1")
	p.recordForward("GET", "/x", "h", 200, 0, true, "")
	if rec.tunnels != 1 {
		t.Errorf("recordTunnel did not invoke injected recorder (tunnels=%d)", rec.tunnels)
	}
}
