package rule

import (
	"sync"
	"time"

	"github.com/dop251/goja"
)

// RuleScript 一个规则脚本（对应一个 .rules 文件）
type RuleScript struct {
	ID       string // 唯一标识（文件名去后缀）
	Name     string // @name 声明的名称
	Priority int    // @priority 声明的优先级（默认 0，高优先）
	Enabled  bool   // 是否启用
	FilePath string // 文件路径

	// 解析结果
	Intercepts []string // @intercept 声明的域名列表
	Rules      []Rule   // 解析后的规则列表（按文件顺序）

	// V2: 脚本私有配置（@config...@end 段，YAML 格式，框架不解析）
	Config map[string]any `json:"config,omitempty"`

	// V2: 内嵌 JS 脚本（name → JS 源码），如 onRequest/onResponse
	Scripts map[string]string `json:"scripts,omitempty"`

	// V2.1: 预编译的 JS 程序（规则加载时编译，运行时直接用）
	// key 同 Scripts（如 "onRequest"/"onResponse"），由 RuleManager 在加载时填充
	CompiledPrograms map[string]*goja.Program `json:"-"`

	// 运行时统计
	Stats       ScriptStats
	ParseErrors []ParseError // 解析错误列表（空表示正常，行级容错不阻止其他规则）
	UpdatedAt   time.Time    // 最后更新时间
}

// ScriptStats 脚本运行时统计
type ScriptStats struct {
	mu        sync.Mutex     // 保护 RuleHits map 的并发写入
	TotalHits int64          // 总命中次数（atomic）
	LastHitAt time.Time      // 最后命中时间
	RuleHits  map[int]int64  // 每条规则的命中次数（按行号索引）
}
