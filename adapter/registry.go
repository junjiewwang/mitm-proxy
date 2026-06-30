package adapter

import "fmt"

// Registry 适配器注册表
// 管理所有已注册的适配器实例
type Registry struct {
	adapters map[string]Adapter
}

// NewRegistry 创建适配器注册表
func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]Adapter),
	}
}

// Register 注册适配器
func (r *Registry) Register(adapter Adapter) error {
	name := adapter.Name()
	if _, exists := r.adapters[name]; exists {
		return fmt.Errorf("adapter already registered: %s", name)
	}
	r.adapters[name] = adapter
	return nil
}

// Get 获取适配器
func (r *Registry) Get(name string) (Adapter, bool) {
	a, ok := r.adapters[name]
	return a, ok
}

// List 列出所有适配器名称
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.adapters))
	for name := range r.adapters {
		names = append(names, name)
	}
	return names
}

// Close 关闭所有适配器
func (r *Registry) Close() error {
	for _, a := range r.adapters {
		if err := a.Close(); err != nil {
			return err
		}
	}
	return nil
}
