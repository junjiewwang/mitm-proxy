package proxy

import (
	"net/http"
	"time"
)

// InterceptMatcher 域名拦截判断器
// 判断一个 HTTPS CONNECT 请求的目标域名是否需要 MITM 解密
type InterceptMatcher interface {
	// ShouldIntercept 判断域名是否需要 MITM 拦截
	ShouldIntercept(hostname string) bool
}

// UpstreamResolver hosts 映射解析器
// 将域名解析为上游服务器地址
type UpstreamResolver interface {
	// ResolveUpstream 获取域名对应的上游地址
	// 返回 "ip:port" 格式，ok=false 表示无映射（使用默认 DNS）
	ResolveUpstream(hostname string) (addr string, ok bool)
}

// RequestMatcher 请求匹配器
// 判断一个 MITM 解密后的 HTTP 请求是否命中某条规则
type RequestMatcher interface {
	// Match 返回匹配结果，matched=false 表示无规则命中（透传到上游）
	Match(r *http.Request) (result *MatchResult, matched bool)
}

// RequestProcessor 被拦截请求的处理器
// 代理核心层只依赖此接口，不关心内部实现
type RequestProcessor interface {
	// Process 处理被拦截的请求，返回构建好的 HTTP 响应
	Process(req *http.Request, body []byte, match *MatchResult) *http.Response
}

// TraceRecorder 请求追踪记录器（可选注入，nil 表示不记录）
type TraceRecorder interface {
	RecordTunnel(targetHost string)
	RecordForward(method, path, host string, statusCode int, elapsed time.Duration, ok bool, errMsg string)
	RecordIntercept(scriptName, ruleName string, statusCode int, elapsed time.Duration)
}

// RuleType 规则类型枚举
type RuleType int

const (
	RuleTypeHosts    RuleType = iota // IP domain — hosts 映射
	RuleTypeProxy                    // /regex/ http://target — 正则代理
	RuleTypePipeline                 // /regex/ pipeline://name — Pipeline 处理（V1 兼容，V2 废弃）
	RuleTypeExact                    // domain/path target — 精确路径代理
	RuleTypeScript                   // /regex/ script://id — JS 脚本处理（V2）
)

// MatchResult 规则匹配结果
type MatchResult struct {
	ScriptID   string   // 所属脚本 ID
	ScriptName string   // 所属脚本名称
	RuleType   RuleType // 规则类型
	RuleLine   int      // 规则行号
	Captures   []string // 正则捕获组（$1~$9）
	Target     string   // 目标地址（已替换捕获组后的最终 URL）
	Adapter    string   // Pipeline 适配器名称（仅 RuleTypePipeline，V1 兼容）
	ScriptRef  string   // JS 脚本引用（仅 RuleTypeScript，如 "self" / "other-id"）
	Stream     bool     // 是否流式传输
	CORS       bool     // 是否处理 CORS
}

// TypeName 返回规则类型字符串（供事件广播等使用）
func (m *MatchResult) TypeName() string {
	switch m.RuleType {
	case RuleTypeHosts:
		return "hosts"
	case RuleTypeProxy:
		return "proxy"
	case RuleTypePipeline:
		return "pipeline"
	case RuleTypeExact:
		return "exact"
	case RuleTypeScript:
		return "script"
	default:
		return "proxy"
	}
}
