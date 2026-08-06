package rule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitm-proxy/core/script"
)

// newTestManager 在临时目录上建一个 manager，返回 manager 与目录路径。
func newTestManager(t *testing.T) (*RuleManager, string) {
	t.Helper()
	dir := t.TempDir()
	m := NewRuleManager(ManagerConfig{Dir: dir}, NewRuleEngine())
	return m, dir
}

// writeRuleFile 在规则目录里写一个 .rules 文件。
func writeRuleFile(t *testing.T, dir, id, content string) string {
	t.Helper()
	path := filepath.Join(dir, id+".rules")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

const sampleRule = "@name Sample\n@priority 5\n@intercept example.com\n/^\\/api/ http://backend\n"

func TestSanitizeID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"小写字母数字保留", "abc123", "abc123"},
		{"大写转小写", "MyScript", "myscript"},
		{"空格转连字符", "my script", "my-script"},
		{"斜杠转连字符", "my/script", "my-script"},
		{"反斜杠转连字符", "my\\script", "my-script"},
		{"连字符与下划线保留", "my-script_v2", "my-script_v2"},
		{"其他符号被丢弃", "my@script!", "myscript"},
		{"中文被丢弃", "我的脚本", ""},
		{"中英混合只保留英文", "脚本abc", "abc"},
		{"首尾连字符被去除", " my script ", "my-script"},
		{"纯符号返回空", "@#$%", ""},
		{"空字符串", "", ""},
		{"纯空格返回空", "   ", ""},
		{"点号被丢弃", "script.rules", "scriptrules"},
		{"路径穿越字符被消解", "../../etc/passwd", "etc-passwd"},
		{"上级目录记号被消解", "..", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeID(tt.in); got != tt.want {
				t.Errorf("sanitizeID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeIDNeverEscapesTheRulesDir(t *testing.T) {
	// disabledMarkerPath 与 Create 都把 ID 直接拼进路径，
	// 因此 sanitizeID 的产物绝不能含有路径分隔符或上级目录记号。
	inputs := []string{
		"../../etc/passwd",
		"..\\..\\windows\\system32",
		"a/../../b",
		"....//....//etc",
		"/absolute/path",
		"..",
		".",
	}
	for _, in := range inputs {
		id := sanitizeID(in)
		if strings.ContainsAny(id, `/\`) {
			t.Errorf("sanitizeID(%q) = %q, which still contains a path separator", in, id)
		}
		if id == ".." || strings.Contains(id, "..") {
			t.Errorf("sanitizeID(%q) = %q, which still contains a parent-dir reference", in, id)
		}
		if id == "" {
			continue // 空 ID 由调用方回退到时间戳
		}
		// 拼接后必须仍落在规则目录内
		joined := filepath.Join("/rules", id+".rules")
		if !strings.HasPrefix(joined, "/rules/") {
			t.Errorf("sanitizeID(%q) = %q escapes the rules dir: %s", in, id, joined)
		}
	}
}

func TestNewRuleManagerDefaults(t *testing.T) {
	m := NewRuleManager(ManagerConfig{}, NewRuleEngine())
	if m.dir != "rules" {
		t.Errorf("dir = %q, want the default \"rules\"", m.dir)
	}
	if m.watchInterval != 2*time.Second {
		t.Errorf("watchInterval = %v, want the default 2s", m.watchInterval)
	}

	m2 := NewRuleManager(ManagerConfig{Dir: "custom", WatchInterval: -1}, NewRuleEngine())
	if m2.dir != "custom" {
		t.Errorf("dir = %q, want custom", m2.dir)
	}
	if m2.watchInterval != 2*time.Second {
		t.Errorf("watchInterval = %v, want the default for a negative input", m2.watchInterval)
	}
}

func TestManagerGetEngine(t *testing.T) {
	e := NewRuleEngine()
	m := NewRuleManager(ManagerConfig{Dir: t.TempDir()}, e)
	if m.GetEngine() != e {
		t.Error("GetEngine should return the engine passed to the constructor")
	}
}

func TestManagerLoadAll(t *testing.T) {
	t.Run("加载目录下的 .rules 文件", func(t *testing.T) {
		m, dir := newTestManager(t)
		writeRuleFile(t, dir, "alpha", sampleRule)
		writeRuleFile(t, dir, "beta", "@name Beta\n/^\\/b/ http://b\n")
		// 非 .rules 文件应被忽略
		os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0644)
		os.Mkdir(filepath.Join(dir, "subdir.rules"), 0755) // 目录也应被跳过

		if err := m.LoadAll(); err != nil {
			t.Fatalf("LoadAll: %v", err)
		}

		if got := len(m.List()); got != 2 {
			t.Fatalf("loaded %d scripts, want 2", got)
		}
		s, ok := m.Get("alpha")
		if !ok {
			t.Fatal("alpha should have been loaded")
		}
		if s.Name != "Sample" || s.Priority != 5 {
			t.Errorf("got (name=%q, priority=%d), want (Sample, 5)", s.Name, s.Priority)
		}
	})

	t.Run("目录不存在时自动创建", func(t *testing.T) {
		base := t.TempDir()
		dir := filepath.Join(base, "nested", "rules")
		m := NewRuleManager(ManagerConfig{Dir: dir}, NewRuleEngine())

		if err := m.LoadAll(); err != nil {
			t.Fatalf("LoadAll: %v", err)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("LoadAll should have created %s", dir)
		}
		if got := len(m.List()); got != 0 {
			t.Errorf("loaded %d scripts, want 0", got)
		}
	})

	t.Run("加载后编译进引擎", func(t *testing.T) {
		m, dir := newTestManager(t)
		writeRuleFile(t, dir, "alpha", sampleRule)
		if err := m.LoadAll(); err != nil {
			t.Fatalf("LoadAll: %v", err)
		}
		if !m.GetEngine().ShouldIntercept("example.com") {
			t.Error("the engine should have been compiled with the loaded @intercept domain")
		}
	})

	t.Run("尊重持久化的禁用标记", func(t *testing.T) {
		m, dir := newTestManager(t)
		writeRuleFile(t, dir, "alpha", sampleRule)
		// 预置禁用标记文件
		os.WriteFile(filepath.Join(dir, ".alpha.disabled"), []byte("1"), 0644)

		if err := m.LoadAll(); err != nil {
			t.Fatalf("LoadAll: %v", err)
		}
		s, _ := m.Get("alpha")
		if s.Enabled {
			t.Error("alpha should be disabled because a persisted marker exists")
		}
		if m.GetEngine().ShouldIntercept("example.com") {
			t.Error("a disabled script must not contribute intercepts")
		}
	})
}

func TestManagerCreate(t *testing.T) {
	t.Run("创建并落盘", func(t *testing.T) {
		m, dir := newTestManager(t)

		s, err := m.Create("My Script", sampleRule)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if s.ID != "my-script" {
			t.Errorf("ID = %q, want my-script (sanitized)", s.ID)
		}

		path := filepath.Join(dir, "my-script.rules")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("the rule file should exist on disk: %v", err)
		}
		if string(content) != sampleRule {
			t.Error("the file content does not match what was passed to Create")
		}

		if _, ok := m.Get("my-script"); !ok {
			t.Error("the created script should be retrievable via Get")
		}
	})

	t.Run("内容未声明 @name 时回退到入参", func(t *testing.T) {
		m, _ := newTestManager(t)
		s, err := m.Create("Fallback Name", "/^\\/x/ http://x\n")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if s.Name != "Fallback Name" {
			t.Errorf("Name = %q, want the argument to be used when @name is absent", s.Name)
		}
	})

	t.Run("内容声明 @name 时优先使用声明值", func(t *testing.T) {
		m, _ := newTestManager(t)
		s, err := m.Create("Arg Name", "@name Declared\n/^\\/x/ http://x\n")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if s.Name != "Declared" {
			t.Errorf("Name = %q, want the @name directive to win", s.Name)
		}
	})

	t.Run("重复 ID 报错", func(t *testing.T) {
		m, _ := newTestManager(t)
		if _, err := m.Create("dup", sampleRule); err != nil {
			t.Fatalf("first Create: %v", err)
		}
		_, err := m.Create("dup", sampleRule)
		if err == nil {
			t.Fatal("creating a duplicate ID should fail")
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("error = %q, want it to mention the conflict", err)
		}
	})

	t.Run("名称无法生成 ID 时回退到时间戳", func(t *testing.T) {
		m, _ := newTestManager(t)
		s, err := m.Create("我的脚本", sampleRule)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if !strings.HasPrefix(s.ID, "script-") {
			t.Errorf("ID = %q, want a script-<timestamp> fallback", s.ID)
		}
	})

	t.Run("创建后引擎已重编译", func(t *testing.T) {
		m, _ := newTestManager(t)
		if _, err := m.Create("intercepting", sampleRule); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if !m.GetEngine().ShouldIntercept("example.com") {
			t.Error("Create should recompile the engine")
		}
	})
}

func TestManagerUpdate(t *testing.T) {
	t.Run("更新内容与文件", func(t *testing.T) {
		m, dir := newTestManager(t)
		m.Create("target", sampleRule)

		updated := "@name Updated\n@intercept new.com\n/^\\/v2/ http://v2\n"
		if err := m.Update("target", updated); err != nil {
			t.Fatalf("Update: %v", err)
		}

		s, _ := m.Get("target")
		if s.Name != "Updated" {
			t.Errorf("Name = %q, want Updated", s.Name)
		}

		content, _ := os.ReadFile(filepath.Join(dir, "target.rules"))
		if string(content) != updated {
			t.Error("the file on disk was not rewritten")
		}
		if !m.GetEngine().ShouldIntercept("new.com") {
			t.Error("Update should recompile the engine")
		}
	})

	t.Run("保留启用状态与命中统计", func(t *testing.T) {
		m, _ := newTestManager(t)
		m.Create("target", sampleRule)
		s, _ := m.Get("target")
		s.Stats.TotalHits = 99
		s.Stats.RuleHits = map[int]int64{4: 99}
		m.Disable("target")

		if err := m.Update("target", "@name V2\n/^\\/v2/ http://v2\n"); err != nil {
			t.Fatalf("Update: %v", err)
		}

		updated, _ := m.Get("target")
		if updated.Enabled {
			t.Error("Update should preserve the disabled state")
		}
		if got := updated.Stats.Hits(); got != 99 {
			t.Errorf("TotalHits = %d, want 99 preserved across the update", got)
		}
		if got := updated.Stats.RuleHits[4]; got != 99 {
			t.Errorf("RuleHits[4] = %d, want 99 preserved", got)
		}
	})

	t.Run("脚本不存在报错", func(t *testing.T) {
		m, _ := newTestManager(t)
		err := m.Update("ghost", sampleRule)
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v, want a not-found error", err)
		}
	})

	t.Run("内容有解析错误仍然保存", func(t *testing.T) {
		m, _ := newTestManager(t)
		m.Create("target", sampleRule)

		// 行级容错：非法行不应阻止保存
		if err := m.Update("target", "@name Broken\n/[unclosed/ http://x\n"); err != nil {
			t.Fatalf("Update should tolerate parse errors, got %v", err)
		}
		s, _ := m.Get("target")
		if len(s.ParseErrors) == 0 {
			t.Error("the parse errors should be recorded on the script")
		}
	})
}

func TestManagerDelete(t *testing.T) {
	t.Run("删除脚本与文件", func(t *testing.T) {
		m, dir := newTestManager(t)
		m.Create("victim", sampleRule)

		if err := m.Delete("victim"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, ok := m.Get("victim"); ok {
			t.Error("the script should be gone from the manager")
		}
		if _, err := os.Stat(filepath.Join(dir, "victim.rules")); !os.IsNotExist(err) {
			t.Error("the rule file should have been removed from disk")
		}
		if m.GetEngine().ShouldIntercept("example.com") {
			t.Error("Delete should recompile the engine")
		}
	})

	t.Run("一并清理禁用标记", func(t *testing.T) {
		m, dir := newTestManager(t)
		m.Create("victim", sampleRule)
		m.Disable("victim")

		marker := filepath.Join(dir, ".victim.disabled")
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("the marker should exist before delete: %v", err)
		}

		if err := m.Delete("victim"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Error("the disabled marker should be cleaned up, otherwise a recreated script would load disabled")
		}
	})

	t.Run("脚本不存在报错", func(t *testing.T) {
		m, _ := newTestManager(t)
		err := m.Delete("ghost")
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("err = %v, want a not-found error", err)
		}
	})

	t.Run("文件已被外部删除时仍能成功", func(t *testing.T) {
		m, dir := newTestManager(t)
		m.Create("victim", sampleRule)
		os.Remove(filepath.Join(dir, "victim.rules"))

		if err := m.Delete("victim"); err != nil {
			t.Errorf("Delete should tolerate a missing file, got %v", err)
		}
		if _, ok := m.Get("victim"); ok {
			t.Error("the script should still be removed from the manager")
		}
	})
}

func TestManagerEnableDisable(t *testing.T) {
	t.Run("停用后持久化标记并退出引擎", func(t *testing.T) {
		m, dir := newTestManager(t)
		m.Create("toggle", sampleRule)

		if err := m.Disable("toggle"); err != nil {
			t.Fatalf("Disable: %v", err)
		}
		s, _ := m.Get("toggle")
		if s.Enabled {
			t.Error("the script should be disabled")
		}
		if _, err := os.Stat(filepath.Join(dir, ".toggle.disabled")); err != nil {
			t.Errorf("a disabled marker file should exist: %v", err)
		}
		if m.GetEngine().ShouldIntercept("example.com") {
			t.Error("a disabled script must not contribute intercepts")
		}
	})

	t.Run("启用后清除标记并回到引擎", func(t *testing.T) {
		m, dir := newTestManager(t)
		m.Create("toggle", sampleRule)
		m.Disable("toggle")

		if err := m.Enable("toggle"); err != nil {
			t.Fatalf("Enable: %v", err)
		}
		s, _ := m.Get("toggle")
		if !s.Enabled {
			t.Error("the script should be enabled")
		}
		if _, err := os.Stat(filepath.Join(dir, ".toggle.disabled")); !os.IsNotExist(err) {
			t.Error("the disabled marker should have been removed")
		}
		if !m.GetEngine().ShouldIntercept("example.com") {
			t.Error("a re-enabled script should contribute intercepts again")
		}
	})

	t.Run("重复操作是幂等的", func(t *testing.T) {
		m, _ := newTestManager(t)
		m.Create("toggle", sampleRule)

		if err := m.Enable("toggle"); err != nil { // 本就是启用状态
			t.Errorf("Enable on an already-enabled script should be a no-op, got %v", err)
		}
		m.Disable("toggle")
		if err := m.Disable("toggle"); err != nil {
			t.Errorf("Disable on an already-disabled script should be a no-op, got %v", err)
		}
	})

	t.Run("脚本不存在报错", func(t *testing.T) {
		m, _ := newTestManager(t)
		if err := m.Enable("ghost"); err == nil {
			t.Error("Enable on a missing script should fail")
		}
		if err := m.Disable("ghost"); err == nil {
			t.Error("Disable on a missing script should fail")
		}
	})
}

func TestManagerDisabledStateSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	writeRuleFile(t, dir, "persist", sampleRule)

	m1 := NewRuleManager(ManagerConfig{Dir: dir}, NewRuleEngine())
	if err := m1.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if err := m1.Disable("persist"); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	// 新 manager 指向同一目录，模拟进程重启
	m2 := NewRuleManager(ManagerConfig{Dir: dir}, NewRuleEngine())
	if err := m2.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	s, ok := m2.Get("persist")
	if !ok {
		t.Fatal("the script should be loaded after restart")
	}
	if s.Enabled {
		t.Error("the disabled state should survive a restart via the marker file")
	}
}

func TestManagerList(t *testing.T) {
	m, dir := newTestManager(t)
	writeRuleFile(t, dir, "b-mid", "@name B\n@priority 50\n")
	writeRuleFile(t, dir, "a-mid", "@name A\n@priority 50\n")
	writeRuleFile(t, dir, "high", "@name H\n@priority 100\n")
	writeRuleFile(t, dir, "low", "@name L\n@priority 1\n")
	if err := m.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	list := m.List()
	// 优先级降序；同优先级按 ID 升序，保证顺序稳定
	want := []string{"high", "a-mid", "b-mid", "low"}
	if len(list) != len(want) {
		t.Fatalf("got %d scripts, want %d", len(list), len(want))
	}
	for i, id := range want {
		if list[i].ID != id {
			t.Errorf("list[%d].ID = %q, want %q", i, list[i].ID, id)
		}
	}
}

func TestManagerListEmpty(t *testing.T) {
	m, _ := newTestManager(t)
	if got := m.List(); len(got) != 0 {
		t.Errorf("got %d scripts, want 0", len(got))
	}
}

func TestManagerGetMissing(t *testing.T) {
	m, _ := newTestManager(t)
	if s, ok := m.Get("ghost"); ok || s != nil {
		t.Errorf("Get on a missing id = (%v, %v), want (nil, false)", s, ok)
	}
}

func TestManagerReload(t *testing.T) {
	t.Run("从磁盘重新读取", func(t *testing.T) {
		m, dir := newTestManager(t)
		m.Create("target", sampleRule)

		// 绕过 manager 直接改文件，模拟外部编辑
		writeRuleFile(t, dir, "target", "@name Edited\n@intercept edited.com\n")

		if err := m.Reload("target"); err != nil {
			t.Fatalf("Reload: %v", err)
		}
		s, _ := m.Get("target")
		if s.Name != "Edited" {
			t.Errorf("Name = %q, want Edited", s.Name)
		}
		if !m.GetEngine().ShouldIntercept("edited.com") {
			t.Error("Reload should recompile the engine")
		}
	})

	t.Run("保留启用状态与统计", func(t *testing.T) {
		m, dir := newTestManager(t)
		m.Create("target", sampleRule)
		s, _ := m.Get("target")
		s.Stats.TotalHits = 7
		m.Disable("target")

		writeRuleFile(t, dir, "target", "@name Edited\n")
		if err := m.Reload("target"); err != nil {
			t.Fatalf("Reload: %v", err)
		}

		reloaded, _ := m.Get("target")
		if reloaded.Enabled {
			t.Error("Reload should preserve the disabled state")
		}
		if got := reloaded.Stats.Hits(); got != 7 {
			t.Errorf("TotalHits = %d, want 7 preserved", got)
		}
	})

	t.Run("脚本不存在报错", func(t *testing.T) {
		m, _ := newTestManager(t)
		if err := m.Reload("ghost"); err == nil {
			t.Error("Reload on a missing script should fail")
		}
	})

	t.Run("文件已删除时报错", func(t *testing.T) {
		m, dir := newTestManager(t)
		m.Create("target", sampleRule)
		os.Remove(filepath.Join(dir, "target.rules"))

		if err := m.Reload("target"); err == nil {
			t.Error("Reload should fail when the underlying file is gone")
		}
	})
}

func TestManagerReloadAll(t *testing.T) {
	m, dir := newTestManager(t)
	m.Create("one", "@name One\n@intercept one.com\n")
	m.Create("two", "@name Two\n@intercept two.com\n")
	m.Disable("two")

	writeRuleFile(t, dir, "one", "@name One Edited\n@intercept one-edited.com\n")
	writeRuleFile(t, dir, "two", "@name Two Edited\n@intercept two-edited.com\n")

	if err := m.ReloadAll(); err != nil {
		t.Fatalf("ReloadAll: %v", err)
	}

	one, _ := m.Get("one")
	if one.Name != "One Edited" {
		t.Errorf("one.Name = %q, want One Edited", one.Name)
	}
	two, _ := m.Get("two")
	if two.Name != "Two Edited" {
		t.Errorf("two.Name = %q, want Two Edited", two.Name)
	}
	if two.Enabled {
		t.Error("ReloadAll should preserve the disabled state")
	}
	if !m.GetEngine().ShouldIntercept("one-edited.com") {
		t.Error("ReloadAll should recompile the engine")
	}
	if m.GetEngine().ShouldIntercept("two-edited.com") {
		t.Error("the disabled script must not contribute intercepts")
	}
}

func TestManagerReloadAllSkipsUnreadableFiles(t *testing.T) {
	m, dir := newTestManager(t)
	m.Create("good", "@name Good\n")
	m.Create("gone", "@name Gone\n")
	os.Remove(filepath.Join(dir, "gone.rules"))

	// 单个文件失败不应中断整体重载
	if err := m.ReloadAll(); err != nil {
		t.Fatalf("ReloadAll should tolerate individual failures, got %v", err)
	}
	if _, ok := m.Get("good"); !ok {
		t.Error("the readable script should still be present")
	}
}

func TestManagerValidate(t *testing.T) {
	m, _ := newTestManager(t)

	if errs := m.Validate(sampleRule); len(errs) != 0 {
		t.Errorf("valid content produced %d errors: %+v", len(errs), errs)
	}

	errs := m.Validate("@name Broken\n/[unclosed/ http://x\n")
	if len(errs) == 0 {
		t.Error("malformed content should produce parse errors")
	}

	// Validate 不落盘、不注册脚本
	if got := len(m.List()); got != 0 {
		t.Errorf("Validate should not register anything, got %d scripts", got)
	}
}

func TestManagerPreCompileSkippedWithoutLoader(t *testing.T) {
	m, _ := newTestManager(t) // loader 为 nil
	content := "@name JS\n@script onRequest\nfunction onRequest(ctx) { return ctx; }\n@end\n"

	s, err := m.Create("js", content)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.CompiledPrograms != nil {
		t.Error("without a loader the manager should skip pre-compilation")
	}
}

func TestManagerPreCompileWithLoader(t *testing.T) {
	dir := t.TempDir()
	m := NewRuleManager(ManagerConfig{Dir: dir, ScriptLoader: script.NewLoader()}, NewRuleEngine())
	content := "@name JS\n@script onRequest\nfunction onRequest(ctx) { return ctx; }\n@end\n"

	s, err := m.Create("js", content)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(s.CompiledPrograms) == 0 {
		t.Fatal("the JS block should have been pre-compiled")
	}
	if _, ok := s.CompiledPrograms["onRequest"]; !ok {
		t.Errorf("CompiledPrograms = %v, want an onRequest entry", s.CompiledPrograms)
	}
}

func TestManagerPreCompileInvalidJSIsTolerated(t *testing.T) {
	dir := t.TempDir()
	m := NewRuleManager(ManagerConfig{Dir: dir, ScriptLoader: script.NewLoader()}, NewRuleEngine())
	content := "@name JS\n@script onRequest\nthis is not ( valid javascript\n@end\n"

	s, err := m.Create("js", content)
	if err != nil {
		t.Fatalf("Create should not fail on a JS compile error, got %v", err)
	}
	// 编译失败时保持 nil，运行时回退到解释执行而不是崩溃
	if s.CompiledPrograms != nil {
		t.Errorf("CompiledPrograms = %v, want nil after a compile failure", s.CompiledPrograms)
	}
}

func TestManagerDisabledMarkerPath(t *testing.T) {
	m := NewRuleManager(ManagerConfig{Dir: "/tmp/rules"}, NewRuleEngine())
	want := filepath.Join("/tmp/rules", ".my-id.disabled")
	if got := m.disabledMarkerPath("my-id"); got != want {
		t.Errorf("disabledMarkerPath = %q, want %q", got, want)
	}
}

func TestManagerSnapshotModTimes(t *testing.T) {
	m, dir := newTestManager(t)
	m.Create("one", sampleRule)
	m.Create("two", sampleRule)

	snap := m.snapshotModTimes()
	if len(snap) != 2 {
		t.Fatalf("snapshot has %d entries, want 2", len(snap))
	}
	for _, id := range []string{"one", "two"} {
		if snap[id].IsZero() {
			t.Errorf("snapshot[%q] is zero, want a real modtime", id)
		}
	}

	// 文件消失后不应出现在快照里
	os.Remove(filepath.Join(dir, "one.rules"))
	if snap2 := m.snapshotModTimes(); len(snap2) != 1 {
		t.Errorf("snapshot has %d entries, want 1 after a file was removed", len(snap2))
	}
}

func TestManagerStopHotReloadWhenDisabled(t *testing.T) {
	m, _ := newTestManager(t)
	m.StartHotReload() // hotReload=false，应直接返回且不起协程
	m.StopHotReload()  // 不应 panic
}

func TestManagerHotReloadDetectsChanges(t *testing.T) {
	dir := t.TempDir()
	writeRuleFile(t, dir, "watched", "@name Original\n@intercept original.com\n")
	m := NewRuleManager(ManagerConfig{
		Dir: dir, HotReload: true, WatchInterval: 10 * time.Millisecond,
	}, NewRuleEngine())
	if err := m.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	m.StartHotReload()
	defer m.StopHotReload()

	// modtime 精度有限，先等一拍再改文件
	time.Sleep(20 * time.Millisecond)
	writeRuleFile(t, dir, "watched", "@name Changed\n@intercept changed.com\n")

	if !eventually(t, time.Second, func() bool {
		s, ok := m.Get("watched")
		return ok && s.Name == "Changed"
	}) {
		s, _ := m.Get("watched")
		t.Fatalf("hot reload did not pick up the edit, name is still %q", s.Name)
	}
	if !m.GetEngine().ShouldIntercept("changed.com") {
		t.Error("hot reload should recompile the engine")
	}
}

func TestManagerHotReloadDetectsNewFile(t *testing.T) {
	dir := t.TempDir()
	m := NewRuleManager(ManagerConfig{
		Dir: dir, HotReload: true, WatchInterval: 10 * time.Millisecond,
	}, NewRuleEngine())
	if err := m.LoadAll(); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	m.StartHotReload()
	defer m.StopHotReload()

	writeRuleFile(t, dir, "fresh", "@name Fresh\n@intercept fresh.com\n")

	if !eventually(t, time.Second, func() bool {
		_, ok := m.Get("fresh")
		return ok
	}) {
		t.Fatal("hot reload did not pick up the new file")
	}
}

func TestManagerCheckAndReloadRemovesDeletedFile(t *testing.T) {
	m, dir := newTestManager(t)
	m.Create("doomed", sampleRule)

	modTimes := m.snapshotModTimes()
	os.Remove(filepath.Join(dir, "doomed.rules"))

	// 直接驱动一轮检查，避免依赖 ticker 时序
	m.checkAndReload(modTimes)

	if _, ok := m.Get("doomed"); ok {
		t.Error("a script whose file was deleted should be dropped")
	}
	if m.GetEngine().ShouldIntercept("example.com") {
		t.Error("the engine should be recompiled after the removal")
	}
}

func TestManagerCheckAndReloadRecreatedFileAutoEnables(t *testing.T) {
	m, dir := newTestManager(t)
	m.Create("phoenix", sampleRule)
	m.Disable("phoenix")

	// 快照为空 → 模拟"上次检查时文件不存在"，本轮发现它存在即视为重建
	m.checkAndReload(map[string]time.Time{})

	s, ok := m.Get("phoenix")
	if !ok {
		t.Fatal("the script should still be registered")
	}
	if !s.Enabled {
		t.Error("a recreated file should be auto-enabled")
	}
	_ = dir
}

func TestManagerCheckAndReloadNoChanges(t *testing.T) {
	m, _ := newTestManager(t)
	m.Create("stable", sampleRule)
	before, _ := m.Get("stable")

	m.checkAndReload(m.snapshotModTimes())

	after, _ := m.Get("stable")
	// 无变更时不应重建对象
	if before != after {
		t.Error("checkAndReload should leave unchanged scripts untouched")
	}
}

// eventually 轮询 cond 直到超时，返回是否满足。
func eventually(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
