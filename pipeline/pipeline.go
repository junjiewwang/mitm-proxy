package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Pipeline 请求处理管线
// 按阶段顺序执行：解析 → 拆包 → 认证注入 → 转发 → 组包
type Pipeline struct {
	// 必选阶段
	Forwarder Forwarder

	// 可选阶段（nil/空 表示跳过）
	RequestTransformers  []RequestTransformer
	AuthInjectors        []AuthInjector
	ResponseTransformers []ResponseTransformer
}

// Execute 执行完整 Pipeline，返回构建好的 HTTP 响应
func (p *Pipeline) Execute(req *http.Request, body []byte) *http.Response {
	ctx := &PipelineContext{
		OriginalRequest: req,
		RawBody:         body,
		Metadata:        make(map[string]any),
	}

	// 1. 解析请求体
	if len(body) > 0 {
		if err := json.Unmarshal(body, &ctx.ParsedRequest); err != nil {
			return buildErrorResponse(400, "parse request body failed: "+err.Error())
		}
	}
	if ctx.ParsedRequest == nil {
		ctx.ParsedRequest = make(map[string]any)
	}

	// 2. 拆包（RequestTransformers 链式执行）
	for _, t := range p.RequestTransformers {
		if err := t.TransformRequest(ctx); err != nil {
			return buildErrorResponse(400, "transform request failed: "+err.Error())
		}
	}
	// 如果没有配置 RequestTransformer，BackendRequest 直接用 ParsedRequest
	if ctx.BackendRequest == nil {
		ctx.BackendRequest = ctx.ParsedRequest
	}

	// 3. 认证注入（AuthInjectors 链式执行）
	for _, a := range p.AuthInjectors {
		if err := a.InjectAuth(ctx); err != nil {
			return buildErrorResponse(500, "inject auth failed: "+err.Error())
		}
	}

	// 4. 转发
	if p.Forwarder == nil {
		return buildErrorResponse(500, "pipeline forwarder not configured")
	}
	if err := p.Forwarder.Forward(ctx); err != nil {
		return buildErrorResponse(502, "forward failed: "+err.Error())
	}

	// 5. 组包（ResponseTransformers 链式执行）
	ctx.FinalResp = ctx.BackendResp // 默认：原样返回
	for _, t := range p.ResponseTransformers {
		if err := t.TransformResponse(ctx); err != nil {
			return buildErrorResponse(500, "transform response failed: "+err.Error())
		}
	}

	return buildJSONResponse(200, ctx.FinalResp)
}

// buildErrorResponse 构建错误 HTTP 响应
func buildErrorResponse(statusCode int, message string) *http.Response {
	body := fmt.Sprintf(`{"code":-1,"message":"%s"}`, message)
	return &http.Response{
		StatusCode:    statusCode,
		Status:        http.StatusText(statusCode),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

// buildJSONResponse 构建 JSON HTTP 响应
func buildJSONResponse(statusCode int, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    statusCode,
		Status:        http.StatusText(statusCode),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"application/json; charset=utf-8"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}
