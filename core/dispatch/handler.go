package dispatch

import (
	"log"
	"net/http"

	"github.com/dop251/goja"

	"mitm-proxy/core/proxy"
	"mitm-proxy/core/script"
)

// RuleHandler 单条规则类型的处理策略
type RuleHandler interface {
	// Handle 处理匹配的请求，由 ProcessorDispatcher 注入自身引用
	Handle(d *ProcessorDispatcher, req *http.Request, body []byte, match *proxy.MatchResult) *http.Response
}

// ========================================
// 内置 Handler：薄层，委托给 Dispatcher 的具体方法
// ========================================

type proxyHandler struct{}

func (h *proxyHandler) Handle(d *ProcessorDispatcher, req *http.Request, body []byte, match *proxy.MatchResult) *http.Response {
	return d.doProxy(req, body, match)
}

type scriptHandler struct{}

func (h *scriptHandler) Handle(d *ProcessorDispatcher, req *http.Request, body []byte, match *proxy.MatchResult) *http.Response {
	return d.doScript(req, body, match)
}

type deprecatedHandler struct {
	message string
}

func (h *deprecatedHandler) Handle(d *ProcessorDispatcher, req *http.Request, body []byte, match *proxy.MatchResult) *http.Response {
	log.Printf("[Dispatch] WARNING: %s", h.message)
	return proxy.BuildErrorResponse(http.StatusGone, h.message)
}

// RegisterBuiltinHandlers 注册所有内置处理器
func RegisterBuiltinHandlers(d *ProcessorDispatcher) {
	d.RegisterHandler(proxy.RuleTypeProxy, &proxyHandler{})
	d.RegisterHandler(proxy.RuleTypeExact, &proxyHandler{}) // exact 复用 proxy 代理
	d.RegisterHandler(proxy.RuleTypeScript, &scriptHandler{})
	d.RegisterHandler(proxy.RuleTypePipeline, &deprecatedHandler{
		message: "pipeline:// is no longer supported. Please migrate your rules to use script:// protocol. " +
			"See docs/refactor-v2-roadmap.md for migration guide.",
	})
}

// 由 handler 调用的具体逻辑，定义在 ProcessorDispatcher 上以避免重复
func (d *ProcessorDispatcher) doProxy(req *http.Request, body []byte, match *proxy.MatchResult) *http.Response {
	if match.Stream {
		log.Printf("[Dispatch] Stream: %s %s → %s", req.Method, req.URL.String(), match.Target)
		return proxy.StreamRequest(req, body, match.Target)
	}
	log.Printf("[Dispatch] Proxy: %s %s → %s", req.Method, req.URL.String(), match.Target)
	resp := proxy.ProxyRequest(req, body, match.Target, d.HTTPClient.Timeout)
	if match.CORS {
		proxy.SetCORSHeaders(resp.Header, req)
	}
	return resp
}

func (d *ProcessorDispatcher) doScript(req *http.Request, body []byte, match *proxy.MatchResult) *http.Response {
	scriptID := match.ScriptRef
	if scriptID == "" || scriptID == "self" {
		scriptID = match.ScriptID
	}
	log.Printf("[Dispatch] Script: %s %s → script://%s (resolved: %s)", req.Method, req.URL.String(), match.ScriptRef, scriptID)

	rs, ok := d.ScriptAccessor.Get(scriptID)
	if !ok {
		return proxy.BuildErrorResponse(http.StatusInternalServerError, "script not found: "+scriptID)
	}

	sc := script.NewScriptContext(req, body, rs.Config)

	if prog, ok := rs.CompiledPrograms["onRequest"]; ok {
		if err := d.executePhase(prog, sc); err != nil {
			return proxy.BuildErrorResponse(http.StatusInternalServerError, "onRequest execution failed: "+err.Error())
		}
	}

	if len(sc.FinalResp) > 0 && sc.BackendURL == "" {
		return d.buildScriptResponse(req, sc, match)
	}
	if sc.BackendURL != "" {
		if err := d.forwardToBackend(sc); err != nil {
			return proxy.BuildErrorResponse(http.StatusBadGateway, "backend forward failed: "+err.Error())
		}
	} else {
		return proxy.BuildErrorResponse(http.StatusInternalServerError, "onRequest must set ctx.backendURL or ctx.finalResp")
	}

	if prog, ok := rs.CompiledPrograms["onResponse"]; ok {
		if err := d.executePhase(prog, sc); err != nil {
			return proxy.BuildErrorResponse(http.StatusInternalServerError, "onResponse execution failed: "+err.Error())
		}
	}
	return d.buildScriptResponse(req, sc, match)
}

func (d *ProcessorDispatcher) executePhase(program *goja.Program, sc *script.ScriptContext) error {
	vm := d.ScriptRuntime.Acquire()
	defer d.ScriptRuntime.Release(vm)
	if err := sc.InjectSetup()(vm); err != nil {
		return err
	}
	if _, err := vm.RunProgram(program); err != nil {
		return err
	}
	return sc.SyncFromVM(vm)
}
