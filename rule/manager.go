package rule

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mitm-proxy/core/script"
)

// RuleManager 规则管理器
// 职责：规则脚本 CRUD + 启停 + 热加载 + 持久化 + JS 预编译
type RuleManager struct {
	mu      sync.RWMutex
	scripts map[string]*RuleScript // ID → Script
	dir     string                 // 规则文件目录（rules/）
	engine  *RuleEngine            // 规则变更时通知引擎重编译
	parser  *Parser
	loader  *script.Loader // JS 脚本预编译器（nil=跳过预编译）

	// 热加载
	hotReload     bool
	watchInterval time.Duration
	stopWatch     chan struct{}
}

// ManagerConfig RuleManager 配置
type ManagerConfig struct {
	Dir           string          // 规则文件目录
	HotReload     bool            // 是否启用热加载
	WatchInterval time.Duration   // 文件监听间隔
	ScriptLoader  *script.Loader  // JS 脚本预编译器（可选，nil 表示不预编译）
}

// NewRuleManager 创建规则管理器
func NewRuleManager(cfg ManagerConfig, engine *RuleEngine) *RuleManager {
	if cfg.Dir == "" {
		cfg.Dir = "rules"
	}
	if cfg.WatchInterval <= 0 {
		cfg.WatchInterval = 2 * time.Second
	}

	m := &RuleManager{
		scripts:       make(map[string]*RuleScript),
		dir:           cfg.Dir,
		engine:        engine,
		parser:        &Parser{},
		loader:        cfg.ScriptLoader,
		hotReload:     cfg.HotReload,
		watchInterval: cfg.WatchInterval,
		stopWatch:     make(chan struct{}),
	}

	return m
}

// disabledMarkerPath 返回禁用标记文件路径：{dir}/.{id}.disabled
func (m *RuleManager) disabledMarkerPath(id string) string {
	return filepath.Join(m.dir, "."+id+".disabled")
}

// isDisabledPersisted 检查是否有持久化的禁用标记
func (m *RuleManager) isDisabledPersisted(id string) bool {
	_, err := os.Stat(m.disabledMarkerPath(id))
	return err == nil
}

// persistDisabled 持久化禁用状态
func (m *RuleManager) persistDisabled(id string, disabled bool) {
	marker := m.disabledMarkerPath(id)
	if disabled {
		os.WriteFile(marker, []byte("1"), 0644)
	} else {
		os.Remove(marker)
	}
}

// LoadAll 加载目录下所有 .rules 文件
func (m *RuleManager) LoadAll() error {
	// 确保目录存在
	if err := os.MkdirAll(m.dir, 0755); err != nil {
		return fmt.Errorf("create rules dir: %w", err)
	}

	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return fmt.Errorf("read rules dir: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".rules") {
			continue
		}
		path := filepath.Join(m.dir, entry.Name())
		script, err := m.parser.ParseFile(path)
		if err != nil {
			log.Printf("[RuleManager] WARNING: skip %s: %v", entry.Name(), err)
			continue
		}
		m.scripts[script.ID] = script
		// 检查持久化的禁用状态
		if m.isDisabledPersisted(script.ID) {
			script.Enabled = false
			log.Printf("[RuleManager] Loaded %s (disabled via persisted marker)", script.ID)
		}
		m.preCompileScripts(script)
		loaded++
		if len(script.ParseErrors) > 0 {
			log.Printf("[RuleManager] Loaded %s (with %d parse errors, first: %s)", script.ID, len(script.ParseErrors), script.ParseErrors[0].Error())
		} else {
			log.Printf("[RuleManager] Loaded %s: name=%q priority=%d rules=%d intercepts=%d",
				script.ID, script.Name, script.Priority, len(script.Rules), len(script.Intercepts))
		}
	}

	log.Printf("[RuleManager] Loaded %d rule scripts from %s", loaded, m.dir)

	// 编译到引擎
	m.compileToEngine()
	return nil
}

// StartHotReload 启动文件监听热加载
func (m *RuleManager) StartHotReload() {
	if !m.hotReload {
		return
	}

	log.Printf("[RuleManager] Hot reload enabled (interval=%s)", m.watchInterval)

	go func() {
		// 记录文件 modtime 基线
		modTimes := m.snapshotModTimes()

		ticker := time.NewTicker(m.watchInterval)
		defer ticker.Stop()

		for {
			select {
			case <-m.stopWatch:
				log.Printf("[RuleManager] Hot reload stopped")
				return
			case <-ticker.C:
				m.checkAndReload(modTimes)
			}
		}
	}()
}

// StopHotReload 停止热加载
func (m *RuleManager) StopHotReload() {
	close(m.stopWatch)
}

// List 获取所有脚本列表（按优先级降序）
func (m *RuleManager) List() []*RuleScript {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*RuleScript, 0, len(m.scripts))
	for _, s := range m.scripts {
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Priority > result[j].Priority
	})
	return result
}

// Get 获取单个脚本
func (m *RuleManager) Get(id string) (*RuleScript, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.scripts[id]
	return s, ok
}

