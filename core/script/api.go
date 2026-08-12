package script

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/dop251/goja"
)

// ScriptContext 每次请求传递给 JS 脚本的上下文
type ScriptContext struct {
	// 输入（只读）
	RawBody string            `json:"rawBody"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Path    string            `json:"path"`
	Query   string            `json:"query"`
	Headers map[string]string `json:"headers"`
	Cookies map[string]string `json:"cookies"` // 解析后的 Cookie key→value

	// 脚本私有配置（@config 段，框架透传）
	Config map[string]any `json:"config"`

	// 转发阶段（可写，onRequest 设置）
	BackendURL     string         `json:"backendURL"`
	BackendRequest map[string]any `json:"backendRequest"`
	BackendHeaders map[string]string `json:"backendHeaders"`

	// 响应阶段（框架写入，onResponse 读取/改写）
	BackendResp   []byte `json:"-"` // 后端响应原始字节
	BackendStatus int    `json:"backendStatus"`

	// 最终响应（可写，onResponse 设置）
	FinalResp    []byte            `json:"-"`
	FinalStatus  int               `json:"finalStatus"`
	FinalHeaders map[string]string `json:"finalHeaders"`

	// 跨函数传递的元数据
	Metadata map[string]any `json:"metadata"`

	// Console 日志（捕获输出）
	ConsoleLogs []ConsoleEntry `json:"-"`
}

// ConsoleEntry 控制台日志条目
type ConsoleEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// NewScriptContext 从 HTTP 请求创建脚本上下文
func NewScriptContext(req *http.Request, body []byte, config map[string]any) *ScriptContext {
	headers := make(map[string]string)
	for k := range req.Header {
		headers[k] = req.Header.Get(k)
	}

	// 解析 Cookie 为 key→value map
	cookies := make(map[string]string)
	cookieHeader := req.Header.Get("Cookie")
	if cookieHeader != "" {
		// Cookie 格式：key1=value1; key2=value2; ...
		parts := strings.Split(cookieHeader, ";")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if eqIdx := strings.Index(part, "="); eqIdx > 0 {
				key := part[:eqIdx]
				val := part[eqIdx+1:]
				// 去掉可能被引号包裹的值
				if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
					val = val[1 : len(val)-1]
				}
				cookies[key] = val
			}
		}
	}

	return &ScriptContext{
		RawBody:  string(body),
		Method:   req.Method,
		URL:      req.URL.String(),
		Path:     req.URL.Path,
		Query:    req.URL.RawQuery,
		Headers:  headers,
		Cookies:  cookies,
		Config:   config,
		Metadata: make(map[string]any),
		FinalStatus: 200,
	}
}

// InjectSetup 创建一个 setup 函数，在执行 JS 前注入 ctx + console 到 goja VM
func (sc *ScriptContext) InjectSetup() func(vm *goja.Runtime) error {
	return func(vm *goja.Runtime) error {
		// 注入 console 对象
		consoleObj := vm.NewObject()
		consoleObj.Set("log", func(call goja.FunctionCall) goja.Value {
			msg := formatConsoleArgs(call.Arguments)
			sc.ConsoleLogs = append(sc.ConsoleLogs, ConsoleEntry{Level: "log", Message: msg})
			return goja.Undefined()
		})
		consoleObj.Set("warn", func(call goja.FunctionCall) goja.Value {
			msg := formatConsoleArgs(call.Arguments)
			sc.ConsoleLogs = append(sc.ConsoleLogs, ConsoleEntry{Level: "warn", Message: msg})
			return goja.Undefined()
		})
		consoleObj.Set("error", func(call goja.FunctionCall) goja.Value {
			msg := formatConsoleArgs(call.Arguments)
			sc.ConsoleLogs = append(sc.ConsoleLogs, ConsoleEntry{Level: "error", Message: msg})
			return goja.Undefined()
		})
		vm.Set("console", consoleObj)

		// 注意：不覆盖 goja 内置的 JSON.parse/JSON.stringify
		// goja 已有完整的 JSON 实现，直接可用

		// 注入 ctx 对象
		ctxObj, err := sc.buildCtxObject(vm)
		if err != nil {
			return fmt.Errorf("build ctx object: %w", err)
		}
		vm.Set("ctx", ctxObj)

		return nil
	}
}

// SyncFromVM 将 goja VM 中 ctx 对象的修改同步回 ScriptContext
func (sc *ScriptContext) SyncFromVM(vm *goja.Runtime) error {
	ctxVal := vm.Get("ctx")
	if ctxVal == nil || goja.IsUndefined(ctxVal) {
		return nil
	}
	ctxObj, ok := ctxVal.(*goja.Object)
	if !ok {
		return nil
	}

	// backendURL
	if v := ctxObj.Get("backendURL"); v != nil && !goja.IsUndefined(v) {
		sc.BackendURL = v.String()
	}

	// backendRequest (JSON object → map[string]any)
	if v := ctxObj.Get("backendRequest"); v != nil && !goja.IsUndefined(v) {
		jsonBytes, _ := json.Marshal(v.Export())
		var m map[string]any
		json.Unmarshal(jsonBytes, &m)
		sc.BackendRequest = m
	}

	// backendHeaders
	if v := ctxObj.Get("backendHeaders"); v != nil && !goja.IsUndefined(v) {
		if exported := v.Export(); exported != nil {
			if m, ok := exported.(map[string]interface{}); ok {
				sc.BackendHeaders = make(map[string]string)
				for k, val := range m {
					sc.BackendHeaders[k] = fmt.Sprint(val)
				}
			}
		}
	}

	// finalResp (string)
	if v := ctxObj.Get("finalResp"); v != nil && !goja.IsUndefined(v) {
		sc.FinalResp = []byte(v.String())
	}

	// finalStatus
	if v := ctxObj.Get("finalStatus"); v != nil && !goja.IsUndefined(v) {
		sc.FinalStatus = int(v.ToInteger())
	}

	// finalHeaders
	if v := ctxObj.Get("finalHeaders"); v != nil && !goja.IsUndefined(v) {
		if exported := v.Export(); exported != nil {
			if m, ok := exported.(map[string]interface{}); ok {
				sc.FinalHeaders = make(map[string]string)
				for k, val := range m {
					sc.FinalHeaders[k] = fmt.Sprint(val)
				}
			}
		}
	}

	// metadata
	if v := ctxObj.Get("metadata"); v != nil && !goja.IsUndefined(v) {
		if exported := v.Export(); exported != nil {
			if m, ok := exported.(map[string]interface{}); ok {
				for k, val := range m {
					sc.Metadata[k] = val
				}
			}
		}
	}

	return nil
}

// buildCtxObject 构建 goja 中的 ctx 对象
func (sc *ScriptContext) buildCtxObject(vm *goja.Runtime) (*goja.Object, error) {
	obj := vm.NewObject()

	// 只读字段
	obj.Set("rawBody", sc.RawBody)
	obj.Set("method", sc.Method)
	obj.Set("url", sc.URL)
	obj.Set("path", sc.Path)
	obj.Set("query", sc.Query)

	// headers
	headersObj := vm.NewObject()
	for k, v := range sc.Headers {
		headersObj.Set(k, v)
	}
	obj.Set("headers", headersObj)

	// cookies
	cookiesObj := vm.NewObject()
	for k, v := range sc.Cookies {
		cookiesObj.Set(k, v)
	}
	obj.Set("cookies", cookiesObj)

	// config（透传 @config 内容）
	obj.Set("config", sc.Config)

	// 可写字段
	obj.Set("backendURL", sc.BackendURL)
	obj.Set("backendRequest", sc.BackendRequest)
	if sc.BackendHeaders != nil {
		bhObj := vm.NewObject()
		for k, v := range sc.BackendHeaders {
			bhObj.Set(k, v)
		}
		obj.Set("backendHeaders", bhObj)
	} else {
		obj.Set("backendHeaders", vm.NewObject())
	}

	// 响应字段
	obj.Set("backendResp", string(sc.BackendResp))
	obj.Set("backendStatus", sc.BackendStatus)
	obj.Set("finalStatus", sc.FinalStatus)
	obj.Set("finalHeaders", vm.NewObject())

	// metadata
	metaObj := vm.NewObject()
	for k, v := range sc.Metadata {
		metaObj.Set(k, v)
	}
	obj.Set("metadata", metaObj)

	return obj, nil
}

// formatConsoleArgs 格式化 console.log 参数
func formatConsoleArgs(args []goja.Value) string {
	strs := make([]string, 0, len(args))
	for _, arg := range args {
		strs = append(strs, arg.String())
	}
	if len(strs) == 1 {
		return strs[0]
	}
	result := ""
	for i, s := range strs {
		if i > 0 {
			result += " "
		}
		result += s
	}
	return result
}
