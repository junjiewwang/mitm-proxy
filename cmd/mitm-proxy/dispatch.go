package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/dop251/goja"

	"mitm-proxy/core/script"
	"mitm-proxy/rule"
	"mitm-proxy/webui"
)

// ScriptAccessor 脚本内容访问接口（由 RuleManager 实现）
type ScriptAccessor interface {
	Get(id string) (*rule.RuleScript, bool)
}

// processorDispatcher 处理器调度器
// 根据 MatchResult 的 RuleType 和 options 分发到不同处理逻辑
// 实现 RequestProcessor 接口
type processorDispatcher struct {
	scriptRuntime  *script.Runtime  // JS 执行引擎
	scriptAccessor ScriptAccessor   // 脚本内容访问器（提供预编译的 JS Program）
	httpClient     *http.Client     // HTTP 转发客户端
	broadcaster    *webui.EventBroadcaster
}

func newProcessorDispatcher(
	scriptRuntime *script.Runtime,
	scriptAccessor ScriptAccessor,
	broadcaster *webui.EventBroadcaster,
) *processorDispatcher {
	return &processorDispatcher{
		scriptRuntime:  scriptRuntime,
		scriptAccessor: scriptAccessor,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  false,
			},
		},
		broadcaster: broadcaster,
	}
}

// Process 实现 RequestProcessor 接口
func (d *processorDispatcher) Process(req *http.Request, body []byte, match *MatchResult) *http.Response {
	start := time.Now()

	// CORS 预检：OPTIONS 请求直接返回 204
	if match.CORS && req.Method == http.MethodOptions {
		resp := d.buildCORSPreflightResponse(req)
		// 读取响应体（用于广播），在不影响 resp.Body 的前提下
		var respBodyBytes []byte
		if resp != nil && resp.Body != nil {
			respBodyBytes, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))
		}
		d.broadcastEvent(req, body, resp, respBodyBytes, match, time.Since(start))
		return resp
	}

	var resp *http.Response
	switch match.RuleType {
	case RuleTypeProxy:
		if match.Stream {
			resp = d.processStream(req, body, match)
		} else {
			resp = d.processProxy(req, body, match)
		}
	case RuleTypeExact:
		resp = d.processProxy(req, body, match)
	case RuleTypeScript:
		resp = d.processScript(req, body, match)
	case RuleTypePipeline:
		// V1 pipeline:// 已废弃，返回错误提示迁移到 script://
		log.Printf("[Dispatch] WARNING: pipeline:// is deprecated, use script:// instead")
		resp = buildErrorResponse(http.StatusGone,
			"pipeline:// is no longer supported. Please migrate your rules to use script:// protocol. "+
				"See docs/refactor-v2-roadmap.md for migration guide.")
	default:
		resp = buildErrorResponse(http.StatusInternalServerError, "unknown rule type")
	}

	// 读取响应体（用于广播），在不影响 resp.Body 的前提下
	var respBodyBytes []byte
	if resp != nil && resp.Body != nil {
		respBodyBytes, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))
	}

	d.broadcastEvent(req, body, resp, respBodyBytes, match, time.Since(start))
	return resp
}

