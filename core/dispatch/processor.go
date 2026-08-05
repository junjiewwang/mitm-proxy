package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"mitm-proxy/core/proxy"
	"mitm-proxy/core/script"
	"mitm-proxy/rule"
)

// ScriptAccessor 脚本内容访问接口（由 RuleManager 实现）
type ScriptAccessor interface {
	Get(id string) (*rule.RuleScript, bool)
}

// ProcessorDispatcher 按规则类型分发请求到策略处理器
// 实现 proxy.RequestProcessor 接口
type ProcessorDispatcher struct {
	ScriptRuntime  *script.Runtime
	ScriptAccessor ScriptAccessor
	HTTPClient     *http.Client
	Broadcaster    Broadcaster

	mu       sync.RWMutex
	handlers map[proxy.RuleType]RuleHandler
}

// NewProcessorDispatcher 创建处理器调度器并注册内置策略
func NewProcessorDispatcher(runtime *script.Runtime, accessor ScriptAccessor, broadcaster Broadcaster) *ProcessorDispatcher {
	d := &ProcessorDispatcher{
		ScriptRuntime:  runtime,
		ScriptAccessor: accessor,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    100,
				IdleConnTimeout: 90 * time.Second,
			},
		},
		Broadcaster: broadcaster,
		handlers:    make(map[proxy.RuleType]RuleHandler),
	}
	RegisterBuiltinHandlers(d)
	return d
}

// RegisterHandler 注册/替换规则处理策略（线程安全）
func (d *ProcessorDispatcher) RegisterHandler(rt proxy.RuleType, h RuleHandler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.handlers[rt] = h
}

// Process 实现 proxy.RequestProcessor 接口
func (d *ProcessorDispatcher) Process(req *http.Request, body []byte, match *proxy.MatchResult) *http.Response {
	start := time.Now()

	if match.CORS && req.Method == http.MethodOptions {
		resp := d.buildCORSPreflightResponse(req)
		respBody := readAndReplaceBody(resp)
		d.broadcast(buildOpts(req, body, resp, respBody, match, time.Since(start)))
		return resp
	}

	// 策略分发
	d.mu.RLock()
	handler, ok := d.handlers[match.RuleType]
	d.mu.RUnlock()

	var resp *http.Response
	if !ok {
		resp = proxy.BuildErrorResponse(http.StatusInternalServerError, "unknown rule type")
	} else {
		resp = handler.Handle(d, req, body, match)
	}

	respBody := readAndReplaceBody(resp)
	d.broadcast(buildOpts(req, body, resp, respBody, match, time.Since(start)))
	return resp
}

// ========================================
// 内部共享方法（供 RuleHandler 策略调用）
// ========================================

func (d *ProcessorDispatcher) buildScriptResponse(req *http.Request, sc *script.ScriptContext, match *proxy.MatchResult) *http.Response {
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
		proxy.SetCORSHeaders(resp.Header, req)
	}
	return resp
}

func (d *ProcessorDispatcher) forwardToBackend(sc *script.ScriptContext) error {
	reqBody, err := json.Marshal(sc.BackendRequest)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest("POST", sc.BackendURL, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range sc.BackendHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := d.HTTPClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	sc.BackendResp = respBody
	sc.BackendStatus = resp.StatusCode
	return nil
}

func (d *ProcessorDispatcher) buildCORSPreflightResponse(req *http.Request) *http.Response {
	resp := proxy.BuildErrorResponse(http.StatusNoContent, "")
	resp.StatusCode = http.StatusNoContent
	resp.Status = http.StatusText(http.StatusNoContent)
	resp.ContentLength = 0
	resp.Header = make(http.Header)
	proxy.SetCORSHeaders(resp.Header, req)
	resp.Header.Set("Access-Control-Max-Age", "86400")
	return resp
}

func (d *ProcessorDispatcher) broadcast(opts BuildOptions) {
	if d.Broadcaster == nil {
		return
	}
	d.Broadcaster.Broadcast(BuildEvent(opts))
}

// ========================================
// 包内辅助函数
// ========================================

func readAndReplaceBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	// 设置 body 读取超时，防止上游 chunked 响应不完整导致永远阻塞
	// 30s 与 pooledClient.Timeout 保持一致
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(resp.Body)
		ch <- result{data, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			log.Printf("[Dispatch] readAndReplaceBody error: %v", r.err)
		}
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(r.data))
		return r.data
	case <-ctx.Done():
		log.Printf("[Dispatch] readAndReplaceBody timeout after 30s, aborting body read")
		resp.Body.Close()
		resp.Body = io.NopCloser(strings.NewReader(""))
		return nil
	}
}

func buildOpts(req *http.Request, reqBody []byte, resp *http.Response, respBody []byte, match *proxy.MatchResult, elapsed time.Duration) BuildOptions {
	opts := BuildOptions{
		Req:      req,
		ReqBody:  reqBody,
		Resp:     resp,
		RespBody: respBody,
		Match:    MatchInfo{ScriptName: match.ScriptName, RuleType: match.TypeName(), Target: match.Target},
		Elapsed:  elapsed,
	}
	if req.Header.Get("X-Replay") == "true" {
		opts.Tags = append(opts.Tags, TagReplay)
	}
	return opts
}
