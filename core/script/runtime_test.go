package script

import (
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

func TestRuntimeOptions(t *testing.T) {
	r := NewRuntime(WithTimeout(3*time.Second), WithMaxVMs(5))
	if r.timeout != 3*time.Second {
		t.Fatalf("expected timeout 3s, got %v", r.timeout)
	}
	if r.maxVMs != 5 {
		t.Fatalf("expected maxVMs 5, got %d", r.maxVMs)
	}
}

func TestRuntimeDefaultTimeout(t *testing.T) {
	r := NewRuntime()
	if r.timeout != 5*time.Second {
		t.Fatalf("expected default timeout 5s, got %v", r.timeout)
	}
	if r.maxVMs != 0 {
		t.Fatalf("expected default maxVMs 0, got %d", r.maxVMs)
	}
}

func TestRuntimeAcquireRelease(t *testing.T) {
	r := NewRuntime()
	vm := r.Acquire()
	if vm == nil {
		t.Fatal("Acquire returned nil")
	}
	// 设置值后归还，池化 VM 应被复用（但不要求一定复用）
	r.Release(vm)

	vm2 := r.Acquire()
	if vm2 == nil {
		t.Fatal("second Acquire returned nil")
	}
	r.Release(vm2)
}

func TestRuntimeExecute(t *testing.T) {
	r := NewRuntime()
	prog, err := goja.Compile("", `21 * 2`, false)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	val, err := r.Execute(prog, func(vm *goja.Runtime) error {
		vm.Set("y", 10)
		return nil
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if val.ToInteger() != 42 {
		t.Fatalf("expected 42, got %v", val)
	}
}

func TestRuntimeExecuteSetupError(t *testing.T) {
	r := NewRuntime()
	prog, _ := goja.Compile("", `1`, false)

	_, err := r.Execute(prog, func(vm *goja.Runtime) error {
		return gojaErr()
	})
	if err == nil {
		t.Fatal("expected setup error to be returned")
	}
}

func TestRuntimeExecuteScriptError(t *testing.T) {
	r := NewRuntime()
	prog, _ := goja.Compile("", `throw new Error("boom")`, false)

	_, err := r.Execute(prog, func(vm *goja.Runtime) error { return nil })
	if err == nil {
		t.Fatal("expected script error to be returned")
	}
}

func TestRuntimeExecuteScriptPanic(t *testing.T) {
	r := NewRuntime()
	// 递归无限调用触发 JS 栈溢出 panic
	prog, _ := goja.Compile("", `function f(){ return f(); } f();`, false)

	_, err := r.Execute(prog, func(vm *goja.Runtime) error { return nil })
	if err == nil {
		t.Fatal("expected panic to be captured as error")
	}
}

func TestRuntimeExecuteTimeout(t *testing.T) {
	r := NewRuntime(WithTimeout(150 * time.Millisecond))
	// 无限循环，应触发超时
	prog, _ := goja.Compile("", `while(true){}`, false)

	start := time.Now()
	_, err := r.Execute(prog, func(vm *goja.Runtime) error { return nil })
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestRuntimeExecuteString(t *testing.T) {
	r := NewRuntime()
	val, err := r.ExecuteString(`3 + 4`, func(vm *goja.Runtime) error { return nil })
	if err != nil {
		t.Fatalf("ExecuteString: %v", err)
	}
	if val.ToInteger() != 7 {
		t.Fatalf("expected 7, got %v", val)
	}
}

func TestRuntimeExecuteStringCompileError(t *testing.T) {
	r := NewRuntime()
	_, err := r.ExecuteString(`(`, func(vm *goja.Runtime) error { return nil })
	if err == nil {
		t.Fatal("expected compile error")
	}
}

func TestRuntimeCompile(t *testing.T) {
	r := NewRuntime()
	prog, err := r.Compile("x.js", `1+1`)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if prog == nil {
		t.Fatal("expected non-nil program")
	}
}

func TestRuntimeExecuteFunction(t *testing.T) {
	r := NewRuntime()
	// 先定义函数
	prog, _ := goja.Compile("", `function add(a,b){ return a+b; }`, false)
	if _, err := r.Execute(prog, func(vm *goja.Runtime) error { return nil }); err != nil {
		t.Fatalf("define function: %v", err)
	}

	vm := r.Acquire()
	defer r.Release(vm)

	b := vm.ToValue(40)
	c := vm.ToValue(2)
	val, err := r.ExecuteFunction(vm, "add", b, c)
	if err != nil {
		t.Fatalf("ExecuteFunction: %v", err)
	}
	if val.ToInteger() != 42 {
		t.Fatalf("expected 42, got %v", val)
	}
}

func TestRuntimeExecuteFunctionNotFound(t *testing.T) {
	r := NewRuntime()
	vm := r.Acquire()
	defer r.Release(vm)

	_, err := r.ExecuteFunction(vm, "doesNotExist")
	if err == nil {
		t.Fatal("expected error for missing function")
	}
}

func TestRuntimeExecuteFunctionTimeout(t *testing.T) {
	r := NewRuntime(WithTimeout(150 * time.Millisecond))
	prog, _ := goja.Compile("", `function loop(){ while(true){} }`, false)
	if _, err := r.Execute(prog, func(vm *goja.Runtime) error { return nil }); err != nil {
		t.Fatalf("define: %v", err)
	}
	vm := r.Acquire()
	defer r.Release(vm)

	start := time.Now()
	_, err := r.ExecuteFunction(vm, "loop")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout too slow: %v", elapsed)
	}
}

// gojaErr 返回一个非空 error，用于测试 setup 失败路径
func gojaErr() error {
	return errTestSetup
}

var errTestSetup = &simpleErr{"setup failed"}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }
