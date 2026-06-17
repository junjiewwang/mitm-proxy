# yunapi-proxy 通用化重构方案 — Roadmap

> 创建时间：2026-06-17
> 状态：方案设计完成，待实施

---

## 一、重构目标

将 yunapi-proxy 从"腾讯云 APM 联调专用工具"重构为**通用的 MITM 正向代理框架**，
业务逻辑作为可插拔的**适配器**通过配置 + 接口注入，向开源项目演进。

### 设计原则

| 原则 | 具体要求 |
|------|---------|
| **高内聚低耦合** | 代理核心层不包含任何业务逻辑，通过接口与适配器交互 |
| **开闭原则 (OCP)** | 新增业务场景只需实现适配器接口 + 添加配置，不改核心代码 |
| **依赖倒置 (DIP)** | 核心层定义接口，适配器层实现接口，通过依赖注入组装 |
| **单一职责 (SRP)** | 每个包/文件只负责一个明确的职责 |
| **配置驱动** | 拦截规则、字段映射、认证注入等全部通过配置文件声明 |

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

### 3.1 分层设计

```
┌─────────────────────────────────────────────────────────────┐
│                      配置层 (config.yaml)                    │
│   通用代理配置 + 拦截规则 + Pipeline 声明 + 适配器参数        │
└──────────────────────────┬──────────────────────────────────┘
                           │
┌──────────────────────────▼──────────────────────────────────┐
│                    代理核心层 (proxy/)                        │
│                                                              │
│  ForwardProxy · MITM 并发处理 · TCP 隧道 · 连接池            │
│  CA 证书管理 · LRU 连接管理 · 有序响应写回                    │
│                                                              │
│  接口定义（interfaces.go）：                                  │
│    InterceptMatcher     — 请求是否需要拦截                    │
│    RequestProcessor     — 拦截后如何处理（Pipeline 编排入口） │
│    TraceRecorder        — 请求追踪记录                        │
└──────────────────────────┬──────────────────────────────────┘
                           │ 依赖注入
┌──────────────────────────▼──────────────────────────────────┐
│                  Pipeline 处理引擎 (pipeline/)                │
│                                                              │
│  ┌─ RequestTransformer ─┐  ┌─ AuthInjector ─┐               │
│  │  拆包：提取/重命名/   │  │  认证信息注入   │               │
│  │  扁平化嵌套字段       │  │  （可选阶段）   │               │
│  └───────────────────────┘  └────────────────┘               │
│  ┌─ Forwarder ───────────┐  ┌─ ResponseTransformer ─┐       │
│  │  转发到后端           │  │  组包：包装/重命名/     │       │
│  │  （通用，各适配器复用）│  │  嵌套响应结构          │       │
│  └───────────────────────┘  └────────────────────────┘       │
│                                                              │
│  Pipeline 编排器：按配置声明的阶段列表顺序执行               │
│  每个阶段可独立替换、跳过、或叠加多个实例                    │
└──────────────────────────┬──────────────────────────────────┘
                           │ 组合
┌──────────────────────────▼──────────────────────────────────┐
│                     适配器层 (adapter/)                       │
│                                                              │
│  adapter/yunapi/        腾讯云 yunapi 适配器                   │
│    ├── adapter.go         组装 Pipeline 阶段                  │
│    ├── transformer.go     实现 RequestTransformer +           │
│    │                      ResponseTransformer                 │
│    ├── auth.go            实现 AuthInjector                   │
│    └── config.go          yunapi 特有配置解析                  │
│                                                              │
│  adapter/generic/       通用适配器（URL 改写 + Header 注入）   │
│    └── adapter.go         简单转发 / Mock（无需拆包组包）      │
│                                                              │
│  adapter/template/      模板驱动适配器（配置化拆包组包）       │
│    └── adapter.go         基于 pipeline 配置声明式组装         │
└──────────────────────────┬──────────────────────────────────┘
                           │
┌──────────────────────────▼──────────────────────────────────┐
│                      拦截规则引擎 (intercept/)                │
│                                                              │
│  基于配置的声明式规则匹配器（实现 InterceptMatcher）           │
│  支持 method / path / path_prefix / query / header 组合匹配  │
└─────────────────────────────────────────────────────────────┘
                           │
┌──────────────────────────▼──────────────────────────────────┐
│                       诊断层 (debug/)                        │
│                                                              │
│  TraceCollector（环形缓冲区）· HTML Dashboard · MCP Server    │
│  实现 TraceRecorder 接口，不依赖具体业务域名                  │
└─────────────────────────────────────────────────────────────┘
```

### 3.2 核心接口定义

#### 3.2.1 代理核心层接口（proxy/interfaces.go）

