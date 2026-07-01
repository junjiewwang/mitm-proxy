package script

import (
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/dop251/goja"
)

// Loader 脚本编译缓存，将 JS 源码编译为可复用的 *goja.Program
// 按内容 hash 缓存，源码变更时自动重新编译
type Loader struct {
	mu     sync.RWMutex
	cache  map[string]*goja.Program // key = sha256(source)
}

// NewLoader 创建脚本加载器
func NewLoader() *Loader {
	return &Loader{
		cache: make(map[string]*goja.Program),
	}
}

// Compile 编译 JS 源码（带缓存）
// name 仅用于报错时的文件名标识，不影响缓存 key
func (l *Loader) Compile(name, source string) (*goja.Program, error) {
	hash := hashSource(source)

	// 读缓存
	l.mu.RLock()
	if prog, ok := l.cache[hash]; ok {
		l.mu.RUnlock()
		return prog, nil
	}
	l.mu.RUnlock()

	// 编译
	prog, err := goja.Compile(name, source, false)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", name, err)
	}

	// 写缓存
	l.mu.Lock()
	l.cache[hash] = prog
	l.mu.Unlock()

	return prog, nil
}

// CompileMulti 批量编译多个 JS 片段
// 返回 map[name]*goja.Program，其中 name 为脚本名称（如 "onRequest"/"onResponse"）
func (l *Loader) CompileMulti(scripts map[string]string) (map[string]*goja.Program, error) {
	result := make(map[string]*goja.Program, len(scripts))
	for name, source := range scripts {
		prog, err := l.Compile(name, source)
		if err != nil {
			return nil, fmt.Errorf("compile %s: %w", name, err)
		}
		result[name] = prog
	}
	return result, nil
}

// Invalidate 清除缓存（热加载时调用）
func (l *Loader) Invalidate() {
	l.mu.Lock()
	l.cache = make(map[string]*goja.Program)
	l.mu.Unlock()
}

// CacheSize 返回缓存大小
func (l *Loader) CacheSize() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.cache)
}

// hashSource 计算源码内容 hash
func hashSource(source string) string {
	h := sha256.Sum256([]byte(source))
	return fmt.Sprintf("%x", h[:16]) // 取前 16 字节足够
}
