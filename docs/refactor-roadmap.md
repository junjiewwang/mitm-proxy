# yunapi-proxy 通用化重构方案 — Roadmap

> 创建时间：2026-06-17  
> 最后更新：2026-06-18  
> 状态：方案设计完成，待实施

---

## 一、重构目标

将 yunapi-proxy 从"腾讯云 APM 联调专用工具"重构为**通用的 MITM 正向代理框架**，
以**统一规则脚本引擎**为核心，支持多规则脚本隔离、动态启停、Web UI 管理，
打造类似 Whistle 的使用体验，同时保留 Pipeline 拆包/组包能力作为差异化优势。

### 设计原则

| 原则 | 具体要求 |
|------|---------|
| **高内聚低耦合** | 代理核心层不包含任何业务逻辑，通过接口与规则引擎/处理器交互 |
| **开闭原则 (OCP)** | 新增规则类型只需实现新 Processor，不改核心代码 |
| **依赖倒置 (DIP)** | 核心层定义接口，规则引擎/处理器层实现接口，通过依赖注入组装 |
| **单一职责 (SRP)** | Parser 只解析、Manager 只管理、Engine 只匹配、Processor 只执行 |
| **配置与逻辑分离** | 所有业务规则在 `rules/` 目录管理，`config.yaml` 只保留系统级配置 |
| **隔离性** | 多脚本独立作用域，启停互不影响；优先级决定冲突时的覆盖顺序 |
| **健壮性** | 单脚本解析失败不影响其他；热更新用写锁 + 原子替换，编译失败回滚 |

---

## 二、现状问题分析

### 2.1 耦合清单

| # | 耦合位置 | 硬编码内容 | 严重度 |
|---|---------|-----------|--------|
| 1 | `proxy.go` `shouldInterceptRequest` | `path == "/cgi/capi"` + `serviceType == "apm"` + `action == "delegate"` | 🔴 高 |
| 2 | `proxy.go` `processIntercept` | 直接调用 `handler.processRequest(body)` | 🔴 高 |
| 3 | `handler.go` 整个文件 | yunapi 请求格式 → TencentCloud API 3.0 格式的双向转换 | 🔴 高 |
| 4 | `handler.go` `wrapResponse` | yunapi 三层嵌套响应格式 `{code, data:{code, cgwerrorCode, data:{Response}}}` | 🔴 高 |
| 5 | `handler.go` `protectedFields` | `Uin`, `SubAccountUin`, `AppId`, `Action`, `ApiModule` | 🟡 中 |
| 6 | `handler.go` `injectAuth` | 腾讯云主/子账号认证模型 | 🟡 中 |
| 7 | `handler.go` `resolveRegion` | 默认 `ap-guangzhou`，腾讯云 Region 映射 | 🟡 中 |
| 8 | `main.go:195` | 路由 `POST "/cgi/capi"` | 🟡 中 |
| 9 | `debug.go` `recordTraceFWD/recordTraceIntercept` | 硬编码 `Host: console.cloud.tencent.com` | 🟢 低 |
| 10 | `mcp.go` `toolCurl` | `serviceType=apm`, `DescribeMetricRecords` 等 APM 接口名 | 🟢 低 |
| 11 | `main.go` `Config` 结构体 | `MockAuth`, `RegionMap` 与通用代理配置混在一起 | 🟢 低 |
| 12 | `main.go` `loadHostsFile` | hosts 解析逻辑与配置加载耦合 | 🟡 中 |
| 13 | `proxy.go` `shouldIntercept` | 域名列表硬编码在 config，无法动态管理 | 🟡 中 |

### 2.2 通用 vs 业务特定 代码占比

```
代理核心（通用）：~55%
  proxy.go (MITM/Tunnel/并发处理)、cert.go、connmgr.go

业务适配（特定）：~30%
  handler.go (全部)、proxy.go (拦截判断+处理)、main.go (路由+配置)

诊断运维：~15%
  debug.go、mcp.go
```

---

## 三、目标架构

### 3.1 架构总览

以**统一规则脚本引擎**为核心，所有代理行为（hosts 映射、MITM 拦截域名、API 拦截、静态资源代理）
全部通过规则脚本声明，规则引擎作为代理核心与处理器之间的调度枢纽。

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              Web UI (管理面板)                                │
│                                                                              │
│  规则脚本列表（启停/编辑/统计）+ 实时日志（SSE） + 系统状态                    │
└──────────────────────────┬───────────────────────────────────────────────────┘
                           │ REST API
┌──────────────────────────▼───────────────────────────────────────────────────┐
│                    Rule Manager (规则管理器)                                   │
│                                                                              │
│  规则脚本 CRUD · 启停控制 · 规则解析 · 热加载 · 规则校验 · 持久化             │
└──────────────────────────┬───────────────────────────────────────────────────┘
                           │ 编译后的规则集
┌──────────────────────────▼───────────────────────────────────────────────────┐
│                    Rule Engine (规则匹配引擎)                                  │
│                                                                              │
│  职责：                                                                       │
│    - ShouldIntercept(hostname) — MITM 域名判断（合并所有 @intercept）          │
│    - ResolveUpstream(hostname) — hosts 映射解析（高优先级覆盖低优先级）        │
│    - Match(req) → MatchResult  — 按优先级匹配规则（第一条命中即停止）          │
│                                                                              │
│  实现接口：InterceptMatcher + UpstreamResolver                                │
└──────────────────────────┬───────────────────────────────────────────────────┘
                           │ 分发到处理器
┌──────────────────────────▼───────────────────────────────────────────────────┐
│                       Processors (处理器集合)                                  │
│                                                                              │
│  ┌─────────────────┐  ┌──────────────────┐  ┌──────────────────────┐        │
│  │ ProxyProcessor  │  │ PipelineProcessor│  │ StreamProcessor      │        │
│  │                 │  │                  │  │                      │        │
│  │ 正则 → URL 重写 │  │ pipeline://name  │  │ @stream 标记的       │        │
│  │ 反向代理        │  │ 拆包/认证/转发/  │  │ SSE/chunked 流式代理 │        │
│  │                 │  │ 组包             │  │                      │        │
│  └─────────────────┘  └──────────────────┘  └──────────────────────┘        │
│                                                                              │
│  每种处理器实现 RequestProcessor 接口                                         │
│  新规则类型 = 新 Processor（开闭原则）                                        │
└──────────────────────────┬───────────────────────────────────────────────────┘
                           │
┌──────────────────────────▼───────────────────────────────────────────────────┐
│                    代理核心层 (proxy/)                                         │
│                                                                              │
│  ForwardProxy · MITM 并发处理 · TCP 隧道 · 连接池                            │
│  CA 证书管理 · LRU 连接管理 · 有序响应写回                                    │
│                                                                              │
│  核心接口依赖（通过依赖注入）：                                               │
│    InterceptMatcher  — 域名是否需要 MITM                                     │
│    UpstreamResolver  — hosts 映射解析                                        │
│    RuleEngine.Match  — 请求匹配 + 分发到 Processor                           │
│    TraceRecorder     — 请求追踪记录                                          │
└──────────────────────────┬───────────────────────────────────────────────────┘
                           │ 组合