```go
// InterceptMatcher 请求拦截匹配器
// 判断一个 MITM 解密后的 HTTP 请求是否需要被拦截处理
type InterceptMatcher interface {
    // Match 返回匹配的规则名，空字符串表示不匹配（透传到上游）
    Match(r *http.Request) string
}

// RequestProcessor 被拦截请求的处理器（适配器的入口接口）
// 代理核心层只依赖此接口，不关心内部是简单转发还是多阶段 Pipeline
type RequestProcessor interface {
    // Process 处理被拦截的请求，返回构建好的 HTTP 响应
    // req: 原始请求（Body 已预读到内存，可安全重复读取）
    // body: 预读的请求体字节
    Process(req *http.Request, body []byte) *http.Response
}

// TraceRecorder 请求追踪记录器（可选注入，nil 表示不记录）
type TraceRecorder interface {
    RecordTunnel(targetHost string)
    RecordForward(method, path, host string, statusCode int, elapsed time.Duration, ok bool, errMsg string)
    RecordIntercept(ruleName, action string, statusCode int, elapsed time.Duration)
}
```

#### 3.2.2 Pipeline 阶段接口（pipeline/interfaces.go）

将"拆包 → 认证注入 → 转发 → 组包"拆分为独立的阶段接口，每个阶段可独立替换、组合或跳过。

```go
// ===== pipeline/interfaces.go =====

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
// 将客户端发来的原始请求体转换为后端期望的格式
//
// 职责示例：
//   - yunapi 适配器：从 {cmd, serviceType, data} 提取并扁平化为 {Action, ApiModule, ...data字段}
//   - generic 适配器：原样透传（不做任何转换）
//   - 自定义适配器：字段重命名、嵌套字段提取、默认值填充
type RequestTransformer interface {
    // TransformRequest 将解析后的请求转换为后端请求格式
    // 输入：ctx.ParsedRequest（JSON 解析后的原始请求）
    // 输出：ctx.BackendRequest（转换后的后端请求体）
    TransformRequest(ctx *PipelineContext) error
}

// AuthInjector 认证信息注入器（可选阶段）
// 向后端请求中注入认证、鉴权相关字段
//
// 职责示例：
//   - yunapi 适配器：注入 Uin、SubAccountUin、AppId
//   - 其他场景：注入 API Key、Bearer Token、签名等
type AuthInjector interface {
    // InjectAuth 向 BackendRequest 中注入认证信息
    InjectAuth(ctx *PipelineContext) error
}

// Forwarder 后端转发器
// 将转换后的请求发送到后端服务并获取响应
//
// 框架提供默认实现（HTTPForwarder），适配器也可自定义：
//   - 默认：HTTP POST JSON 转发
//   - 自定义：gRPC 转发、多后端扇出、重试/降级等
type Forwarder interface {
    // Forward 转发请求到后端
    // 输入：ctx.BackendRequest + ctx.BackendURL
    // 输出：ctx.BackendResp
    Forward(ctx *PipelineContext) error
}

// ResponseTransformer 响应转换器（组包阶段）
// 将后端响应转换为客户端期望的格式
//
// 职责示例：
//   - yunapi 适配器：包装为三层嵌套 {code, data:{code, cgwerrorCode, data:{Response}}}
//   - generic 适配器：原样透传后端响应
//   - 自定义适配器：字段重命名、错误码映射、字段裁剪
type ResponseTransformer interface {
    // TransformResponse 将后端响应转换为客户端期望的格式
    // 输入：ctx.BackendResp（后端原始响应）
    // 输出：ctx.FinalResp（最终返回给客户端的响应）
    TransformResponse(ctx *PipelineContext) error
}
```

#### 3.2.3 Pipeline 编排器（pipeline/pipeline.go）

```go
// ===== pipeline/pipeline.go =====

// Pipeline 请求处理管线
// 按阶段顺序执行：解析 → 拆包 → 认证注入 → 转发 → 组包
// 每个阶段可为 nil（跳过），也可叠加多个实例（链式执行）
type Pipeline struct {
    // 必选阶段
    Forwarder            Forwarder

    // 可选阶段（nil 表示跳过）
    RequestTransformers  []RequestTransformer  // 支持多个转换器链式执行
    AuthInjectors        []AuthInjector        // 支持多个注入器（如同时注入 auth + region）
    ResponseTransformers []ResponseTransformer // 支持多个响应转换器
}

// Process 实现 RequestProcessor 接口，执行完整 Pipeline
func (p *Pipeline) Process(req *http.Request, body []byte) *http.Response {
    ctx := &PipelineContext{
        OriginalRequest: req,
        RawBody:         body,
        Metadata:        make(map[string]any),
    }

    // 1. 解析请求体
    if err := json.Unmarshal(body, &ctx.ParsedRequest); err != nil {
        return buildErrorResponse(400, "parse request body failed")
    }

    // 2. 拆包（RequestTransformers 链式执行）
    for _, t := range p.RequestTransformers {
        if err := t.TransformRequest(ctx); err != nil {
            return buildErrorResponse(400, "transform request failed: "+err.Error())
        }
    }
    // 如果没有配置 RequestTransformer，BackendRequest 直接用 ParsedRequest
    if ctx.BackendRequest == nil {
        ctx.BackendRequest = ctx.ParsedRequest
    }

    // 3. 认证注入（AuthInjectors 链式执行）
    for _, a := range p.AuthInjectors {
        if err := a.InjectAuth(ctx); err != nil {
            return buildErrorResponse(500, "inject auth failed: "+err.Error())
        }
    }

    // 4. 转发
    if err := p.Forwarder.Forward(ctx); err != nil {
        return buildErrorResponse(502, "forward failed: "+err.Error())
    }

    // 5. 组包（ResponseTransformers 链式执行）
    ctx.FinalResp = ctx.BackendResp // 默认：原样返回
    for _, t := range p.ResponseTransformers {
        if err := t.TransformResponse(ctx); err != nil {
            return buildErrorResponse(500, "transform response failed: "+err.Error())
        }
    }

    return buildJSONResponse(200, ctx.FinalResp)
}
```

