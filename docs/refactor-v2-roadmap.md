# mitm-proxy V2 重构方案 — JS 脚本化 + 配置自管理

> 创建时间：2026-06-30  
> 最后更新：2026-06-30  
> 状态：✅ 全部 Sprint 完成  
> 前置：V1 Sprint 1~5 已完成（adapter 插件化 + module 重命名）

---

## 一、重构目标

将 V1 的"Go 编译期 adapter 插件"架构演进为**"JS 运行时脚本化"架构**：

1. **业务逻辑用 JS 脚本承载**，与规则声明内聚在同一个 `.rules` 文件中
2. **业务配置由脚本自管理**（`@config` 段），框架 `config.yaml` 不含任何业务字段
3. **Web UI 支持 JS 脚本在线编辑 + 调试**
4. **删除 Go 编译期 adapter 体系**（`adapter/`、`pipeline/` 包），简化架构
5. **main 包代码抽取到 `core/` 子包**，形成可被外部 import 的框架

### 设计原则

| 原则 | V2 体现 |
|------|---------|
| **SRP** | `config.yaml` 只管框架；业务配置在 `@config` 段由脚本自管理 |
| **OCP** | 新增业务逻辑 = 新建/修改 `.rules` 文件，框架零改动 |
| **DIP** | 框架定义 `ctx` API 契约，JS 脚本实现具体转换逻辑 |
| **高内聚** | 一个 `.rules` 文件 = 规则声明 + 配置 + JS 逻辑 = 完整功能单元 |
| **低耦合** | 框架不解析 `@config` 内容，原样透传给 `ctx.config` |
| **配置自管理** | 插件/脚本的配置存放在脚本文件内部，不依赖外部配置文件 |

---

## 二、V1 → V2 变更分析

### 2.1 需要删除的代码

| 位置 | 行数 | 原因 |
|------|------|------|
| `adapter/interfaces.go` | 16 | Go 编译期 Adapter 接口，被 JS 脚本替代 |
| `adapter/registry.go` | 52 | 适配器注册表，被 ScriptProcessor 内部管理替代 |
| `adapter/yunapi/adapter.go` | 87 | yunapi Go 适配器，逻辑迁移到 JS 脚本 |
| `adapter/yunapi/transformer.go` | 232 | 拆包/组包逻辑，迁移到 `rules/apm-dev.rules` 的 `@script` 段 |
| `adapter/yunapi/auth.go` | 41 | 认证注入逻辑，合并到 JS `onRequest` |
| `adapter/yunapi/config.go` | 39 | yunapi 配置结构体，迁移到 `@config` 段 |
| `pipeline/interfaces.go` | 44 | Pipeline 阶段接口，被 JS 的 `onRequest`/`onResponse` 替代 |
| `pipeline/pipeline.go` | 107 | Pipeline 编排器，被 `ScriptProcessor` 替代 |
| `pipeline/forwarder.go` | 72 | HTTP 转发器，合并到 `ScriptProcessor` |
| `main.go` 中 `AdapterConfig` | ~70 | 适配器配置段，不再需要 |
| `main.go` 中 `registerPipelineRoute` | ~40 | Pipeline 直连路由，不再需要 |
| `config.yaml` 中 `adapters` 段 | 全部 | 业务配置迁移到 `@config` |
| **总计** | ~800 行 | |

### 2.2 需要新增的代码

| 位置 | 预估行数 | 说明 |
|------|---------|------|
| `core/script/runtime.go` | ~200 | goja VM 池 + 沙箱 + 超时保护 |
| `core/script/api.go` | ~150 | 暴露给 JS 的 `ctx`/`console`/`fetch` API |
| `core/script/debugger.go` | ~200 | 调试执行器（分步执行 + 日志捕获 + dry-run） |
| `core/script/loader.go` | ~100 | 脚本编译 + 缓存 + 热更新 |
| `rule/parser.go` 扩展 | ~150 | 解析 `@config...@end` 和 `@script...@end` 段 |
| `rule/types.go` 扩展 | ~30 | `RuleScript` 增加 `Config`/`Scripts` 字段；新增 `RuleTypeScript` |
| `dispatch.go` 扩展 | ~50 | 新增 `processScript` 分支 |
| `webui/handler.go` 扩展 | ~100 | 调试 API `POST /api/rules/:id/debug` |
| `webui/static/app.js` 重写 | ~800 | 混合编辑器 + 调试面板 |
| `webui/static/style.css` 重写 | ~300 | 新 UI 样式 |
| `rules/apm-dev.rules` 重写 | ~80 | 添加 `@config` + `@script` 段 |
| **总计** | ~2160 行 | |