┌──────────────────────────▼───────────────────────────────────────────────────┐
│                     Pipeline 处理引擎 (pipeline/)                              │
│                                                                              │
│  ┌─ RequestTransformer ─┐  ┌─ AuthInjector ─┐                               │
│  │  拆包：提取/重命名/   │  │  认证信息注入   │                               │
│  │  扁平化嵌套字段       │  │  （可选阶段）   │                               │
│  └───────────────────────┘  └────────────────┘                               │
│  ┌─ Forwarder ───────────┐  ┌─ ResponseTransformer ─┐                       │
│  │  转发到后端           │  │  组包：包装/重命名/     │                       │
│  │  （通用，各适配器复用）│  │  嵌套响应结构          │                       │
│  └───────────────────────┘  └────────────────────────┘                       │
│                                                                              │
│  Pipeline 编排器：按配置声明的阶段列表顺序执行                               │
│  每个阶段可独立替换、跳过、或叠加多个实例                                    │
└──────────────────────────┬───────────────────────────────────────────────────┘
                           │
┌──────────────────────────▼───────────────────────────────────────────────────┐
│                     适配器层 (adapter/)                                        │
│                                                                              │
│  adapter/yunapi/        腾讯云 yunapi 适配器（组装 Pipeline 阶段）              │
│  adapter/generic/       通用适配器（URL 改写 + Header 注入）                   │
│  adapter/template/      模板驱动适配器（配置化拆包组包）                       │
└──────────────────────────┬───────────────────────────────────────────────────┘
                           │
┌──────────────────────────▼───────────────────────────────────────────────────┐
│                       诊断层 (debug/)                                         │
│                                                                              │
│  TraceCollector（环形缓冲区）· MCP Server                                     │
│  实现 TraceRecorder 接口，不依赖具体业务域名                                  │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 核心接口定义

#### 3.2.1 代理核心层接口（proxy/interfaces.go）

```go
// InterceptMatcher 域名拦截判断器
// 判断一个 HTTPS CONNECT 请求的目标域名是否需要 MITM 解密
type InterceptMatcher interface {
    // ShouldIntercept 判断域名是否需要 MITM 拦截
    // 数据来源：所有启用脚本的 @intercept 指令合并
    ShouldIntercept(hostname string) bool
}

// UpstreamResolver hosts 映射解析器
// 将域名解析为上游服务器地址（类似 hosts 文件的功能）
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

// MatchResult 规则匹配结果
type MatchResult struct {
    ScriptID   string   // 所属脚本 ID
    ScriptName string   // 所属脚本名称
    RuleType   RuleType // 规则类型
    RuleLine   int      // 规则行号（便于调试）
    Captures   []string // 正则捕获组（$1~$9）
    Target     string   // 目标地址模板（已替换捕获组后的最终 URL）
    Adapter    string   // Pipeline 适配器名称（仅 RuleTypePipeline）
    Stream     bool     // 是否流式传输
    CORS       bool     // 是否处理 CORS
}
```

#### 3.2.2 Pipeline 阶段接口（pipeline/interfaces.go）

将"拆包 → 认证注入 → 转发 → 组包"拆分为独立的阶段接口：

```go
// PipelineContext 贯穿整个 Pipeline 的上下文，各阶段通过它传递数据
type PipelineContext struct {
    // 原始请求信息（只读）
    OriginalRequest *http.Request
    RawBody         []byte

    // Pipeline 各阶段共享的结构化数据（可读写）
    ParsedRequest  map[string]any // 解析后的请求体（JSON 解析结果）
    BackendRequest map[string]any // 转换后的后端请求体（拆包结果）
    BackendURL     string         // 转发目标 URL
    BackendResp    []byte         // 后端原始响应
    FinalResp      []byte         // 最终返回给客户端的响应

    // 元数据（跨阶段传递附加信息）
    Metadata map[string]any
}

// RequestTransformer 请求转换器（拆包阶段）
type RequestTransformer interface {
    TransformRequest(ctx *PipelineContext) error
}

// AuthInjector 认证信息注入器（可选阶段）
type AuthInjector interface {
    InjectAuth(ctx *PipelineContext) error
}

// Forwarder 后端转发器
type Forwarder interface {
    Forward(ctx *PipelineContext) error
}

// ResponseTransformer 响应转换器（组包阶段）
type ResponseTransformer interface {
    TransformResponse(ctx *PipelineContext) error
}
```

#### 3.2.3 Pipeline 编排器（pipeline/pipeline.go）

```go
// Pipeline 请求处理管线
// 实现 RequestProcessor 接口（通过 PipelineProcessor 包装）
type Pipeline struct {
    Forwarder            Forwarder
    RequestTransformers  []RequestTransformer
    AuthInjectors        []AuthInjector
    ResponseTransformers []ResponseTransformer
}

// Execute 执行完整 Pipeline
func (p *Pipeline) Execute(req *http.Request, body []byte) *http.Response {
    ctx := &PipelineContext{
        OriginalRequest: req,
        RawBody:         body,
        Metadata:        make(map[string]any),
    }

    // 1. 解析请求体 → ctx.ParsedRequest
    // 2. 拆包（RequestTransformers 链式执行）→ ctx.BackendRequest
    // 3. 认证注入（AuthInjectors 链式执行）→ ctx.BackendRequest 含认证字段
    // 4. 转发（Forwarder）→ ctx.BackendResp
    // 5. 组包（ResponseTransformers 链式执行）→ ctx.FinalResp
    // ... 实现细节见 Sprint 1
}
```

#### 3.2.4 适配器接口（adapter/interfaces.go）

```go
// Adapter 适配器接口（组装 Pipeline 阶段）
type Adapter interface {
    // Name 适配器名称（被 pipeline:// 规则引用）
    Name() string
    // Init 初始化适配器（传入配置）
    Init(rawConfig map[string]any) error
    // GetPipeline 返回组装好的 Pipeline
    GetPipeline() *Pipeline
    // Close 清理资源
    Close() error
}
```

### 3.3 统一规则语法设计

采用 Whistle 兼容的文本规则语法，扩展 `pipeline://` 和 `@` 指令：