#### 3.2.4 适配器接口（adapter/interfaces.go）

```go
// ===== adapter/interfaces.go =====

// Adapter 适配器接口（组合 RequestProcessor + 生命周期管理）
// 适配器内部通过组装 Pipeline 阶段实现 Process 方法
type Adapter interface {
    RequestProcessor

    // Name 适配器名称（用于日志和配置索引）
    Name() string

    // Init 初始化适配器（传入适配器配置段，组装 Pipeline）
    Init(rawConfig map[string]any) error

    // Routes 返回需要注册到管理服务的额外路由（可选）
    Routes() []Route

    // Close 清理资源
    Close() error
}

// Route 适配器声明的 HTTP 路由
type Route struct {
    Method  string           // GET / POST / ANY
    Path    string           // 路由路径
    Handler http.HandlerFunc
}
```

#### 3.2.5 各适配器与 Pipeline 的关系

```
┌─────────────────────────────────────────────────────────────────┐
│                     Pipeline 阶段组合对比                        │
├─────────────┬──────────────────┬─────────────────┬──────────────┤
│   阶段       │ yunapi 适配器     │ generic 适配器   │ template 适配器│
├─────────────┼──────────────────┼─────────────────┼──────────────┤
│ 请求解析     │ JSON + form 双格式│ JSON            │ JSON          │
│ RequestTrans │ ✅ 拆包：cmd→Action│ ❌ 跳过（透传） │ ✅ 配置驱动    │
│             │ data 扁平化       │                 │ 字段映射规则   │
│ AuthInjector│ ✅ Uin/AppId 注入 │ ✅ Header 注入   │ ✅ 配置声明    │
│ Forwarder   │ HTTPForwarder    │ HTTPForwarder   │ HTTPForwarder │
│ ResponseTrans│ ✅ 三层嵌套包装  │ ❌ 跳过（原样）  │ ✅ 模板驱动    │
└─────────────┴──────────────────┴─────────────────┴──────────────┘
```

### 3.3 目标目录结构

```
yunapi-proxy/
├── main.go                        # 启动入口（组装各层组件）
├── config.yaml                    # 统一配置文件
├── hosts.txt                      # hosts 映射（可选）
│
├── proxy/                         # 【核心层】通用 MITM 正向代理
│   ├── interfaces.go             #   核心接口（InterceptMatcher / RequestProcessor / TraceRecorder）
│   ├── proxy.go                  #   ForwardProxy 主体（ServeHTTP / handleConnect / handleHTTP）
│   ├── mitm.go                   #   MITM 并发处理 + 有序写回（handleMITM / processForward）
│   ├── tunnel.go                 #   TCP 隧道透传（handleTunnel）
│   ├── cert.go                   #   CA 证书管理（LoadOrGenerateCA / GenerateCert）
│   ├── connmgr.go                #   LRU 连接管理器（ConnManager）
│   ├── helpers.go                #   通用工具函数（isHopByHop / splitHostPort / buildErrorResponse / CORS）
│   └── config.go                 #   代理层配置结构体（ProxyConfig）
│
├── pipeline/                      # 【Pipeline 处理引擎】请求处理管线
│   ├── interfaces.go             #   阶段接口（RequestTransformer / AuthInjector / Forwarder / ResponseTransformer）
│   ├── context.go                #   PipelineContext 上下文结构体
│   ├── pipeline.go               #   Pipeline 编排器（实现 RequestProcessor，按阶段顺序执行）
│   └── forwarder.go              #   HTTPForwarder 默认转发器实现（HTTP POST JSON）
│
├── intercept/                     # 【拦截规则引擎】声明式匹配
│   ├── rule.go                   #   Rule 结构体 + 基于配置的 Match 逻辑
│   └── engine.go                 #   RuleEngine（实现 InterceptMatcher，管理多条规则 + 适配器映射）
│
├── adapter/                       # 【适配器层】可插拔的业务实现
│   ├── interfaces.go             #   Adapter 接口定义 + Route 结构体
│   ├── registry.go               #   适配器注册表（Register / Get）
│   │
│   ├── yunapi/                   #   腾讯云 yunapi 适配器
│   │   ├── adapter.go            #     实现 Adapter 接口，组装 Pipeline
│   │   ├── transformer.go        #     实现 RequestTransformer（拆包）+ ResponseTransformer（组包）
│   │   ├── auth.go               #     实现 AuthInjector（Uin/SubAccountUin/AppId + Region 注入）
│   │   └── config.go             #     YunAPIConfig 结构体（auth / field_mapping / region_map）
│   │
│   ├── generic/                  #   通用转发适配器
│   │   └── adapter.go            #     URL 改写 + Header 注入 + 无 Pipeline 直接透传
│   │
│   └── template/                 #   模板驱动适配器（配置化 Pipeline）
│       ├── adapter.go            #     从配置文件声明式组装 Pipeline 各阶段
│       ├── field_mapper.go       #     通用字段映射 RequestTransformer（配置驱动）
│       └── response_wrapper.go   #     模板化 ResponseTransformer（配置驱动）
│
├── debug/                         # 【诊断层】
│   ├── collector.go              #   TraceCollector（实现 TraceRecorder，环形缓冲区）
│   ├── dashboard.go              #   HTML 诊断面板 + JSON API
│   └── mcp.go                    #   MCP Server（JSON-RPC 2.0）
│
└── docs/
    ├── yunapi-proxy.md            # 项目说明文档
    └── refactor-roadmap.md        # 本文档
```

