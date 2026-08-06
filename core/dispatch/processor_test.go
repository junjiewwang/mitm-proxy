package dispatch

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mitm-proxy/core/event"
	"mitm-proxy/core/proxy"
	"mitm-proxy/core/script"
	"mitm-proxy/rule"
)

func TestMain(m *testing.M) {
	// 分发过程会打印大量日志，测试中静音
	log.SetOutput(io.Discard)
	code := m.Run()
	log.SetOutput(os.Stderr)
	os.Exit(code)
}

// recordingBroadcaster 记录收到的事件，供断言使用。
type recordingBroadcaster struct {
	mu     sync.Mutex
	events []*event.Event
}

func (b *recordingBroadcaster) Broadcast(evt *event.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, evt)
}

func (b *recordingBroadcaster) all() []*event.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*event.Event, len(b.events))
	copy(out, b.events)
	return out
}

// stubHandler 返回预设响应，用于验证分发路由。
type stubHandler struct {
	called atomic.Bool
	resp   *http.Response
}

func (h *stubHandler) Handle(d *ProcessorDispatcher, req *http.Request, body []byte, match *proxy.MatchResult) *http.Response {
	h.called.Store(true)
	if h.resp != nil {
		return h.resp
	}
	return proxy.BuildErrorResponse(http.StatusOK, "stub")
}

// mapAccessor 以内存 map 实现 ScriptAccessor。
type mapAccessor map[string]*rule.RuleScript

func (a mapAccessor) Get(id string) (*rule.RuleScript, bool) {
	s, ok := a[id]
	return s, ok
}

func newTestDispatcher(b Broadcaster) *ProcessorDispatcher {
	return NewProcessorDispatcher(script.NewRuntime(), mapAccessor{}, b)
}

func mustGet(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return r
}

func TestNewProcessorDispatcherRegistersBuiltins(t *testing.T) {
	d := newTestDispatcher(nil)

	for _, rt := range []proxy.RuleType{
		proxy.RuleTypeProxy, proxy.RuleTypeExact,
		proxy.RuleTypeScript, proxy.RuleTypePipeline,
	} {
		d.mu.RLock()
		_, ok := d.handlers[rt]
		d.mu.RUnlock()
		if !ok {
			t.Errorf("no builtin handler registered for rule type %v", rt)
		}
	}

	if d.HTTPClient == nil || d.HTTPClient.Timeout != 30*time.Second {
		t.Errorf("HTTPClient timeout = %v, want 30s", d.HTTPClient.Timeout)
	}
}

func TestRegisterHandlerReplaces(t *testing.T) {
	d := newTestDispatcher(nil)
	stub := &stubHandler{}
	d.RegisterHandler(proxy.RuleTypeProxy, stub)

	d.mu.RLock()
	got := d.handlers[proxy.RuleTypeProxy]
	d.mu.RUnlock()
	if got != stub {
		t.Error("RegisterHandler should replace the existing handler")
	}
}

func TestProcessDispatchesToHandler(t *testing.T) {
	bc := &recordingBroadcaster{}
	d := newTestDispatcher(bc)
	stub := &stubHandler{}
	d.RegisterHandler(proxy.RuleTypeProxy, stub)

	req := mustGet(t, "http://example.com/api")
	match := &proxy.MatchResult{RuleType: proxy.RuleTypeProxy, ScriptName: "s", Target: "http://backend"}

	resp := d.Process(req, nil, match)

	if !stub.called.Load() {
		t.Error("the registered handler should have been invoked")
	}
	if resp == nil {
		t.Fatal("Process returned nil")
	}
	if got := len(bc.all()); got != 1 {
		t.Errorf("broadcast %d events, want 1", got)
	}
}

func TestProcessUnknownRuleType(t *testing.T) {
	bc := &recordingBroadcaster{}
	d := newTestDispatcher(bc)
	// 清掉所有内置 handler，制造"未知类型"
	d.mu.Lock()
	d.handlers = make(map[proxy.RuleType]RuleHandler)
	d.mu.Unlock()

	req := mustGet(t, "http://example.com/api")
	resp := d.Process(req, nil, &proxy.MatchResult{RuleType: proxy.RuleTypeProxy})

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "unknown rule type") {
		t.Errorf("body = %q, want it to mention the unknown rule type", body)
	}
}