```
┌─────────────────────────────────────────────────────────────────────┐
│                     规则脚本语法                                       │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  1. Hosts 映射（兼容 Whistle）                                        │
│     IP domain1 domain2 ...                                           │
│     9.223.72.139 console.cloud.tencent.com                           │
│                                                                      │
│  2. 精确路径代理                                                      │
│     domain/path  target                                              │
│     console.cloud.tencent.com/cgi/capi  http://127.0.0.1:8082       │
│                                                                      │
│  3. 正则匹配代理（Whistle 风格）                                      │
│     /regex/  target  [@options]                                       │
│     /\/qcloud\/tea\/app\/(tapm[\w-]*)\.js$/  http://127.0.0.1:8322/$1.js │
│                                                                      │
│  4. Pipeline 代理（扩展语法）                                          │
│     /regex/  pipeline://adapter_name  [@options]                      │
│     /\/cgi\/capi\?.*serviceType=apm/  pipeline://yunapi               │
│                                                                      │
│  5. MITM 拦截域名声明                                                 │
│     @intercept domain1 domain2 ...                                    │
│     @intercept cloudcache.tencent-cloud.com                           │
│                                                                      │
│  6. 元指令                                                            │
│     @name "脚本名称"                                                  │
│     @priority 100        # 优先级（越高越先匹配）                     │
│     @timeout 30s         # 全局超时                                   │
│                                                                      │
│  7. Options（附加在规则行尾）                                          │
│     @stream   — SSE/chunked 流式代理                                  │
│     @cors     — 自动处理 OPTIONS 预检                                 │
│     @timeout=Xs — 单条规则超时覆盖                                    │
│                                                                      │
│  8. 注释                                                              │
│     # 这是一行注释                                                    │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘
```

### 3.4 规则脚本示例

#### 脚本 1：`rules/apm-dev.rules`（APM 本地开发全套）

```bash
# =============================================
# APM 本地联调规则
# 包含：hosts映射 + API拦截 + 前端资源代理
# =============================================
@name "APM 本地开发"
@priority 100

# --- MITM 拦截域名声明 ---
@intercept console.cloud.tencent.com
@intercept console-hc.cloud.tencent.com
@intercept cloudcache.tencent-cloud.com

# --- Hosts 映射 ---
9.223.72.139 console.cloud.tencent.com console-hc.cloud.tencent.com
139.199.214.200 cloudcache.tencent-cloud.com

# --- API 拦截（走 Pipeline 拆包/组包）---
/\/cgi\/capi\?.*action=delegate&.*serviceType=apm/  pipeline://yunapi
/\/_api\/apm\//  pipeline://yunapi  @cors

# --- 前端资源代理（CDN → 本地 webpack-dev-server）---
/\/qcloud\/tea\/app\/(tapm[\w-]*)(\.(zh|en|jp|ko|dev))?(\.\w{10})?\.(js|css)(\.map)?$/  http://127.0.0.1:8322/$1.$5$6
/\/qcloud\/tea\/app\/(.+\/)?__webpack_hmr$/  http://127.0.0.1:8322/__webpack_hmr  @stream
/\/qcloud\/tea\/app\/(.+\.)?(\w+)\.(hot-update\.(js|json))$/  http://127.0.0.1:8322/$1$2.$3
```

#### 脚本 2：`rules/cls-dev.rules`（CLS 产品联调，独立隔离）

```bash
@name "CLS 本地开发"
@priority 90

@intercept cloudcache.tencent-cloud.com

# CLS 前端资源代理
/\/qcloud\/tea\/app\/(cls[\w-]*)(\.\w{10})?\.(js|css)(\.map)?$/  http://127.0.0.1:9000/$1.$3$4
/\/qcloud\/tea\/app\/(.+\/)?__webpack_hmr$/  http://127.0.0.1:9000/__webpack_hmr  @stream
```

#### 脚本 3：`rules/test-env.rules`（测试环境 hosts）

```bash
@name "测试环境 Hosts"
@priority 50

# 指向测试环境
9.223.72.139 eb.qcloud.com
9.223.72.139 buy.cloud.tencent.com
9.223.72.139 api.cloud.tencent.com
11.141.253.239 ap-guangzhou.cls.tencentcs.com
```

### 3.5 规则引擎核心数据结构

#### 3.5.1 规则类型与规则对象（rule/types.go）

```go
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
    Type     RuleType        // 规则类型
    Line     int             // 源文件行号（便于错误定位）
    Raw      string          // 原始文本

    // Hosts 类型
    IP       string          // 目标 IP
    Domains  []string        // 域名列表

    // Proxy/Pipeline/Exact 类型
    Pattern  *regexp.Regexp  // URL path 正则（Proxy/Pipeline）
    Domain   string          // 匹配域名（Exact）
    Path     string          // 匹配路径（Exact）
    Target   string          // 目标地址模板（支持 $1~$9 捕获组引用）
    Adapter  string          // Pipeline 适配器名称（仅 RuleTypePipeline）

    // Options（@标记）
    Stream   bool            // @stream — SSE/chunked 流式代理
    CORS     bool            // @cors — 自动处理 OPTIONS 预检
    Timeout  time.Duration   // @timeout=Xs — 单条规则超时
}
```

#### 3.5.2 规则脚本（rule/script.go）

```go
// RuleScript 一个规则脚本（对应一个 .rules 文件）
type RuleScript struct {
    ID        string        // 唯一标识（文件名去后缀）
    Name      string        // @name 声明的名称
    Priority  int           // @priority 声明的优先级（默认 0，高优先）
    Enabled   bool          // 是否启用
    FilePath  string        // 文件路径

    // 解析结果
    Intercepts []string     // @intercept 声明的域名列表
    Rules      []Rule       // 解析后的规则列表（按文件顺序）

    // 运行时统计
    Stats      ScriptStats
    ParseError error        // 解析错误（nil 表示正常）
    UpdatedAt  time.Time    // 最后更新时间
}

// ScriptStats 脚本运行时统计
type ScriptStats struct {
    TotalHits   int64          // 总命中次数
    LastHitAt   time.Time      // 最后命中时间
    RuleHits    map[int]int64  // 每条规则的命中次数（按行号索引）
}
```

#### 3.5.3 规则管理器（rule/manager.go）

```go
// RuleManager 规则管理器
// 职责：脚本 CRUD + 启停 + 热加载 + 持久化
type RuleManager struct {
    mu       sync.RWMutex
    scripts  map[string]*RuleScript  // ID → Script
    sorted   []*RuleScript           // 按 priority 降序排列
    dir      string                  // 规则文件目录（rules/）
    engine   *RuleEngine             // 变更时通知引擎重编译
    watcher  *fsnotify.Watcher       // 文件监听（热加载）
}

// 公开 API（供 Web UI REST API 调用）
func (m *RuleManager) List() []*RuleScript
func (m *RuleManager) Get(id string) (*RuleScript, error)
func (m *RuleManager) Create(name, content string) (*RuleScript, error)
func (m *RuleManager) Update(id, content string) error
func (m *RuleManager) Delete(id string) error
func (m *RuleManager) Enable(id string) error
func (m *RuleManager) Disable(id string) error
func (m *RuleManager) Reload(id string) error
func (m *RuleManager) ReloadAll() error
func (m *RuleManager) Validate(content string) []ParseError
```

#### 3.5.4 规则匹配引擎（rule/engine.go）