### 3.4 目标配置文件格式

```yaml
# ===== 通用代理配置 =====
server:
  listen: ":8082"

proxy:
  listen: ":8081"
  cert_dir: "certs"
  hosts_file: "hosts.txt"
  upstream_map: {}
  max_connections: 100
  idle_timeout: 10s
  intercept_domains:
    - "console.cloud.tencent.com"
    - "console-hc.cloud.tencent.com"

# ===== 拦截规则（声明式，替代硬编码）=====
# 规则按顺序匹配，第一条匹配即停止
intercept_rules:
  # 规则 1：yunapi 格式 — POST /cgi/capi?action=delegate&serviceType=apm
  - name: "yunapi-apm"
    match:
      method: "POST"
      path: "/cgi/capi"
      query:
        action: "delegate"
        serviceType: "apm"
    adapter: "yunapi"

  # 规则 2：直连 API 格式 — /_api/apm/{Action}（GET 或 POST）
  # 使用 path_prefix 前缀匹配，同样走 yunapi 适配器（需要拆包组包）
  - name: "direct-api-apm"
    match:
      path_prefix: "/_api/apm/"
    adapter: "yunapi"
    options:
      cors: true   # 跨域场景自动处理 OPTIONS 预检

# ===== 适配器配置（按名称索引）=====
adapters:
  # ── yunapi 适配器 ──────────────────────────────────────────
  # 使用内置 Pipeline：拆包 → 认证注入 → 转发 → 组包
  yunapi:
    type: "yunapi"          # 适配器类型（对应 adapter/yunapi/）

    # 后端转发配置（Forwarder 阶段）
    backend:
      url: "http://127.0.0.1:16686"
      api_path: "/api/v1"
      timeout: 30s

    # Pipeline 配置
    pipeline:

      # ── 拆包阶段（RequestTransformer）──
      # 将前端请求格式转换为后端扁平格式
      request_transform:
        # 顶层字段映射：前端字段名 → 后端字段名
        field_mapping:
          cmd: "Action"
          serviceType: "ApiModule"

        # 嵌套字段提取：将 data.* 扁平化到顶层
        flatten_fields:
          - source: "data"        # 源字段（嵌套 map）
            target: "."           # "." 表示提取到顶层
            strategy: "merge"     # merge: 合并到顶层; replace: 覆盖同名

        # 保护字段：data 中的这些字段不允许覆盖已有的顶层字段
        protected_fields:
          - "Uin"
          - "SubAccountUin"
          - "AppId"
          - "Action"
          - "ApiModule"

      # ── 认证注入阶段（AuthInjector）──
      auth:
        # 固定值注入（fallback，当请求中缺少时使用）
        inject_fields:
          Uin: "1500000686"
          SubAccountUin: "700001199694"
          AppId: 251005942

        # 从请求字段映射（优先级高于固定值）
        field_source:
          Uin: "ownerUin"         # 请求中的 ownerUin → 后端的 Uin
          SubAccountUin: "uin"    # 请求中的 uin → 后端的 SubAccountUin

      # ── Region 映射（作为 RequestTransformer 的子阶段）──
      region:
        source_field: "regionId"  # 从请求中读取此字段
        target_field: "Region"    # 写入后端请求的此字段
        default: "ap-guangzhou"
        mapping:
          1: "ap-guangzhou"
          4: "ap-shanghai"
          8: "ap-beijing"

      # ── 组包阶段（ResponseTransformer）──
      # 将后端响应包装为前端期望的格式
      response_transform:
        # 包装模板：将后端原始响应包装为指定的嵌套结构
        # ${backend_response} 是占位符，代表后端返回的原始 JSON
        # ${error_code} 是从后端响应中提取的错误码
        wrapper_template:
          code: 0
          data:
            code: "${error_code}"
            cgwerrorCode: 0
            data:
              Response: "${backend_response}"
          mccode: 0
          errObj: {}
          seqId: "${generated_seq_id}"
          reqId: "${generated_req_id}"

        # 错误码提取规则：从后端响应中提取业务错误码
        error_code_path: "Response.Error.Code"
        error_code_default: 0

  # ── generic 适配器（示例）──────────────────────────────────
  # 无 Pipeline，简单透传
  # my-mock:
  #   type: "generic"
  #   backend:
  #     url: "http://127.0.0.1:9090"
  #     timeout: 10s
  #   headers:
  #     X-Forwarded-By: "yunapi-proxy"

  # ── template 适配器（示例）─────────────────────────────────
  # 纯配置驱动的 Pipeline，不需要写 Go 代码
  # my-custom-api:
  #   type: "template"
  #   backend:
  #     url: "http://127.0.0.1:8080"
  #     api_path: "/api"
  #     timeout: 15s
  #   pipeline:
  #     request_transform:
  #       field_mapping:
  #         action: "Method"
  #         params: "Parameters"
  #     auth:
  #       inject_fields:
  #         ApiKey: "${env:MY_API_KEY}"    # 支持环境变量引用
  #     response_transform:
  #       wrapper_template:
  #         success: true
  #         result: "${backend_response}"

# ===== 诊断配置 =====
debug:
  enable: true
  buffer_size: 500
  mcp_enable: true
```

