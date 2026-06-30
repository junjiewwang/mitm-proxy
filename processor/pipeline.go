package processor

import (
	"log"
	"net/http"

	"mitm-proxy/pipeline"
	"mitm-proxy/proxy"
)

// Adapter Pipeline 适配器接口
// 由 adapter/ 包中的具体适配器实现
type Adapter interface {
	// Name 适配器名称（被 pipeline:// 规则引用）
	Name() string
	// GetPipeline 返回组装好的 Pipeline
	GetPipeline() *pipeline.Pipeline
}

// PipelineProcessor 调用命名适配器的 Pipeline
// 实现 RequestProcessor 接口
type PipelineProcessor struct {
	adapters map[string]Adapter // 适配器名称 → 适配器实例
}

// NewPipelineProcessor 创建 Pipeline 处理器
func NewPipelineProcessor() *PipelineProcessor {
	return &PipelineProcessor{
		adapters: make(map[string]Adapter),
	}
}

// RegisterAdapter 注册适配器
func (p *PipelineProcessor) RegisterAdapter(adapter Adapter) {
	p.adapters[adapter.Name()] = adapter
}

// Process 根据 match.Adapter 调用对应适配器的 Pipeline
func (p *PipelineProcessor) Process(req *http.Request, body []byte, match *proxy.MatchResult) *http.Response {
	adapter, ok := p.adapters[match.Adapter]
	if !ok {
		log.Printf("[PipelineProcessor] adapter not found: %s", match.Adapter)
		return buildErrorResp(http.StatusInternalServerError, "adapter not found: "+match.Adapter)
	}

	log.Printf("[PipelineProcessor] %s %s → pipeline://%s", req.Method, req.URL.String(), match.Adapter)
	return adapter.GetPipeline().Execute(req, body)
}
