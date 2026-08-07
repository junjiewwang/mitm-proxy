package proxy

import (
	"net/http"
	"testing"
)

// --- mock 依赖，用于测试未导出方法 ---

type fakeInterceptMatcher struct{ hit bool }

func (m *fakeInterceptMatcher) ShouldIntercept(hostname string) bool { return m.hit }

type fakeUpstreamResolver struct{ addr string; ok bool }

func (m *fakeUpstreamResolver) ResolveUpstream(hostname string) (string, bool) {
	return m.addr, m.ok
}

func TestSplitHostPort(t *testing.T) {
	if h, p := splitHostPort("example.com"); h != "example.com" || p != "" {
		t.Errorf("no port: got %q,%q", h, p)
	}
	if h, p := splitHostPort("example.com:443"); h != "example.com" || p != "443" {
		t.Errorf("with port: got %q,%q", h, p)
	}
	if h, p := splitHostPort("api.example.com:8080"); h != "api.example.com" || p != "8080" {
		t.Errorf("with non-standard port: got %q,%q", h, p)
	}
}

func TestTruncateStr(t *testing.T) {
	if s := truncateStr("short", 120); s != "short" {
		t.Errorf("short string changed: %q", s)
	}
	long := ""
	for i := 0; i < 200; i++ {
		long += "a"
	}
	trunc := truncateStr(long, 100)
	if len(trunc) != 103 {
		t.Errorf("expected 103, got %d", len(trunc))
	}
	if trunc[100:] != "..." {
		t.Errorf("should end with '...': %q", trunc)
	}
}

func TestShouldInterceptWithMatcher(t *testing.T) {
	p := &ForwardProxy{
		cfg:              &ProxyConfig{},
		interceptMatcher: &fakeInterceptMatcher{hit: true},
	}
	if !p.shouldIntercept("anything.example.com") {
		t.Error("expected intercept=true from matcher")
	}

	p.interceptMatcher = &fakeInterceptMatcher{hit: false}
	if p.shouldIntercept("anything.example.com") {
		t.Error("expected intercept=false from matcher")
	}
}

func TestShouldInterceptFallbackToList(t *testing.T) {
	p := &ForwardProxy{
		cfg: &ProxyConfig{InterceptDomains: []string{"example.com"}},
	}
	if !p.shouldIntercept("api.example.com") {
		t.Error("subdomain should be intercepted")
	}
	if !p.shouldIntercept("example.com") {
		t.Error("exact domain should be intercepted")
	}
	if p.shouldIntercept("other.com") {
		t.Error("other domain should NOT be intercepted")
	}
}

func TestGetUpstreamAddrWithResolver(t *testing.T) {
	p := &ForwardProxy{
		cfg:              &ProxyConfig{},
		upstreamResolver: &fakeUpstreamResolver{addr: "10.0.0.1:8443", ok: true},
	}
	if got := p.getUpstreamAddr("api.example.com"); got != "10.0.0.1:8443" {
		t.Errorf("expected resolver addr, got %q", got)
	}
}

func TestGetUpstreamAddrFallbackToMap(t *testing.T) {
	p := &ForwardProxy{
		cfg:              &ProxyConfig{UpstreamMap: map[string]string{"svc.local": "127.0.0.1:9000"}},
		upstreamResolver: &fakeUpstreamResolver{ok: false},
	}
	if got := p.getUpstreamAddr("svc.local"); got != "127.0.0.1:9000" {
		t.Errorf("expected map addr, got %q", got)
	}
}

func TestGetUpstreamAddrDefault(t *testing.T) {
	p := &ForwardProxy{
		cfg:              &ProxyConfig{},
		upstreamResolver: &fakeUpstreamResolver{ok: false},
	}
	if got := p.getUpstreamAddr("api.example.com"); got != "api.example.com:443" {
		t.Errorf("expected default :443, got %q", got)
	}
}

func TestBuildCORSPreflightResponse(t *testing.T) {
	p := &ForwardProxy{}
	req, _ := http.NewRequest("OPTIONS", "/x", nil)
	req.Header.Set("Origin", "https://app.example.com")
	resp := p.buildCORSPreflightResponse(req)

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Errorf("CORS origin not set: %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
	if resp.Header.Get("Access-Control-Max-Age") != "86400" {
		t.Errorf("max-age not set")
	}
}

func TestRecordTunnelNilSafe(t *testing.T) {
	p := &ForwardProxy{}
	// 不 panic 即可（traceRecorder 为 nil）
	p.recordTunnel("host")
}

func TestRecordForwardNilSafe(t *testing.T) {
	p := &ForwardProxy{}
	p.recordForward("GET", "/x", "h", 200, 0, true, "")
}