#### 3.4.1 Pipeline 配置说明

| 配置段 | 对应 Pipeline 阶段 | 是否必填 | 说明 |
|-------|-------------------|---------|------|
| `pipeline.request_transform` | `RequestTransformer` | 否 | 不配置则原样透传请求体 |
| `pipeline.request_transform.field_mapping` | 字段重命名 | 否 | `源字段: 目标字段` 映射 |
| `pipeline.request_transform.flatten_fields` | 嵌套字段提取 | 否 | 将嵌套 map 扁平化到顶层 |
| `pipeline.request_transform.protected_fields` | 字段保护 | 否 | 扁平化时不允许覆盖的字段 |
| `pipeline.auth` | `AuthInjector` | 否 | 不配置则不注入认证信息 |
| `pipeline.auth.inject_fields` | 固定值注入 | 否 | fallback 默认值 |
| `pipeline.auth.field_source` | 字段来源映射 | 否 | 从请求中提取（优先级高于固定值） |
| `pipeline.region` | `RequestTransformer`（子阶段） | 否 | 数字→字符串映射转换 |
| `pipeline.response_transform` | `ResponseTransformer` | 否 | 不配置则原样返回后端响应 |
| `pipeline.response_transform.wrapper_template` | 响应包装模板 | 否 | 支持 `${占位符}` 引用 |
| `pipeline.response_transform.error_code_path` | 错误码提取 | 否 | JSONPath 风格的路径表达式 |

---

## 四、实施 Roadmap

### Sprint 1：核心接口抽取 + Pipeline 引擎 + 目录拆分

> **目标**：将代理核心与业务逻辑解耦，引入 Pipeline 处理引擎抽象拆包/组包逻辑，现有功能完全不变，纯内部重构。

#### 任务列表

| # | 任务 | 说明 |
|---|------|------|
| 1.1 | 创建 `proxy/interfaces.go` | 定义 `InterceptMatcher`、`RequestProcessor`、`TraceRecorder` 接口 |
| 1.2 | 创建 `pipeline/interfaces.go` | 定义 Pipeline 各阶段接口：`RequestTransformer`、`AuthInjector`、`Forwarder`、`ResponseTransformer` |
| 1.3 | 创建 `pipeline/context.go` | `PipelineContext` 上下文结构体（贯穿各阶段的数据载体） |
| 1.4 | 创建 `pipeline/pipeline.go` | `Pipeline` 编排器（实现 `RequestProcessor`，按阶段链式执行） |
| 1.5 | 创建 `pipeline/forwarder.go` | `HTTPForwarder` 默认转发器实现（HTTP POST JSON，连接池复用） |
| 1.6 | 创建 `adapter/interfaces.go` | 定义 `Adapter` 接口 + `Route` 结构体 |
| 1.7 | 拆分 `proxy.go` → `proxy/proxy.go` + `proxy/mitm.go` + `proxy/tunnel.go` + `proxy/helpers.go` | 按职责拆分，MITM/隧道/工具函数各自独立 |
| 1.8 | 迁移 `cert.go` → `proxy/cert.go` | 直接移动，无逻辑改动 |
| 1.9 | 迁移 `connmgr.go` → `proxy/connmgr.go` | 直接移动，无逻辑改动 |
| 1.10 | 创建 `proxy/config.go` | 抽取代理层通用配置结构体 `ProxyConfig` |
| 1.11 | 改造 `ForwardProxy` | 持有 `InterceptMatcher` 和 `map[string]RequestProcessor` 替代硬编码的 `handler` 和 `classifyRequest` |
| 1.12 | 创建 `adapter/yunapi/` | 将 `handler.go` 拆分迁移：`transformer.go`（实现 `RequestTransformer` + `ResponseTransformer`）、`auth.go`（实现 `AuthInjector`）、`adapter.go`（组装 Pipeline） |
| 1.13 | 创建 `adapter/registry.go` | 适配器注册表（`Register` / `Get`） |
| 1.14 | 迁移 `debug.go` → `debug/collector.go` + `debug/dashboard.go` | 拆分诊断收集器和 HTML 面板 |
| 1.15 | 迁移 `mcp.go` → `debug/mcp.go` | 移动到 debug 包 |
| 1.16 | 改造 `main.go` | 依赖注入组装：加载配置 → 创建适配器（组装 Pipeline）→ 注入到代理核心 → 启动 |
| 1.17 | 编译通过 + 功能回归验证 | 启动代理，通过浏览器访问 APM 控制台，验证拦截/转发/诊断全部正常 |