### 2.3 需要重构的代码

| 位置 | 变更 |
|------|------|
| `main.go` | 删除 adapter 初始化 + `AdapterConfig`；`config.yaml` 不含 `adapters` 段 |
| `dispatch.go` | `processPipeline` → `processScript`，不再依赖 `adapter.Registry` |
| `config.yaml` | 删除 `adapters` 和 `backend` 段 |
| `proxy/interfaces.go` | `MatchResult` 增加 `RuleTypeScript`；`Adapter` 字段语义变为 `ScriptRef` |
| `rule/engine.go` | `Match` 返回 `RuleTypeScript` 类型匹配结果 |
| `main 包` → `core/` | proxy.go / cert.go / connmgr.go / transport.go 迁移到 core 子包 |

---

## 三、目标架构

### 3.1 目录结构

```
mitm-proxy/
├── cmd/mitm-proxy/
│   └── main.go                  ← 极简入口：加载配置 → 组装 → 启动
│
├── core/                         ← 通用框架核心（可被外部 import）
│   ├── proxy/                    ← MITM 正向代理引擎
│   │   ├── proxy.go              ←   ForwardProxy 主体
│   │   ├── interfaces.go         ←   核心接口 + MatchResult + RuleType
│   │   ├── cert.go               ←   CA 证书管理
│   │   ├── connmgr.go            ←   LRU 连接管理器
│   │   └── transport.go          ←   proxyRequest / streamRequest
│   │
│   ├── rule/                     ← 规则引擎
│   │   ├── types.go              ←   Rule / RuleType / ParseError
│   │   ├── script.go             ←   RuleScript（增加 Config/Scripts 字段）
│   │   ├── parser.go             ←   解析器（扩展 @config/@script/@end）
│   │   ├── engine.go             ←   匹配引擎
│   │   ├── manager.go            ←   规则 CRUD + 热加载
│   │   └── validator.go          ←   规则校验
│   │
│   ├── script/                   ← JS 脚本运行时（NEW）
│   │   ├── runtime.go            ←   goja VM 池 + 沙箱 + 超时保护
│   │   ├── api.go                ←   JS API：ctx / console / config
│   │   ├── debugger.go           ←   调试执行（分步 + 日志捕获 + dry-run）
│   │   └── loader.go             ←   脚本编译 + 缓存
│   │
│   ├── dispatch/                 ← 处理器调度
│   │   └── dispatcher.go         ←   按 RuleType 分发（proxy/stream/script）
│   │
│   └── webui/                    ← Web UI
│       ├── handler.go            ←   REST API + 调试端点
│       ├── events.go             ←   SSE 实时日志
│       └── static/
│           ├── index.html
│           ├── app.js            ←   编辑器 + 调试面板
│           └── style.css
│
├── config.yaml                   ← 纯框架配置（不含任何业务字段）
│
├── rules/                        ← 规则脚本（一个文件 = 完整功能单元）
│   ├── apm-dev.rules             ←   含 @config + @script + 规则声明
│   └── test-env.rules            ←   纯 hosts/代理规则（无 JS）
│
└── docs/
```

### 3.2 规则脚本格式（V2 扩展）

