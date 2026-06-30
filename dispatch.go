package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"time"

	"mitm-proxy/adapter"
	"mitm-proxy/webui"
)

// processorDispatcher 处理器调度器
// 根据 MatchResult 的 RuleType 和 options 分发到不同处理逻辑
// 实现 RequestProcessor 接口
type processorDispatcher struct {
	registry     *adapter.Registry          // 适配器注册表（按名称查找）
	proxyTimeout time.Duration
	broadcaster  *webui.EventBroadcaster
}

func newProcessorDispatcher(cfg *Config, registry *adapter.Registry, broadcaster *webui.EventBroadcaster) *processorDispatcher {
	timeout := cfg.Backend.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &processorDispatcher{
		registry:     registry,
		proxyTimeout: timeout,
		broadcaster:  broadcaster,
	}
}

// Process 实现 RequestProcessor 接口
func (d *processorDispatcher) Process(req *http.Request, body []byte, match *MatchResult) *http.Response {
	start := time.Now()

	// CORS 预检：OPTIONS 请求直接返回 204
	if match.CORS && req.Method == http.MethodOptions {
		resp := d.buildCORSPreflightResponse(req)
		d.broadcastEvent(req, match, resp.StatusCode, time.Since(start), "")
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
	case RuleTypePipeline:
		resp = d.processPipeline(req, body, match)
	case RuleTypeExact:
		resp = d.processProxy(req, body, match)
	default:
		resp = buildErrorResponse(http.StatusInternalServerError, "unknown rule type")
	}

	errMsg := ""
	if resp.StatusCode >= 400 {
		errMsg = resp.Status
	}
	d.broadcastEvent(req, match, resp.StatusCode, time.Since(start), errMsg)
	return resp
}

// broadcastEvent 向 SSE 客户端广播请求处理事件
func (d *processorDispatcher) broadcastEvent(req *http.Request, match *MatchResult, statusCode int, elapsed time.Duration, errMsg string) {
	if d.broadcaster == nil {
		return
	}

	ruleType := "proxy"
	switch match.RuleType {
	case RuleTypePipeline:
		ruleType = "pipeline"
	case RuleTypeExact:
		ruleType = "exact"
	}

	d.broadcaster.Broadcast(&webui.Event{
		Type:      "match",
		Time:      time.Now().Format(time.RFC3339),
		Method:    req.Method,
		URL:       req.URL.String(),
		Status:    statusCode,
		LatencyMs: elapsed.Milliseconds(),
		Script:    match.ScriptName,
		RuleType:  ruleType,
		Target:    match.Target,
		Error:     errMsg,
	})
}

// processProxy URL 重写 + 反向代理
func (d *processorDispatcher) processProxy(req *http.Request, body []byte, match *MatchResult) *http.Response {
	log.Printf("[Dispatch] Proxy: %s %s → %s", req.Method, req.URL.String(), match.Target)

	resp := proxyRequest(req, body, match.Target, d.proxyTimeout)

	// 附加 CORS 头（如果规则标记了 @cors）
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

// processPipeline 通过适配器注册表查找并调用对应的 Pipeline
func (d *processorDispatcher) processPipeline(req *http.Request, body []byte, match *MatchResult) *http.Response {
	log.Printf("[Dispatch] Pipeline: %s %s → pipeline://%s", req.Method, req.URL.String(), match.Adapter)

	// 从注册表按名称查找适配器
	adapterInstance, ok := d.registry.Get(match.Adapter)
	if !ok {
		log.Printf("[Dispatch] adapter not found: %s", match.Adapter)
		return buildErrorResponse(http.StatusInternalServerError, "adapter not found: "+match.Adapter)
	}

	// 调用适配器的 Pipeline 执行完整流程：拆包 → 认证注入 → 转发 → 组包
	pipelineResp := adapterInstance.GetPipeline().Execute(req, body)

	// 将 Pipeline 返回的 *http.Response 读取到内存（确保可在 writer goroutine 中安全写出）
	respBody, err := io.ReadAll(pipelineResp.Body)
	if err != nil {
		log.Printf("[Dispatch] read pipeline response failed: %v", err)
		return buildErrorResponse(http.StatusInternalServerError, "read pipeline response failed")
	}
	pipelineResp.Body.Close()

	resp := &http.Response{
		StatusCode:    pipelineResp.StatusCode,
		Status:        http.StatusText(pipelineResp.StatusCode),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(respBody)),
		ContentLength: int64(len(respBody)),
	}
	resp.Header.Set("Content-Type", "application/json; charset=utf-8")

	if match.CORS {
		setCORSHeaders(resp.Header, req)
	}
	return resp
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