#### 预期结果

- 目录结构从扁平单包 → 分层多包（`proxy/`、`pipeline/`、`adapter/yunapi/`、`debug/`）
- `proxy/` 包只依赖 `RequestProcessor` 接口，不知道 Pipeline 内部结构
- `pipeline/` 包定义阶段接口 + 编排器，不依赖任何具体适配器
- `adapter/yunapi/` 通过组装 Pipeline 阶段实现 `RequestProcessor`
- handler.go 中的拆包逻辑迁移到 `adapter/yunapi/transformer.go`，组包逻辑同上
- 所有现有功能保持不变

#### 验收标准

| # | 验收项 | 验证方式 |
|---|--------|---------|
| ✅ | 编译通过，无 lint error | `go build ./...` 无错误 |
| ✅ | `proxy/` 包不依赖 `adapter/` 和 `pipeline/` 包 | `go list -deps ./proxy/` 中不含 adapter、pipeline |
| ✅ | `pipeline/` 包不依赖 `adapter/` 包 | `go list -deps ./pipeline/` 中不含 adapter |
| ✅ | APM 控制台页面正常加载 | 浏览器通过代理访问 `console.cloud.tencent.com`，APM 页面数据正常 |
| ✅ | yunapi 拦截请求正常 | debug 面板 `/debug/status` 显示 INTERCEPT 请求记录 |
| ✅ | 直连 API 拦截请求正常 | `/_api/apm/DescribeSpanTreeByID` 经过拆包 → 转发 → 组包，数据正确 |
| ✅ | 非拦截请求正常透传 | debug 面板显示 FWD 请求记录 |
| ✅ | TCP 隧道正常 | CDN 资源（platform.js 等）正常加载 |
| ✅ | 并发处理仍然生效 | 多个 API 请求并行处理，页面加载时间与重构前一致 |
| ✅ | 诊断面板正常 | `/debug` HTML 页面可访问，`/debug/status` JSON 返回正常 |
| ✅ | Pipeline 阶段可跳过 | 创建不带 ResponseTransformer 的 Pipeline，后端响应原样返回 |

---

### Sprint 2：声明式拦截规则引擎 + Pipeline 配置化 + 通用适配器

> **目标**：拦截规则从硬编码变为配置驱动，Pipeline 各阶段支持通过配置声明式组装，新增 generic 和 template 适配器验证扩展性。

#### 任务列表

| # | 任务 | 说明 |
|---|------|------|
| 2.1 | 创建 `intercept/rule.go` | `Rule` 结构体：`Name`、`MatchConfig`（method/path/path_prefix/query/header）、`AdapterName`、`Options`（如 cors） |
| 2.2 | 创建 `intercept/engine.go` | `RuleEngine` 实现 `InterceptMatcher`，从配置加载规则列表 |
| 2.3 | 实现 `MatchConfig` 匹配逻辑 | 支持精确匹配（path）、前缀匹配（path_prefix）、Query 参数匹配、Header 匹配 |
| 2.4 | 改造配置文件格式 | 新增 `intercept_rules` 和 `adapters` 配置段，向后兼容旧配置 |
| 2.5 | 创建 `adapter/generic/adapter.go` | 通用转发适配器：URL 改写 + Header 注入 + 无 Pipeline 直接透传 |
| 2.6 | 创建 `adapter/template/` | 模板驱动适配器：从配置声明式组装 Pipeline 各阶段 |
| 2.7 | 创建 `adapter/template/field_mapper.go` | 通用 `RequestTransformer`：配置驱动的字段映射 + 嵌套字段提取 + 保护字段检查 |
| 2.8 | 创建 `adapter/template/response_wrapper.go` | 通用 `ResponseTransformer`：配置驱动的响应包装模板 + 错误码提取 |
| 2.9 | Pipeline 配置解析 | 从 `adapters.xxx.pipeline` 配置段解析 `request_transform` / `auth` / `region` / `response_transform`，自动组装 Pipeline |
| 2.10 | 改造 `main.go` 配置加载 | 解析 `intercept_rules` → 创建 `RuleEngine`；解析 `adapters` → 根据 `type` 创建对应适配器 → 注册到 registry |
| 2.11 | 新增集成测试 | 用 generic 和 template 适配器各配置一个拦截规则，验证扩展性 |

#### 预期结果

- 拦截规则完全通过 `config.yaml` 配置，零代码改动即可新增拦截场景
- generic 适配器可用于简单的 URL 改写/Header 注入场景
- template 适配器可通过配置文件声明式定义拆包/组包规则，无需写 Go 代码
- yunapi 适配器可逐步迁移为 template 适配器（验证配置化方案的完整性）
- 向后兼容：现有 yunapi 配置继续正常工作

#### 验收标准

