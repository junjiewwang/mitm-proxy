package rule

import (
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"
	"testing"

	"mitm-proxy/core/proxy"
)

func TestMain(m *testing.M) {
	// 引擎在 Compile 时会打印统计与冲突日志，测试中静音以保持输出可读
	log.SetOutput(io.Discard)
	code := m.Run()
	log.SetOutput(os.Stderr)
	os.Exit(code)
}

// mustReq 构造一个用于匹配的请求；rawURL 需包含 scheme 和 host。
func mustReq(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("build request %q: %v", rawURL, err)
	}
	return r
}

func TestReplaceCaptures(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		captures []string
		want     string
	}{
		{
			name:     "单个捕获组",
			target:   "http://backend/$1",
			captures: []string{"/api/users", "users"},
			want:     "http://backend/users",
		},
		{
			name:     "多个捕获组",
			target:   "http://backend/$1/$2",
			captures: []string{"/api/v1/users", "v1", "users"},
			want:     "http://backend/v1/users",
		},
		{
			name:     "同一占位符重复出现",
			target:   "http://$1.internal/$1",
			captures: []string{"/svc", "svc"},
			want:     "http://svc.internal/svc",
		},
		{
			name:     "无占位符时原样返回",
			target:   "http://backend/fixed",
			captures: []string{"/api", "api"},
			want:     "http://backend/fixed",
		},
		{
			name:     "captures 只有整体匹配时不替换",
			target:   "http://backend/$1",
			captures: []string{"/api"},
			want:     "http://backend/$1",
		},
		{
			// $0 不是捕获组占位符，替换从 $1 开始
			name:     "$0 不被替换",
			target:   "http://backend/$0",
			captures: []string{"/api", "api"},
			want:     "http://backend/$0",
		},
		{
			name:     "超出捕获数量的占位符保留原样",
			target:   "http://backend/$1/$5",
			captures: []string{"/api/x", "x"},
			want:     "http://backend/x/$5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := replaceCaptures(tt.target, tt.captures); got != tt.want {
				t.Errorf("replaceCaptures(%q, %v) = %q, want %q", tt.target, tt.captures, got, tt.want)
			}
		})
	}
}

func TestEngineCompileSortsByPriorityAndFiltersDisabled(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{ID: "low", Priority: 1, Enabled: true},
		{ID: "high", Priority: 100, Enabled: true},
		{ID: "off", Priority: 999, Enabled: false}, // 禁用脚本不应参与
		{ID: "mid", Priority: 50, Enabled: true},
	})

	if len(e.scripts) != 3 {
		t.Fatalf("compiled %d scripts, want 3 (disabled one excluded)", len(e.scripts))
	}
	want := []string{"high", "mid", "low"}
	for i, id := range want {
		if e.scripts[i].ID != id {
			t.Errorf("scripts[%d] = %q, want %q (descending priority)", i, e.scripts[i].ID, id)
		}
	}
}

func TestEngineShouldIntercept(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{ID: "s1", Enabled: true, Intercepts: []string{"api.example.com", "*.wildcard.com"}},
		{ID: "s2", Enabled: true, Intercepts: []string{"other.com"}},
		{ID: "off", Enabled: false, Intercepts: []string{"disabled.com"}},
	})

	tests := []struct {
		hostname string
		want     bool
		why      string
	}{
		{"api.example.com", true, "精确命中"},
		{"other.com", true, "并集包含第二个脚本的域名"},
		{"sub.wildcard.com", true, "通配符匹配子域名"},
		{"deep.sub.wildcard.com", true, "通配符匹配多级子域名"},
		{"nope.com", false, "未声明的域名"},
		{"disabled.com", false, "禁用脚本的域名不生效"},
		{"api.example.com.evil.com", false, "后缀伪造不应命中精确域名"},
		{"wildcard.com", false, "*.wildcard.com 不匹配裸域名"},
		{"", false, "空 hostname"},
	}
	for _, tt := range tests {
		if got := e.ShouldIntercept(tt.hostname); got != tt.want {
			t.Errorf("ShouldIntercept(%q) = %v, want %v (%s)", tt.hostname, got, tt.want, tt.why)
		}
	}
}