```
# V1 语法（完全保留）
@name "脚本名称"
@priority 100
@intercept domain1.com domain2.com
IP domain1 domain2                    # hosts 映射
/regex/  http://target  [@options]    # 正则代理
domain/path  target                   # 精确路径代理

# V2 新增语法
@config                               # 脚本私有配置（YAML 格式）
key: value                            #   框架不解析，原样透传给 ctx.config
nested:                               #   支持任意嵌套
  sub_key: sub_value
@end                                  # 配置段结束标记

@script onRequest                     # 内嵌 JS 函数定义
  const body = JSON.parse(ctx.rawBody);
  ctx.backendURL = ctx.config.backend.url;
  ctx.backendRequest = { Action: body.cmd };
@end                                  # 脚本段结束标记

@script onResponse                    # 响应转换
  const resp = JSON.parse(ctx.backendResp);
  ctx.finalResp = JSON.stringify({ code: 0, data: resp });
@end

@script onRequest file://transform.js # 引用外部 JS 文件（复杂逻辑拆分）

# V2 新增规则协议
/regex/  script://self  [@options]    # 匹配后执行本脚本的 onRequest/onResponse
/regex/  script://other-script-id     # 引用其他脚本的 JS 函数
```

### 3.3 JS API 设计

```javascript
// ═══ ctx 对象（框架注入，每次请求一个新实例）═══

ctx.rawBody           // string   — 原始请求体
ctx.method            // string   — HTTP 方法
ctx.url               // string   — 完整请求 URL
ctx.path              // string   — URL path
ctx.query             // string   — URL query string
ctx.headers           // object   — 请求头（只读）

ctx.config            // object   — @config 段内容（框架透传，脚本自管理）

ctx.backendURL        // string   — [可写] 转发目标 URL
ctx.backendRequest    // object   — [可写] 转发请求体（JSON）
ctx.backendHeaders    // object   — [可写] 转发请求头
ctx.backendResp       // string   — [只读] 后端响应体（onResponse 阶段可用）
ctx.backendStatus     // number   — [只读] 后端响应状态码

ctx.finalResp         // string   — [可写] 最终返回给客户端的响应体
ctx.finalStatus       // number   — [可写] 最终响应状态码（默认 200）
ctx.finalHeaders      // object   — [可写] 最终响应头

ctx.metadata          // object   — 跨函数传递的元数据

// ═══ console 对象（日志输出到调试面板）═══

console.log(msg)      // 普通日志
console.warn(msg)     // 警告日志
console.error(msg)    // 错误日志

// ═══ 工具函数 ═══

JSON.parse(str)       // 标准 JSON 解析
JSON.stringify(obj)   // 标准 JSON 序列化
```

### 3.4 ScriptProcessor 执行流程

```
ScriptProcessor.Process(req, body, match)
    │
    ├─ 1. 从 RuleManager 获取 script（match.ScriptID）
    │     → 读取 script.Config（@config 段 YAML → map[string]any）
    │     → 读取 script.Scripts（@script 段 JS 源码）
    │
    ├─ 2. 从 VM 池获取一个 goja.Runtime
    │     → 注入 ctx 对象（rawBody / method / url / config / ...）
    │     → 注入 console 对象（日志捕获）
    │
    ├─ 3. 执行 onRequest(ctx)
    │     → 脚本设置 ctx.backendURL / ctx.backendRequest
    │     → 超时保护（默认 5s）
    │
    ├─ 4. HTTP 转发
    │     → POST ctx.backendRequest → ctx.backendURL
    │     → 结果写入 ctx.backendResp / ctx.backendStatus
    │
    ├─ 5. 执行 onResponse(ctx)（如果定义了）
    │     → 脚本设置 ctx.finalResp
    │     → 超时保护（默认 5s）
    │
    ├─ 6. 构建 HTTP Response 返回
    │     → Body = ctx.finalResp（如未设置则用 ctx.backendResp）
    │     → StatusCode = ctx.finalStatus（如未设置则用 200）
    │
    └─ 7. 归还 VM 到池
```

### 3.5 调试 API