| # | 验收项 | 验证方式 |
|---|--------|---------|
| ✅ | yunapi 适配器通过新配置格式正常工作 | APM 控制台功能回归（yunapi + 直连 API 两种格式） |
| ✅ | 新增拦截规则不需要改 Go 代码 | 在 config.yaml 中添加 generic 规则，重启代理即生效 |
| ✅ | generic 适配器可正常转发 | 配置一个 generic 规则拦截某个 path → 转发到本地 mock server |
| ✅ | template 适配器可声明式定义 Pipeline | 纯配置创建一个含拆包+组包的 template 适配器，转发到 mock server 验证 |
| ✅ | Pipeline 字段映射正确 | template 适配器的 `field_mapping` 配置生效，后端收到正确的字段名 |
| ✅ | Pipeline 嵌套字段提取正确 | `flatten_fields` 配置生效，嵌套 map 正确扁平化到顶层 |
| ✅ | Pipeline 响应包装正确 | `wrapper_template` 配置生效，客户端收到正确的嵌套格式 |
| ✅ | 规则不匹配时正常透传 | 未匹配的请求仍透传到上游 |
| ✅ | 多条规则按顺序匹配 | 配置多条规则，验证第一条匹配即停止 |
| ✅ | CORS 预检自动处理 | 配置 `options.cors: true` 的规则，OPTIONS 请求自动返回 204 |
| ✅ | 配置校验 | 启动时对无效配置（缺少必填项、适配器不存在、Pipeline 配置错误等）报清晰错误并退出 |

---

### Sprint 3：诊断层解耦 + 开源准备

> **目标**：诊断层不依赖业务域名，完善文档和 README，达到开源可发布状态。

#### 任务列表

| # | 任务 | 说明 |
|---|------|------|
| 3.1 | 改造 `debug/collector.go` | `RecordForward` / `RecordIntercept` 的 Host 从请求上下文获取，不硬编码 |
| 3.2 | 改造 `debug/dashboard.go` | HTML 标题/描述从配置读取，不硬编码 `yunapi-proxy` |
| 3.3 | 改造 `debug/mcp.go` | MCP 工具描述通用化；`proxy_curl` 工具的默认参数从适配器获取 |
| 3.4 | 添加 `adapter/generic/` 的 MCP 扩展 | 通用适配器的 curl 测试工具 |
| 3.5 | 编写 README.md | 面向开源的完整 README：功能介绍、快速开始、配置参考、适配器开发指南 |
| 3.6 | 添加 LICENSE | 选择开源协议（如 MIT / Apache 2.0） |
| 3.7 | 添加 `.goreleaser.yaml` | 自动构建多平台二进制 |
| 3.8 | 添加 `examples/` 目录 | 提供示例配置：yunapi 场景、generic 场景、多规则组合场景 |
| 3.9 | 添加 Makefile | `make build` / `make test` / `make release` |
| 3.10 | 代码审计 | 检查并清理所有硬编码的域名、账号、内网 IP 等敏感信息 |

#### 预期结果

- 诊断面板和 MCP Server 完全通用化，不含任何业务域名
- README 完整，新用户 5 分钟内可运行起来
- 代码中无敏感信息泄漏，可安全开源

#### 验收标准

| # | 验收项 | 验证方式 |
|---|--------|---------|
| ✅ | `debug/` 包不包含任何硬编码域名 | `grep -r "cloud.tencent.com" debug/` 无结果 |
| ✅ | `proxy/` 包不包含任何业务域名 | `grep -r "cloud.tencent.com" proxy/` 无结果 |
| ✅ | README 包含快速开始指南 | 按 README 步骤从零配置并运行成功 |
| ✅ | 示例配置可运行 | `examples/` 下每个配置文件都能正常启动代理 |
| ✅ | 无敏感信息泄漏 | `grep -rE "(uin|appid|1500000|700001|9\.223)" .` 只出现在 examples 中 |
| ✅ | 跨平台编译 | `GOOS=linux GOARCH=amd64 go build` 和 `GOOS=darwin GOARCH=arm64 go build` 均成功 |
| ✅ | 所有功能正常 | yunapi + generic 双适配器场景端到端验证通过 |

---

## 五、风险评估

| 风险 | 影响 | 缓解措施 |
|------|------|---------|
| Sprint 1 重构引入回归 bug | 现有功能不可用 | 每步改动后编译 + 端到端验证；可随时 git revert |
| Pipeline 阶段接口粒度不合适 | 某些场景需要跨阶段共享状态 | `PipelineContext.Metadata` 提供通用 KV 存储；阶段接口保持最小，必要时扩展 |
| 配置驱动的 Pipeline 表达力不足 | 复杂场景仍需写 Go 代码 | template 适配器覆盖 80% 场景；特殊场景回退为自定义适配器 |
| 配置格式变更导致不兼容 | 用户需更新配置 | Sprint 2 保持向后兼容，旧配置格式自动转换 |
| 适配器接口设计不够通用 | 后续扩展困难 | Sprint 1 先用 yunapi 验证 Pipeline 各阶段；Sprint 2 用 generic + template 两个额外场景验证 |
| 开源后敏感信息泄漏 | 安全风险 | Sprint 3 专门做代码审计，CI 中加 secret scanning |

---

## 六、改动量预估

