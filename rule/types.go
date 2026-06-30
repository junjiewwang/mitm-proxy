package rule

import (
	"fmt"
	"regexp"
	"time"
)

// RuleType 规则类型枚举
type RuleType int

const (
	RuleTypeHosts    RuleType = iota // IP domain — hosts 映射
	RuleTypeProxy                    // /regex/ http://target — 正则代理
	RuleTypePipeline                 // /regex/ pipeline://name — Pipeline 处理
	RuleTypeExact                    // domain/path target — 精确路径代理
)

// Rule 单条解析后的规则
type Rule struct {
	Type RuleType // 规则类型
	Line int      // 源文件行号（便于错误定位）
	Raw  string   // 原始文本

	// Hosts 类型
	IP      string   // 目标 IP
	Domains []string // 域名列表

	// Proxy/Pipeline/Exact 类型
	Pattern *regexp.Regexp // URL path 正则（Proxy/Pipeline）
	Domain  string         // 匹配域名（Exact）
	Path    string         // 匹配路径（Exact）
	Target  string         // 目标地址模板（支持 $1~$9 捕获组引用）
	Adapter string         // Pipeline 适配器名称（仅 RuleTypePipeline）

	// Options（@标记）
	Stream  bool          // @stream — SSE/chunked 流式代理
	CORS    bool          // @cors — 自动处理 OPTIONS 预检
	Timeout time.Duration // @timeout=Xs — 单条规则超时
}

// ParseError 解析错误（单行级别，不阻止其他行）
type ParseError struct {
	Line    int    // 行号
	Column  int    // 列号
	Message string // 错误信息
	Raw     string // 原始行文本
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Message)
}