func TestEngineHostsMergeAndResolveUpstream(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "s1", Priority: 10, Enabled: true,
			Rules: []Rule{
				{Type: RuleTypeHosts, Line: 1, IP: "1.1.1.1", Domains: []string{"a.com"}},
				{Type: RuleTypeHosts, Line: 2, IP: "2.2.2.2:8080", Domains: []string{"b.com", "c.com"}},
			},
		},
	})

	tests := []struct {
		domain string
		want   string
		ok     bool
	}{
		{"a.com", "1.1.1.1:443", true},   // 无端口时补默认 443
		{"b.com", "2.2.2.2:8080", true},  // 显式端口保留
		{"c.com", "2.2.2.2:8080", true},  // 一条规则的多个域名都生效
		{"unknown.com", "", false},
	}
	for _, tt := range tests {
		got, ok := e.ResolveUpstream(tt.domain)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ResolveUpstream(%q) = (%q, %v), want (%q, %v)", tt.domain, got, ok, tt.want, tt.ok)
		}
	}
}

func TestEngineHostsHighPriorityWins(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "low", Priority: 1, Enabled: true,
			Rules: []Rule{{Type: RuleTypeHosts, Line: 3, IP: "1.1.1.1", Domains: []string{"shared.com"}}},
		},
		{
			ID: "high", Priority: 100, Enabled: true,
			Rules: []Rule{{Type: RuleTypeHosts, Line: 7, IP: "9.9.9.9", Domains: []string{"shared.com"}}},
		},
	})

	got, ok := e.ResolveUpstream("shared.com")
	if !ok || got != "9.9.9.9:443" {
		t.Errorf("ResolveUpstream(shared.com) = (%q, %v), want the high-priority script to win with 9.9.9.9:443", got, ok)
	}
}

// TestEngineHostsConflictDetection 覆盖 86a0059 引入的冲突检测机制。
func TestEngineHostsConflictDetection(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "low", Priority: 1, Enabled: true,
			Rules: []Rule{{Type: RuleTypeHosts, Line: 3, IP: "1.1.1.1", Domains: []string{"shared.com"}}},
		},
		{
			ID: "high", Priority: 100, Enabled: true,
			Rules: []Rule{{Type: RuleTypeHosts, Line: 7, IP: "9.9.9.9", Domains: []string{"shared.com"}}},
		},
	})

	conflicts := e.HostsConflicts()
	if len(conflicts) != 1 {
		t.Fatalf("got %d conflicts, want 1", len(conflicts))
	}
	c := conflicts[0]
	if c.Domain != "shared.com" {
		t.Errorf("Domain = %q, want shared.com", c.Domain)
	}
	if c.WinnerIP != "9.9.9.9:443" || c.WinnerScript != "high" || c.WinnerLine != 7 {
		t.Errorf("winner = (%s, %s, %d), want (9.9.9.9:443, high, 7)", c.WinnerIP, c.WinnerScript, c.WinnerLine)
	}
	if c.LoserIP != "1.1.1.1:443" || c.LoserScript != "low" || c.LoserLine != 3 {
		t.Errorf("loser = (%s, %s, %d), want (1.1.1.1:443, low, 3)", c.LoserIP, c.LoserScript, c.LoserLine)
	}
}