// Create 创建新脚本
func (m *RuleManager) Create(name, content string) (*RuleScript, error) {
	// 生成 ID（文件名安全）
	id := sanitizeID(name)
	if id == "" {
		id = fmt.Sprintf("script-%d", time.Now().UnixMilli())
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.scripts[id]; exists {
		return nil, fmt.Errorf("script already exists: %s", id)
	}

	// 写文件
	path := filepath.Join(m.dir, id+".rules")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("write rule file: %w", err)
	}

	// 解析
	script, _ := m.parser.ParseContent(content)
	script.ID = id
	script.FilePath = path
	script.UpdatedAt = time.Now()
	if script.Name == "" {
		script.Name = name
	}

	m.scripts[id] = script
	m.preCompileScripts(script)

	// 重编译
	m.compileToEngine()

	log.Printf("[RuleManager] Created script: %s (name=%q)", id, script.Name)
	return script, nil
}

// Update 更新脚本内容
func (m *RuleManager) Update(id, content string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	old, ok := m.scripts[id]
	if !ok {
		return fmt.Errorf("script not found: %s", id)
	}

	// 写文件
	if err := os.WriteFile(old.FilePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write rule file: %w", err)
	}

	// 重新解析
	script, parseErrs := m.parser.ParseContent(content)
	script.ID = id
	script.FilePath = old.FilePath
	script.Enabled = old.Enabled
	script.Stats = old.Stats // 保留统计
	script.UpdatedAt = time.Now()

	m.scripts[id] = script
	m.preCompileScripts(script)

	// 重编译
	m.compileToEngine()

	log.Printf("[RuleManager] Updated script: %s (rules: %d→%d, scripts: %d, parse_errors: %d)",
		id, len(old.Rules), len(script.Rules), len(script.Scripts), len(parseErrs))
	return nil
}

// Delete 删除脚本
func (m *RuleManager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	script, ok := m.scripts[id]
	if !ok {
		return fmt.Errorf("script not found: %s", id)
	}

	// 删除标记文件（如果有）
	os.Remove(m.disabledMarkerPath(id))
	// 删除 rules 文件
	if err := os.Remove(script.FilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove rule file: %w", err)
	}

	delete(m.scripts, id)

	// 重编译
	m.compileToEngine()

	log.Printf("[RuleManager] Deleted script: %s", id)
	return nil
}

// Enable 启用脚本
func (m *RuleManager) Enable(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	script, ok := m.scripts[id]
	if !ok {
		return fmt.Errorf("script not found: %s", id)
	}

	if script.Enabled {
		return nil // 已启用
	}

	script.Enabled = true
	m.persistDisabled(id, false)
	m.compileToEngine()

	log.Printf("[RuleManager] Enabled script: %s", id)
	return nil
}

// Disable 停用脚本
func (m *RuleManager) Disable(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	script, ok := m.scripts[id]
	if !ok {
		return fmt.Errorf("script not found: %s", id)
	}

	if !script.Enabled {
		return nil // 已停用
	}

	script.Enabled = false
	m.persistDisabled(id, true)
	m.compileToEngine()

	log.Printf("[RuleManager] Disabled script: %s", id)
	return nil
}

// Reload 重载单个脚本（从文件重新解析）
func (m *RuleManager) Reload(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	old, ok := m.scripts[id]
	if !ok {
		return fmt.Errorf("script not found: %s", id)
	}

	script, err := m.parser.ParseFile(old.FilePath)
	if err != nil {
		return fmt.Errorf("reload script: %w", err)
	}

	script.Enabled = old.Enabled
	script.Stats = old.Stats
	m.scripts[id] = script
	m.preCompileScripts(script)

	m.compileToEngine()

	log.Printf("[RuleManager] Reloaded script: %s", id)
	return nil
}

// ReloadAll 重载所有脚本
func (m *RuleManager) ReloadAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, old := range m.scripts {
		script, err := m.parser.ParseFile(old.FilePath)
		if err != nil {
			log.Printf("[RuleManager] WARNING: reload %s failed: %v", id, err)
			continue
		}
		script.Enabled = old.Enabled
		script.Stats = old.Stats
		m.scripts[id] = script
		m.preCompileScripts(script)
	}

	m.compileToEngine()

	log.Printf("[RuleManager] Reloaded all scripts")
	return nil
}

// Validate 校验规则内容（不保存）
func (m *RuleManager) Validate(content string) []ParseError {
	_, errors := m.parser.ParseContent(content)
	return errors
}

// GetEngine 获取关联的 RuleEngine（供外部使用）
func (m *RuleManager) GetEngine() *RuleEngine {
	return m.engine
}

// ========================================
// 内部方法
// ========================================

// compileToEngine 将当前所有脚本编译到引擎（调用方需持有 mu 锁）
func (m *RuleManager) compileToEngine() {
	scripts := make([]*RuleScript, 0, len(m.scripts))
	for _, s := range m.scripts {
		scripts = append(scripts, s)
	}
	m.engine.Compile(scripts)
}

