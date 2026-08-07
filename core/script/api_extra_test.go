package script

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

func TestNewScriptContextFields(t *testing.T) {
	req, _ := http.NewRequest("PUT", "/p/a?x=1&y=2", strings.NewReader(`{"k":"v"}`))
	req.Header.Set("X-Test", "hello")
	sc := NewScriptContext(req, []byte(`{"k":"v"}`), map[string]any{"token": "abc"})

	if sc.Method != "PUT" {
		t.Errorf("Method = %q", sc.Method)
	}
	if sc.Path != "/p/a" {
		t.Errorf("Path = %q", sc.Path)
	}
	if sc.Query != "x=1&y=2" {
		t.Errorf("Query = %q", sc.Query)
	}
	if sc.URL != req.URL.String() {
		t.Errorf("URL = %q", sc.URL)
	}
	if sc.RawBody != `{"k":"v"}` {
		t.Errorf("RawBody = %q", sc.RawBody)
	}
	if sc.Headers["X-Test"] != "hello" {
		t.Errorf("Headers[X-Test] = %q", sc.Headers["X-Test"])
	}
	if sc.Config["token"] != "abc" {
		t.Errorf("Config not passed through")
	}
	if sc.FinalStatus != 200 {
		t.Errorf("FinalStatus default = %d", sc.FinalStatus)
	}
	if sc.Metadata == nil {
		t.Error("Metadata should be initialized")
	}
}

func TestInjectSetupConsoleLevels(t *testing.T) {
	req, _ := http.NewRequest("POST", "/x", nil)
	sc := NewScriptContext(req, nil, nil)
	vm := goja.New()
	if err := sc.InjectSetup()(vm); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := vm.RunString(`
		console.log("a", "b");
		console.warn("careful");
		console.error("oops");
	`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(sc.ConsoleLogs) != 3 {
		t.Fatalf("expected 3 logs, got %d: %+v", len(sc.ConsoleLogs), sc.ConsoleLogs)
	}
	if sc.ConsoleLogs[0].Level != "log" || sc.ConsoleLogs[0].Message != "a b" {
		t.Errorf("log entry wrong: %+v", sc.ConsoleLogs[0])
	}
	if sc.ConsoleLogs[1].Level != "warn" || sc.ConsoleLogs[1].Message != "careful" {
		t.Errorf("warn entry wrong: %+v", sc.ConsoleLogs[1])
	}
	if sc.ConsoleLogs[2].Level != "error" || sc.ConsoleLogs[2].Message != "oops" {
		t.Errorf("error entry wrong: %+v", sc.ConsoleLogs[2])
	}
}

func TestSyncFromVMAllFields(t *testing.T) {
	req, _ := http.NewRequest("POST", "/x", nil)
	sc := NewScriptContext(req, nil, nil)
	vm := goja.New()
	if err := sc.InjectSetup()(vm); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := vm.RunString(`
		ctx.backendURL = "http://b.local";
		ctx.backendRequest = {cmd:"T", n: 7};
		ctx.backendHeaders = {Auth: "Bearer x"};
		ctx.finalResp = "{\"ok\":true}";
		ctx.finalStatus = 418;
		ctx.finalHeaders = {"X-Custom": "1"};
		ctx.metadata = {trace: "abc"};
	`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if err := sc.SyncFromVM(vm); err != nil {
		t.Fatalf("sync: %v", err)
	}

	if sc.BackendURL != "http://b.local" {
		t.Errorf("BackendURL = %q", sc.BackendURL)
	}
	if sc.BackendRequest["cmd"] != "T" {
		t.Errorf("BackendRequest = %+v", sc.BackendRequest)
	}
	if sc.BackendHeaders["Auth"] != "Bearer x" {
		t.Errorf("BackendHeaders = %+v", sc.BackendHeaders)
	}
	if string(sc.FinalResp) != `{"ok":true}` {
		t.Errorf("FinalResp = %q", string(sc.FinalResp))
	}
	if sc.FinalStatus != 418 {
		t.Errorf("FinalStatus = %d", sc.FinalStatus)
	}
	if sc.FinalHeaders["X-Custom"] != "1" {
		t.Errorf("FinalHeaders = %+v", sc.FinalHeaders)
	}
	if sc.Metadata["trace"] != "abc" {
		t.Errorf("Metadata = %+v", sc.Metadata)
	}
}

func TestBuildCtxObjectBackendHeaders(t *testing.T) {
	req, _ := http.NewRequest("POST", "/x", nil)
	sc := NewScriptContext(req, nil, nil)
	sc.BackendHeaders = map[string]string{"Pre": "set"}
	vm := goja.New()
	if err := sc.InjectSetup()(vm); err != nil {
		t.Fatalf("setup: %v", err)
	}
	obj := vm.Get("ctx").(*goja.Object)
	bh := obj.Get("backendHeaders")
	if bh == nil || goja.IsUndefined(bh) {
		t.Fatal("backendHeaders should be present object")
	}
	if exported := bh.Export().(map[string]interface{}); exported["Pre"] != "set" {
		t.Errorf("backendHeaders not propagated: %+v", exported)
	}
}

func TestFormatConsoleArgsSingle(t *testing.T) {
	req, _ := http.NewRequest("POST", "/x", nil)
	sc := NewScriptContext(req, nil, nil)
	vm := goja.New()
	sc.InjectSetup()(vm)
	vm.RunString(`console.log("only-one")`)
	if len(sc.ConsoleLogs) != 1 || sc.ConsoleLogs[0].Message != "only-one" {
		t.Fatalf("single arg formatting wrong: %+v", sc.ConsoleLogs)
	}
}