// TestEngineHostsSameIPNoConflict 是 e112160 的回归测试：
// 同一域名映射到相同 IP 时静默覆盖，不应误报冲突。
func TestEngineHostsSameIPNoConflict(t *testing.T) {
	t.Run("跨脚本相同 IP", func(t *testing.T) {
		e := NewRuleEngine()
		e.Compile([]*RuleScript{
			{
				ID: "a", Priority: 1, Enabled: true,
				Rules: []Rule{{Type: RuleTypeHosts, Line: 3, IP: "1.1.1.1", Domains: []string{"same.com"}}},
			},
			{
				ID: "b", Priority: 100, Enabled: true,
				Rules: []Rule{{Type: RuleTypeHosts, Line: 5, IP: "1.1.1.1", Domains: []string{"same.com"}}},
			},
		})
		if got := e.HostsConflicts(); len(got) != 0 {
			t.Errorf("got %d conflicts, want 0 — identical IPs must not be reported: %+v", len(got), got)
		}
	})

	t.Run("补全端口后等价的写法", func(t *testing.T) {
		// "1.1.1.1" 与 "1.1.1.1:443" 归一化后相同，不应算冲突
		e := NewRuleEngine()
		e.Compile([]*RuleScript{
			{
				ID: "a", Priority: 1, Enabled: true,
				Rules: []Rule{{Type: RuleTypeHosts, Line: 3, IP: "1.1.1.1", Domains: []string{"same.com"}}},
			},
			{
				ID: "b", Priority: 100, Enabled: true,
				Rules: []Rule{{Type: RuleTypeHosts, Line: 5, IP: "1.1.1.1:443", Domains: []string{"same.com"}}},
			},
		})
		if got := e.HostsConflicts(); len(got) != 0 {
			t.Errorf("got %d conflicts, want 0 — 1.1.1.1 and 1.1.1.1:443 are equivalent: %+v", len(got), got)
		}
	})

	t.Run("不同端口仍算冲突", func(t *testing.T) {
		e := NewRuleEngine()
		e.Compile([]*RuleScript{
			{
				ID: "a", Priority: 1, Enabled: true,
				Rules: []Rule{{Type: RuleTypeHosts, Line: 3, IP: "1.1.1.1:8080", Domains: []string{"same.com"}}},
			},
			{
				ID: "b", Priority: 100, Enabled: true,
				Rules: []Rule{{Type: RuleTypeHosts, Line: 5, IP: "1.1.1.1:9090", Domains: []string{"same.com"}}},
			},
		})
		if got := e.HostsConflicts(); len(got) != 1 {
			t.Errorf("got %d conflicts, want 1 — different ports are a real conflict", len(got))
		}
	})
}

func TestEngineHostsConflictSameScript(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "solo", Priority: 1, Enabled: true,
			Rules: []Rule{
				{Type: RuleTypeHosts, Line: 3, IP: "1.1.1.1", Domains: []string{"dup.com"}},
				{Type: RuleTypeHosts, Line: 4, IP: "2.2.2.2", Domains: []string{"dup.com"}},
			},
		},
	})

	conflicts := e.HostsConflicts()
	if len(conflicts) != 1 {
		t.Fatalf("got %d conflicts, want 1 for an intra-script duplicate", len(conflicts))
	}
	c := conflicts[0]
	if c.WinnerScript != c.LoserScript {
		t.Errorf("winner/loser scripts = %q/%q, want both to be solo", c.WinnerScript, c.LoserScript)
	}
	// 同脚本内按文件顺序，后出现的行覆盖先出现的行
	if c.WinnerLine != 4 || c.LoserLine != 3 {
		t.Errorf("winner/loser lines = %d/%d, want 4/3 (later line wins)", c.WinnerLine, c.LoserLine)
	}
}

func TestEngineHostsSnapshotIsACopy(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "s1", Enabled: true,
			Rules: []Rule{{Type: RuleTypeHosts, Line: 1, IP: "1.1.1.1", Domains: []string{"a.com"}}},
		},
	})

	snap := e.HostsSnapshot()
	if snap["a.com"] != "1.1.1.1:443" {
		t.Fatalf("snapshot[a.com] = %q, want 1.1.1.1:443", snap["a.com"])
	}

	// 修改快照不应影响引擎内部状态
	snap["a.com"] = "tampered"
	snap["injected.com"] = "6.6.6.6:443"

	if got, _ := e.ResolveUpstream("a.com"); got != "1.1.1.1:443" {
		t.Errorf("engine state leaked: ResolveUpstream(a.com) = %q", got)
	}
	if _, ok := e.ResolveUpstream("injected.com"); ok {
		t.Error("engine state leaked: injected.com should not resolve")
	}
}

