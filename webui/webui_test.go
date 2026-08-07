package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"mitm-proxy/core/ca"
	"mitm-proxy/core/replay"
	"mitm-proxy/core/script"
	"mitm-proxy/rule"
)

// --- 测试依赖 ---

type fakeStatusProvider struct{}

func (fakeStatusProvider) GetStatus() map[string]interface{} {
	return map[string]interface{}{"mode": "test"}
}

type fakeReplayer struct{}

func (fakeReplayer) Replay(ctx context.Context, req replay.Request) (*replay.Response, error) {
	return &replay.Response{
		StatusCode:  200,
		Body:        `{"replayed":true}`,
		ContentType: "application/json",
	}, nil
}

func writeRuleFile(t *testing.T, dir, id, content string) {
	t.Helper()
	path := filepath.Join(dir, id+".rules")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

const debugScript = `@name DebugScript
@priority 10

@script onRequest
  ctx.backendURL = "http://backend.local/api";
@end

/^\\/foo/ script://self
`

func setupEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	manager := rule.NewRuleManager(
		rule.ManagerConfig{Dir: dir, ScriptLoader: script.NewLoader()},
		rule.NewRuleEngine(),
	)
	writeRuleFile(t, dir, "debugscript", debugScript)
	if err := manager.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	caDir := t.TempDir()
	caMgr, err := ca.GenerateCA(caDir, []byte("test-seed"))
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	engine := gin.New()
	RegisterRoutes(
		engine,
		manager,
		NewEventBroadcaster(10),
		fakeStatusProvider{},
		script.NewDebugger(script.NewRuntime()),
		fakeReplayer{},
		caMgr,
	)
	return engine
}

func doRequest(engine *gin.Engine, method, path string, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	return w
}