func TestProcessCORSPreflightShortCircuits(t *testing.T) {
	bc := &recordingBroadcaster{}
	d := newTestDispatcher(bc)
	stub := &stubHandler{}
	d.RegisterHandler(proxy.RuleTypeProxy, stub)

	req, _ := http.NewRequest(http.MethodOptions, "http://example.com/api", nil)
	req.Header.Set("Origin", "https://app.example.com")
	match := &proxy.MatchResult{RuleType: proxy.RuleTypeProxy, CORS: true}

	resp := d.Process(req, nil, match)

	if stub.called.Load() {
		t.Error("a CORS preflight must not reach the rule handler")
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("StatusCode = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin = %q, want the request Origin", got)
	}
	if got := resp.Header.Get("Access-Control-Max-Age"); got != "86400" {
		t.Errorf("Max-Age = %q, want 86400", got)
	}
	if got := len(bc.all()); got != 1 {
		t.Errorf("broadcast %d events, want 1 — preflights are still observable", got)
	}
}

func TestProcessCORSNonOptionsStillDispatches(t *testing.T) {
	d := newTestDispatcher(nil)
	stub := &stubHandler{}
	d.RegisterHandler(proxy.RuleTypeProxy, stub)

	// CORS=true 但方法不是 OPTIONS → 应正常走 handler
	d.Process(mustGet(t, "http://example.com/api"), nil,
		&proxy.MatchResult{RuleType: proxy.RuleTypeProxy, CORS: true})

	if !stub.called.Load() {
		t.Error("a non-OPTIONS request should reach the handler even when CORS is on")
	}
}

func TestProcessNilBroadcasterIsSafe(t *testing.T) {
	d := newTestDispatcher(nil) // Broadcaster 为 nil
	d.RegisterHandler(proxy.RuleTypeProxy, &stubHandler{})

	// 不应 panic
	resp := d.Process(mustGet(t, "http://example.com/api"), nil,
		&proxy.MatchResult{RuleType: proxy.RuleTypeProxy})
	if resp == nil {
		t.Fatal("Process returned nil")
	}
}

func TestBuildCORSPreflightResponse(t *testing.T) {
	d := newTestDispatcher(nil)
	req, _ := http.NewRequest(http.MethodOptions, "http://example.com/api", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Headers", "X-Custom")

	resp := d.buildCORSPreflightResponse(req)

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("StatusCode = %d, want 204", resp.StatusCode)
	}
	if resp.ContentLength != 0 {
		t.Errorf("ContentLength = %d, want 0", resp.ContentLength)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); got != "X-Custom" {
		t.Errorf("Allow-Headers = %q, want X-Custom echoed back", got)
	}
}