// preCompileScripts 预编译脚本的 @script 块（调用方需持有 mu 锁）
// 如果 Loader 未设置或脚本没有 @script 块，则跳过
func (m *RuleManager) preCompileScripts(s *RuleScript) {
	if m.loader == nil || len(s.Scripts) == 0 {
		return
	}
	progs, err := m.loader.CompileMulti(s.Scripts)
	if err != nil {
		log.Printf("[RuleManager] WARNING: pre-compile %s scripts failed: %v", s.ID, err)
		return
	}
	s.CompiledPrograms = progs
	log.Printf("[RuleManager] Pre-compiled %d scripts for %s", len(progs), s.ID)
}

// snapshotModTimes 快照当前所有规则文件的修改时间
func (m *RuleManager) snapshotModTimes() map[string]time.Time {
	modTimes := make(map[string]time.Time)

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, s := range m.scripts {
		if info, err := os.Stat(s.FilePath); err == nil {
			modTimes[s.ID] = info.ModTime()
		}
	}
	return modTimes
}

// checkAndReload 检查文件变更并热加载
func (m *RuleManager) checkAndReload(modTimes map[string]time.Time) {
	changed := false

	m.mu.RLock()
	toReload := make([]string, 0)
	toRecreate := make([]string, 0) // 被删除后又重建的文件（需自动启用）
	toDelete := make([]string, 0)   // 被删除的文件
	for id, s := range m.scripts {
		info, err := os.Stat(s.FilePath)
		if err != nil {
			// 文件被删除，标记待移除
			toDelete = append(toDelete, id)
			continue
		}
		lastMod, ok := modTimes[id]
		if !ok {
			// 此文件在上次快照时不存在 → 被删除后重建，自动启用
			toRecreate = append(toRecreate, id)
			modTimes[id] = info.ModTime()
		} else if info.ModTime().After(lastMod) {
			toReload = append(toReload, id)
			modTimes[id] = info.ModTime()
		}
	}
	m.mu.RUnlock()

	// 检查新增文件
	entries, err := os.ReadDir(m.dir)
	if err == nil {
		m.mu.RLock()
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".rules") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".rules")
			if _, exists := m.scripts[id]; !exists {
				toReload = append(toReload, "new:"+entry.Name())
			}
		}
		m.mu.RUnlock()
	}

	if len(toReload) == 0 {
		return
	}

	// 执行重载
	m.mu.Lock()
	defer m.mu.Unlock()

	// 处理被删除的文件
	for _, id := range toDelete {
		delete(m.scripts, id)
		delete(modTimes, id)
		os.Remove(m.disabledMarkerPath(id)) // 清理禁用标记
		log.Printf("[RuleManager] Hot reload: removed deleted script %s", id)
		changed = true
	}

	// 处理被删除后又重建的文件（自动启用）
	for _, id := range toRecreate {
		old, ok := m.scripts[id]
		if !ok {
			continue
		}
		script, parseErr := m.parser.ParseFile(old.FilePath)
		if parseErr != nil {
			log.Printf("[RuleManager] Hot reload: skip recreated %s: %v", id, parseErr)
			continue
		}
		script.Enabled = true // 重建文件，自动启用
		script.Stats = old.Stats
		m.scripts[id] = script
		m.preCompileScripts(script)
		log.Printf("[RuleManager] Hot reload: recreated script %s (auto-enabled)", id)
		changed = true
	}

	for _, item := range toReload {
		if strings.HasPrefix(item, "new:") {
			// 新文件
			fileName := strings.TrimPrefix(item, "new:")
			path := filepath.Join(m.dir, fileName)
			script, parseErr := m.parser.ParseFile(path)
			if parseErr != nil {
				log.Printf("[RuleManager] Hot reload: skip new file %s: %v", fileName, parseErr)
				continue
			}
			m.scripts[script.ID] = script
			m.preCompileScripts(script)
			log.Printf("[RuleManager] Hot reload: loaded new script %s", script.ID)
			changed = true
		} else {
			// 已有文件变更
			old, ok := m.scripts[item]
			if !ok {
				continue
			}
			script, parseErr := m.parser.ParseFile(old.FilePath)
			if parseErr != nil {
				log.Printf("[RuleManager] Hot reload: skip %s: %v", item, parseErr)
				continue
			}
			script.Enabled = old.Enabled
			script.Stats = old.Stats
			m.scripts[item] = script
			m.preCompileScripts(script)
			log.Printf("[RuleManager] Hot reload: reloaded %s", item)
			changed = true
		}
	}

	if changed {
		m.compileToEngine()
		log.Printf("[RuleManager] Hot reload: engine recompiled")
	}
}

// sanitizeID 将名称转为文件名安全的 ID
func sanitizeID(name string) string {
	// 替换非字母数字为连字符
	var result strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			result.WriteRune(r)
		} else if r == ' ' || r == '/' || r == '\\' {
			result.WriteRune('-')
		}
	}
	s := result.String()
	// 去除首尾连字符
	s = strings.Trim(s, "-")
	if s == "" {
		return ""
	}
	return s
}