func TestEngineHostsConflictsIsACopy(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "a", Priority: 1, Enabled: true,
			Rules: []Rule{{Type: RuleTypeHosts, Line: 1, IP: "1.1.1.1", Domains: []string{"x.com"}}},
		},
		{
			ID: "b", Priority: 2, Enabled: true,
			Rules: []Rule{{Type: RuleTypeHosts, Line: 2, IP: "2.2.2.2", Domains: []string{"x.com"}}},
		},
	})

	got := e.HostsConflicts()
	if len(got) != 1 {
		t.Fatalf("got %d conflicts, want 1", len(got))
	}
	got[0].Domain = "tampered"

	if again := e.HostsConflicts(); again[0].Domain != "x.com" {
		t.Errorf("engine state leaked: conflict domain became %q", again[0].Domain)
	}
}

func TestEngineCompileReplacesPreviousState(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "old", Enabled: true, Intercepts: []string{"old.com"},
			Rules: []Rule{{Type: RuleTypeHosts, Line: 1, IP: "1.1.1.1", Domains: []string{"old.com"}}},
		},
	})
	e.Compile([]*RuleScript{
		{
			ID: "new", Enabled: true, Intercepts: []string{"new.com"},
			Rules: []Rule{{Type: RuleTypeHosts, Line: 1, IP: "2.2.2.2", Domains: []string{"new.com"}}},
		},
	})

	// 重编译必须完全替换旧状态，而不是叠加
	if e.ShouldIntercept("old.com") {
		t.Error("old.com should no longer be intercepted after recompile")
	}
	if !e.ShouldIntercept("new.com") {
		t.Error("new.com should be intercepted after recompile")
	}
	if _, ok := e.ResolveUpstream("old.com"); ok {
		t.Error("old.com should no longer resolve after recompile")
	}
}

func TestEngineMatchRegexProxy(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "s1", Name: "Script One", Enabled: true,
			Rules: []Rule{{
				Type:    RuleTypeProxy,
				Line:    5,
				Pattern: regexp.MustCompile(`^/api/(\w+)$`),
				Target:  "http://backend/$1",
			}},
		},
	})

	result, ok := e.Match(mustReq(t, "http://example.com/api/users"))
	if !ok {
		t.Fatal("Match should have hit the proxy rule")
	}
	if result.RuleType != proxy.RuleTypeProxy {
		t.Errorf("RuleType = %v, want proxy", result.RuleType)
	}
	if result.Target != "http://backend/users" {
		t.Errorf("Target = %q, want http://backend/users (captures substituted)", result.Target)
	}
	if result.ScriptID != "s1" || result.ScriptName != "Script One" || result.RuleLine != 5 {
		t.Errorf("provenance = (%s, %s, %d), want (s1, Script One, 5)", result.ScriptID, result.ScriptName, result.RuleLine)
	}

	if _, ok := e.Match(mustReq(t, "http://example.com/other")); ok {
		t.Error("Match should not hit for a non-matching path")
	}
}