// broadcastEvent 向 SSE 客户端广播请求处理事件
func (d *processorDispatcher) broadcastEvent(req *http.Request, reqBody []byte, resp *http.Response, respBody []byte, match *MatchResult, elapsed time.Duration) {
	if d.broadcaster == nil {
		return
	}

	ruleType := "proxy"
	switch match.RuleType {
	case RuleTypePipeline:
		ruleType = "pipeline"
	case RuleTypeExact:
		ruleType = "exact"
	case RuleTypeScript:
		ruleType = "script"
	}

	// 状态码和错误信息从 resp 推导
	var statusCode int
	var errMsg string
	if resp != nil {
		statusCode = resp.StatusCode
		if resp.StatusCode >= 400 {
			errMsg = resp.Status
		}
	}

	// 提取请求头
	reqHeaders := make(map[string]string)
	for k, v := range req.Header {
		reqHeaders[k] = strings.Join(v, ", ")
	}

	// 截断请求体 (4096 bytes)
	var reqBodyStr string
	if len(reqBody) > 0 {
		if len(reqBody) > 4096 {
			reqBodyStr = string(reqBody[:4096]) + "\n...(truncated)"
		} else {
			reqBodyStr = string(reqBody)
		}
	}

	// 提取响应头
	respHeaders := make(map[string]string)
	var contentType string
	var contentLength int64
	if resp != nil {
		for k, v := range resp.Header {
			respHeaders[k] = strings.Join(v, ", ")
		}
		contentType = resp.Header.Get("Content-Type")
		contentLength = resp.ContentLength
		if contentLength == -1 {
			contentLength = int64(len(respBody))
		}
	}

	// 截断响应体 (4096 bytes)
	var respBodyStr string
	if len(respBody) > 0 {
		if len(respBody) > 4096 {
			respBodyStr = string(respBody[:4096]) + "\n...(truncated)"
		} else {
			respBodyStr = string(respBody)
		}
	}

	eventType := "match"
	if req.Header.Get("X-Replay") == "true" {
		eventType = "replay"
	}
	d.broadcaster.Broadcast(&webui.Event{
		Type:          eventType,
		Time:          time.Now().Format(time.RFC3339),
		Method:        req.Method,
		URL:           req.URL.String(),
		Status:        statusCode,
		LatencyMs:     elapsed.Milliseconds(),
		Script:        match.ScriptName,
		RuleType:      ruleType,
		Target:        match.Target,
		Error:         errMsg,
		ReqHeaders:    reqHeaders,
		ReqBody:       reqBodyStr,
		RespHeaders:   respHeaders,
		RespBody:      respBodyStr,
		ContentType:   contentType,
		ContentLength: contentLength,
	})
}

// processProxy URL 重写 + 反向代理
func (d *processorDispatcher) processProxy(req *http.Request, body []byte, match *MatchResult) *http.Response {
	log.Printf("[Dispatch] Proxy: %s %s → %s", req.Method, req.URL.String(), match.Target)
	resp := proxyRequest(req, body, match.Target, d.httpClient.Timeout)
	if match.CORS {
		setCORSHeaders(resp.Header, req)
	}
	return resp
}

// processStream 流式代理（SSE/chunked）
func (d *processorDispatcher) processStream(req *http.Request, body []byte, match *MatchResult) *http.Response {
	log.Printf("[Dispatch] Stream: %s %s → %s", req.Method, req.URL.String(), match.Target)
	return streamRequest(req, body, match.Target)
}

// processScript JS 脚本处理：onRequest → HTTP 转发 → onResponse
func (d *processorDispatcher) processScript(req *http.Request, body []byte, match *MatchResult) *http.Response {
	// 1. 解析 script:// 引用：self → 当前脚本 ID
	scriptID := match.ScriptRef
	if scriptID == "" || scriptID == "self" {
		scriptID = match.ScriptID
	}

	log.Printf("[Dispatch] Script: %s %s → script://%s (resolved: %s)",
		req.Method, req.URL.String(), match.ScriptRef, scriptID)

	// 2. 从 RuleManager 获取脚本的 Config + Scripts
	rs, ok := d.scriptAccessor.Get(scriptID)
	if !ok {
		log.Printf("[Dispatch] script not found: %s", scriptID)
		return buildErrorResponse(http.StatusInternalServerError, "script not found: "+scriptID)
	}

	// 3. 构建脚本上下文
	sc := script.NewScriptContext(req, body, rs.Config)

	// ===== 阶段一：onRequest =====
	if prog, ok := rs.CompiledPrograms["onRequest"]; ok {
		if err := d.executePhase(prog, sc); err != nil {
			log.Printf("[Dispatch] onRequest execution failed: %v", err)
			return buildErrorResponse(http.StatusInternalServerError, "onRequest execution failed: "+err.Error())
		}
	} else {
		log.Printf("[Dispatch] no onRequest script for %s, passing through", scriptID)
	}

	// 如果 onRequest 直接设置了 finalResp（短路，不转发后端）
	if len(sc.FinalResp) > 0 && sc.BackendURL == "" {
		return d.buildScriptResponse(req, sc, match)
	}

	// ===== HTTP 转发 =====
	if sc.BackendURL != "" {
		if err := d.forwardToBackend(sc); err != nil {
			log.Printf("[Dispatch] backend forward failed: %v", err)
			return buildErrorResponse(http.StatusBadGateway, "backend forward failed: "+err.Error())
		}
	} else {
		return buildErrorResponse(http.StatusInternalServerError, "onRequest must set ctx.backendURL or ctx.finalResp")
	}

	// ===== 阶段二：onResponse =====
	if prog, ok := rs.CompiledPrograms["onResponse"]; ok {
		if err := d.executePhase(prog, sc); err != nil {
			log.Printf("[Dispatch] onResponse execution failed: %v", err)
			return buildErrorResponse(http.StatusInternalServerError, "onResponse execution failed: "+err.Error())
		}
	}

	// 7. 构建 HTTP 响应
	return d.buildScriptResponse(req, sc, match)
}