```go
// RuleEngine 规则匹配引擎（线程安全，支持热更新）
type RuleEngine struct {
    mu              sync.RWMutex
    compiledScripts []*CompiledScript  // 编译后的规则（按优先级排序）
    interceptSet    map[string]bool    // 合并后的 MITM 域名集
    hostsMap        map[string]string  // 合并后的 hosts 映射（高优先级覆盖）
}

// ShouldIntercept 实现 InterceptMatcher 接口
// 合并所有启用脚本的 @intercept 域名取并集
func (e *RuleEngine) ShouldIntercept(hostname string) bool

// ResolveUpstream 实现 UpstreamResolver 接口
// 高优先级脚本的 hosts 映射覆盖低优先级
func (e *RuleEngine) ResolveUpstream(hostname string) (string, bool)

// Match 实现 RequestMatcher 接口
// 按优先级遍历脚本 → 按顺序匹配规则 → 第一条命中即停止
func (e *RuleEngine) Match(r *http.Request) (*MatchResult, bool)

// Recompile 热更新：Manager 调用，重新编译规则集
// 使用写锁 + 原子替换，编译失败回滚
func (e *RuleEngine) Recompile(scripts []*RuleScript)
```

#### 3.5.5 规则解析器（rule/parser.go）

```go
// Parser 规则文件解析器（Whistle 兼容扩展语法）
type Parser struct{}

// ParseFile 解析规则文件
func (p *Parser) ParseFile(path string) (*RuleScript, error)

// ParseContent 解析规则内容字符串（供 Web UI 实时校验）
func (p *Parser) ParseContent(content string) (*RuleScript, []ParseError)

// ParseError 解析错误（单行级别，不阻止其他行）
type ParseError struct {
    Line    int
    Column  int
    Message string
    Raw     string
}

// 解析规则：
//   行首 "#"        → 注释，跳过
//   行首 "@"        → 指令（@name / @priority / @intercept / @timeout）
//   行首 "/"        → 正则规则（/regex/ target [@options]）
//   行首 IP格式     → Hosts 映射（IP domain1 domain2 ...）
//   行首 domain/    → 精确路径代理（domain/path target）
//   空行            → 跳过
```

### 3.6 处理器设计

#### 3.6.1 处理器类型

| 处理器 | 规则类型 | 职责 | 适用场景 |
|--------|---------|------|---------|
| `ProxyProcessor` | RuleTypeProxy / RuleTypeExact | 正则/精确 URL 匹配 → 捕获组替换 → 反向代理 | 静态资源代理、URL 重写 |
| `PipelineProcessor` | RuleTypePipeline | 调用命名适配器的 Pipeline 执行拆包/认证/转发/组包 | API 拦截转换 |
| `StreamProcessor` | RuleTypeProxy（@stream） | SSE/chunked 流式反向代理（不读完 body） | Webpack HMR |

#### 3.6.2 ProxyProcessor（正则代理 + 反向代理）

```go
// ProxyProcessor 正则匹配 → URL 重写 → 反向代理
type ProxyProcessor struct {
    httpClient *http.Client
}

func (p *ProxyProcessor) Process(req *http.Request, body []byte, match *MatchResult) *http.Response {
    // match.Target 已是完成捕获组替换后的目标 URL
    // 1. 创建代理请求
    // 2. 复制 Header（排除 hop-by-hop）
    // 3. 转发并返回响应
}
```

#### 3.6.3 StreamProcessor（SSE 流式代理）

```go
// StreamProcessor 流式反向代理（SSE/chunked 长连接）
type StreamProcessor struct {
    httpClient *http.Client
}

func (s *StreamProcessor) Process(req *http.Request, body []byte, match *MatchResult) *http.Response {
    // 1. 向目标地址发起请求
    // 2. 不做 io.ReadAll — Body 保持为 io.ReadCloser 流
    // 3. 返回 ContentLength: -1（chunked）
    // writer goroutine 会流式写出 Body 到 TLS 连接
    return &http.Response{
        StatusCode:    resp.StatusCode,
        Header:        filterHopByHop(resp.Header),
        Body:          resp.Body,      // 流式传递
        ContentLength: -1,             // chunked
    }
}
```

#### 3.6.4 PipelineProcessor（Pipeline 调度）

```go
// PipelineProcessor 调用命名适配器的 Pipeline
type PipelineProcessor struct {
    adapters map[string]Adapter  // 适配器注册表
}

func (p *PipelineProcessor) Process(req *http.Request, body []byte, match *MatchResult) *http.Response {
    adapter, ok := p.adapters[match.Adapter]
    if !ok {
        return buildErrorResponse(500, "adapter not found: "+match.Adapter)
    }
    return adapter.GetPipeline().Execute(req, body)
}
```

### 3.7 与代理核心层的集成

```go
// proxy/proxy.go（重构后）

type ForwardProxy struct {
    // 通用配置
    certDir    string
    connMgr    *ConnManager
    httpClient *http.Client

    // 通过依赖注入的接口（由 RuleEngine 实现）
    interceptMatcher InterceptMatcher    // 域名是否 MITM
    upstreamResolver UpstreamResolver    // hosts 映射
    requestMatcher   RequestMatcher      // 请求规则匹配

    // 处理器集合
    processors map[RuleType]RequestProcessor

    // 可选
    traceRecorder TraceRecorder
}

// shouldIntercept → 委托给 RuleEngine
func (p *ForwardProxy) shouldIntercept(hostname string) bool {
    return p.interceptMatcher.ShouldIntercept(hostname)
}

// getUpstreamAddr → 委托给 RuleEngine
func (p *ForwardProxy) getUpstreamAddr(hostname string) string {
    if addr, ok := p.upstreamResolver.ResolveUpstream(hostname); ok {
        return addr
    }
    return hostname + ":443"
}

// MITM worker → 委托给 RuleEngine + Processors
// go func(seq, req) {
//     result, matched := p.requestMatcher.Match(req)
//     if matched {
//         processor := p.processors[result.RuleType]
//         resp = processor.Process(req, body, result)
//     } else {
//         resp = p.processForward(req, hostname)
//     }
// }
```

### 3.8 流式传输（HMR/SSE）设计要点

`__webpack_hmr` 是 Server-Sent Events（SSE）长连接，需要特殊处理：

1. **不读完 Body**：响应 Body 保持为 `io.ReadCloser` 流，`http.Response.Write` 会自动 chunked 写出
2. **独立连接**：浏览器为 SSE 开独立 TCP 连接，不影响同域其他请求的并发
3. **Writer 阻塞**：流式响应占用 MITM session 直到连接关闭；实际场景中，浏览器为 HMR 开独立连接，该 session 只服务于这一个流
4. **超时排除**：`@stream` 规则不受全局 timeout 限制

### 3.9 目标配置文件格式

规则全部迁移到 `rules/` 目录后，`config.yaml` 只保留系统级配置：

