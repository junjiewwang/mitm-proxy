package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestHandlerExposesRuntimeAndBusinessMetrics 验证 /metrics 端点同时暴露 runtime 和业务指标
func TestHandlerExposesRuntimeAndBusinessMetrics(t *testing.T) {
	// 记录一些业务指标
	RecordRequest("script")
	RecordRequest("proxy")
	RecordRuleHit("apm-dev")
	RecordError()
	ObserveRequestDuration(0.05)
	ObserveUpstreamDuration(0.03)
	SetMITMConnections("active", 7)

	// 通过 Handler 抓取指标
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	// 断言 runtime 指标存在
	if !strings.Contains(body, "go_goroutines") {
		t.Error("missing go_goroutines metric")
	}
	if !strings.Contains(body, "go_memstats_alloc_bytes") {
		t.Error("missing go_memstats_alloc_bytes metric")
	}
	if !strings.Contains(body, "process_resident_memory_bytes") {
		t.Error("missing process_resident_memory_bytes metric")
	}

	// 断言业务指标存在且 label 正确
	if !strings.Contains(body, `mitm_proxy_requests_total{type="script"} 1`) {
		t.Error("missing mitm_proxy_requests_total{type=script} = 1")
	}
	if !strings.Contains(body, `mitm_proxy_requests_total{type="proxy"} 1`) {
		t.Error("missing mitm_proxy_requests_total{type=proxy} = 1")
	}
	if !strings.Contains(body, `mitm_proxy_rule_hits_total{script="apm-dev"} 1`) {
		t.Error("missing mitm_proxy_rule_hits_total{script=apm-dev} = 1")
	}
	if !strings.Contains(body, "mitm_proxy_errors_total 1") {
		t.Error("missing mitm_proxy_errors_total = 1")
	}
	if !strings.Contains(body, `mitm_proxy_mitm_connections{state="active"} 7`) {
		t.Error("missing mitm_proxy_mitm_connections{state=active} = 7")
	}
	if !strings.Contains(body, "mitm_proxy_request_duration_seconds_count 1") {
		t.Error("missing mitm_proxy_request_duration_seconds_count")
	}
}

// TestRegistryIsolatedFromGlobalDefault 验证使用独立 registry，不污染全局默认
func TestRegistryIsolatedFromGlobalDefault(t *testing.T) {
	// 全局 default registry 不应包含业务指标（因为使用了自定义 registry）
	globalGathered, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather default registry: %v", err)
	}
	for _, mf := range globalGathered {
		if strings.HasPrefix(mf.GetName(), "mitm_proxy_") {
			t.Errorf("business metric %s leaked into global default registry", mf.GetName())
		}
	}
}
