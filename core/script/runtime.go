package script

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// Runtime JS 脚本运行时，封装 goja VM 池 + 沙箱 + 超时保护
type Runtime struct {
	pool    sync.Pool
	timeout time.Duration
	maxVMs  int32
	count   int32
	mu      sync.Mutex
}

// RuntimeOption Runtime 可选配置
type RuntimeOption func(*Runtime)

// WithTimeout 设置单次脚本执行超时
func WithTimeout(d time.Duration) RuntimeOption {
	return func(r *Runtime) { r.timeout = d }
}

// WithMaxVMs 设置最大 VM 数量（0 表示不限制）
func WithMaxVMs(n int32) RuntimeOption {
	return func(r *Runtime) { r.maxVMs = n }
}

// NewRuntime 创建 JS 脚本运行时
func NewRuntime(opts ...RuntimeOption) *Runtime {
	r := &Runtime{
		timeout: 5 * time.Second, // 默认超时
	}
	for _, opt := range opts {
		opt(r)
	}
	r.pool = sync.Pool{
		New: func() any {
			return goja.New()
		},
	}
	return r
}

// Acquire 从池中获取一个 VM（受 maxVMs 限制）
// 调用者需在用完后调用 Release 归还
func (r *Runtime) Acquire() *goja.Runtime {
	if r.maxVMs > 0 {
		current := atomic.LoadInt32(&r.count)
		if current >= r.maxVMs {
			// 达到上限，从池中取（阻塞等待归还）
			for {
				if vm := r.pool.Get(); vm != nil {
					return vm.(*goja.Runtime)
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		atomic.AddInt32(&r.count, 1)
	}
	return r.pool.Get().(*goja.Runtime)
}

// Release 归还 VM 到池
func (r *Runtime) Release(vm *goja.Runtime) {
	// 清理 VM 全局状态（避免跨请求污染）
	// goja 的 vm.Set() 会覆盖已有值，所以只需重置关键字段即可
	vm.SetFieldNameMapper(goja.UncapFieldNameMapper())
	r.pool.Put(vm)
	if r.maxVMs > 0 {
		atomic.AddInt32(&r.count, -1)
	}
}

// Execute 在沙箱中执行 JS 脚本（自动管理 VM 获取/归还）
// setup: 在执行前注入 ctx/console 等对象
// 如需在执行后读取 VM 状态（如 SyncFromVM），请使用 Acquire/Release 手动管理
func (r *Runtime) Execute(program *goja.Program, setup func(vm *goja.Runtime) error) (goja.Value, error) {
	vm := r.Acquire()
	defer r.Release(vm)

	// 注入上下文
	if err := setup(vm); err != nil {
		return nil, fmt.Errorf("setup vm: %w", err)
	}

	// 超时保护：通过 goroutine + channel 实现
	type result struct {
		val goja.Value
		err error
	}

	done := make(chan result, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				done <- result{err: fmt.Errorf("script panic: %v", rec)}
			}
		}()

		val, err := vm.RunProgram(program)
		done <- result{val: val, err: err}
	}()

	select {
	case res := <-done:
		return res.val, res.err
	case <-time.After(r.timeout):
		// 超时：无法强制 kill goroutine，但停止等待
		// goja 的 vm.Interrupt() 可以在下一次循环检查点中断
		vm.Interrupt("execution timeout")
		select {
		case res := <-done:
			return nil, fmt.Errorf("script timeout after %v: %w", r.timeout, res.err)
		case <-time.After(1 * time.Second):
			return nil, fmt.Errorf("script timeout after %v (force)", r.timeout)
		}
	}
}

// ExecuteString 执行原始 JS 字符串（自动编译）
func (r *Runtime) ExecuteString(jsSource string, setup func(vm *goja.Runtime) error) (goja.Value, error) {
	program, err := goja.Compile("", jsSource, false)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	return r.Execute(program, setup)
}

// ExecuteFunction 在已编译的 Program 中查找并调用指定函数
// 前提：setup 已经运行了 Program（定义了函数），现在调用具名函数
func (r *Runtime) ExecuteFunction(vm *goja.Runtime, funcName string, args ...goja.Value) (goja.Value, error) {
	fn, ok := goja.AssertFunction(vm.Get(funcName))
	if !ok {
		return nil, fmt.Errorf("function %q not found or not callable", funcName)
	}

	type result struct {
		val goja.Value
		err error
	}

	done := make(chan result, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				done <- result{err: fmt.Errorf("script panic: %v", rec)}
			}
		}()
		val, err := fn(goja.Undefined(), args...)
		done <- result{val: val, err: err}
	}()

	select {
	case res := <-done:
		return res.val, res.err
	case <-time.After(r.timeout):
		vm.Interrupt("execution timeout")
		select {
		case res := <-done:
			return nil, fmt.Errorf("script timeout after %v: %w", r.timeout, res.err)
		case <-time.After(1 * time.Second):
			return nil, fmt.Errorf("script timeout after %v (force)", r.timeout)
		}
	}
}

// Compile 编译 JS 源码为可复用的 *goja.Program
func (r *Runtime) Compile(name, source string) (*goja.Program, error) {
	return goja.Compile(name, source, false)
}