```
POST /api/rules/:id/debug
Request:
{
  "method": "POST",
  "path": "/cgi/capi?serviceType=apm&action=delegate",
  "body": "{\"cmd\":\"DescribeMetricRecords\",\"serviceType\":\"apm\",\"regionId\":1}",
  "dry_run": true       // true=不转发后端
}

Response:
{
  "steps": [
    {
      "stage": "rule_match",
      "matched": true,
      "rule": "/\\/cgi\\/capi/ → script://self",
      "elapsed_ms": 0.05
    },
    {
      "stage": "onRequest",
      "output": {
        "backendURL": "http://127.0.0.1:16686/api/v1",
        "backendRequest": { "Action": "DescribeMetricRecords", ... }
      },
      "console_logs": ["region resolved: 1 → ap-guangzhou"],
      "elapsed_ms": 0.3
    },
    {
      "stage": "forward",
      "skipped": true,          // dry_run=true 时跳过
      "would_send_to": "http://127.0.0.1:16686/api/v1"
    },
    {
      "stage": "onResponse",
      "skipped": true
    }
  ],
  "total_elapsed_ms": 0.35
}
```

### 3.6 config.yaml（V2 目标格式）

```yaml
# mitm-proxy 框架配置 — 不含任何业务字段
server:
  listen: ":8082"

proxy:
  enable: true
  listen: ":8081"
  cert_dir: "certs"

rules:
  dir: "rules"
  hot_reload: true
  watch_interval: 2s

webui:
  enable: true

debug:
  enable: true
  buffer_size: 200
```

注意：没有 `backend`、没有 `adapters`、没有 `auth`、没有 `region_map`。

### 3.7 apm-dev.rules（V2 目标格式）

```bash
# =============================================
# APM 本地联调规则
# 包含：hosts 映射 + API 拦截 + 前端资源代理
# =============================================
@name "APM 本地开发"
@priority 100

# ═══ 脚本私有配置（框架不解析，透传给 ctx.config）═══
@config
backend:
  url: "http://127.0.0.1:16686"
  api_path: "/api/v1"
  timeout: 30
auth:
  uin: "your-uin"
  sub_account_uin: "your-sub-account-uin"
  app_id: 0
region_map:
  1: "ap-guangzhou"
  4: "ap-shanghai"
  5: "ap-hongkong"
  8: "ap-beijing"
  9: "ap-singapore"
  16: "ap-chengdu"
  17: "eu-frankfurt"
@end

# ═══ 请求转换（yunapi → TencentCloud API 3.0）═══
@script onRequest
  const { backend, auth, region_map } = ctx.config;
  const body = JSON.parse(ctx.rawBody);

  // 拆包：字段映射
  ctx.backendURL = backend.url + backend.api_path;
  ctx.backendRequest = {
    Action: body.cmd,
    ApiModule: body.serviceType,
    Region: region_map[body.regionId] || "ap-guangzhou",
  };

  // 认证注入（优先请求携带值，fallback 到配置）
  ctx.backendRequest.Uin = body.data?.ownerUin || auth.uin;
  ctx.backendRequest.SubAccountUin = body.data?.uin || auth.sub_account_uin;
  ctx.backendRequest.AppId = auth.app_id;

  // 扁平化 data 字段（保护字段不可覆盖）
  const protectedKeys = new Set(Object.keys(ctx.backendRequest));
  if (body.data) {
    for (const [k, v] of Object.entries(body.data)) {
      if (!protectedKeys.has(k)) {
        ctx.backendRequest[k] = v;
      }
    }
  }

  console.log("Action:", ctx.backendRequest.Action, "Region:", ctx.backendRequest.Region);
@end

# ═══ 响应转换（TencentCloud API 3.0 → yunapi 三层嵌套）═══
@script onResponse
  const resp = JSON.parse(ctx.backendResp);

  // 提取错误码
  let innerCode = 0;
  if (resp.Response?.Error) {
    innerCode = resp.Response.Error.Code || -1;
  }

  // 组包：yunapi 三层嵌套格式
  ctx.finalResp = JSON.stringify({
    code: 0,
    data: {
      code: innerCode,
      cgwerrorCode: 0,
      data: { Response: resp.Response || resp },
    },
    mccode: 0,
    errObj: {},
    seqId: "proxy-" + Date.now(),
  });
@end

# ═══ MITM 拦截域名 ═══
@intercept console.example.com
@intercept console-hc.example.com
@intercept cdn.example.com

# ═══ Hosts 映射 ═══
10.0.0.1 console.example.com console-hc.example.com
10.0.0.2 cdn.example.com

# ═══ API 拦截（走 JS 脚本拆包/组包）═══
/\/cgi\/capi\?.*action=delegate&.*serviceType=apm/  script://self
/\/_api\/apm\//  script://self  @cors

# ═══ 前端资源代理（CDN → 本地 webpack-dev-server）═══
/\/qcloud\/tea\/app\/(tapm[\w-]*)(\.(zh|en|jp|ko|dev))?(\.\w{10})?\.(js|css)(\.map)?$/  http://127.0.0.1:8322/$1.$5$6
/\/qcloud\/tea\/app\/(.+\/)?__webpack_hmr$/  http://127.0.0.1:8322/__webpack_hmr  @stream
/\/qcloud\/tea\/app\/(.+\.)?(\w+)\.(hot-update\.(js|json))$/  http://127.0.0.1:8322/$1$2.$3
```