```yaml
# 代理核心配置（与业务规则完全分离）
server:
  listen: ":8082"         # 管理服务 + Web UI + REST API

proxy:
  listen: ":8081"         # 代理端口
  cert_dir: "certs"       # CA 证书目录
  max_connections: 100
  idle_timeout: 10s

# 规则引擎配置
rules:
  dir: "rules"            # 规则脚本目录（每个 .rules 文件一个脚本）
  hot_reload: true        # 是否监听文件变化自动重载
  watch_interval: 2s      # 文件监听间隔

# Pipeline 适配器配置（被 pipeline:// 规则引用）
adapters:
  yunapi:
    type: "yunapi"
    backend:
      url: "http://127.0.0.1:16686"
      api_path: "/api/v1"
      timeout: 30s
    auth:
      uin: "1500000686"
      sub_account_uin: "700001199694"
      app_id: 251005942
    region_map:
      1: "ap-guangzhou"
      4: "ap-shanghai"
      8: "ap-beijing"

# Web UI 配置
webui:
  enable: true
  # 通过 server.listen 同端口访问（路径前缀 /ui/）

# 诊断配置
debug:
  enable: true
  buffer_size: 500
  mcp_enable: true
```

### 3.10 目标目录结构

```
yunapi-proxy/
├── main.go                     # 启动入口：加载配置 → 创建 Manager/Engine → 注入到 Proxy → 启动
├── config.yaml                 # 精简的系统级配置
│
├── rules/                      # 规则脚本目录（每个文件一个独立脚本）
│   ├── apm-dev.rules          #   APM 本地开发规则
│   ├── cls-dev.rules          #   CLS 本地开发规则（示例）
│   └── test-env.rules         #   测试环境 hosts
│
├── rule/                       # 【规则引擎包】高内聚
│   ├── types.go               #   Rule / RuleType / MatchResult 类型定义
│   ├── script.go              #   RuleScript + ScriptStats 结构体
│   ├── parser.go              #   规则文件解析器（Whistle 兼容扩展语法）
│   ├── validator.go           #   规则校验（语法 + 正则合法性 + 适配器引用检查）
│   ├── manager.go             #   RuleManager（CRUD + 启停 + 热加载 + 文件监听）
│   └── engine.go              #   RuleEngine（匹配 + 域名/hosts 合并 + 读写锁）
│
├── processor/                  # 【处理器包】根据规则类型分发执行
│   ├── interfaces.go          #   RequestProcessor 接口定义
│   ├── proxy.go               #   ProxyProcessor（正则 → URL 重写 → 反向代理）
│   ├── pipeline.go            #   PipelineProcessor（调用适配器 Pipeline）
│   ├── stream.go              #   StreamProcessor（SSE/chunked 流式代理）
│   └── registry.go            #   处理器注册表（RuleType → Processor 映射）
│
├── proxy/                      # 【代理核心包】通用 MITM 正向代理
│   ├── interfaces.go          #   InterceptMatcher / UpstreamResolver / RequestMatcher / TraceRecorder
│   ├── proxy.go               #   ForwardProxy 主体（ServeHTTP / handleConnect / handleHTTP）
│   ├── mitm.go                #   MITM 并发处理 + 有序响应写回
│   ├── tunnel.go              #   TCP 隧道透传
│   ├── cert.go                #   CA 证书管理
│   ├── connmgr.go             #   LRU 连接管理器
│   ├── helpers.go             #   通用工具函数（isHopByHop / splitHostPort / CORS）
│   └── config.go              #   ProxyConfig 结构体
│
├── pipeline/                   # 【Pipeline 处理引擎】
│   ├── interfaces.go          #   阶段接口（RequestTransformer / AuthInjector / Forwarder / ResponseTransformer）
│   ├── context.go             #   PipelineContext 上下文
│   ├── pipeline.go            #   Pipeline 编排器
│   └── forwarder.go           #   HTTPForwarder 默认转发器
│
├── adapter/                    # 【适配器层】可插拔的业务实现
│   ├── interfaces.go          #   Adapter 接口定义
│   ├── registry.go            #   适配器注册表
│   ├── yunapi/                #   腾讯云 yunapi 适配器
│   │   ├── adapter.go         #     组装 Pipeline 阶段
│   │   ├── transformer.go     #     RequestTransformer + ResponseTransformer
│   │   ├── auth.go            #     AuthInjector（Uin/SubAccountUin/AppId）
│   │   └── config.go          #     YunAPIConfig 结构体
│   ├── generic/               #   通用转发适配器
│   │   └── adapter.go         #     URL 改写 + Header 注入
│   └── template/              #   模板驱动适配器
│       ├── adapter.go         #     配置化组装 Pipeline
│       ├── field_mapper.go    #     配置驱动的字段映射
│       └── response_wrapper.go #    配置驱动的响应包装
│
├── webui/                      # 【Web UI 包】管理面板
│   ├── handler.go             #   REST API handler
│   ├── events.go              #   SSE 实时日志推送
│   └── static/                #   前端静态资源（embed 嵌入）
│       ├── index.html
│       ├── app.js
│       └── style.css
│
├── debug/                      # 【诊断包】
│   ├── collector.go           #   TraceCollector（实现 TraceRecorder）
│   └── mcp.go                 #   MCP Server
│
└── docs/
    ├── yunapi-proxy.md         # 项目说明文档
    └── refactor-roadmap.md    # 本文档
```

### 3.11 Web UI REST API

```
# 规则脚本 CRUD
GET    /api/rules                → 列表（含启用状态、统计信息）
POST   /api/rules                → 创建（body: {name, content}）
GET    /api/rules/:id            → 详情（含解析后的 Rules + 统计）
PUT    /api/rules/:id            → 更新内容（body: {content}）
DELETE /api/rules/:id            → 删除

# 启停控制
POST   /api/rules/:id/enable     → 启用
POST   /api/rules/:id/disable    → 停用
POST   /api/rules/:id/reload     → 重载

# 批量操作
POST   /api/rules/reorder        → 调整优先级（body: {ids: [...]})
POST   /api/rules/reload-all     → 全部重载

# 校验（不保存，用于编辑器实时校验）
POST   /api/rules/validate       → 校验规则内容
                                    body: {content: "..."}
                                    resp: {valid, errors: [{line, message}]}

# 运行时状态
GET    /api/status               → MITM 域名集、hosts 映射数、活跃连接

# 实时日志（SSE 推送）
GET    /api/events               → Server-Sent Events 实时匹配日志
```

### 3.12 与 Whistle 的功能对比

| 功能 | Whistle | yunapi-proxy（本方案） |
|------|---------|----------------------|
| Hosts 映射 | ✅ | ✅ 完全兼容语法 |
| 正则 URL 代理 | ✅ | ✅ 兼容 + 扩展（`@stream` `@cors`） |
| 多规则文件 | ✅ Rules 面板 | ✅ 多脚本隔离 + 优先级 |
| 启停控制 | ✅ 勾选框 | ✅ Web UI + REST API |
| 实时日志 | ✅ Network 面板 | ✅ SSE 推送 + 过滤 |
| 规则编辑器 | ✅ 内置 | ✅ 语法高亮 + 实时校验 |
| Pipeline 拆包/组包 | ❌ | ✅ `pipeline://` 扩展语法 |
| MITM 域名管理 | 手动配置 | ✅ `@intercept` 自动合并 |
| 优先级控制 | 文件顺序 | ✅ `@priority` 显式声明 |
| 热加载 | ✅ 保存即生效 | ✅ 文件监听 + API 触发 |
| 匹配统计 | ❌ | ✅ 每条规则命中计数 |
| 规则校验 | 基础 | ✅ 语法 + 正则 + 适配器引用检查 |

