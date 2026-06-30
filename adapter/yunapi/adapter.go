package yunapi

import (
	"log"

	"mitm-proxy/pipeline"
)

// Adapter yunapi 适配器
// 将腾讯云 yunapi 前端请求格式转换为 TencentCloud API 3.0 格式
// 组装 Pipeline 阶段：拆包(RequestTransformer) → 认证(AuthInjector) → 转发(Forwarder) → 组包(ResponseTransformer)
type Adapter struct {
	cfg      *Config
	pipeline *pipeline.Pipeline
}

// New 创建 yunapi 适配器
func New(cfg *Config) *Adapter {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	backendURL := cfg.Backend.URL + cfg.Backend.APIPath

	p := &pipeline.Pipeline{
		Forwarder: pipeline.NewHTTPForwarder(cfg.Backend.Timeout),
		RequestTransformers: []pipeline.RequestTransformer{
			NewRequestTransformer(cfg.RegionMap),
		},
		AuthInjectors: []pipeline.AuthInjector{
			NewAuthInjector(cfg.Auth),
		},
		ResponseTransformers: []pipeline.ResponseTransformer{
			NewResponseTransformer(),
		},
	}

	// 设置 Pipeline 的 BackendURL（通过 Forwarder 的默认 URL）
	// Pipeline 执行时如果 ctx.BackendURL 为空，需要在 RequestTransformer 中设置
	// 这里通过包装 Forwarder 来注入默认 URL
	p.Forwarder = &backendURLForwarder{
		inner:      p.Forwarder,
		backendURL: backendURL,
	}

	log.Printf("[Adapter] yunapi adapter initialized: backend=%s", backendURL)

	return &Adapter{
		cfg:      cfg,
		pipeline: p,
	}
}

// Name 实现 adapter.Adapter 接口
func (a *Adapter) Name() string {
	return "yunapi"
}

// Init 实现 adapter.Adapter 接口
func (a *Adapter) Init(rawConfig map[string]any) error {
	// 配置已在 New 中通过强类型传入，此处预留用于动态配置场景
	return nil
}

// GetPipeline 实现 adapter.Adapter 接口
func (a *Adapter) GetPipeline() *pipeline.Pipeline {
	return a.pipeline
}

// Close 实现 adapter.Adapter 接口
func (a *Adapter) Close() error {
	return nil
}

// backendURLForwarder 包装 Forwarder，自动注入默认 BackendURL
type backendURLForwarder struct {
	inner      pipeline.Forwarder
	backendURL string
}

func (f *backendURLForwarder) Forward(ctx *pipeline.PipelineContext) error {
	if ctx.BackendURL == "" {
		ctx.BackendURL = f.backendURL
	}
	return f.inner.Forward(ctx)
}