func TestBuildScriptResponse(t *testing.T) {
	d := newTestDispatcher(nil)
	req := mustGet(t, "http://example.com/api")

	t.Run("优先使用 finalResp 与 finalStatus", func(t *testing.T) {
		sc := &script.ScriptContext{
			FinalResp: []byte(`{"final":true}`), FinalStatus: 201,
			BackendResp: []byte(`{"backend":true}`), BackendStatus: 500,
		}
		resp := d.buildScriptResponse(req, sc, &proxy.MatchResult{})

		if resp.StatusCode != 201 {
			t.Errorf("StatusCode = %d, want 201", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != `{"final":true}` {
			t.Errorf("body = %q, want the final response", body)
		}
		if resp.ContentLength != int64(len(body)) {
			t.Errorf("ContentLength = %d, want %d", resp.ContentLength, len(body))
		}
	})

	t.Run("finalResp 为空时回退到 backendResp", func(t *testing.T) {
		sc := &script.ScriptContext{BackendResp: []byte(`{"backend":true}`), BackendStatus: 202}
		resp := d.buildScriptResponse(req, sc, &proxy.MatchResult{})

		if resp.StatusCode != 202 {
			t.Errorf("StatusCode = %d, want 202", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != `{"backend":true}` {
			t.Errorf("body = %q, want the backend response", body)
		}
	})

	t.Run("两个状态码都为空时默认 200", func(t *testing.T) {
		resp := d.buildScriptResponse(req, &script.ScriptContext{}, &proxy.MatchResult{})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("总是设置 JSON Content-Type", func(t *testing.T) {
		resp := d.buildScriptResponse(req, &script.ScriptContext{}, &proxy.MatchResult{})
		if got := resp.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("Content-Type = %q, want JSON", got)
		}
	})

	t.Run("CORS 开启时附加跨域头", func(t *testing.T) {
		req2 := mustGet(t, "http://example.com/api")
		req2.Header.Set("Origin", "https://app.example.com")
		resp := d.buildScriptResponse(req2, &script.ScriptContext{}, &proxy.MatchResult{CORS: true})

		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
			t.Errorf("Allow-Origin = %q, want the origin echoed", got)
		}
	})

	t.Run("CORS 关闭时不加跨域头", func(t *testing.T) {
		resp := d.buildScriptResponse(req, &script.ScriptContext{}, &proxy.MatchResult{CORS: false})
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("Allow-Origin = %q, want it absent", got)
		}
	})

	t.Run("req 为 nil 时不 panic", func(t *testing.T) {
		resp := d.buildScriptResponse(nil, &script.ScriptContext{}, &proxy.MatchResult{CORS: true})
		if resp == nil {
			t.Fatal("buildScriptResponse returned nil")
		}
	})
}

func TestForwardToBackend(t *testing.T) {
	t.Run("成功转发并回填响应", func(t *testing.T) {
		var gotBody []byte
		var gotHeader string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			gotHeader = r.Header.Get("X-Custom")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"ok":true}`))
		}))
		defer srv.Close()

		d := newTestDispatcher(nil)
		sc := &script.ScriptContext{
			BackendURL:     srv.URL,
			BackendRequest: map[string]any{"key": "value"},
			BackendHeaders: map[string]string{"X-Custom": "abc"},
		}

		if err := d.forwardToBackend(sc); err != nil {
			t.Fatalf("forwardToBackend: %v", err)
		}
		if sc.BackendStatus != http.StatusCreated {
			t.Errorf("BackendStatus = %d, want 201", sc.BackendStatus)
		}
		if string(sc.BackendResp) != `{"ok":true}` {
			t.Errorf("BackendResp = %q, want the upstream body", sc.BackendResp)
		}
		if !strings.Contains(string(gotBody), `"key":"value"`) {
			t.Errorf("upstream received %q, want the marshalled backendRequest", gotBody)
		}
		if gotHeader != "abc" {
			t.Errorf("upstream saw X-Custom = %q, want abc", gotHeader)
		}
	})

	t.Run("无法连接时返回错误", func(t *testing.T) {
		d := newTestDispatcher(nil)
		sc := &script.ScriptContext{BackendURL: "http://127.0.0.1:1", BackendRequest: map[string]any{}}
		if err := d.forwardToBackend(sc); err == nil {
			t.Error("forwardToBackend should fail against a closed port")
		}
	})

	t.Run("URL 非法时返回错误", func(t *testing.T) {
		d := newTestDispatcher(nil)
		sc := &script.ScriptContext{BackendURL: "://bad", BackendRequest: map[string]any{}}
		if err := d.forwardToBackend(sc); err == nil {
			t.Error("forwardToBackend should fail on a malformed URL")
		}
	})
}

func TestReadAndReplaceBody(t *testing.T) {
	t.Run("读取后 body 仍可重复消费", func(t *testing.T) {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader("hello"))}

		got := readAndReplaceBody(resp)
		if string(got) != "hello" {
			t.Errorf("returned %q, want hello", got)
		}
		// 关键行为：body 被替换成可重放的 reader，下游仍能读到同样内容
		again, _ := io.ReadAll(resp.Body)
		if string(again) != "hello" {
			t.Errorf("re-read body = %q, want hello", again)
		}
	})

	t.Run("nil 响应返回 nil", func(t *testing.T) {
		if got := readAndReplaceBody(nil); got != nil {
			t.Errorf("readAndReplaceBody(nil) = %v, want nil", got)
		}
	})

	t.Run("nil body 返回 nil", func(t *testing.T) {
		if got := readAndReplaceBody(&http.Response{}); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("空 body", func(t *testing.T) {
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(""))}
		if got := readAndReplaceBody(resp); len(got) != 0 {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("读取出错时返回已读部分并保持 body 可用", func(t *testing.T) {
		resp := &http.Response{Body: io.NopCloser(&errReader{})}
		got := readAndReplaceBody(resp)
		if len(got) != 0 {
			t.Errorf("got %q, want empty on a read error", got)
		}
		// 出错后 body 仍应被替换成可读对象，避免下游 nil 解引用
		if resp.Body == nil {
			t.Error("Body should have been replaced even after a read error")
		}
		if _, err := io.ReadAll(resp.Body); err != nil {
			t.Errorf("the replacement body should read cleanly, got %v", err)
		}
	})
}

// errReader 第一次读取即返回错误。
type errReader struct{}

func (r *errReader) Read(p []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestBuildOpts(t *testing.T) {
	t.Run("填充基础字段", func(t *testing.T) {
		req := mustGet(t, "http://example.com/api")
		resp := &http.Response{StatusCode: 200}
		match := &proxy.MatchResult{
			RuleType: proxy.RuleTypeScript, ScriptName: "my-script", Target: "http://backend",
		}

		opts := buildOpts(req, []byte("req"), resp, []byte("resp"), match, 5*time.Millisecond)

		if opts.Req != req || opts.Resp != resp {
			t.Error("request/response should be carried through verbatim")
		}
		if string(opts.ReqBody) != "req" || string(opts.RespBody) != "resp" {
			t.Errorf("bodies = (%q, %q), want (req, resp)", opts.ReqBody, opts.RespBody)
		}
		if opts.Match.ScriptName != "my-script" || opts.Match.Target != "http://backend" {
			t.Errorf("match info = %+v, want the script name and target", opts.Match)
		}
		if opts.Match.RuleType != "script" {
			t.Errorf("RuleType = %q, want the script type name", opts.Match.RuleType)
		}
		if opts.Elapsed != 5*time.Millisecond {
			t.Errorf("Elapsed = %v, want 5ms", opts.Elapsed)
		}
	})

	t.Run("X-Replay 头打上 replay 标签", func(t *testing.T) {
		req := mustGet(t, "http://example.com/api")
		req.Header.Set("X-Replay", "true")

		opts := buildOpts(req, nil, nil, nil, &proxy.MatchResult{}, 0)

		if len(opts.Tags) != 1 || opts.Tags[0] != TagReplay {
			t.Errorf("Tags = %v, want [replay]", opts.Tags)
		}
	})

	t.Run("非 true 的 X-Replay 不打标签", func(t *testing.T) {
		req := mustGet(t, "http://example.com/api")
		req.Header.Set("X-Replay", "false")

		if opts := buildOpts(req, nil, nil, nil, &proxy.MatchResult{}, 0); len(opts.Tags) != 0 {
			t.Errorf("Tags = %v, want none", opts.Tags)
		}
	})

	t.Run("无 X-Replay 头不打标签", func(t *testing.T) {
		req := mustGet(t, "http://example.com/api")
		if opts := buildOpts(req, nil, nil, nil, &proxy.MatchResult{}, 0); len(opts.Tags) != 0 {
			t.Errorf("Tags = %v, want none", opts.Tags)
		}
	})
}

func TestDeprecatedPipelineHandler(t *testing.T) {
	bc := &recordingBroadcaster{}
	d := newTestDispatcher(bc)

	resp := d.Process(mustGet(t, "http://example.com/api"), nil,
		&proxy.MatchResult{RuleType: proxy.RuleTypePipeline, Adapter: "legacy"})

	if resp.StatusCode != http.StatusGone {
		t.Errorf("StatusCode = %d, want 410 Gone for the retired pipeline:// protocol", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "no longer supported") {
		t.Errorf("body = %q, want a migration hint", body)
	}
}

func TestDoScriptMissingScript(t *testing.T) {
	d := NewProcessorDispatcher(script.NewRuntime(), mapAccessor{}, nil)

	resp := d.doScript(mustGet(t, "http://example.com/api"), nil,
		&proxy.MatchResult{RuleType: proxy.RuleTypeScript, ScriptID: "ghost", ScriptRef: "self"})

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "script not found") {
		t.Errorf("body = %q, want a script-not-found message", body)
	}
}

func TestDoScriptWithoutBackendOrFinalResp(t *testing.T) {
	// 脚本没有 onRequest，也就没设置 backendURL/finalResp → 应报配置错误
	acc := mapAccessor{"s1": {ID: "s1", Name: "S1"}}
	d := NewProcessorDispatcher(script.NewRuntime(), acc, nil)

	resp := d.doScript(mustGet(t, "http://example.com/api"), nil,
		&proxy.MatchResult{RuleType: proxy.RuleTypeScript, ScriptID: "s1"})

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("StatusCode = %d, want 500", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "backendURL") {
		t.Errorf("body = %q, want it to explain the missing backendURL/finalResp", body)
	}
}

func TestDoScriptResolvesScriptRef(t *testing.T) {
	// ScriptRef 为空或 "self" 时应回退到 ScriptID
	acc := mapAccessor{"owner": {ID: "owner"}, "other": {ID: "other"}}
	d := NewProcessorDispatcher(script.NewRuntime(), acc, nil)
	req := mustGet(t, "http://example.com/api")

	for _, ref := range []string{"", "self"} {
		resp := d.doScript(req, nil, &proxy.MatchResult{ScriptID: "owner", ScriptRef: ref})
		body, _ := io.ReadAll(resp.Body)
		// 找得到脚本 → 不应是 "script not found"
		if strings.Contains(string(body), "script not found") {
			t.Errorf("ScriptRef=%q should resolve to the owning script", ref)
		}
	}

	// 显式引用另一个脚本
	resp := d.doScript(req, nil, &proxy.MatchResult{ScriptID: "owner", ScriptRef: "other"})
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "script not found") {
		t.Error("an explicit ScriptRef should resolve to the referenced script")
	}

	resp = d.doScript(req, nil, &proxy.MatchResult{ScriptID: "owner", ScriptRef: "ghost"})
	body, _ = io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "script not found") {
		t.Errorf("an unknown ScriptRef should fail, got %q", body)
	}
}

func TestDoProxyForwardsAndAppliesCORS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("upstream"))
	}))
	defer srv.Close()

	d := newTestDispatcher(nil)
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api", bytes.NewReader([]byte("payload")))
	req.Header.Set("Origin", "https://app.example.com")

	resp := d.doProxy(req, []byte("payload"), &proxy.MatchResult{Target: srv.URL, CORS: true})

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Allow-Origin = %q, want the CORS header applied to the proxied response", got)
	}
}

// TestProcessConcurrent 在 -race 下验证 handler 注册与分发可以并发进行。
func TestProcessConcurrent(t *testing.T) {
	bc := &recordingBroadcaster{}
	d := newTestDispatcher(bc)
	d.RegisterHandler(proxy.RuleTypeProxy, &stubHandler{})

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				d.Process(mustGet(t, "http://example.com/api"), nil,
					&proxy.MatchResult{RuleType: proxy.RuleTypeProxy})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			d.RegisterHandler(proxy.RuleTypeProxy, &stubHandler{})
		}
	}()
	wg.Wait()

	if got := len(bc.all()); got != 400 {
		t.Errorf("broadcast %d events, want 400", got)
	}
}