---

## 四、核心交互流程

### 4.1 代理层请求分发（重构后）

```
请求到达 proxy:8081
    │
    ▼
ForwardProxy.ServeHTTP()
    │
    ├─ CONNECT → handleConnect()
    │       │
    │       ├─ engine.ShouldIntercept(hostname)?  ← InterceptMatcher 接口
    │       │       │                               （合并所有启用脚本的 @intercept）
    │       │  YES  ▼
    │       │  handleMITM()
    │       │       │
    │       │  for { http.ReadRequest(reader) }
    │       │       │
    │       │  go func(seq, req) {
    │       │       │
    │       │       ├─ result, matched := engine.Match(req)  ← RequestMatcher 接口
    │       │       │
    │       │       ├─ matched?
    │       │       │       │
    │       │       │  YES  ▼
    │       │       │  processor := processors[result.RuleType]
    │       │       │  resp := processor.Process(req, body, result)  ← RequestProcessor
    │       │       │  traceRecorder.RecordIntercept(...)
    │       │       │
    │       │       │  NO   ▼
    │       │       │  resp := processForward(req, hostname)  ← 通用透传
    │       │       │  traceRecorder.RecordForward(...)
    │       │       │
    │       │       └─ respChan <- pendingResponse{seq, resp}
    │       │  }
    │       │
    │       │  writer goroutine: 按 seq 顺序写回 tlsConn
    │       │
    │       │  NO
    │       │  ▼
    │       └─ handleTunnel() — TCP 直接透传
    │               │
    │               └─ 上游地址 = engine.ResolveUpstream(hostname)  ← UpstreamResolver
    │
    └─ 非 CONNECT → handleHTTP() — HTTP 代理转发
```

### 4.2 规则匹配流程

```
engine.Match(req) — 请求: GET https://cloudcache.tencent-cloud.com/qcloud/tea/app/tapmOverview.zh.a1b2c3.js
    │
    ▼
for script in sorted_by_priority(enabled_scripts):   // 按 @priority 降序
    │
    ├─ [P:100] "APM 本地开发"
    │   for rule in script.rules:
    │       │
    │       ├─ hosts 规则 → 跳过（hosts 不参与 Match，由 ResolveUpstream 使用）
    │       ├─ pipeline://yunapi 规则 → 正则不匹配
    │       ├─ /tapm[\w-]*...(js|css)/ → ✅ MATCH!
    │       │     captures: [$1=tapmOverview, $2=.zh, $5=js, $6=""]
    │       │     target:   "http://127.0.0.1:8322/$1.$5$6"
    │       │     → 替换: "http://127.0.0.1:8322/tapmOverview.js"
    │       │
    │       └─ 返回 MatchResult{
    │               RuleType: RuleTypeProxy,
    │               Target:   "http://127.0.0.1:8322/tapmOverview.js",
    │               Stream:   false,
    │          }
    │
    ▼
分发到 ProxyProcessor → 反向代理到 http://127.0.0.1:8322/tapmOverview.js
    │
    ▼
返回本地 webpack-dev-server 构建的 JS 文件 ✅
```

### 4.3 Pipeline 内部执行流程

```
PipelineProcessor.Process(req, body, match)
    │
    ├─ 获取适配器: adapters[match.Adapter]  // "yunapi"
    │
    ▼
Pipeline.Execute(req, body)
    │
    ├─ 1. 解析请求体 → ctx.ParsedRequest
    │
    ├─ 2. 拆包（RequestTransformers 链式执行）
    │       ├─ yunapi transformer: cmd→Action, serviceType→ApiModule, data.*→扁平化
    │       └─ region transformer: regionId(数字)→Region(字符串)
    │       → ctx.BackendRequest
    │
    ├─ 3. 认证注入（AuthInjectors 链式执行）
    │       └─ yunapi auth: ownerUin→Uin, uin→SubAccountUin, 注入 AppId
    │       → ctx.BackendRequest（含认证字段）
    │
    ├─ 4. 转发（Forwarder）
    │       └─ HTTPForwarder: POST ctx.BackendRequest → backend_url
    │       → ctx.BackendResp
    │
    └─ 5. 组包（ResponseTransformers 链式执行）
            └─ yunapi response: 包装为 {code:0, data:{code, data:{Response}}}
            → ctx.FinalResp → 构建 HTTP Response 返回
```

---

## 五、实施 Roadmap

### Sprint 1：代理核心层抽取 + Pipeline 引擎 + 基础规则解析

> **目标**：将代理核心与业务逻辑解耦，引入 Pipeline 处理引擎，创建规则引擎基础结构。现有功能完全不变，纯内部重构。

#### 任务列表

| # | 任务 | 说明 |
|---|------|------|
| 1.1 | 创建 `proxy/interfaces.go` | 定义 `InterceptMatcher`、`UpstreamResolver`、`RequestMatcher`、`RequestProcessor`、`TraceRecorder` 接口 |
| 1.2 | 创建 `pipeline/interfaces.go` | 定义 `RequestTransformer`、`AuthInjector`、`Forwarder`、`ResponseTransformer` |
| 1.3 | 创建 `pipeline/context.go` | `PipelineContext` 上下文结构体 |
| 1.4 | 创建 `pipeline/pipeline.go` | Pipeline 编排器（实现按阶段链式执行） |
| 1.5 | 创建 `pipeline/forwarder.go` | `HTTPForwarder` 默认转发器 |
| 1.6 | 创建 `rule/types.go` | `Rule`、`RuleType`、`MatchResult` 类型定义 |
| 1.7 | 创建 `rule/script.go` | `RuleScript`、`ScriptStats` 结构体 |
| 1.8 | 创建 `rule/parser.go` | 规则文件解析器（支持 hosts / 正则 / pipeline:// / @指令） |
| 1.9 | 创建 `rule/engine.go` | `RuleEngine` 基础实现（`ShouldIntercept` + `ResolveUpstream` + `Match`） |
| 1.10 | 拆分 `proxy.go` → `proxy/` 包 | `proxy.go` + `mitm.go` + `tunnel.go` + `helpers.go` + `config.go` |
| 1.11 | 迁移 `cert.go` → `proxy/cert.go` | 直接移动 |
| 1.12 | 迁移 `connmgr.go` → `proxy/connmgr.go` | 直接移动 |
| 1.13 | 改造 `ForwardProxy` | 持有 `InterceptMatcher`、`UpstreamResolver`、`RequestMatcher` 接口，替代硬编码 |
| 1.14 | 创建 `adapter/interfaces.go` + `adapter/registry.go` | 适配器接口 + 注册表 |
| 1.15 | 创建 `adapter/yunapi/` | 迁移 `handler.go` → `transformer.go` + `auth.go` + `adapter.go` |
| 1.16 | 创建 `processor/` 包 | `ProxyProcessor` + `PipelineProcessor` + `StreamProcessor` + registry |
| 1.17 | 创建 `rules/apm-dev.rules` | 将当前硬编码规则迁移为规则脚本文件 |
| 1.18 | 改造 `main.go` | 加载配置 → 解析规则 → 创建 Engine → 创建 Processors → 注入到 Proxy → 启动 |
| 1.19 | 迁移 `debug.go` → `debug/collector.go` | 实现 TraceRecorder 接口 |
| 1.20 | 编译通过 + 功能回归验证 | 启动代理，APM 控制台功能完全正常 |

