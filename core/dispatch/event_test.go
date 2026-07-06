package dispatch

import (
	"net/http"
	"testing"
	"time"
)

func TestBuildEventBasic(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://example.com/api", nil)
	req.Header.Set("Content-Type", "application/json")

	resp := &http.Response{
		StatusCode:    200,
		ContentLength: 10,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
	}
	opts := BuildOptions{
		Req:      req,
		ReqBody:  []byte(`{"key":"value"}`),
		Resp:     resp,
		RespBody: []byte(`{"ok":true}`),
		Match:    MatchInfo{ScriptName: "test-script", RuleType: "script", Target: "http://backend"},
		Elapsed:  123 * time.Millisecond,
	}
	evt := BuildEvent(opts)

	if evt.Type != "match" {
		t.Errorf("expected type 'match', got %q", evt.Type)
	}
	if evt.Method != "POST" {
		t.Errorf("expected method 'POST', got %q", evt.Method)
	}
	if evt.Status != 200 {
		t.Errorf("expected status 200, got %d", evt.Status)
	}
	if evt.LatencyMs != 123 {
		t.Errorf("expected latency 123ms, got %d", evt.LatencyMs)
	}
	if evt.Script != "test-script" {
		t.Errorf("expected script 'test-script', got %q", evt.Script)
	}
	if evt.RuleType != "script" {
		t.Errorf("expected rule_type 'script', got %q", evt.RuleType)
	}
	if evt.Target != "http://backend" {
		t.Errorf("expected target 'http://backend', got %q", evt.Target)
	}
	if evt.ContentType != "application/json" {
		t.Errorf("expected content_type 'application/json', got %q", evt.ContentType)
	}
	if evt.ContentLength != 10 {
		t.Errorf("expected content_length 10, got %d", evt.ContentLength)
	}
	// 请求头
	if evt.ReqHeaders["Content-Type"] != "application/json" {
		t.Errorf("expected req header Content-Type, got %v", evt.ReqHeaders)
	}
	// body 不截断
	if evt.ReqBody != `{"key":"value"}` {
		t.Errorf("expected req_body, got %q", evt.ReqBody)
	}
	if evt.RespBody != `{"ok":true}` {
		t.Errorf("expected resp_body, got %q", evt.RespBody)
	}
}

func TestBuildEventReplayTag(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.com/", nil)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}}
	opts := BuildOptions{
		Req:      req,
		Resp:     resp,
		Match:    MatchInfo{ScriptName: "s", RuleType: "proxy", Target: "http://t"},
		Tags:     []EventTag{TagReplay},
	}
	evt := BuildEvent(opts)
	if evt.Type != "replay" {
		t.Errorf("expected type 'replay' with TagReplay, got %q", evt.Type)
	}
}

func TestBuildEventErrorStatus(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.com/", nil)
	resp := &http.Response{StatusCode: 500, Status: "500 Internal Server Error", Header: http.Header{}}
	opts := BuildOptions{
		Req:     req,
		Resp:    resp,
		Match:   MatchInfo{ScriptName: "s", RuleType: "proxy", Target: "http://t"},
	}
	evt := BuildEvent(opts)
	if evt.Error != "500 Internal Server Error" {
		t.Errorf("expected error for 5xx, got %q", evt.Error)
	}
}

func TestBuildEventNoErrorFor2xx(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://example.com/", nil)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}}
	opts := BuildOptions{
		Req:     req,
		Resp:    resp,
		Match:   MatchInfo{ScriptName: "s", RuleType: "proxy", Target: "http://t"},
	}
	evt := BuildEvent(opts)
	if evt.Error != "" {
		t.Errorf("expected no error for 2xx, got %q", evt.Error)
	}
}

func TestTruncateBody(t *testing.T) {
	// 小 body 不截断
	small := []byte("hello")
	if s := truncateBody(small); s != "hello" {
		t.Errorf("expected 'hello', got %q", s)
	}
	// 空 body
	if s := truncateBody(nil); s != "" {
		t.Errorf("expected empty string, got %q", s)
	}
	if s := truncateBody([]byte{}); s != "" {
		t.Errorf("expected empty string, got %q", s)
	}
	// 大 body 截断
	big := make([]byte, 5000)
	for i := range big {
		big[i] = 'x'
	}
	s := truncateBody(big)
	if len(s) > 4096+len("\n...(truncated)") {
		t.Errorf("expected truncated body, got length %d", len(s))
	}
	if !contains(s, "...(truncated)") {
		t.Errorf("expected truncation marker")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && findSubstr(s, substr)
}

func findSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
