package rule

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"mitm-proxy/core/proxy"
)

// HostsConflict 记录一条 hosts 覆盖冲突信息（供诊断和 WebUI 使用）
type HostsConflict struct {
	Domain       string `json:"domain"`        // 冲突的域名
	WinnerIP     string `json:"winner_ip"`     // 最终生效的 IP:port
	WinnerScript string `json:"winner_script"` // 最终生效来源脚本 ID
	WinnerLine   int    `json:"winner_line"`   // 最终生效来源行号
	LoserIP      string `json:"loser_ip"`      // 被覆盖的 IP:port
	LoserScript  string `json:"loser_script"`  // 被覆盖来源脚本 ID
	LoserLine    int    `json:"loser_line"`    // 被覆盖来源行号
}

// RuleEngine 规则匹配引擎（线程安全，支持热更新）
// 实现 proxy.InterceptMatcher + proxy.UpstreamResolver + proxy.RequestMatcher
type RuleEngine struct {
	mu             sync.RWMutex
	scripts        []*RuleScript     // 按 priority 降序排列
	interceptSet   map[string]bool   // 合并后的 MITM 域名集
	hostsMap       map[string]string // 合并后的 hosts 映射（domain → ip:port）

	// 上一次编译时检测到的 hosts 冲突列表（供诊断 API 使用）
	hostsConflicts []HostsConflict
}

// NewRuleEngine 创建规则引擎
func NewRuleEngine() *RuleEngine {
	return &RuleEngine{
		interceptSet: make(map[string]bool),
		hostsMap:     make(map[string]string),
	}
}

// Compile 编译规则集（由 Manager 调用，或启动时调用）
// 合并所有启用脚本的 @intercept 域名和 hosts 映射
func (e *RuleEngine) Compile(scripts []*RuleScript) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// 按 priority 降序排序
	sorted := make([]*RuleScript, 0, len(scripts))
	for _, s := range scripts {
		if s.Enabled {
			sorted = append(sorted, s)
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Priority > sorted[j].Priority
	})

	e.scripts = sorted

	// 合并 intercept 域名（所有启用脚本取并集）
	interceptSet := make(map[string]bool)
	for _, s := range sorted {
		for _, domain := range s.Intercepts {
			interceptSet[domain] = true
		}
	}
	e.interceptSet = interceptSet

	// 合并 hosts 映射（高优先级覆盖低优先级）
	// 同时检测跨脚本（或同脚本内）的冲突
	hostsMap := make(map[string]string)
	// hostsSource 记录每个域名的来源信息：domain → (IP, scriptID, line)
	type hostsEntry struct {
		ip       string
		scriptID string
		line     int
	}
	hostsSource := make(map[string]hostsEntry)
	var conflicts []HostsConflict

	// 从低优先级到高优先级遍历，高优先级覆盖
	for i := len(sorted) - 1; i >= 0; i-- {
		for _, rule := range sorted[i].Rules {
			if rule.Type == RuleTypeHosts {
				addr := rule.IP
				if !strings.Contains(addr, ":") {
					addr += ":443"
				}
				for _, domain := range rule.Domains {
					// 仅当同一域名映射到不同 IP 时才记录冲突；相同 IP 静默覆盖
					if prev, exists := hostsSource[domain]; exists && prev.ip != addr {
						conflicts = append(conflicts, HostsConflict{
							Domain:       domain,
							WinnerIP:     addr,
							WinnerScript: sorted[i].ID,
							WinnerLine:   rule.Line,
							LoserIP:      prev.ip,
							LoserScript:  prev.scriptID,
							LoserLine:    prev.line,
						})
					}
					hostsMap[domain] = addr
					hostsSource[domain] = hostsEntry{ip: addr, scriptID: sorted[i].ID, line: rule.Line}
				}
			}
		}
	}
	e.hostsMap = hostsMap
	e.hostsConflicts = conflicts

	// 输出冲突日志（仅在存在冲突时打印）
	if len(conflicts) > 0 {
		log.Printf("[RuleEngine] ⚠ %d hosts conflict(s) detected:", len(conflicts))
		for _, c := range conflicts {
			if c.WinnerScript == c.LoserScript {
				// 同脚本内的冲突
				log.Printf("[RuleEngine]   %s: line %d (%s) overrides line %d (%s) [same script: %s]",
					c.Domain, c.WinnerLine, c.WinnerIP, c.LoserLine, c.LoserIP, c.WinnerScript)
			} else {
				// 跨脚本的冲突
				log.Printf("[RuleEngine]   %s: [%s:%d] %s → overrides → [%s:%d] %s",
					c.Domain, c.WinnerScript, c.WinnerLine, c.WinnerIP, c.LoserScript, c.LoserLine, c.LoserIP)
			}
		}
	}

	// 统计日志：方便验证规则更新是否生效
	totalRules := 0
	for _, s := range sorted {
		totalRules += len(s.Rules)
	}
	log.Printf("[RuleEngine] Recompiled: %d scripts (%d enabled), %d rules, %d intercepts, %d hosts",
		len(scripts), len(sorted), totalRules, len(interceptSet), len(hostsMap))
}

