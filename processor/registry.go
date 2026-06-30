package processor

import (
	"net/http"

	"mitm-proxy/proxy"
)

// Registry 处理器注册表
// 根据规则类型分发到对应的 RequestProcessor
type Registry struct {
	processors map[proxy.RuleType]proxy.RequestProcessor
}

// NewRegistry 创建处理器注册表
func NewRegistry() *Registry {
	return &Registry{
		processors: make(map[proxy.RuleType]proxy.RequestProcessor),
	}
}

// Register 注册处理器
func (r *Registry) Register(ruleType proxy.RuleType, processor proxy.RequestProcessor) {
	r.processors[ruleType] = processor
}

// Get 获取处理器
func (r *Registry) Get(ruleType proxy.RuleType) (proxy.RequestProcessor, bool) {
	p, ok := r.processors[ruleType]
	return p, ok
}

// Process 根据 MatchResult 的 RuleType 分发到对应处理器
func (r *Registry) Process(req *http.Request, body []byte, match *proxy.MatchResult) *http.Response {
	p, ok := r.processors[match.RuleType]
	if !ok {
		return buildErrorResp(http.StatusInternalServerError, "no processor for rule type")
	}
	return p.Process(req, body, match)
}