#### 预期结果

- 目录结构从扁平单包 → 分层多包
- `proxy/` 只依赖接口，不知道规则如何存储和匹配
- `rule/` 负责解析和匹配，不知道具体如何处理
- `processor/` 负责执行，不知道规则如何管理
- `pipeline/` 定义阶段接口和编排器，不依赖具体适配器
- hosts.txt 功能迁移为规则脚本（`rules/apm-dev.rules` 中的 hosts 行）
- 所有现有功能保持不变

#### 验收标准

| # | 验收项 | 验证方式 |
|---|--------|---------|
| ✅ | 编译通过，无 lint error | `go build ./...` |
| ✅ | `proxy/` 不依赖 `rule/`、`processor/`、`adapter/`、`pipeline/` | `go list -deps ./proxy/` |
| ✅ | `pipeline/` 不依赖 `adapter/` | `go list -deps ./pipeline/` |
| ✅ | `rule/` 不依赖 `processor/`、`proxy/` | `go list -deps ./rule/` |
| ✅ | APM 控制台页面正常加载 | 浏览器通过代理访问 |
| ✅ | yunapi 拦截正常 | debug 面板显示 INTERCEPT 记录 |
| ✅ | 直连 API 拦截正常 | `/_api/apm/` 请求正常 |
| ✅ | 非拦截请求正常透传 | FWD 记录正常 |
| ✅ | TCP 隧道正常 | CDN 资源正常加载 |
| ✅ | 并发处理正常 | 页面加载时间与重构前一致 |

---

### Sprint 2：规则管理 + 热加载 + 静态资源代理

> **目标**：完善规则管理能力（CRUD + 启停 + 热加载），实现静态资源代理（前端 HMR），支持多脚本隔离。

#### 任务列表

| # | 任务 | 说明 |
|---|------|------|
| 2.1 | 创建 `rule/manager.go` | RuleManager 完整实现（CRUD + 启停 + 持久化） |
| 2.2 | 实现文件监听热加载 | fsnotify 监听 `rules/` 目录变化，自动 reload |
| 2.3 | 创建 `rule/validator.go` | 规则校验（语法 + 正则合法性 + 适配器引用检查） |
| 2.4 | 完善 `ProxyProcessor` | 支持正则捕获组替换、Header 复制、超时控制 |
| 2.5 | 完善 `StreamProcessor` | SSE 流式代理（不读完 body，管道式传输） |
| 2.6 | 实现 `@stream` 选项 | Match 结果含 Stream 标记 → 分发到 StreamProcessor |
| 2.7 | 实现 `@cors` 选项 | OPTIONS 预检自动返回 204，实际请求附加 CORS 头 |
| 2.8 | `cloudcache.tencent-cloud.com` MITM 支持 | 通过 `@intercept` 声明，验证 CDN 域名 MITM |
| 2.9 | 创建 `rules/cls-dev.rules` | 验证多脚本隔离（与 apm-dev 共存） |
| 2.10 | 创建 `rules/test-env.rules` | 迁移现有 hosts.txt 中的测试环境映射 |
| 2.11 | 创建 `adapter/generic/` | 通用适配器（URL 改写 + Header 注入 + 无 Pipeline） |
| 2.12 | 创建 `adapter/template/` | 模板驱动适配器（配置化 Pipeline） |
| 2.13 | 端到端验证 | APM 前端资源走本地 dev-server + HMR 热更新 + API 拦截 |

#### 预期结果

- 规则脚本支持创建/编辑/删除/启停，文件变化自动热加载
- 静态资源（JS/CSS）从 CDN 重定向到本地 webpack-dev-server
- HMR SSE 流式传输正常工作
- 多脚本（APM + CLS + test-env）可共存，互不干扰
- 彻底消除 `hosts.txt`，全部由规则脚本管理

#### 验收标准

| # | 验收项 | 验证方式 |
|---|--------|---------|
| ✅ | 静态资源代理生效 | `tapmOverview.js` 从本地 dev-server 获取 |
| ✅ | HMR 热更新正常 | 修改代码 → 浏览器自动刷新 |
| ✅ | 多脚本隔离 | 禁用 CLS 脚本不影响 APM 脚本 |
| ✅ | 热加载生效 | 修改 .rules 文件 → 2s 内规则自动更新 |
| ✅ | 脚本启停不重启 | API 调用 disable/enable → 立即生效 |
| ✅ | 正则不匹配时透传 | CDN 上非 tapm 资源正常通过 MITM 透传 |
| ✅ | hosts.txt 已删除 | 全部由 rules/ 目录管理 |
| ✅ | generic 适配器可用 | 配置验证通过 |
| ✅ | template 适配器可用 | 配置验证通过 |

---

### Sprint 3：Web UI 管理面板 + 实时日志

> **目标**：提供可视化管理面板，支持规则编辑、启停、实时匹配日志，达到类似 Whistle 的使用体验。

#### 任务列表

| # | 任务 | 说明 |
|---|------|------|
| 3.1 | 创建 `webui/handler.go` | REST API handler（规则 CRUD + 启停 + 校验 + 状态） |
| 3.2 | 创建 `webui/events.go` | SSE 实时日志推送（规则匹配事件） |
| 3.3 | 创建 `webui/static/index.html` | 单页应用入口 |
| 3.4 | 创建 `webui/static/app.js` | 规则列表（启停/优先级）+ 规则编辑器（语法高亮 + 实时校验）+ 日志面板 |
| 3.5 | 创建 `webui/static/style.css` | 样式 |
| 3.6 | 使用 `embed` 嵌入静态资源 | 打包为单一二进制 |
| 3.7 | 改造 `debug/` 包 | 精简，部分功能迁入 webui（诊断面板 → Web UI） |
| 3.8 | 实现规则编辑器语法高亮 | 支持 hosts / 正则 / @指令 的着色和自动补全 |
| 3.9 | 实现实时日志过滤 | 按脚本名/规则类型/状态码过滤 |
| 3.10 | 实现匹配统计展示 | 每条规则的命中次数、最后命中时间 |
| 3.11 | 端到端验证 | Web UI 创建/编辑/启停规则 → 代理行为实时生效 |

#### 预期结果

- 浏览器访问 `http://localhost:8082/ui/` 可管理所有规则
- 规则编辑器带语法高亮和实时校验
- 实时日志 SSE 推送，支持过滤和暂停
- 匹配统计可视化展示
- 单一二进制，无需外部依赖

#### 验收标准

