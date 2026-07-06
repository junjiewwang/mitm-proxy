package main

import (
	"net/http"
	"time"

	"mitm-proxy/core/proxy"
)

// ========================================
// 核心类型别名：让 cmd 包代码可直接使用 proxy 包类型
// ========================================

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
	RuleTypeScript   = proxy.RuleTypeScript
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

// TraceRecorder 请求追踪记录器（可选注入，实现 proxy.TraceRecorder）
type TraceRecorder interface {
	RecordTunnel(targetHost string)
	RecordForward(method, path, host string, statusCode int, elapsed time.Duration, ok bool, errMsg string)
	RecordIntercept(scriptName, ruleName string, statusCode int, elapsed time.Duration)
}

// 从 core/proxy 搬迁的类型别名
type (
	ForwardProxy = proxy.ForwardProxy
	ConnManager  = proxy.ConnManager
	CACert       = proxy.CACert
	ProxyConfig  = proxy.ProxyConfig
)

// 从 core/proxy 搬迁的函数代理
func NewForwardProxy(cfg *ProxyConfig, opts ...proxy.ProxyOption) (*ForwardProxy, error) {
	return proxy.NewForwardProxy(cfg, opts...)
}
func LoadOrGenerateCA(certDir string) (*CACert, error) { return proxy.LoadOrGenerateCA(certDir) }
func NewConnManager(maxConns int, idleTimeout time.Duration) *ConnManager {
	return proxy.NewConnManager(maxConns, idleTimeout)
}

func WithInterceptMatcher(m InterceptMatcher) proxy.ProxyOption  { return proxy.WithInterceptMatcher(m) }
func WithUpstreamResolver(r UpstreamResolver) proxy.ProxyOption  { return proxy.WithUpstreamResolver(r) }
func WithRequestMatcher(m RequestMatcher) proxy.ProxyOption      { return proxy.WithRequestMatcher(m) }
func WithRequestProcessor(p RequestProcessor) proxy.ProxyOption  { return proxy.WithRequestProcessor(p) }
func WithTraceRecorder(r TraceRecorder) proxy.ProxyOption        { return proxy.WithTraceRecorder(r) }
