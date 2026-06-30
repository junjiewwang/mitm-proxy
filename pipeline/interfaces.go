package pipeline

import "net/http"

// PipelineContext 贯穿整个 Pipeline 的上下文，各阶段通过它传递数据
type PipelineContext struct {
	// 原始请求信息（只读）
	OriginalRequest *http.Request
	RawBody         []byte

	// Pipeline 各阶段共享的结构化数据（可读写）
	ParsedRequest  map[string]any // 解析后的请求体（JSON 解析结果）
	BackendRequest map[string]any // 转换后的后端请求体（拆包结果）
	BackendURL     string         // 转发目标 URL
	BackendResp    []byte         // 后端原始响应
	FinalResp      []byte         // 最终返回给客户端的响应

	// 元数据（跨阶段传递附加信息）
	Metadata map[string]any
}

// RequestTransformer 请求转换器（拆包阶段）
// 将客户端发来的原始请求体转换为后端期望的格式
type RequestTransformer interface {
	TransformRequest(ctx *PipelineContext) error
}

// AuthInjector 认证信息注入器（可选阶段）
// 向后端请求中注入认证、鉴权相关字段
type AuthInjector interface {
	InjectAuth(ctx *PipelineContext) error
}

// Forwarder 后端转发器
// 将转换后的请求发送到后端服务并获取响应
type Forwarder interface {
	Forward(ctx *PipelineContext) error
}

// ResponseTransformer 响应转换器（组包阶段）
// 将后端响应转换为客户端期望的格式
type ResponseTransformer interface {
	TransformResponse(ctx *PipelineContext) error
}