| # | 验收项 | 验证方式 |
|---|--------|---------|
| ✅ | Web UI 可访问 | `http://localhost:8082/ui/` 正常加载 |
| ✅ | 规则列表展示 | 显示所有脚本 + 启用状态 + 命中统计 |
| ✅ | 在线编辑规则 | 编辑 → 保存 → 2s 内生效 |
| ✅ | 实时校验 | 输入错误语法时立即显示错误提示 |
| ✅ | 启停控制 | 点击开关 → 规则立即启停 |
| ✅ | 实时日志 | SSE 推送匹配事件，不丢失 |
| ✅ | 日志过滤 | 按脚本/类型过滤正确 |
| ✅ | 单一二进制 | `go build` 产出单文件，含所有静态资源 |

---

### Sprint 4：诊断层解耦 + 开源准备

> **目标**：诊断层不依赖业务域名，完善文档，达到开源可发布状态。

#### 任务列表

| # | 任务 | 说明 |
|---|------|------|
| 4.1 | 改造 `debug/collector.go` | Host 从请求上下文获取，不硬编码 |
| 4.2 | 改造 `debug/mcp.go` | MCP 工具描述通用化 |
| 4.3 | 编写 README.md | 功能介绍、快速开始、配置参考、规则语法参考、适配器开发指南 |
| 4.4 | 添加 LICENSE | MIT / Apache 2.0 |
| 4.5 | 添加 `examples/` 目录 | 示例配置和规则脚本 |
| 4.6 | 添加 Makefile | `make build` / `make test` / `make release` |
| 4.7 | 添加 `.goreleaser.yaml` | 多平台二进制构建 |
| 4.8 | 代码审计 | 清理硬编码域名、账号、内网 IP 等敏感信息 |
| 4.9 | 性能测试 | 规则匹配性能基准测试（100 条规则 < 1ms） |
| 4.10 | 集成测试 | 完整的端到端测试用例 |

#### 验收标准

| # | 验收项 | 验证方式 |
|---|--------|---------|
| ✅ | `proxy/` 无业务域名 | `grep -r "cloud.tencent.com" proxy/` 无结果 |
| ✅ | `rule/` 无业务域名 | `grep -r "cloud.tencent.com" rule/` 无结果 |
| ✅ | README 完整 | 新用户 5 分钟内可运行 |
| ✅ | 示例可运行 | `examples/` 下配置均可正常启动 |
| ✅ | 无敏感信息 | `grep -rE "(uin|appid|1500000|700001|9\.223)" .` 只在 examples 中 |
| ✅ | 跨平台编译 | linux/amd64 + darwin/arm64 编译成功 |
| ✅ | 规则匹配性能 | 100 条规则匹配 < 1ms（benchmark） |

---

## 六、风险评估

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| Sprint 1 重构引入回归 | 现有功能不可用 | 每步编译 + 端到端验证；可随时 git revert |
| 规则语法设计不够通用 | 覆盖不了某些场景 | 保留 `pipeline://` 扩展口，复杂场景写自定义适配器 |
| 正则性能（复杂正则回溯） | 匹配延迟高 | 正则编译时校验复杂度；匹配超时保护（10ms） |
| 热加载竞态 | 规则不一致 | 写锁 + 原子替换；编译失败回滚到旧版本 |
| SSE 流式传输阻塞 | 影响同连接其他请求 | 浏览器为 SSE 开独立连接（实测验证） |
| Pipeline 阶段接口粒度 | 跨阶段共享状态困难 | `PipelineContext.Metadata` 通用 KV 存储 |
| 配置格式变更 | 用户需更新配置 | Sprint 1 提供自动迁移脚本（旧 config → 新 config + rules） |
| 开源后敏感信息泄漏 | 安全风险 | Sprint 4 专门代码审计 + CI secret scanning |

---

## 七、改动量预估

| Sprint | 新增代码 | 迁移/重构代码 | 删除代码 | 核心产出 |
|--------|---------|-------------|---------|---------|
| Sprint 1 | ~800 行 | ~600 行 | ~50 行 | 分层架构 + 规则引擎基础 + Pipeline + yunapi 适配器 |
| Sprint 2 | ~600 行 | ~100 行 | ~300 行（hosts.txt） | 规则管理 + 热加载 + 静态代理 + 流式传输 |
| Sprint 3 | ~1000 行 | ~50 行 | ~30 行 | Web UI 管理面板 + 实时日志 |
| Sprint 4 | ~300 行 | ~100 行 | ~50 行 | 文档 + 示例 + 开源准备 |

---

## 八、设计决策记录

| # | 决策点 | 选择 | 理由 |
|---|--------|------|------|
| 1 | 规则存储格式 | 文本规则脚本（`.rules` 文件） | Whistle 兼容，人类可读，Git 友好，支持注释 |
| 2 | 规则管理方式 | 文件系统 + Web UI + REST API | 开发者可用编辑器直接改文件，也可用 Web UI 在线管理 |
| 3 | 静态代理 vs Pipeline | 独立 Processor | 不需要拆包/认证/组包，强行套 Pipeline 增加复杂度 |
| 4 | 多脚本优先级 | `@priority` 显式声明 | 比文件名排序更直观，避免重命名影响行为 |
| 5 | MITM 域名管理 | `@intercept` 指令 + 自动合并 | 规则和域名声明在一起，不需要两处配置 |
| 6 | hosts 功能定位 | 规则脚本中的一种规则类型 | 统一管理，不需要单独的 hosts.txt 文件 |
| 7 | 热加载机制 | fsnotify + 写锁 + 原子替换 | 无需重启，编译失败回滚保证稳定 |
| 8 | Pipeline vs 规则引擎 | 并存，Pipeline 被规则引擎调度 | Pipeline 处理 API 拆包/组包（复杂），规则引擎负责匹配/分发（简单） |
| 9 | Web UI 技术栈 | 原生 JS + embed 嵌入 | 零依赖，单一二进制，避免 Node.js 构建步骤 |
| 10 | SSE 流式传输 | `@stream` 标记 + StreamProcessor | 显式标记避免误判；独立处理器隔离复杂度 |

---

## 九、后续演进方向（超出当前 Roadmap）

| 方向 | 说明 |
|------|------|
| **HTTP/2 MITM** | 代理和浏览器之间也使用 HTTP/2，彻底消除协议降级 |
| **WebSocket 拦截** | 支持 WebSocket 升级请求的拦截和转发 |
| **Pipeline 中间件链** | 日志、限流、熔断、重试等通用中间件 |
| **Pipeline 条件分支** | 根据请求内容动态选择不同阶段组合 |
| **gRPC Forwarder** | Pipeline 支持 gRPC 协议转发 |
| **响应模板引擎** | Go template 语法的灵活响应包装 |
| **插件系统** | 外部二进制/动态库加载适配器 |
| **录制/回放** | 请求录制 + Mock 回放（测试和 Demo） |
| **规则脚本模板市场** | 预置常见场景的规则脚本，一键导入 |
| **规则组/继承** | `@import` 引用其他脚本，减少重复 |
| **分布式配置** | 规则脚本远程存储 + 多实例同步 |
| **性能面板** | 请求耗时分布、慢请求排行、连接池使用率 |
