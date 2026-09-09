package script

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dop251/goja"
)

func compileStage(t *testing.T, src string) *goja.Program {
	t.Helper()
	prog, err := goja.Compile("stage", src, false)
	if err != nil {
		t.Fatalf("compile stage: %v", err)
	}
	return prog
}

func TestDebuggerEmptyScriptID(t *testing.T) {
	d := NewDebugger(NewRuntime())
	res := d.Execute(DebugRequest{Method: "POST", Path: "/x"}, "", nil, nil, false, false, true)
	if len(res.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(res.Steps))
	}
	if res.Steps[0].Stage != "error" {
		t.Fatalf("expected 'error' stage, got %q", res.Steps[0].Stage)
	}
	if res.Steps[0].Error == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestDebuggerRuleMatchStep(t *testing.T) {
	d := NewDebugger(NewRuntime())
	res := d.Execute(DebugRequest{Method: "POST", Path: "/x"}, "script-123", nil, nil, false, false, true)
	if len(res.Steps) == 0 || res.Steps[0].Stage != "rule_match" {
		t.Fatalf("expected first step rule_match, got %+v", res.Steps)
	}
	if !res.Steps[0].Matched {
		t.Fatal("rule_match step should be matched")
	}
}

func TestDebuggerNoScriptStagesSkipped(t *testing.T) {
	d := NewDebugger(NewRuntime())
	res := d.Execute(DebugRequest{Method: "GET", Path: "/api"}, "sid", nil, nil, false, false, true)

	// 查找 onRequest / onResponse / forward 步骤
	var onReq, onResp, fwd bool
	for _, s := range res.Steps {
		switch s.Stage {
		case "onRequest":
			onReq = true
			if !s.Skipped {
				t.Errorf("onRequest should be skipped when hasOnReq=false")
			}
		case "onResponse":
			onResp = true
			if !s.Skipped {
				t.Errorf("onResponse should be skipped when hasOnResp=false")
			}
		case "forward":
			fwd = true
		}
	}
	if !onReq || !onResp {
		t.Fatal("expected onRequest and onResponse steps present")
	}
	if !fwd {
		t.Fatal("expected forward step")
	}
}

func TestDebuggerOnRequestSetsBackendURLDryRun(t *testing.T) {
	d := NewDebugger(NewRuntime())
	onReq := compileStage(t, `ctx.backendURL = "http://backend.local/api"; ctx.backendRequest = {cmd:"Test"}; console.log("hi", "there");`)

	progs := map[string]*goja.Program{"onRequest": onReq}
	res := d.Execute(
		DebugRequest{Method: "POST", Path: "/cgi/capi", Body: `{"cmd":"Test"}`, DryRun: true},
		"sid", nil, progs, true, false, true,
	)

	var onReqStep, fwdStep *DebugStep
	for i := range res.Steps {
		switch res.Steps[i].Stage {
		case "onRequest":
			onReqStep = &res.Steps[i]
		case "forward":
			fwdStep = &res.Steps[i]
		}
	}
	if onReqStep == nil {
		t.Fatal("missing onRequest step")
	}
	if onReqStep.Error != "" {
		t.Fatalf("onRequest step error: %s", onReqStep.Error)
	}
	if len(onReqStep.ConsoleLogs) != 1 || onReqStep.ConsoleLogs[0] != "[log] hi there" {
		t.Fatalf("unexpected console logs: %+v", onReqStep.ConsoleLogs)
	}
	out := onReqStep.Output.(map[string]interface{})
	if out["backendURL"] != "http://backend.local/api" {
		t.Fatalf("backendURL output wrong: %+v", out["backendURL"])
	}

	if fwdStep == nil {
		t.Fatal("missing forward step")
	}
	if !fwdStep.Skipped {
		t.Fatal("forward should be skipped in dry_run")
	}
	fwdOut := fwdStep.Output.(map[string]interface{})
	if fwdOut["would_send_to"] != "http://backend.local/api" {
		t.Fatalf("would_send_to wrong: %+v", fwdOut["would_send_to"])
	}
}

func TestDebuggerForwardRealBackend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		fmt.Fprint(w, `{"Response":{"Data":"ok"}}`)
	}))
	defer srv.Close()

	d := NewDebugger(NewRuntime())
	onReq := compileStage(t, fmt.Sprintf(`ctx.backendURL = %q; ctx.backendRequest = {cmd:"T"};`, srv.URL))

	progs := map[string]*goja.Program{"onRequest": onReq}
	res := d.Execute(
		DebugRequest{Method: "POST", Path: "/cgi/capi", Body: `{"cmd":"T"}`, DryRun: false},
		"sid", nil, progs, true, false, true,
	)

	var fwdStep *DebugStep
	for i := range res.Steps {
		if res.Steps[i].Stage == "forward" {
			fwdStep = &res.Steps[i]
		}
	}
	if fwdStep == nil {
		t.Fatal("missing forward step")
	}
	if fwdStep.Error != "" {
		t.Fatalf("forward step error: %s", fwdStep.Error)
	}
	out := fwdStep.Output.(map[string]interface{})
	if out["status_code"].(int) != 200 {
		t.Fatalf("expected 200 status, got %v", out["status_code"])
	}
}

func TestDebuggerOnRequestErrorStops(t *testing.T) {
	d := NewDebugger(NewRuntime())
	onReq := compileStage(t, `throw new Error("bad request phase")`)
	progs := map[string]*goja.Program{"onRequest": onReq}

	res := d.Execute(
		DebugRequest{Method: "POST", Path: "/x"},
		"sid", nil, progs, true, false, true,
	)

	var onReqStep, fwdStep *DebugStep
	for i := range res.Steps {
		switch res.Steps[i].Stage {
		case "onRequest":
			onReqStep = &res.Steps[i]
		case "forward":
			fwdStep = &res.Steps[i]
		}
	}
	if onReqStep == nil || onReqStep.Error == "" {
		t.Fatal("expected onRequest error")
	}
	if fwdStep != nil {
		t.Fatal("forward should not run after onRequest error")
	}
}

func TestDebuggerOnResponsePhase(t *testing.T) {
	d := NewDebugger(NewRuntime())
	onResp := compileStage(t, `ctx.finalResp = JSON.stringify({code:0}); ctx.finalStatus = 201;`)

	progs := map[string]*goja.Program{"onResponse": onResp}
	res := d.Execute(
		DebugRequest{Method: "POST", Path: "/x", DryRun: true},
		"sid", nil, progs, false, true, true,
	)

	var respStep *DebugStep
	for i := range res.Steps {
		if res.Steps[i].Stage == "onResponse" {
			respStep = &res.Steps[i]
		}
	}
	if respStep == nil {
		t.Fatal("missing onResponse step")
	}
	if respStep.Error != "" {
		t.Fatalf("onResponse error: %s", respStep.Error)
	}
	out := respStep.Output.(map[string]interface{})
	if out["finalStatus"] != 201 {
		t.Fatalf("finalStatus wrong: %+v", out["finalStatus"])
	}
}
