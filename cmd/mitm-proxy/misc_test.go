package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"mitm-proxy/core/proxy"
)

// --- main.go: 配置加载 ---

func TestLoadHostsFile(t *testing.T) {
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "hosts")
	content := `10.0.0.1 api.example.com service.example.com
127.0.0.1:9000 localsvc
# inline comment 192.168.1.1 ignored.example.com
10.0.0.2 proxy://skip.example.com
2001:db8::1 ipv6host
`
	if err := os.WriteFile(hostsPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	upstream := map[string]string{}
	if err := loadHostsFile(hostsPath, upstream); err != nil {
		t.Fatalf("loadHostsFile: %v", err)
	}

	if upstream["api.example.com"] != "10.0.0.1:443" {
		t.Errorf("api.example.com = %q, want 10.0.0.1:443", upstream["api.example.com"])
	}
	if upstream["service.example.com"] != "10.0.0.1:443" {
		t.Errorf("service.example.com = %q, want 10.0.0.1:443", upstream["service.example.com"])
	}
	if upstream["localsvc"] != "127.0.0.1:9000" {
		t.Errorf("localsvc = %q, want 127.0.0.1:9000", upstream["localsvc"])
	}
	// 以 proxy:// 开头的域名应被跳过
	if _, ok := upstream["skip.example.com"]; ok {
		t.Error("skip.example.com (proxy://) should be skipped")
	}
	// IPv6 地址应被保留
	if upstream["ipv6host"] != "2001:db8::1" {
		t.Errorf("ipv6host = %q, want 2001:db8::1", upstream["ipv6host"])
	}
	// 内联注释后的域名不应被解析
	if _, ok := upstream["ignored.example.com"]; ok {
		t.Error("ignored.example.com (inline comment) should be skipped")
	}
}