// TestEngineMatchRegexQueryHandling 覆盖 matchRegex 里按规则类型决定是否拼接 query 的分支。
func TestEngineMatchRegexQueryHandling(t *testing.T) {
	t.Run("proxy 规则只匹配 path", func(t *testing.T) {
		e := NewRuleEngine()
		e.Compile([]*RuleScript{
			{
				ID: "s", Enabled: true,
				Rules: []Rule{{
					Type:    RuleTypeProxy,
					Line:    1,
					Pattern: regexp.MustCompile(`^/api/list$`), // $ 锚点
					Target:  "http://backend/list",
				}},
			},
		})
		// query 存在时仍应命中，否则 $ 锚点会被 query 破坏
		if _, ok := e.Match(mustReq(t, "http://example.com/api/list?page=2")); !ok {
			t.Error("proxy rule should match on path alone, ignoring the query string")
		}
	})

	t.Run("script 规则参与 query 匹配", func(t *testing.T) {
		e := NewRuleEngine()
		e.Compile([]*RuleScript{
			{
				ID: "s", Enabled: true,
				Rules: []Rule{{
					Type:      RuleTypeScript,
					Line:      1,
					Pattern:   regexp.MustCompile(`^/cgi\?serviceType=apm$`),
					ScriptRef: "self",
				}},
			},
		})
		result, ok := e.Match(mustReq(t, "http://example.com/cgi?serviceType=apm"))
		if !ok {
			t.Fatal("script rule should match against path+query")
		}
		if result.RuleType != proxy.RuleTypeScript {
			t.Errorf("RuleType = %v, want script", result.RuleType)
		}
		if result.ScriptRef != "self" {
			t.Errorf("ScriptRef = %q, want self", result.ScriptRef)
		}
		if _, ok := e.Match(mustReq(t, "http://example.com/cgi?serviceType=other")); ok {
			t.Error("script rule should not match a different query value")
		}
	})

	t.Run("pipeline 规则携带 adapter", func(t *testing.T) {
		e := NewRuleEngine()
		e.Compile([]*RuleScript{
			{
				ID: "s", Enabled: true,
				Rules: []Rule{{
					Type:    RuleTypePipeline,
					Line:    1,
					Pattern: regexp.MustCompile(`^/pipe`),
					Adapter: "my-adapter",
				}},
			},
		})
		result, ok := e.Match(mustReq(t, "http://example.com/pipe"))
		if !ok {
			t.Fatal("pipeline rule should match")
		}
		if result.RuleType != proxy.RuleTypePipeline || result.Adapter != "my-adapter" {
			t.Errorf("got (%v, %q), want (pipeline, my-adapter)", result.RuleType, result.Adapter)
		}
	})
}

func TestEngineMatchRegexNilPattern(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "s", Enabled: true,
			Rules: []Rule{{Type: RuleTypeProxy, Line: 1, Pattern: nil, Target: "http://backend"}},
		},
	})

	if _, ok := e.Match(mustReq(t, "http://example.com/anything")); ok {
		t.Error("a rule with a nil pattern must never match")
	}
}

func TestEngineMatchExact(t *testing.T) {
	newEngine := func(rule Rule) *RuleEngine {
		e := NewRuleEngine()
		e.Compile([]*RuleScript{{ID: "s", Enabled: true, Rules: []Rule{rule}}})
		return e
	}

	t.Run("域名与路径都匹配", func(t *testing.T) {
		e := newEngine(Rule{
			Type: RuleTypeExact, Line: 2,
			Domain: "example.com", Path: "/exact", Target: "http://backend/exact",
		})
		result, ok := e.Match(mustReq(t, "http://example.com/exact"))
		if !ok {
			t.Fatal("exact rule should match")
		}
		if result.RuleType != proxy.RuleTypeExact {
			t.Errorf("RuleType = %v, want exact", result.RuleType)
		}
		if result.Target != "http://backend/exact" {
			t.Errorf("Target = %q, want http://backend/exact", result.Target)
		}
	})

	t.Run("域名不匹配则跳过", func(t *testing.T) {
		e := newEngine(Rule{
			Type: RuleTypeExact, Line: 2,
			Domain: "example.com", Path: "/exact", Target: "http://backend/exact",
		})
		if _, ok := e.Match(mustReq(t, "http://other.com/exact")); ok {
			t.Error("exact rule should not match a different host")
		}
	})

	t.Run("路径不匹配则跳过", func(t *testing.T) {
		e := newEngine(Rule{
			Type: RuleTypeExact, Line: 2,
			Domain: "example.com", Path: "/exact", Target: "http://backend/exact",
		})
		if _, ok := e.Match(mustReq(t, "http://example.com/different")); ok {
			t.Error("exact rule should not match a different path")
		}
	})

	t.Run("空域名表示不限域名", func(t *testing.T) {
		e := newEngine(Rule{Type: RuleTypeExact, Line: 2, Path: "/any", Target: "http://backend"})
		if _, ok := e.Match(mustReq(t, "http://whatever.com/any")); !ok {
			t.Error("an empty Domain should match any host")
		}
	})

	t.Run("空路径表示不限路径", func(t *testing.T) {
		e := newEngine(Rule{Type: RuleTypeExact, Line: 2, Domain: "example.com", Target: "http://backend"})
		if _, ok := e.Match(mustReq(t, "http://example.com/whatever")); !ok {
			t.Error("an empty Path should match any path")
		}
	})

	t.Run("exact 规则不做捕获组替换", func(t *testing.T) {
		e := newEngine(Rule{
			Type: RuleTypeExact, Line: 2,
			Domain: "example.com", Path: "/p", Target: "http://backend/$1",
		})
		result, ok := e.Match(mustReq(t, "http://example.com/p"))
		if !ok {
			t.Fatal("exact rule should match")
		}
		if result.Target != "http://backend/$1" {
			t.Errorf("Target = %q, want the template kept verbatim", result.Target)
		}
	})
}