// ShouldIntercept 实现 proxy.InterceptMatcher 接口
func (e *RuleEngine) ShouldIntercept(hostname string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.interceptSet[hostname] {
		return true
	}
	// 支持通配符子域名匹配
	for domain := range e.interceptSet {
		if strings.HasPrefix(domain, "*.") {
			suffix := domain[1:] // ".example.com"
			if strings.HasSuffix(hostname, suffix) {
				return true
			}
		}
	}
	return false
}

// ResolveUpstream 实现 proxy.UpstreamResolver 接口
func (e *RuleEngine) ResolveUpstream(hostname string) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if addr, ok := e.hostsMap[hostname]; ok {
		return addr, true
	}
	return "", false
}

// HostsSnapshot 返回当前生效的 hosts 映射快照（domain → ip:port），供 WebUI 和调试使用
func (e *RuleEngine) HostsSnapshot() map[string]string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	snapshot := make(map[string]string, len(e.hostsMap))
	for k, v := range e.hostsMap {
		snapshot[k] = v
	}
	return snapshot
}

// HostsConflicts 返回上一次编译时检测到的 hosts 冲突列表，供诊断 API 和 WebUI 使用
func (e *RuleEngine) HostsConflicts() []HostsConflict {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]HostsConflict, len(e.hostsConflicts))
	copy(result, e.hostsConflicts)
	return result
}

// Match 实现 proxy.RequestMatcher 接口
// 按优先级遍历脚本 → 按顺序匹配规则 → 第一条命中即停止
func (e *RuleEngine) Match(r *http.Request) (*proxy.MatchResult, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, script := range e.scripts {
		for i := range script.Rules {
			rule := &script.Rules[i]

			// hosts 规则不参与 Match（由 ResolveUpstream 使用）
			if rule.Type == RuleTypeHosts {
				continue
			}

			if result := e.matchRule(r, script, rule); result != nil {
				// 更新统计
				atomic.AddInt64(&script.Stats.TotalHits, 1)
				if script.Stats.RuleHits == nil {
					script.Stats.RuleHits = make(map[int]int64)
				}
				script.Stats.RuleHits[rule.Line]++
				return result, true
			}
		}
	}

	return nil, false
}

// matchRule 尝试将请求与单条规则匹配
func (e *RuleEngine) matchRule(r *http.Request, script *RuleScript, rule *Rule) *proxy.MatchResult {
	switch rule.Type {
	case RuleTypeProxy, RuleTypePipeline, RuleTypeScript:
		return e.matchRegex(r, script, rule)
	case RuleTypeExact:
		return e.matchExact(r, script, rule)
	}
	return nil
}

// matchRegex 正则匹配
func (e *RuleEngine) matchRegex(r *http.Request, script *RuleScript, rule *Rule) *proxy.MatchResult {
	if rule.Pattern == nil {
		return nil
	}

	// 构建匹配字符串
	// proxy/exact 规则：只匹配 path（query string 会破坏 $ 锚点）
	// script/pipeline 规则：需要 query string 匹配参数（如 serviceType=apm）
	matchStr := r.URL.Path
	if (rule.Type == RuleTypeScript || rule.Type == RuleTypePipeline) && r.URL.RawQuery != "" {
		matchStr = r.URL.Path + "?" + r.URL.RawQuery
	}

	matches := rule.Pattern.FindStringSubmatch(matchStr)
	if matches == nil {
		return nil
	}

	result := &proxy.MatchResult{
		ScriptID:   script.ID,
		ScriptName: script.Name,
		RuleLine:   rule.Line,
		Captures:   matches,
		Stream:     rule.Stream,
		CORS:       rule.CORS,
	}

	switch rule.Type {
	case RuleTypeProxy:
		result.RuleType = proxy.RuleTypeProxy
		result.Target = replaceCaptures(rule.Target, matches)
	case RuleTypePipeline:
		result.RuleType = proxy.RuleTypePipeline
		result.Adapter = rule.Adapter
	case RuleTypeScript:
		result.RuleType = proxy.RuleTypeScript
		result.ScriptRef = rule.ScriptRef
	}

	return result
}

// matchExact 精确路径匹配
func (e *RuleEngine) matchExact(r *http.Request, script *RuleScript, rule *Rule) *proxy.MatchResult {
	// 域名匹配
	if rule.Domain != "" && r.Host != rule.Domain && r.URL.Host != rule.Domain {
		return nil
	}
	// 路径匹配
	if rule.Path != "" && r.URL.Path != rule.Path {
		return nil
	}

	return &proxy.MatchResult{
		ScriptID:   script.ID,
		ScriptName: script.Name,
		RuleType:   proxy.RuleTypeExact,
		RuleLine:   rule.Line,
		Target:     rule.Target,
		CORS:       rule.CORS,
	}
}

// replaceCaptures 将目标模板中的 $1~$9 替换为正则捕获组
func replaceCaptures(target string, captures []string) string {
	result := target
	for i := 1; i < len(captures) && i <= 9; i++ {
		placeholder := "$" + string(rune('0'+i))
		result = strings.ReplaceAll(result, placeholder, captures[i])
	}
	return result
}