---

## 四、实施 Sprint 规划

### Sprint V2.1：JS 运行时 + Parser 扩展 + ScriptProcessor ✅ 已完成

> **目标**：规则脚本支持 `@config`/`@script`/`script://` 语法，JS 脚本可以处理请求。yunapi 业务逻辑从 Go adapter 迁移到 JS 脚本。编译通过 + 功能等价验证。
>
> **详细进度**: 见 [docs/sprint6-v2.1-progress.md](sprint6-v2.1-progress.md)

#### 前置依赖

- `go get github.com/nicohman/goja`（纯 Go 的 ES5.1+ JS 引擎，无 CGO）

#### 任务列表

| # | 任务 | 说明 | 预估行数 |
|---|------|------|---------|
| 1.1 | 创建 `core/script/runtime.go` | goja VM 池（sync.Pool）+ 沙箱配置 + 脚本执行超时保护（context cancel） | ~200 |
| 1.2 | 创建 `core/script/api.go` | 定义 `ScriptContext` Go 结构体 → JS `ctx` 对象映射；注入 `console.log/warn/error` | ~150 |
| 1.3 | 创建 `core/script/loader.go` | 脚本编译（`goja.Compile`）+ 编译结果缓存（脚本内容 hash → `*goja.Program`） | ~100 |
| 1.4 | 扩展 `rule/types.go` | `RuleScript` 增加 `Config map[string]any`、`Scripts map[string]string`（name→JS源码）；新增 `RuleTypeScript` | ~30 |
| 1.5 | 扩展 `rule/parser.go` | 解析 `@config...@end`（YAML→map）、`@script name...@end`（提取 JS 源码）、`@script name file://path`（读外部文件）、`script://` 协议 | ~150 |
| 1.6 | 扩展 `dispatch.go` | 新增 `processScript` 方法：获取脚本 → VM 执行 onRequest → HTTP 转发 → VM 执行 onResponse → 构建响应 | ~100 |
| 1.7 | 扩展 `proxy/interfaces.go` | `MatchResult` 新增 `ScriptRef string`；`RuleType` 新增 `RuleTypeScript` | ~10 |
| 1.8 | 扩展 `rule/engine.go` | `matchRegex` 支持 `script://` target → 返回 `RuleTypeScript` + `ScriptRef` | ~20 |
| 1.9 | 重写 `rules/apm-dev.rules` | 添加 `@config` + `@script onRequest/onResponse` + 将 `pipeline://yunapi` 改为 `script://self` | ~80 |
| 1.10 | 修改 `main.go` | 删除 `AdapterConfig`/adapter 注册逻辑/`registerPipelineRoute`/`/cgi/capi` 路由；简化 `Config` 结构体 | 删除 ~120 |
| 1.11 | 修改 `config.yaml` | 删除 `backend` 和 `adapters` 段 | 删除 ~30 |
| 1.12 | 删除 `adapter/` 整个目录 | 不再需要 Go 编译期 adapter | 删除 ~467 |
| 1.13 | 删除 `pipeline/` 整个目录 | 被 ScriptProcessor 替代 | 删除 ~223 |
| 1.14 | 编译通过 + 功能等价验证 | `go build ./...` + `go vet ./...` + 手动验证 apm-dev.rules 的 JS 脚本能正确拆包/组包 | — |