func TestEngineMatchSkipsHostsRules(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "s", Enabled: true,
			Rules: []Rule{{Type: RuleTypeHosts, Line: 1, IP: "1.1.1.1", Domains: []string{"example.com"}}},
		},
	})

	// hosts 规则只服务于 ResolveUpstream，不参与请求匹配
	if _, ok := e.Match(mustReq(t, "http://example.com/anything")); ok {
		t.Error("hosts rules must not participate in Match")
	}
}

func TestEngineMatchRuleUnknownType(t *testing.T) {
	e := NewRuleEngine()
	script := &RuleScript{ID: "s", Enabled: true}

	// hosts 会被 Match 提前跳过，只能直接调 matchRule 覆盖 default 分支
	hosts := &Rule{Type: RuleTypeHosts, Line: 1, IP: "1.1.1.1", Domains: []string{"a.com"}}
	if got := e.matchRule(mustReq(t, "http://a.com/x"), script, hosts); got != nil {
		t.Errorf("matchRule on a hosts rule = %+v, want nil", got)
	}

	unknown := &Rule{Type: RuleType(99), Line: 1}
	if got := e.matchRule(mustReq(t, "http://a.com/x"), script, unknown); got != nil {
		t.Errorf("matchRule on an unknown rule type = %+v, want nil", got)
	}
}

func TestEngineMatchFirstHitWins(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "low", Priority: 1, Enabled: true,
			Rules: []Rule{{
				Type: RuleTypeProxy, Line: 1,
				Pattern: regexp.MustCompile(`^/api`), Target: "http://low",
			}},
		},
		{
			ID: "high", Priority: 100, Enabled: true,
			Rules: []Rule{{
				Type: RuleTypeProxy, Line: 1,
				Pattern: regexp.MustCompile(`^/api`), Target: "http://high",
			}},
		},
	})

	result, ok := e.Match(mustReq(t, "http://example.com/api/x"))
	if !ok {
		t.Fatal("Match should hit")
	}
	if result.Target != "http://high" {
		t.Errorf("Target = %q, want http://high (higher priority wins)", result.Target)
	}
}

func TestEngineMatchRuleOrderWithinScript(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "s", Enabled: true,
			Rules: []Rule{
				{Type: RuleTypeProxy, Line: 1, Pattern: regexp.MustCompile(`^/api`), Target: "http://first"},
				{Type: RuleTypeProxy, Line: 2, Pattern: regexp.MustCompile(`^/api/x`), Target: "http://second"},
			},
		},
	})

	result, _ := e.Match(mustReq(t, "http://example.com/api/x"))
	// 同脚本内按文件顺序，第一条命中即停止
	if result.Target != "http://first" {
		t.Errorf("Target = %q, want http://first (file order decides)", result.Target)
	}
	if result.RuleLine != 1 {
		t.Errorf("RuleLine = %d, want 1", result.RuleLine)
	}
}

