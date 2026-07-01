package rule

import (
	"testing"

	"mitm-proxy/core/script"
)

func TestParseV2Syntax(t *testing.T) {
	content := `@name "APM 本地开发"
@priority 100

@config
backend:
  url: "http://127.0.0.1:16686"
  api_path: "/api/v1"
auth:
  uin: "test-uin"
  app_id: 123
@end

@script onRequest
  var body = JSON.parse(ctx.rawBody);
  ctx.backendURL = ctx.config.backend.url + ctx.config.backend.api_path;
  console.log("Action: " + body.cmd);
@end

@script onResponse
  var resp = JSON.parse(ctx.backendResp);
  ctx.finalResp = JSON.stringify({ code: 0, data: resp });
@end

@intercept test.example.com

10.0.0.1 test.example.com

/\/cgi\/capi\?.*action=delegate/  script://self
`

	p := &Parser{}
	script, errs := p.ParseContent(content)

	if script.Name != "APM 本地开发" {
		t.Errorf("Name = %q, want %q", script.Name, "APM 本地开发")
	}
	if script.Priority != 100 {
		t.Errorf("Priority = %d, want 100", script.Priority)
	}

	// Check config
	if script.Config == nil {
		t.Fatal("Config is nil")
	}
	backend, ok := script.Config["backend"].(map[string]any)
	if !ok {
		t.Fatal("Config.backend not found or wrong type")
	}
	if backend["url"] != "http://127.0.0.1:16686" {
		t.Errorf("backend.url = %v", backend["url"])
	}

	// Check scripts
	if len(script.Scripts) != 2 {
		t.Errorf("Scripts count = %d, want 2", len(script.Scripts))
	}
	if onReq, ok := script.Scripts["onRequest"]; !ok || onReq == "" {
		t.Error("onRequest script missing or empty")
	}
	if onResp, ok := script.Scripts["onResponse"]; !ok || onResp == "" {
		t.Error("onResponse script missing or empty")
	}

	// Check rules
	if len(script.Rules) != 2 { // 1 hosts + 1 script
		t.Errorf("Rules count = %d, want 2", len(script.Rules))
	}

	// Check script:// rule
	var foundScript bool
	for _, r := range script.Rules {
		if r.Type == RuleTypeScript && r.ScriptRef == "self" {
			foundScript = true
			break
		}
	}
	if !foundScript {
		t.Error("script://self rule not found")
	}

	// No errors
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("Parse error: %s", e.Message)
		}
	}
}

func TestPreCompileScripts(t *testing.T) {
	content := `@name "test"
@priority 10

@config
url: "http://localhost"
@end

@script onRequest
  ctx.backendURL = ctx.config.url;
@end

@script onResponse
  ctx.finalResp = ctx.backendResp;
@end

@intercept test.example.com
/\/api\/test/  script://self
`

	p := &Parser{}
	scriptData, errs := p.ParseContent(content)
	if len(errs) > 0 {
		t.Fatalf("Parse errors: %v", errs)
	}

	// 模拟 RuleManager 的预编译流程
	loader := script.NewLoader()
	progs, err := loader.CompileMulti(scriptData.Scripts)
	if err != nil {
		t.Fatalf("PreCompile failed: %v", err)
	}
	scriptData.CompiledPrograms = progs

	// 验证预编译结果
	if prog, ok := scriptData.CompiledPrograms["onRequest"]; !ok || prog == nil {
		t.Error("onRequest program not compiled")
	}
	if prog, ok := scriptData.CompiledPrograms["onResponse"]; !ok || prog == nil {
		t.Error("onResponse program not compiled")
	}
	if len(scriptData.CompiledPrograms) != 2 {
		t.Errorf("expected 2 compiled programs, got %d", len(scriptData.CompiledPrograms))
	}

	t.Log("Pre-compile cache works correctly")
}

func TestManagerPreCompile(t *testing.T) {
	// 端到端验证：RuleManager 加载规则后 CompiledPrograms 不为空
	content := `@name "e2e"
@priority 50

@config
backend:
  url: "http://localhost:8080"
  api_path: "/api"
auth:
  uin: "test-uin"
@end

@script onRequest
  ctx.backendURL = ctx.config.backend.url + ctx.config.backend.api_path;
  ctx.backendRequest = { Action: "Test" };
@end

@intercept test.local
/\/test/  script://self
`

	engine := NewRuleEngine()
	loader := script.NewLoader()

	// 模拟 LoadAll 流程
	p := &Parser{}
	scriptData, errs := p.ParseContent(content)
	if len(errs) > 0 {
		t.Fatalf("Parse errors: %v", errs)
	}

	// 预编译（RuleManager.preCompileScripts 的逻辑）
	progs, err := loader.CompileMulti(scriptData.Scripts)
	if err != nil {
		t.Fatalf("CompileMulti failed: %v", err)
	}
	scriptData.CompiledPrograms = progs

	// 编译到引擎
	engine.Compile([]*RuleScript{scriptData})

	// 验证引擎可以匹配 script://self
	// （这里只验证数据就绪，真实 HTTP 匹配需要集成测试）

	if scriptData.CompiledPrograms == nil {
		t.Fatal("CompiledPrograms is nil")
	}
	if len(scriptData.CompiledPrograms) != 1 {
		t.Errorf("expected 1 compiled program, got %d", len(scriptData.CompiledPrograms))
	}

	t.Logf("CompiledPrograms ready: %d entries", len(scriptData.CompiledPrograms))
}