#### 验收标准

| # | 验收项 | 验证方式 |
|---|--------|---------|
| ✅ | `go build ./...` 编译通过 | 命令行 |
| ✅ | `go vet ./...` 无警告 | 命令行 |
| ✅ | `adapter/` 目录已删除 | 文件系统 |
| ✅ | `pipeline/` 目录已删除 | 文件系统 |
| ✅ | `config.yaml` 不含 `adapters`/`backend` 段 | 文件内容 |
| ✅ | `apm-dev.rules` 包含 `@config` + `@script` 段 | 文件内容 |
| ✅ | `@config` 段被正确解析为 `map[string]any` | Parser 单元测试 |
| ✅ | `@script` 段被正确解析为 JS 源码字符串 | Parser 单元测试 |
| ✅ | JS `onRequest` 正确执行拆包逻辑 | 调试验证 |
| ✅ | JS `onResponse` 正确执行组包逻辑 | 调试验证 |
| ✅ | `script://self` 规则正确匹配并调用 JS | 端到端验证 |
| ✅ | 现有 hosts/proxy/stream 规则不受影响 | 回归验证 |

#### 风险

| 风险 | 缓解 |
|------|------|
| goja 不支持某些 ES6+ 语法 | 限制为 ES5.1 + 部分 ES6（箭头函数、const/let、模板字符串）；goja 支持这些 |
| JS 执行超时导致请求阻塞 | `context.WithTimeout` + goroutine panic recover |
| VM 池内存占用 | 初始池大小 4，最大 16；空闲 VM 超时回收 |

---

### Sprint V2.2：调试面板 + Web UI 增强 ✅ 已完成

> **目标**：Web UI 支持 JS 脚本在线编辑和调试（手动输入请求 → 分步执行 → 查看结果）。
>
> **详细进展**: 语法高亮混合编辑器 + Debug Tab（分步执行 + console 输出）

#### 任务列表

| # | 任务 | 说明 | 预估行数 |
|---|------|------|---------|
| 2.1 | 创建 `core/script/debugger.go` | 调试执行器：接收请求参数 → 分步执行 onRequest/onResponse → 捕获 console.log → 返回分步结果 | ~200 |
| 2.2 | 扩展 `webui/handler.go` | `POST /api/rules/:id/debug` 端点 + `GET /api/rules/:id/content` 返回含 JS 的完整文件内容 | ~100 |
| 2.3 | 重写 `webui/static/app.js` | 混合编辑器：@config 段 YAML 高亮 + @script 段 JS 高亮 + 规则段 Rules 高亮；调试面板 UI | ~800 |
| 2.4 | 重写 `webui/static/style.css` | 新 UI 样式：深色主题、编辑器区域、调试面板、控制台输出 | ~300 |
| 2.5 | 重写 `webui/static/index.html` | 页面布局：左侧脚本列表 + 右侧编辑器/调试 + 底部实时日志 | ~50 |
| 2.6 | 扩展 `webui/handler.go` | 调试时的 console.log 通过 SSE 推送到前端调试面板 | ~50 |

#### 验收标准

| # | 验收项 | 验证方式 |
|---|--------|---------|
| ✅ | Web UI 编辑器显示 .rules 文件内容 | 浏览器访问 /ui/ |
| ✅ | @config 段 YAML 高亮 | 视觉验证 |
| ✅ | @script 段 JS 高亮 | 视觉验证 |
| ✅ | 调试面板可手动输入请求 | UI 操作 |
| ✅ | 点"执行"后显示分步结果 | UI 操作 |
| ✅ | dry_run 模式不转发后端 | API 验证 |
| ✅ | console.log 输出显示在控制台区域 | UI 验证 |
| ✅ | 保存后规则 2s 内热加载生效 | 功能验证 |

