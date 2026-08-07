package script

import (
	"strings"
	"testing"

	"github.com/dop251/goja"
)

func TestLoaderCompileCachesByContent(t *testing.T) {
	l := NewLoader()

	src := `console.log("hello")`
	p1, err := l.Compile("a.js", src)
	if err != nil {
		t.Fatalf("compile 1: %v", err)
	}
	if l.CacheSize() != 1 {
		t.Fatalf("expected cache size 1, got %d", l.CacheSize())
	}

	// 相同内容（不同 name）命中缓存，返回同一 *Program
	p2, err := l.Compile("b.js", src)
	if err != nil {
		t.Fatalf("compile 2: %v", err)
	}
	if p1 != p2 {
		t.Fatal("expected same *Program from cache for identical source")
	}
	if l.CacheSize() != 1 {
		t.Fatalf("expected cache size still 1, got %d", l.CacheSize())
	}
}

func TestLoaderCompileDifferentContentDistinct(t *testing.T) {
	l := NewLoader()

	p1, err := l.Compile("a.js", `1+1`)
	if err != nil {
		t.Fatalf("compile 1: %v", err)
	}
	p2, err := l.Compile("a.js", `2+2`)
	if err != nil {
		t.Fatalf("compile 2: %v", err)
	}
	if p1 == p2 {
		t.Fatal("expected distinct *Program for different source")
	}
	if l.CacheSize() != 2 {
		t.Fatalf("expected cache size 2, got %d", l.CacheSize())
	}
}

func TestLoaderCompileError(t *testing.T) {
	l := NewLoader()

	_, err := l.Compile("bad.js", `function( {{{`)
	if err == nil {
		t.Fatal("expected compile error for invalid JS")
	}
	// 编译失败不应污染缓存
	if l.CacheSize() != 0 {
		t.Fatalf("expected empty cache after failed compile, got %d", l.CacheSize())
	}
}

func TestLoaderCompileMulti(t *testing.T) {
	l := NewLoader()
	scripts := map[string]string{
		"onRequest":  `ctx.backendURL = "http://x"`,
		"onResponse": `ctx.finalResp = "{}"`,
	}

	progs, err := l.CompileMulti(scripts)
	if err != nil {
		t.Fatalf("CompileMulti: %v", err)
	}
	if len(progs) != 2 {
		t.Fatalf("expected 2 programs, got %d", len(progs))
	}
	if _, ok := progs["onRequest"]; !ok {
		t.Fatal("missing onRequest program")
	}
	if _, ok := progs["onResponse"]; !ok {
		t.Fatal("missing onResponse program")
	}
	if l.CacheSize() != 2 {
		t.Fatalf("expected cache size 2, got %d", l.CacheSize())
	}
}

func TestLoaderCompileMultiError(t *testing.T) {
	l := NewLoader()
	scripts := map[string]string{
		"onRequest": `valid();`,
		"broken":    `function( {{{`,
	}

	_, err := l.CompileMulti(scripts)
	if err == nil {
		t.Fatal("expected error from CompileMulti on invalid script")
	}
}

func TestLoaderInvalidate(t *testing.T) {
	l := NewLoader()
	if _, err := l.Compile("a.js", `1`); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if l.CacheSize() != 1 {
		t.Fatalf("expected 1, got %d", l.CacheSize())
	}

	l.Invalidate()
	if l.CacheSize() != 0 {
		t.Fatalf("expected 0 after invalidate, got %d", l.CacheSize())
	}

	// 重新编译同内容得到新 *Program
	p, err := l.Compile("a.js", `1`)
	if err != nil {
		t.Fatalf("recompile: %v", err)
	}
	pAfter, err := l.Compile("a.js", `1`)
	if err != nil {
		t.Fatalf("recompile 2: %v", err)
	}
	if p != pAfter {
		t.Fatal("expected same program after invalidate+recompile (content hash identical)")
	}
}

func TestHashSourceDeterministic(t *testing.T) {
	if hashSource("foo") != hashSource("foo") {
		t.Fatal("hashSource not deterministic for same input")
	}
	if hashSource("foo") == hashSource("bar") {
		t.Fatal("hashSource collided for different inputs (extremely unlikely)")
	}
}

// 验证 Compile 返回的 *Program 确实可执行
func TestLoaderCompiledProgramRuns(t *testing.T) {
	l := NewLoader()
	prog, err := l.Compile("r.js", `2+3`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	vm := goja.New()
	v, err := vm.RunProgram(prog)
	if err != nil {
		t.Fatalf("run program: %v", err)
	}
	if v == nil || v.ToInteger() != 5 {
		t.Fatalf("expected 5, got %v", v)
	}
}

// 验证编译错误被包装为可读信息
func TestLoaderWrapsCompileError(t *testing.T) {
	l := NewLoader()
	_, err := l.Compile("x.js", `(`)
	if err == nil {
		t.Fatal("expected wrapped error")
	}
	if err.Error() == "" {
		t.Fatal("unexpected empty error")
	}
	if !strings.Contains(err.Error(), "compile") {
		t.Fatalf("error should mention 'compile': %v", err)
	}
}
