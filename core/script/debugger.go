package script

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// DebugRequest 调试请求参数
type DebugRequest struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Body    string `json:"body"`
	DryRun  bool   `json:"dry_run"`
	Headers string `json:"headers"` // 可选：自定义请求头（每行 Key: Value）
	Cookie  string `json:"cookie"`  // 可选：自定义 Cookie 字符串
}

// DebugStep 调试执行的一个步骤
type DebugStep struct {
	Stage       string        `json:"stage"`                 // rule_match | onRequest | forward | onResponse
	Skipped     bool          `json:"skipped,omitempty"`     // 是否跳过（如 dry_run 转发）
	Matched     bool          `json:"matched,omitempty"`     // 规则是否匹配
	Error       string        `json:"error,omitempty"`       // 错误信息
	Output      interface{}   `json:"output,omitempty"`      // 步骤输出
	ConsoleLogs []string      `json:"console_logs,omitempty"` // console.log 输出
	ElapsedMs   float64       `json:"elapsed_ms"`            // 耗时（毫秒）
}

// DebugResult 调试完整结果
type DebugResult struct {
	Steps          []DebugStep `json:"steps"`
	TotalElapsedMs float64     `json:"total_elapsed_ms"`
}

// Debugger JS 脚本调试执行器
type Debugger struct {
	runtime *Runtime
}

// NewDebugger 创建调试器
func NewDebugger(runtime *Runtime) *Debugger {
	return &Debugger{runtime: runtime}
}

// Execute 执行分步调试
// scriptID: 目标脚本 ID
// config: @config 段内容
// compiledProgs: 预编译的 JS 程序
// hasOnReq/hasOnResp: 是否有对应阶段的脚本
func (d *Debugger) Execute(req DebugRequest, scriptID string, config map[string]any, compiledProgs map[string]*goja.Program, hasOnReq, hasOnResp bool) *DebugResult {
	startTotal := time.Now()
	result := &DebugResult{}

	if scriptID == "" {
		result.Steps = append(result.Steps, DebugStep{Stage: "error", Error: "no script_id provided", ElapsedMs: 0})
		result.TotalElapsedMs = 0
		return result
	}

	// Step 1: rule_match (always matched since script was already resolved)
	result.Steps = append(result.Steps, DebugStep{
		Stage:   "rule_match",
		Matched: true,
		Output:  map[string]interface{}{"script_id": scriptID},
		ElapsedMs: 0,
	})

	// Build HTTP request for context
	// NOCA:server_side_request_forgery(req.Path 由 webui 端点 ValidateExternalURL 校验)
	httpReq, _ := http.NewRequest(req.Method, req.Path, nil)
	if req.Method == "" {
		httpReq.Method = "POST"
	}
	parsedURL, _ := url.Parse(req.Path)
	if parsedURL != nil {
		httpReq.URL = parsedURL
	}
	// 应用调试输入的自定义 Headers 和 Cookie
	if req.Headers != "" {
		for _, line := range strings.Split(req.Headers, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || !strings.Contains(line, ":") {
				continue
			}
			parts := strings.SplitN(line, ":", 2)
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			httpReq.Header.Set(key, val)
		}
	}
	if req.Cookie != "" {
		httpReq.Header.Set("Cookie", req.Cookie)
	}

	sc := NewScriptContext(httpReq, []byte(req.Body), config)

	// Step 2: onRequest
	onReqStep := d.stepExecute("onRequest", compiledProgs["onRequest"], sc, hasOnReq)
	result.Steps = append(result.Steps, onReqStep)
	if onReqStep.Error != "" {
		result.TotalElapsedMs = time.Since(startTotal).Seconds() * 1000
		return result
	}

	// Step 3: forward (or skip in dry_run)
	if sc.BackendURL != "" && req.DryRun {
		// dry_run 跳过实际转发，注入最小占位响应使 onResponse 可正常执行
		sc.BackendResp = []byte(`{"Response":{}}`)
		sc.BackendStatus = 200
		result.Steps = append(result.Steps, DebugStep{
			Stage:     "forward",
			Skipped:   true,
			Output:    map[string]interface{}{"would_send_to": sc.BackendURL, "request": sc.BackendRequest, "note": "dry_run: backend mocked"},
			ElapsedMs: time.Since(startTotal).Seconds() * 1000,
		})
	} else if sc.BackendURL != "" {
		fwdStep := d.stepForward(sc)
		result.Steps = append(result.Steps, fwdStep)
		if fwdStep.Error != "" {
			result.TotalElapsedMs = time.Since(startTotal).Seconds() * 1000
			return result
		}
	} else {
		result.Steps = append(result.Steps, DebugStep{
			Stage:     "forward",
			Skipped:   true,
			Output:    map[string]interface{}{"note": "no backendURL set, onRequest may have set finalResp directly"},
			ElapsedMs: time.Since(startTotal).Seconds() * 1000,
		})
	}

	// Step 4: onResponse
	onRespStep := d.stepExecute("onResponse", compiledProgs["onResponse"], sc, hasOnResp)
	result.Steps = append(result.Steps, onRespStep)

	result.TotalElapsedMs = time.Since(startTotal).Seconds() * 1000
	return result
}

// stepExecute 执行一个 JS 阶段
func (d *Debugger) stepExecute(stage string, prog *goja.Program, sc *ScriptContext, hasScript bool) DebugStep {
	start := time.Now()
	step := DebugStep{Stage: stage}

	if !hasScript || prog == nil {
		step.Skipped = true
		step.Output = map[string]interface{}{"note": fmt.Sprintf("no %s script defined", stage)}
		step.ElapsedMs = time.Since(start).Seconds() * 1000
		return step
	}

	vm := d.runtime.Acquire()
	defer d.runtime.Release(vm)

	if err := sc.InjectSetup()(vm); err != nil {
		step.Error = fmt.Sprintf("setup: %v", err)
		step.ElapsedMs = time.Since(start).Seconds() * 1000
		return step
	}

	if _, err := vm.RunProgram(prog); err != nil {
		step.Error = fmt.Sprintf("runtime: %v", err)
		// Still collect console logs
		for _, entry := range sc.ConsoleLogs {
			step.ConsoleLogs = append(step.ConsoleLogs, fmt.Sprintf("[%s] %s", entry.Level, entry.Message))
		}
		step.ElapsedMs = time.Since(start).Seconds() * 1000
		return step
	}

	if err := sc.SyncFromVM(vm); err != nil {
		step.Error = fmt.Sprintf("sync: %v", err)
		step.ElapsedMs = time.Since(start).Seconds() * 1000
		return step
	}

	// Collect console logs
	for _, entry := range sc.ConsoleLogs {
		step.ConsoleLogs = append(step.ConsoleLogs, fmt.Sprintf("[%s] %s", entry.Level, entry.Message))
	}
	// Reset logs for next phase
	sc.ConsoleLogs = nil

	// Build output
	output := make(map[string]interface{})
	switch stage {
	case "onRequest":
		output["backendURL"] = sc.BackendURL
		output["backendRequest"] = sc.BackendRequest
		output["backendHeaders"] = sc.BackendHeaders
	case "onResponse":
		output["finalStatus"] = sc.FinalStatus
		if len(sc.FinalResp) > 0 {
			output["finalRespPreview"] = truncateString(string(sc.FinalResp), 500)
		}
	}
	step.Output = output
	step.ElapsedMs = time.Since(start).Seconds() * 1000
	return step
}

// stepForward 实际转发到后端
func (d *Debugger) stepForward(sc *ScriptContext) DebugStep {
	start := time.Now()
	step := DebugStep{Stage: "forward"}

	reqBody, err := json.Marshal(sc.BackendRequest)
	if err != nil {
		step.Error = fmt.Sprintf("marshal: %v", err)
		step.ElapsedMs = time.Since(start).Seconds() * 1000
		return step
	}

	// NOCA:server_side_request_forgery(sc.BackendURL 由受信任的 JS 脚本 onRequest 阶段设置)
	httpReq, err := http.NewRequest("POST", sc.BackendURL, bytes.NewReader(reqBody))
	if err != nil {
		step.Error = fmt.Sprintf("create request: %v", err)
		step.ElapsedMs = time.Since(start).Seconds() * 1000
		return step
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		step.Error = fmt.Sprintf("forward: %v", err)
		step.ElapsedMs = time.Since(start).Seconds() * 1000
		return step
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	sc.BackendResp = respBody
	sc.BackendStatus = resp.StatusCode

	step.Output = map[string]interface{}{
		"status_code": resp.StatusCode,
		"body_size":   len(respBody),
		"body_preview": truncateString(string(respBody), 300),
	}
	step.ElapsedMs = time.Since(start).Seconds() * 1000
	return step
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
