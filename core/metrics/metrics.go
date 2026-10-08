// Package metrics 提供 Prometheus 自监控指标。
//
// 包含两类指标：
//   - runtime 指标（GoCollector + ProcessCollector，开箱即用）：go_goroutines、go_memstats_*、process_* 等
//   - 业务指标（mitm_proxy_ 前缀）：请求数、延迟、连接数、规则命中数、错误数
//
// 使用自定义 Registry（而非全局 default registry），便于测试和外部复用。
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// registry 使用独立注册表，避免污染全局默认注册表，也便于测试隔离
var registry = prometheus.NewRegistry()

// 业务指标定义
var (
	// RequestsTotal 请求总数，按类型（script/proxy/tunnel/error）区分
	RequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mitm_proxy_requests_total",
			Help: "Total number of proxied requests by type.",
		},
		[]string{"type"},
	)

	// RequestDuration 代理层请求耗时（秒）
	RequestDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "mitm_proxy_request_duration_seconds",
			Help:    "Duration of proxy requests in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)

	// UpstreamDuration 上游转发耗时（秒）
	UpstreamDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "mitm_proxy_upstream_duration_seconds",
			Help:    "Duration of upstream forwarding in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)

	// MITMConnections MITM 连接数（active/evicted/max）
	MITMConnections = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "mitm_proxy_mitm_connections",
			Help: "Current MITM connection count by state.",
		},
		[]string{"state"},
	)

	// RuleHits 规则命中数，按脚本 ID 区分
	RuleHits = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mitm_proxy_rule_hits_total",
			Help: "Total rule hits by script.",
		},
		[]string{"script"},
	)

	// ErrorsTotal 错误总数
	ErrorsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "mitm_proxy_errors_total",
			Help: "Total number of proxy errors.",
		},
	)
)

func init() {
	// 注册业务指标
	registry.MustRegister(
		RequestsTotal,
		RequestDuration,
		UpstreamDuration,
		MITMConnections,
		RuleHits,
		ErrorsTotal,
	)
	// 注册 runtime 指标（goroutine、内存、GC、进程资源等）
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// Handler 返回 /metrics 端点的 http.Handler
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

// ========================================
// 便捷记录方法（业务代码调用）
// ========================================

// RecordRequest 记录一条请求（按类型）
func RecordRequest(rtype string) {
	RequestsTotal.WithLabelValues(rtype).Inc()
}

// ObserveRequestDuration 记录代理层请求耗时（秒）
func ObserveRequestDuration(seconds float64) {
	RequestDuration.Observe(seconds)
}

// ObserveUpstreamDuration 记录上游转发耗时（秒）
func ObserveUpstreamDuration(seconds float64) {
	UpstreamDuration.Observe(seconds)
}

// SetMITMConnections 设置 MITM 连接数（state ∈ active/evicted/max）
func SetMITMConnections(state string, value float64) {
	MITMConnections.WithLabelValues(state).Set(value)
}

// RecordRuleHit 记录一条规则命中（按脚本 ID）
func RecordRuleHit(script string) {
	RuleHits.WithLabelValues(script).Inc()
}

// RecordError 记录一条错误
func RecordError() {
	ErrorsTotal.Inc()
}
