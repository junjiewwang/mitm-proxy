package yunapi

import (
	"mitm-proxy/pipeline"
)

// AuthInjector 注入腾讯云认证字段：Uin、SubAccountUin、AppId
// 优先使用请求中携带的值，fallback 到配置的 mock 值
type AuthInjector struct {
	cfg AuthConfig
}

// NewAuthInjector 创建认证注入器
func NewAuthInjector(cfg AuthConfig) *AuthInjector {
	return &AuthInjector{cfg: cfg}
}

// InjectAuth 实现 pipeline.AuthInjector 接口
func (a *AuthInjector) InjectAuth(ctx *pipeline.PipelineContext) error {
	if ctx.BackendRequest == nil {
		ctx.BackendRequest = make(map[string]any)
	}

	uin := a.cfg.Uin
	subAccountUin := a.cfg.SubAccountUin

	// 优先使用请求中携带的值（通过 Metadata 由 RequestTransformer 传递）
	if ownerUin, ok := ctx.Metadata["ownerUin"].(string); ok && ownerUin != "" {
		uin = ownerUin
	}
	if reqUin, ok := ctx.Metadata["uin"].(string); ok && reqUin != "" {
		subAccountUin = reqUin
	}

	ctx.BackendRequest["Uin"] = uin
	ctx.BackendRequest["SubAccountUin"] = subAccountUin
	ctx.BackendRequest["AppId"] = a.cfg.AppId

	return nil
}
