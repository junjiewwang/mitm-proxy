package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"mitm-proxy/core/script"
	"mitm-proxy/rule"
)

func newCmdManager(t *testing.T) *rule.RuleManager {
	t.Helper()
	dir := t.TempDir()
	return rule.NewRuleManager(
		rule.ManagerConfig{Dir: dir, ScriptLoader: script.NewLoader()},
		rule.NewRuleEngine(),
	)
}

func doRequest(engine *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	return w
}

const sampleRuleContent = `@name Sample
@priority 5
@intercept example.com
/^\/api/ http://backend
`

func newRuleAPIEngine(m *rule.RuleManager) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerRuleAPI(engine, m)
	return engine
}

func TestRuleAPIListEmpty(t *testing.T) {
	engine := newRuleAPIEngine(newCmdManager(t))
	w := doRequest(engine, "GET", "/api/rules", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/rules = %d", w.Code)
	}
	var out struct {
		Scripts        []map[string]interface{} `json:"scripts"`
		HostsConflicts []interface{}            `json:"hosts_conflicts"`
		HostsCount     int                     `json:"hosts_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Scripts == nil {
		t.Error("scripts field missing")
	}
}

func TestRuleAPIListWithScript(t *testing.T) {
	m := newCmdManager(t)
	if _, err := m.Create("sample", sampleRuleContent); err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine := newRuleAPIEngine(m)
	w := doRequest(engine, "GET", "/api/rules", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/rules = %d", w.Code)
	}
	var out struct {
		Scripts []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			RuleCount int    `json:"rule_count"`
		} `json:"scripts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Scripts) != 1 {
		t.Fatalf("expected 1 script, got %d", len(out.Scripts))
	}
	if out.Scripts[0].Name != "Sample" {
		t.Errorf("name = %q, want Sample", out.Scripts[0].Name)
	}
	if out.Scripts[0].RuleCount != 1 {
		t.Errorf("rule_count = %d, want 1", out.Scripts[0].RuleCount)
	}
}

func TestRuleAPIGetFoundAndMissing(t *testing.T) {
	m := newCmdManager(t)
	if _, err := m.Create("sample", sampleRuleContent); err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine := newRuleAPIEngine(m)

	w := doRequest(engine, "GET", "/api/rules/sample", "")
	if w.Code != http.StatusOK {
		t.Errorf("GET found = %d, want 200", w.Code)
	}

	w = doRequest(engine, "GET", "/api/rules/missing", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("GET missing = %d, want 404", w.Code)
	}
}

func TestRuleAPICreate(t *testing.T) {
	engine := newRuleAPIEngine(newCmdManager(t))

	// 正常创建
	w := doRequest(engine, "POST", "/api/rules", `{"name":"newsample","content":"`+jsonEscape(sampleRuleContent)+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST create = %d, body=%s", w.Code, w.Body.String())
	}

	// 缺少必填字段
	w = doRequest(engine, "POST", "/api/rules", `{"name":"x"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST create (no content) = %d, want 400", w.Code)
	}

	// 非法 JSON
	w = doRequest(engine, "POST", "/api/rules", `{bad`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST create (bad json) = %d, want 400", w.Code)
	}
}

func TestRuleAPIUpdate(t *testing.T) {
	m := newCmdManager(t)
	if _, err := m.Create("sample", sampleRuleContent); err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine := newRuleAPIEngine(m)

	w := doRequest(engine, "PUT", "/api/rules/sample", `{"content":"@name Updated\n/^/ proxy://http://x\n"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT update = %d, body=%s", w.Code, w.Body.String())
	}

	// 缺少 content
	w = doRequest(engine, "PUT", "/api/rules/sample", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("PUT update (no content) = %d, want 400", w.Code)
	}
}

func TestRuleAPIDeleteNotFound(t *testing.T) {
	engine := newRuleAPIEngine(newCmdManager(t))
	w := doRequest(engine, "DELETE", "/api/rules/nope", "")
	if w.Code != http.StatusInternalServerError {
		t.Errorf("DELETE missing = %d, want 500", w.Code)
	}
}

func TestRuleAPIEnableDisableReload(t *testing.T) {
	m := newCmdManager(t)
	if _, err := m.Create("sample", sampleRuleContent); err != nil {
		t.Fatalf("Create: %v", err)
	}
	engine := newRuleAPIEngine(m)

	if w := doRequest(engine, "POST", "/api/rules/sample/enable", ""); w.Code != http.StatusOK {
		t.Errorf("enable = %d", w.Code)
	}
	if w := doRequest(engine, "POST", "/api/rules/sample/disable", ""); w.Code != http.StatusOK {
		t.Errorf("disable = %d", w.Code)
	}
	if w := doRequest(engine, "POST", "/api/rules/sample/reload", ""); w.Code != http.StatusOK {
		t.Errorf("reload = %d", w.Code)
	}
	if w := doRequest(engine, "POST", "/api/rules/reload-all", ""); w.Code != http.StatusOK {
		t.Errorf("reload-all = %d", w.Code)
	}

	// 不存在的脚本 → 500
	if w := doRequest(engine, "POST", "/api/rules/nope/enable", ""); w.Code != http.StatusInternalServerError {
		t.Errorf("enable missing = %d, want 500", w.Code)
	}
}

func TestRuleAPIValidate(t *testing.T) {
	engine := newRuleAPIEngine(newCmdManager(t))

	w := doRequest(engine, "POST", "/api/rules/validate", `{"content":"`+jsonEscape(sampleRuleContent)+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("validate = %d, body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Valid       bool `json:"valid"`
		ErrorCount  int  `json:"error_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Valid {
		t.Errorf("expected valid content, got error_count=%d", out.ErrorCount)
	}

	// 缺少 content
	w = doRequest(engine, "POST", "/api/rules/validate", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("validate (no content) = %d, want 400", w.Code)
	}
}

// jsonEscape 将多行规则内容转义为 JSON 字符串
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1]) // 去掉首尾引号
}