// executePhase 执行单个 JS 阶段（acquire → setup → execute → sync → release）
func (d *processorDispatcher) executePhase(program *goja.Program, sc *script.ScriptContext) error {
	vm := d.scriptRuntime.Acquire()
	defer d.scriptRuntime.Release(vm)

	if err := sc.InjectSetup()(vm); err != nil {
		return err
	}

	// 运行编译后的脚本
	_, err := vm.RunProgram(program)
	if err != nil {
		return err
	}

	// 同步 VM 中 ctx 的修改回 ScriptContext
	return sc.SyncFromVM(vm)
}

// buildScriptResponse 从 ScriptContext 构建 HTTP 响应
func (d *processorDispatcher) buildScriptResponse(req *http.Request, sc *script.ScriptContext, match *MatchResult) *http.Response {
	respBody := sc.FinalResp
	statusCode := sc.FinalStatus

	if len(respBody) == 0 {
		respBody = sc.BackendResp
	}
	if statusCode == 0 {
		statusCode = sc.BackendStatus
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
	}

	resp := &http.Response{
		StatusCode:    statusCode,
		Status:        http.StatusText(statusCode),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(respBody)),
		ContentLength: int64(len(respBody)),
	}
	resp.Header.Set("Content-Type", "application/json; charset=utf-8")

	if match.CORS && req != nil {
		setCORSHeaders(resp.Header, req)
	}

	return resp
}

// forwardToBackend 将脚本构建的请求转发到后端
func (d *processorDispatcher) forwardToBackend(sc *script.ScriptContext) error {
	// 序列化请求体
	reqBody, err := json.Marshal(sc.BackendRequest)
	if err != nil {
		return err
	}

	// 创建转发请求
	httpReq, err := http.NewRequest("POST", sc.BackendURL, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range sc.BackendHeaders {
		httpReq.Header.Set(k, v)
	}

	// 发送请求
	resp, err := d.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 读取响应
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	sc.BackendResp = respBody
	sc.BackendStatus = resp.StatusCode
	return nil
}

// buildCORSPreflightResponse 构建 CORS 预检响应
func (d *processorDispatcher) buildCORSPreflightResponse(req *http.Request) *http.Response {
	resp := buildErrorResponse(http.StatusNoContent, "")
	resp.StatusCode = http.StatusNoContent
	resp.Status = http.StatusText(http.StatusNoContent)
	resp.ContentLength = 0
	resp.Header = make(http.Header)
	setCORSHeaders(resp.Header, req)
	resp.Header.Set("Access-Control-Max-Age", "86400")
	log.Printf("[Dispatch] CORS preflight: %s %s → 204", req.Method, req.URL.Path)
	return resp
}
