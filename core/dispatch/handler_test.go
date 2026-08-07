package dispatch

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dop251/goja"

	"mitm-proxy/core/proxy"
	"mitm-proxy/core/script"
	"mitm-proxy/rule"
)

// compileProg 编译一段 JS 脚本为可复用 Program
func compileProg(t *testing.T, src string) *goja.Program {
	t.Helper()
	prog, err := goja.Compile("stage", src, false)
	if err != nil {
		t.Fatalf("compile script: %v", err)
	}
	return prog
}

func readBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestDoScriptSetsFinalRespDirectly 验证 onRequest 直接设置 finalResp 时无需转发后端
func TestDoScriptSetsFinalRespDirectly(t *testing.T) {
	prog := compileProg(t, `ctx.finalResp = JSON.stringify({code:0, data:"direct"});`)
	acc := mapAccessor{
		"self": &rule.RuleScript{ID: "self", CompiledPrograms: map[string]*goja.Program{"onRequest": prog}},
	}
	d := NewProcessorDispatcher(script.NewRuntime(), acc, nil)
	match := &proxy.MatchResult{RuleType: proxy.RuleTypeScript, ScriptID: "self"}
	req := mustGet(t, "http://x/p")

	resp := d.Process(req, nil, match)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if body := readBody(resp); !strings.Contains(body, `"data":"direct"`) {
		t.Errorf("unexpected body: %s", body)
	}
}

// TestDoScriptForwardsToBackend 验证 onRequest 设置 backendURL 时正确转发到后端
func TestDoScriptForwardsToBackend(t *testing.T) {
	var gotBody []byte
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"backend":"ok"}`)
	}))
	defer backend.Close()

	prog := compileProg(t, `ctx.backendURL = "` + backend.URL + `"; ctx.backendRequest = {cmd:"hi"};`)
	acc := mapAccessor{
		"self": &rule.RuleScript{ID: "self", CompiledPrograms: map[string]*goja.Program{"onRequest": prog}},
	}
	d := NewProcessorDispatcher(script.NewRuntime(), acc, nil)
	match := &proxy.MatchResult{RuleType: proxy.RuleTypeScript, ScriptID: "self"}
	req := mustGet(t, "http://x/p")

	resp := d.Process(req, nil, match)
	// 仅 onRequest 设置 backendURL（无 onResponse 改写 finalStatus）时，
	// buildScriptResponse 使用 ScriptContext 默认 FinalStatus=200，
	// 但响应体确实来自后端转发结果。
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if body := readBody(resp); !strings.Contains(body, `"backend":"ok"`) {
		t.Errorf("unexpected body: %s", body)
	}
	if !strings.Contains(string(gotBody), `"cmd":"hi"`) {
		t.Errorf("backend received %q, want the scripted request body", gotBody)
	}
}

// TestDoScriptOnResponsePhase 验证 onResponse 阶段能改写后端响应
func TestDoScriptOnResponsePhase(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"raw":"response"}`)
	}))
	defer backend.Close()

	onReq := compileProg(t, `ctx.backendURL = "` + backend.URL + `"; ctx.backendRequest = {x:1};`)
	onResp := compileProg(t, `ctx.finalResp = JSON.stringify({wrapped: ctx.backendResp});`)
	acc := mapAccessor{
		"self": {
			ID: "self",
			CompiledPrograms: map[string]*goja.Program{
				"onRequest":  onReq,
				"onResponse": onResp,
			},
		},
	}
	d := NewProcessorDispatcher(script.NewRuntime(), acc, nil)
	match := &proxy.MatchResult{RuleType: proxy.RuleTypeScript, ScriptID: "self"}
	req := mustGet(t, "http://x/p")

	resp := d.Process(req, nil, match)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readBody(resp)
	if !strings.Contains(body, "wrapped") || !strings.Contains(body, "raw") {
		t.Errorf("onResponse did not transform backend resp: %s", body)
	}
}

// TestDoScriptOnRequestError 验证 onRequest 执行失败返回 500
func TestDoScriptOnRequestError(t *testing.T) {
	prog := compileProg(t, `throw new Error("boom")`)
	acc := mapAccessor{
		"self": &rule.RuleScript{ID: "self", CompiledPrograms: map[string]*goja.Program{"onRequest": prog}},
	}
	d := NewProcessorDispatcher(script.NewRuntime(), acc, nil)
	match := &proxy.MatchResult{RuleType: proxy.RuleTypeScript, ScriptID: "self"}
	req := mustGet(t, "http://x/p")

	resp := d.Process(req, nil, match)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
	if !strings.Contains(readBody(resp), "onRequest execution failed") {
		t.Errorf("unexpected body: %s", readBody(resp))
	}
}

// TestDoScriptOnResponseError 验证 onResponse 执行失败返回 500
func TestDoScriptOnResponseError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"raw":"x"}`)
	}))
	defer backend.Close()

	onReq := compileProg(t, `ctx.backendURL = "` + backend.URL + `"; ctx.backendRequest = {x:1};`)
	onResp := compileProg(t, `throw new Error("resp boom")`)
	acc := mapAccessor{
		"self": {
			ID: "self",
			CompiledPrograms: map[string]*goja.Program{
				"onRequest":  onReq,
				"onResponse": onResp,
			},
		},
	}
	d := NewProcessorDispatcher(script.NewRuntime(), acc, nil)
	match := &proxy.MatchResult{RuleType: proxy.RuleTypeScript, ScriptID: "self"}
	req := mustGet(t, "http://x/p")

	resp := d.Process(req, nil, match)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
	if !strings.Contains(readBody(resp), "onResponse execution failed") {
		t.Errorf("unexpected body: %s", readBody(resp))
	}
}

// TestDoProxyStreamMode 验证 Stream=true 时走流式转发
func TestDoProxyStreamMode(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "streamed")
	}))
	defer backend.Close()

	d := NewProcessorDispatcher(script.NewRuntime(), mapAccessor{}, nil)
	match := &proxy.MatchResult{RuleType: proxy.RuleTypeProxy, Target: backend.URL, Stream: true}
	req := mustGet(t, "http://x/p")

	resp := d.Process(req, nil, match)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if body := readBody(resp); body != "streamed" {
		t.Errorf("unexpected body: %q", body)
	}
}

// TestDoProxyUpstreamFailure 验证上游不可达时返回 502
func TestDoProxyUpstreamFailure(t *testing.T) {
	d := NewProcessorDispatcher(script.NewRuntime(), mapAccessor{}, nil)
	match := &proxy.MatchResult{RuleType: proxy.RuleTypeProxy, Target: "http://127.0.0.1:1"}
	req := mustGet(t, "http://x/p")

	resp := d.Process(req, nil, match)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
}

// TestDoScriptRefExternal 验证 ScriptRef 指向其它脚本时被正确解析
func TestDoScriptRefExternal(t *testing.T) {
	prog := compileProg(t, `ctx.finalResp = "from-other";`)
	acc := mapAccessor{
		"other": &rule.RuleScript{ID: "other", CompiledPrograms: map[string]*goja.Program{"onRequest": prog}},
	}
	d := NewProcessorDispatcher(script.NewRuntime(), acc, nil)
	// ScriptRef 指向 other，ScriptID 是未注册的 self
	match := &proxy.MatchResult{RuleType: proxy.RuleTypeScript, ScriptID: "self", ScriptRef: "other"}
	req := mustGet(t, "http://x/p")

	resp := d.Process(req, nil, match)
	if !strings.Contains(readBody(resp), "from-other") {
		t.Errorf("script ref not resolved: %s", readBody(resp))
	}
}