---

### Sprint V2.3：main 包抽取 + core/ 形成框架 ✅ 已完成

> **目标**：将 main 包中的通用代码抽取到 `core/` 子包，形成可被外部 import 的框架结构。
>
> **详细进展**: cmd/mitm-proxy/ + core/proxy/ 成立，外部可 import

#### 任务列表

| # | 任务 | 说明 |
|---|------|------|
| 3.1 | 迁移 `proxy.go` → `core/proxy/proxy.go` | ForwardProxy 主体 |
| 3.2 | 迁移 `cert.go` → `core/proxy/cert.go` | CA 证书管理 |
| 3.3 | 迁移 `connmgr.go` → `core/proxy/connmgr.go` | 连接管理器 |
| 3.4 | 迁移 `transport.go` → `core/proxy/transport.go` | 通用 HTTP 转发 |
| 3.5 | 迁移 `dispatch.go` → `core/dispatch/dispatcher.go` | 处理器调度 |
| 3.6 | 迁移 `debug.go` → `core/debug/collector.go` | 诊断收集器 |
| 3.7 | 迁移 `mcp.go` → `core/debug/mcp.go` | MCP Server |
| 3.8 | 迁移 `ruleapi.go` → `core/webui/ruleapi.go` | 规则 REST API |
| 3.9 | 迁移 `interfaces.go` → 删除 | 类型别名不再需要，直接引用 proxy 包 |
| 3.10 | 创建 `cmd/mitm-proxy/main.go` | 极简启动入口 |
| 3.11 | 删除根目录 main 包文件 | proxy.go / dispatch.go / ... 已迁移 |
| 3.12 | 更新所有 import 路径 | `mitm-proxy/core/...` |
| 3.13 | 编译 + 回归验证 | `go build ./...` + `go vet ./...` |

#### 验收标准

| # | 验收项 | 验证方式 |
|---|--------|---------|
| ✅ | 根目录无 main 包 .go 文件（除 cmd/） | 文件系统 |
| ✅ | `core/proxy/` 不依赖 `core/rule/`、`core/script/` | `go list -deps` |
| ✅ | `core/rule/` 不依赖 `core/proxy/`（除 types） | `go list -deps` |
| ✅ | `core/script/` 不依赖 `core/proxy/` | `go list -deps` |
| ✅ | 外部项目可 `import "mitm-proxy/core/proxy"` | 模拟验证 |
| ✅ | 所有功能正常 | 端到端回归 |

---

### Sprint V2.4：文档 + 清理 + 开源准备 ✅ 已完成

> **目标**：完善文档，清理敏感信息，准备开源发布。
>
> **详细进展**: README 更新、JS API 参考文档、示例规则、Makefile/goreleaser 更新、root 旧文件清理、rules/ gitignored

#### 任务列表

| # | 任务 |
|---|------|
| 4.1 | 更新 `README.md`：V2 架构说明 + 快速开始 + JS 脚本语法参考 + 调试指南 |
| 4.2 | 更新 `docs/refactor-roadmap.md`：标记 V1 完成，链接到本文档 |
| 4.3 | 创建 `docs/script-api-reference.md`：JS API 完整参考 |
| 4.4 | 创建 `examples/` 目录：示例规则脚本 |
| 4.5 | 敏感信息审计：扫描源码中的 IP/UIN/域名 |
| 4.6 | 更新 `Makefile` 和 `.goreleaser.yaml` |
| 4.7 | 性能基准测试：JS 脚本执行 < 5ms（单次 onRequest） |

---

## 五、代码量变化预估

| 维度 | V1 当前 | V2 目标 | 变化 |
|------|---------|---------|------|
| main 包文件 | 10 个 (~3200 行) | 1 个 (~30 行) | -3170 行 |
| adapter/ | 6 个 (~467 行) | 0 | -467 行 |
| pipeline/ | 3 个 (~223 行) | 0 | -223 行 |
| core/script/ (NEW) | 0 | 4 个 (~650 行) | +650 行 |
| rule/ 扩展 | 6 个 (~880 行) | 6 个 (~1060 行) | +180 行 |
| webui/ 扩展 | 4 个 (~420 行) | 5 个 (~1670 行) | +1250 行 |
| **总计** | ~5190 行 Go | ~3610 行 Go + ~1150 行 JS/CSS | 净减 ~1580 行 Go |

