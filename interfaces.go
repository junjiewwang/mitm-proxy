package main

import (
	"net/http"
	"time"

	"mitm-proxy/proxy"
)

// ========================================
// 通用代理架构接口定义
// 直接引用 proxy 包中的核心类型，避免重复定义
// ========================================

// 类型别名：让根包代码可以直接使用，无需到处写 proxy.XXX
type (
	MatchResult = proxy.MatchResult
	RuleType    = proxy.RuleType
)

// 常量别名
const (
	RuleTypeHosts    = proxy.RuleTypeHosts
	RuleTypeProxy    = proxy.RuleTypeProxy
	RuleTypePipeline = proxy.RuleTypePipeline
	RuleTypeExact    = proxy.RuleTypeExact
)

// InterceptMatcher 域名拦截判断器
type InterceptMatcher interface {
	ShouldIntercept(hostname string) bool
}

// UpstreamResolver hosts 映射解析器
type UpstreamResolver interface {
	ResolveUpstream(hostname string) (addr string, ok bool)
}

// RequestMatcher 请求匹配器
type RequestMatcher interface {
	Match(r *http.Request) (result *proxy.MatchResult, matched bool)
}

// RequestProcessor 被拦截请求的通用处理器
type RequestProcessor interface {
	Process(req *http.Request, body []byte, match *proxy.MatchResult) *http.Response
}

// TraceRecorder 请求追踪记录器（可选注入）
type TraceRecorder interface {
	RecordTunnel(targetHost string)
	RecordForward(method, path, host string, statusCode int, elapsed time.Duration, ok bool, errMsg string)
	RecordIntercept(scriptName, ruleName string, statusCode int, elapsed time.Duration)
}