func TestLoadHostsFileMissing(t *testing.T) {
	if err := loadHostsFile("/nonexistent/path/hosts", map[string]string{}); err == nil {
		t.Error("expected error for missing hosts file")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := `server:
  listen: ":9090"
proxy:
  enable: true
  cert_dir: /tmp/certs
rules:
  dir: /tmp/rules
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Server.Listen != ":9090" {
		t.Errorf("Server.Listen = %q", cfg.Server.Listen)
	}
	if cfg.Proxy.CertDir != "/tmp/certs" {
		t.Errorf("Proxy.CertDir = %q", cfg.Proxy.CertDir)
	}
	// 默认 listen 应为 :8081（未显式设置）
	if cfg.Proxy.Listen != ":8081" {
		t.Errorf("Proxy.Listen = %q, want default :8081", cfg.Proxy.Listen)
	}
	if cfg.Rules.WatchInterval != 2*time.Second {
		t.Errorf("Rules.WatchInterval = %v, want 2s default", cfg.Rules.WatchInterval)
	}
}

func TestLoadConfigWithHostsFile(t *testing.T) {
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "hosts")
	os.WriteFile(hostsPath, []byte("10.0.0.5 mapped.example.com\n"), 0644)

	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := "proxy:\n  hosts_file: " + hostsPath + "\n"
	os.WriteFile(cfgPath, []byte(yaml), 0644)

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Proxy.UpstreamMap["mapped.example.com"] != "10.0.0.5:443" {
		t.Errorf("hosts mapping not loaded: %+v", cfg.Proxy.UpstreamMap)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := loadConfig("/nonexistent/config.yaml"); err == nil {
		t.Error("expected error for missing config")
	}
}

func TestConfigProxyConfig(t *testing.T) {
	cfg := &Config{}
	cfg.Proxy.CertDir = "/tmp/c"
	cfg.Proxy.InterceptDomains = []string{"a.com"}
	cfg.Proxy.UpstreamMap = map[string]string{"x": "1.2.3.4:443"}
	pc := cfg.proxyConfig()
	if pc.CertDir != "/tmp/c" || len(pc.InterceptDomains) != 1 || pc.UpstreamMap["x"] != "1.2.3.4:443" {
		t.Errorf("proxyConfig() = %+v", pc)
	}
}

// --- debug.go: DebugCollector ---

func TestDebugCollectorRecordSnapshot(t *testing.T) {
	dc := NewDebugCollector(3)
	dc.Record(&RequestTrace{Method: "GET", Path: "/a", StatusCode: 200})
	dc.Record(&RequestTrace{Method: "POST", Path: "/b", StatusCode: 500})
	dc.Record(&RequestTrace{Method: "GET", Path: "/c", StatusCode: 200})
	dc.Record(&RequestTrace{Method: "GET", Path: "/d", StatusCode: 200}) // 超出容量，应滚动

	recent, total, errs, uptime := dc.Snapshot()
	if len(recent) != 3 {
		t.Errorf("recent len = %d, want 3 (ring buffer capped)", len(recent))
	}
	if total != 4 {
		t.Errorf("total = %d, want 4", total)
	}
	if errs != 1 {
		t.Errorf("errs = %d, want 1", errs)
	}
	if uptime < 0 {
		t.Errorf("uptime = %v, want >= 0", uptime)
	}
}

func TestDumpJSON(t *testing.T) {
	s := dumpJSON(map[string]int{"a": 1})
	var m map[string]int
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("dumpJSON not valid JSON: %v", err)
	}
	if m["a"] != 1 {
		t.Errorf("dumpJSON = %s", s)
	}
}

// --- mcp.go: 纯函数 + MCP server ---

func TestMCPHelpers(t *testing.T) {
	if !contains("hello world", "world") {
		t.Error("contains should be true")
	}
	if contains("hello", "xyz") {
		t.Error("contains should be false")
	}
	if indexOf("abcde", "cd") != 2 {
		t.Errorf("indexOf = %d, want 2", indexOf("abcde", "cd"))
	}
	if indexOf("abc", "z") != -1 {
		t.Errorf("indexOf not found should be -1")
	}
	if max(3, 7) != 7 || max(9, 2) != 9 {
		t.Error("max wrong")
	}
	if toJSON(map[string]int{"k": 1}) == "" {
		t.Error("toJSON empty")
	}
}

func newTestMCPServer() *mcpServer {
	return &mcpServer{
		collector: NewDebugCollector(10),
		connMgr:   proxy.NewConnManager(10, time.Second),
		cfg:       &Config{},
	}
}

func TestMCPServerHandleRPC(t *testing.T) {
	s := newTestMCPServer()

	// initialize
	resp := s.handleRPC(&jsonRPCRequest{Method: "initialize", ID: 1})
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}

	// tools/list
	resp = s.handleRPC(&jsonRPCRequest{Method: "tools/list", ID: 2})
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}

	// tools/call 各工具
	for _, name := range []string{"proxy_status", "proxy_requests", "proxy_errors", "proxy_connections", "proxy_curl"} {
		params, _ := json.Marshal(map[string]interface{}{"name": name, "arguments": map[string]interface{}{"limit": 5}})
		resp = s.handleRPC(&jsonRPCRequest{Method: "tools/call", ID: 3, Params: params})
		if resp.Error != nil {
			t.Errorf("tools/call %s error: %+v", name, resp.Error)
		}
	}

	// 未知方法
	resp = s.handleRPC(&jsonRPCRequest{Method: "bogus", ID: 4})
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("unknown method should error -32601, got %+v", resp.Error)
	}

	// tools/call 无效参数
	resp = s.handleRPC(&jsonRPCRequest{Method: "tools/call", ID: 5, Params: json.RawMessage("not-json")})
	if resp.Error == nil {
		t.Error("invalid params should error")
	}
}

func TestMCPServerRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerMCPRoutes(engine, newTestMCPServer())

	// JSON-RPC 请求
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	w := doRequest(engine, "POST", "/mcp", body)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /mcp = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "proxy_status") {
		t.Errorf("tools/list response missing tools: %s", w.Body.String())
	}

	// 非法 JSON
	w = doRequest(engine, "POST", "/mcp", `{bad`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /mcp (bad json) = %d, want 400", w.Code)
	}

	// SSE endpoint
	w = doRequest(engine, "GET", "/mcp/sse", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /mcp/sse = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "/mcp") {
		t.Errorf("SSE body unexpected: %s", w.Body.String())
	}
}