---

## 六、风险评估

| 风险 | 影响 | 缓解 |
|------|------|------|
| goja 依赖增加二进制大小 | ~10MB | 可接受，单一二进制仍是优势 |
| JS 性能不如 Go adapter | API 代理延迟增加 1~3ms | 本工具是开发代理，QPS<100，不敏感 |
| ES5.1 语法限制 | 无法使用 async/await | 当前场景不需要异步；后续可用 goja 的 Promise polyfill |
| 大型 @script 段影响 .rules 可读性 | 单文件过长 | 支持 `@script name file://xxx.js` 外部引用 |
| 调试面板增加 WebUI 复杂度 | 前端代码量增长 | 使用 vanilla JS + CSS，不引入框架 |
| Sprint V2.3 main 包抽取改动大 | 可能引入回归 | 纯移动文件 + 改 import，逻辑不变 |

---

## 七、设计决策记录

| # | 决策点 | 选择 | 被否决方案 | 理由 |
|---|--------|------|-----------|------|
| 1 | 业务逻辑承载方式 | JS 脚本（运行时） | Go adapter（编译时） | 热更新 + 低门槛 + Web UI 可编辑 |
| 2 | 配置管理方式 | `@config` 段内嵌在 .rules 文件 | 独立 `plugin.yaml` 文件 | 高内聚：一个文件 = 完整功能单元 |
| 3 | JS 引擎 | goja（纯 Go ES5.1+） | v8go（CGO）/ yaegi（Go 解释器） | 零 CGO + 成熟 + API 简单 |
| 4 | OTel Collector 模式 | 否决 | 多 Module + Builder | 过度工程：适配器数量少、单人维护 |
| 5 | 脚本与规则的关系 | 内聚在同一个 .rules 文件 | 独立 plugins/ 目录 | Web UI 只需管理一种文件类型 |
| 6 | `script://` vs `pipeline://` | `script://` 新协议 | 复用 `pipeline://` | 语义不同：script 是 JS 运行时，pipeline 是 Go 编译期 |
| 7 | 框架代码组织 | `core/` 子包 | 根包 main | core 可被外部 import，main 不行 |
| 8 | Web UI 编辑器 | vanilla JS + 混合高亮 | Monaco Editor / CodeMirror | 零依赖 + 单一二进制 + 够用 |

---

## 八、与 V1 的兼容性

**不向后兼容**（用户确认）：

| 变更 | V1 | V2 |
|------|----|----|
| `pipeline://yunapi` | ✅ 支持 | ❌ 删除，改用 `script://self` |
| `config.yaml` 中 `adapters` 段 | ✅ 支持 | ❌ 删除，配置在 `@config` 段 |
| `config.yaml` 中 `backend` 段 | ✅ 支持 | ❌ 删除 |
| Go adapter 编译注册 | ✅ 支持 | ❌ 删除 |
| `/pipeline/:adapter` 路由 | ✅ 支持 | ❌ 删除 |
| `/cgi/capi` 路由 | ✅ 支持 | ❌ 删除 |

迁移路径：将 `adapter/yunapi/` 的 Go 逻辑等价翻译为 `apm-dev.rules` 中的 JS `@script` 段。

---

## 九、后续演进

| 方向 | 说明 |
|------|------|
| `@script` 支持 TypeScript | goja 支持 ES5.1，可通过预编译 TS→JS 支持 |
| 脚本模板市场 | 预置常见 API 代理模板（REST→REST / gRPC→REST） |
| 脚本间共享库 | `@import "utils.js"` 引用公共函数 |
| 断点调试 | JS `debugger` 语句 + WebSocket 实时交互 |
| 请求录制/回放 | 录制真实请求 → 一键填入调试面板 |
