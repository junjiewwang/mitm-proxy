package script

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

func TestBackendRespInjection(t *testing.T) {
	// 模拟 onResponse 阶段：backendResp 已设置
	req, _ := http.NewRequest("POST", "/cgi/capi?action=delegate&serviceType=apm", strings.NewReader(""))
	sc := NewScriptContext(req, []byte(`{"cmd":"Test"}`), nil)

	// 模拟 HTTP forward 后的状态
	sc.BackendResp = []byte(`{"Response":{"Data":{"foo":"bar"},"RequestId":"abc-123"}}`)
	sc.BackendStatus = 200

	// 编译 onResponse 脚本
	jsSource := `
		var resp = JSON.parse(ctx.backendResp);
		var innerCode = 0;
		if (resp.Response && resp.Response.Error) {
			innerCode = resp.Response.Error.Code || -1;
		}
		ctx.finalResp = JSON.stringify({
			code: 0,
			data: {
				code: innerCode,
				cgwerrorCode: 0,
				data: { Response: resp.Response || resp }
			},
			mccode: 0,
			errObj: {},
			seqId: "proxy-test"
		});
	`

	prog, err := goja.Compile("onResponse", jsSource, false)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	vm := goja.New()
	if err := sc.InjectSetup()(vm); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// 验证 backendResp 已注入
	backendRespVal := vm.Get("ctx").(*goja.Object).Get("backendResp")
	if backendRespVal == nil || goja.IsUndefined(backendRespVal) {
		t.Fatal("backendResp is undefined in VM — BUG: not injected")
	}
	backendRespStr := backendRespVal.String()
	if !strings.Contains(backendRespStr, "abc-123") {
		t.Errorf("backendResp content wrong: %s", backendRespStr)
	}
	t.Logf("backendResp injected correctly: %d bytes", len(backendRespStr))

	// 运行脚本
	if _, err := vm.RunProgram(prog); err != nil {
		t.Fatalf("run onResponse: %v", err)
	}

	// 同步回 ScriptContext
	if err := sc.SyncFromVM(vm); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if len(sc.FinalResp) == 0 {
		t.Fatal("finalResp should not be empty after onResponse")
	}
	if !strings.Contains(string(sc.FinalResp), `"Response"`) {
		t.Errorf("finalResp does not contain Response wrapper: %s", string(sc.FinalResp))
	}
	t.Logf("finalResp: %s", string(sc.FinalResp)[:100]+"...")
}

func TestBackendRespNotSetInOnRequest(t *testing.T) {
	// onRequest 阶段：backendResp 为空是正常的
	req, _ := http.NewRequest("POST", "/cgi/capi", strings.NewReader(""))
	sc := NewScriptContext(req, []byte(`{"cmd":"Test","serviceType":"apm","regionId":1}`), nil)

	vm := goja.New()
	sc.InjectSetup()(vm)

	// backendResp 应该存在但是空字符串
	backendRespVal := vm.Get("ctx").(*goja.Object).Get("backendResp")
	if backendRespVal == nil || goja.IsUndefined(backendRespVal) {
		t.Fatal("backendResp should exist (even if empty)")
	}
	if s := backendRespVal.String(); s != "" {
		t.Logf("backendResp in onRequest: %q (expected empty)", s)
	}
}
