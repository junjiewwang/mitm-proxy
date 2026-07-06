// Package event 定义代理事件数据结构，供 dispatch、webui 等模块共享。
// 位于 core/ 下层，避免领域层依赖 UI 层。
package event

// Event 代理请求事件（SSE 推送 / API 返回共用）
type Event struct {
	Type      string `json:"type"`
	Time      string `json:"time"`
	Method    string `json:"method"`
	URL       string `json:"url"`
	Status    int    `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
	Script    string `json:"script"`
	RuleType  string `json:"rule_type"`
	Target    string `json:"target"`
	Error     string `json:"error,omitempty"`
	// Inspectors 字段
	ReqHeaders    map[string]string `json:"req_headers,omitempty"`
	ReqBody       string            `json:"req_body,omitempty"`
	RespHeaders   map[string]string `json:"resp_headers,omitempty"`
	RespBody      string            `json:"resp_body,omitempty"`
	ContentType   string            `json:"content_type,omitempty"`
	ContentLength int64             `json:"content_length,omitempty"`
}