| Sprint | 新增代码 | 迁移/重构代码 | 删除代码 | 总工作量 |
|--------|---------|-------------|---------|---------|
| Sprint 1 | ~400 行（接口定义 + Pipeline 引擎 + 注册表 + main 组装） | ~600 行（拆分迁移 handler.go → adapter/yunapi/） | ~50 行（死代码） | 较大 |
| Sprint 2 | ~500 行（规则引擎 + generic 适配器 + template 适配器 + Pipeline 配置解析） | ~100 行（main + config 改造） | ~20 行 | 较大 |
| Sprint 3 | ~200 行（README + examples + Makefile） | ~100 行（诊断层改造） | ~30 行（硬编码清理） | 较小 |

---

## 七、核心交互流程（重构后）

### 7.1 代理层请求分发

```
请求到达 proxy:8081
    │
    ▼
ForwardProxy.ServeHTTP()
    │
    ├─ CONNECT → handleConnect()
    │       │
    │       ├─ interceptDomains 列表匹配？
    │       │       │
    │       │  YES  ▼
    │       │  handleMITM()
    │       │       │
    │       │  for { http.ReadRequest(reader) }
    │       │       │
    │       │  go func(seq, req) {
    │       │       │
    │       │       ├─ ruleName := interceptMatcher.Match(req)  ← InterceptMatcher 接口
    │       │       │
    │       │       ├─ ruleName != "" ?
    │       │       │       │
    │       │       │  YES  ▼
    │       │       │  processor := processorMap[ruleName]
    │       │       │  resp := processor.Process(req, body)     ← RequestProcessor 接口
    │       │       │  traceRecorder.RecordIntercept(...)        ← TraceRecorder 接口
    │       │       │
    │       │       │  NO   ▼
    │       │       │  resp := processForward(req, hostname)    ← 通用透传（核心层内置）
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
    │
    └─ 非 CONNECT → handleHTTP() — HTTP 代理转发
```

### 7.2 Pipeline 内部执行流程

```
processor.Process(req, body)
    │
    ▼
Pipeline.Process()
    │
    ├─ 1. 解析请求体 → ctx.ParsedRequest
    │
    ├─ 2. 拆包（RequestTransformers 链式执行）
    │       │
    │       ├─ yunapi transformer:
    │       │     cmd → Action
    │       │     serviceType → ApiModule
    │       │     data.* → 扁平化到顶层（检查 protectedFields）
    │       │
    │       ├─ region transformer:
    │       │     regionId(数字) → Region(字符串)
    │       │
    │       └─ template field_mapper（配置驱动）:
    │             按 field_mapping 配置做字段重命名
    │             按 flatten_fields 配置做嵌套提取
    │
    │       → ctx.BackendRequest
    │
    ├─ 3. 认证注入（AuthInjectors 链式执行）
    │       │
    │       ├─ yunapi auth:
    │       │     ownerUin → Uin, uin → SubAccountUin
    │       │     fallback → mock_auth 配置
    │       │     注入 AppId
    │       │
    │       └─ template auth（配置驱动）:
    │             按 field_source 映射 + inject_fields 默认值
    │
    │       → ctx.BackendRequest（已注入认证字段）
    │
    ├─ 4. 转发（Forwarder）
    │       │
    │       └─ HTTPForwarder:
    │             POST ctx.BackendRequest → ctx.BackendURL
    │             → ctx.BackendResp
    │
    └─ 5. 组包（ResponseTransformers 链式执行）
            │
            ├─ yunapi response transformer:
            │     提取 Response.Error.Code → error_code
            │     包装为三层嵌套：
            │     {code:0, data:{code:error_code, cgwerrorCode:0,
            │      data:{Response:...}}, mccode:0, ...}
            │
            ├─ template response_wrapper（配置驱动）:
            │     按 wrapper_template 配置包装
            │     按 error_code_path 配置提取错误码
            │
            └─ generic（无 ResponseTransformer）:
                  ctx.FinalResp = ctx.BackendResp（原样返回）
```

---

## 八、后续演进方向（超出当前 Roadmap）

以下功能不在当前 3 个 Sprint 范围内，作为开源后的长期演进方向：

| 方向 | 说明 |
|------|------|
| **HTTP/2 MITM** | 代理和浏览器之间也使用 HTTP/2，彻底消除协议降级 |
| **WebSocket 拦截** | 支持 WebSocket 升级请求的拦截和转发 |
| **Pipeline 中间件链** | 在 Pipeline 阶段之间插入通用中间件（日志、限流、熔断、重试） |
| **Pipeline 条件分支** | 根据请求内容动态选择不同的 Pipeline 阶段组合（如按 Action 走不同转换规则） |
| **gRPC Forwarder** | Pipeline 支持 gRPC 协议转发，不局限于 HTTP JSON |
| **响应模板引擎** | `wrapper_template` 支持 Go template 语法，提供更灵活的响应包装能力 |
| **Web UI 配置管理** | 通过 Web 界面管理拦截规则和 Pipeline 配置，无需重启 |
| **插件系统** | 支持通过外部二进制/动态库加载适配器和 Pipeline 阶段 |
| **录制/回放** | 请求录制 + Mock 回放，用于测试和 Demo |