func TestEngineMatchPropagatesFlags(t *testing.T) {
	e := NewRuleEngine()
	e.Compile([]*RuleScript{
		{
			ID: "s", Enabled: true,
			Rules: []Rule{{
				Type: RuleTypeProxy, Line: 1,
				Pattern: regexp.MustCompile(`^/sse`), Target: "http://backend",
				Stream: true, CORS: true,
			}},
		},
	})

	result, ok := e.Match(mustReq(t, "http://example.com/sse"))
	if !ok {
		t.Fatal("Match should hit")
	}
	if !result.Stream || !result.CORS {
		t.Errorf("Stream/CORS = %v/%v, want both true", result.Stream, result.CORS)
	}
}

func TestEngineMatchRecordsStats(t *testing.T) {
	script := &RuleScript{
		ID: "s", Enabled: true,
		Rules: []Rule{
			{Type: RuleTypeProxy, Line: 4, Pattern: regexp.MustCompile(`^/a`), Target: "http://a"},
			{Type: RuleTypeProxy, Line: 9, Pattern: regexp.MustCompile(`^/b`), Target: "http://b"},
		},
	}
	e := NewRuleEngine()
	e.Compile([]*RuleScript{script})

	e.Match(mustReq(t, "http://example.com/a"))
	e.Match(mustReq(t, "http://example.com/a"))
	e.Match(mustReq(t, "http://example.com/b"))
	e.Match(mustReq(t, "http://example.com/miss")) // 未命中不应计数

	if script.Stats.TotalHits != 3 {
		t.Errorf("TotalHits = %d, want 3", script.Stats.TotalHits)
	}
	if got := script.Stats.RuleHits[4]; got != 2 {
		t.Errorf("RuleHits[4] = %d, want 2", got)
	}
	if got := script.Stats.RuleHits[9]; got != 1 {
		t.Errorf("RuleHits[9] = %d, want 1", got)
	}
}

func TestEngineMatchEmptyEngine(t *testing.T) {
	e := NewRuleEngine()
	if _, ok := e.Match(mustReq(t, "http://example.com/api")); ok {
		t.Error("an engine with no compiled scripts should never match")
	}

	// 未编译时的只读接口也应安全返回零值
	if e.ShouldIntercept("example.com") {
		t.Error("ShouldIntercept should be false on an empty engine")
	}
	if _, ok := e.ResolveUpstream("example.com"); ok {
		t.Error("ResolveUpstream should miss on an empty engine")
	}
	if len(e.HostsSnapshot()) != 0 || len(e.HostsConflicts()) != 0 {
		t.Error("snapshots should be empty on an empty engine")
	}
}

// TestEngineConcurrentMatchAndCompile 在 -race 下验证热更新与匹配可以并发进行。
func TestEngineConcurrentMatchAndCompile(t *testing.T) {
	e := NewRuleEngine()
	build := func(target string) []*RuleScript {
		return []*RuleScript{{
			ID: "s", Enabled: true, Intercepts: []string{"example.com"},
			Rules: []Rule{
				{Type: RuleTypeProxy, Line: 1, Pattern: regexp.MustCompile(`^/api`), Target: target},
				{Type: RuleTypeHosts, Line: 2, IP: "1.1.1.1", Domains: []string{"example.com"}},
			},
		}}
	}
	e.Compile(build("http://initial"))

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			e.Compile(build("http://recompiled"))
		}
		close(stop)
	}()

	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := mustReq(t, "http://example.com/api/x")
			for {
				select {
				case <-stop:
					return
				default:
					e.Match(req)
					e.ShouldIntercept("example.com")
					e.ResolveUpstream("example.com")
					e.HostsSnapshot()
					e.HostsConflicts()
				}
			}
		}()
	}
	wg.Wait()
}
