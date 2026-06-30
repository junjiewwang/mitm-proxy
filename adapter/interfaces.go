package adapter

import "mitm-proxy/pipeline"

// Adapter 适配器接口
// 适配器负责组装 Pipeline 各阶段实例，提供给 PipelineProcessor 调用
type Adapter interface {
	// Name 适配器名称（被 pipeline:// 规则引用）
	Name() string
	// Init 初始化适配器（传入配置）
	Init(rawConfig map[string]any) error
	// GetPipeline 返回组装好的 Pipeline
	GetPipeline() *pipeline.Pipeline
	// Close 清理资源
	Close() error
}