func TestRegisterRoutesUI(t *testing.T) {
	engine := setupEngine(t)

	// /ui 应 301 重定向到 /ui/
	w := doRequest(engine, "GET", "/ui", "")
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("GET /ui = %d, want 301", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/ui/" {
		t.Errorf("Location = %q, want /ui/", loc)
	}

	// /ui/ 应返回 index.html（优先本地 webui/static，回退到 embed.FS）
	w = doRequest(engine, "GET", "/ui/", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /ui/ = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "<html") && !strings.Contains(w.Body.String(), "DOCTYPE") {
		t.Errorf("/ui/ did not serve an HTML document: %q", w.Body.String()[:min(80, len(w.Body.String()))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestRegisterRoutesStatus(t *testing.T) {
	engine := setupEngine(t)
	w := doRequest(engine, "GET", "/api/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/status = %d", w.Code)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["rules"]; !ok {
		t.Error("status response missing 'rules'")
	}
	if _, ok := out["sse_clients"]; !ok {
		t.Error("status response missing 'sse_clients'")
	}
}

func TestRegisterRoutesScripts(t *testing.T) {
	engine := setupEngine(t)
	w := doRequest(engine, "GET", "/api/scripts", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/scripts = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "debugscript") {
		t.Errorf("scripts response missing debugscript: %s", w.Body.String())
	}
}

func TestRegisterRoutesRequestsWithFilter(t *testing.T) {
	engine := setupEngine(t)
	// 无过滤
	w := doRequest(engine, "GET", "/api/requests", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/requests = %d", w.Code)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["total"]; !ok {
		t.Error("requests response missing 'total'")
	}

	// 非法 query
	w = doRequest(engine, "GET", "/api/requests?limit=abc", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid query should be 400, got %d", w.Code)
	}

	// 带过滤条件
	w = doRequest(engine, "GET", "/api/requests?type=match&status=2xx&limit=10&offset=0", "")
	if w.Code != http.StatusOK {
		t.Errorf("filtered /api/requests = %d", w.Code)
	}
}

func TestRegisterRoutesDiagnostics(t *testing.T) {
	engine := setupEngine(t)
	w := doRequest(engine, "GET", "/api/diagnostics", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/diagnostics = %d", w.Code)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["summary"]; !ok {
		t.Error("diagnostics missing 'summary'")
	}
	if _, ok := out["scripts"]; !ok {
		t.Error("diagnostics missing 'scripts'")
	}
}

func TestRegisterRoutesRuleContent(t *testing.T) {
	engine := setupEngine(t)
	// 存在的脚本
	w := doRequest(engine, "GET", "/api/rules/debugscript/content", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET content (found) = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "DebugScript") {
		t.Errorf("content missing script name: %s", w.Body.String())
	}

	// 不存在的脚本
	w = doRequest(engine, "GET", "/api/rules/nope/content", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET content (missing) = %d, want 404", w.Code)
	}
}

func TestRegisterRoutesRuleDebug(t *testing.T) {
	engine := setupEngine(t)
	// 命中规则的路径（SSRF 校验要求完整 http URL）
	body := `{"method":"POST","path":"http://test.example.com/foo","dry_run":true}`
	w := doRequest(engine, "POST", "/api/rules/debugscript/debug", body)
	if w.Code != http.StatusOK {
		t.Fatalf("POST debug = %d, body=%s", w.Code, w.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["steps"]; !ok {
		t.Error("debug response missing 'steps'")
	}

	// 未命中规则的路径
	w = doRequest(engine, "POST", "/api/rules/debugscript/debug", `{"method":"POST","path":"http://test.example.com/bar"}`)
	if w.Code != http.StatusOK {
		t.Errorf("debug (no match) = %d", w.Code)
	}

	// 非法 path（SSRF 校验失败）
	w = doRequest(engine, "POST", "/api/rules/debugscript/debug", `{"method":"POST","path":"not-a-url"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("debug (bad path) = %d, want 400", w.Code)
	}

	// 非法 JSON
	w = doRequest(engine, "POST", "/api/rules/debugscript/debug", `{bad`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("debug (bad json) = %d, want 400", w.Code)
	}
}

func TestRegisterRoutesReplay(t *testing.T) {
	engine := setupEngine(t)
	// 正常重放（fakeReplayer）
	w := doRequest(engine, "POST", "/api/replay", `{"method":"GET","url":"http://example.com/api"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST replay = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "replayed") {
		t.Errorf("replay response unexpected: %s", w.Body.String())
	}

	// 缺少 url
	w = doRequest(engine, "POST", "/api/replay", `{"method":"GET"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("replay (no url) = %d, want 400", w.Code)
	}

	// 非法 JSON
	w = doRequest(engine, "POST", "/api/replay", `{bad`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("replay (bad json) = %d, want 400", w.Code)
	}
}

func TestRegisterRoutesCADownload(t *testing.T) {
	engine := setupEngine(t)
	// crt 下载
	w := doRequest(engine, "GET", "/api/ca/download?format=crt", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET ca download crt = %d", w.Code)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("crt download missing attachment header: %q", cd)
	}

	// mobileconfig
	w = doRequest(engine, "GET", "/api/ca/download?format=mobileconfig", "")
	if w.Code != http.StatusOK {
		t.Errorf("mobileconfig download = %d", w.Code)
	}

	// 非法格式
	w = doRequest(engine, "GET", "/api/ca/download?format=bad", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("ca download (bad format) = %d, want 400", w.Code)
	}
}

// TestRegisterRoutesNilOptionals 用 nil 的可选依赖注册，覆盖 5xx 降级分支
func TestRegisterRoutesNilOptionals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	manager := rule.NewRuleManager(rule.ManagerConfig{Dir: dir}, rule.NewRuleEngine())
	writeRuleFile(t, dir, "plain", "@name Plain\n/^\\/x/ script://self\n")
	if err := manager.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	engine := gin.New()
	RegisterRoutes(engine, manager, NewEventBroadcaster(10), nil, nil, nil, nil)

	// CA 未加载 → 503
	w := doRequest(engine, "GET", "/api/ca/status", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /api/ca/status (nil) = %d, want 503", w.Code)
	}
	w = doRequest(engine, "GET", "/api/ca/download?format=crt", "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /api/ca/download (nil) = %d, want 503", w.Code)
	}

	// Replayer 未配置 → 503
	w = doRequest(engine, "POST", "/api/replay", `{"method":"GET","url":"http://example.com"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /api/replay (nil) = %d, want 503", w.Code)
	}

	// Debugger 未配置 → 500
	w = doRequest(engine, "POST", "/api/rules/plain/debug", `{"method":"POST","path":"http://x.com/x"}`)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("POST /api/rules/plain/debug (nil debugger) = %d, want 500", w.Code)
	}
}

// TestHandleSSE 隔离测试 SSE 处理函数：用已取消的 context 让其在发送历史事件后立即退出
func TestHandleSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	b := NewEventBroadcaster(10)
	b.Broadcast(&Event{URL: "http://example.com/1", Status: 200})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消，使 clientGone 通道可读
	c.Request = httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)

	handleSSE(c, b)

	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", w.Header().Get("Content-Type"))
	}
	// 应已将历史事件以 SSE 格式写出
	if !strings.Contains(w.Body.String(), "data:") {
		t.Errorf("SSE body missing 'data:' prefix: %q", w.Body.String())
	}
}
