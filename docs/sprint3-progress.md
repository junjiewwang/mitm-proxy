# Sprint 3：Web UI 管理面板 + 实时日志 — 实施进度

> 创建时间：2026-06-30  
> 最后更新：2026-06-30  
> 状态：核心代码完成（编译 + vet 通过）

---

## 目标

提供可视化管理面板，支持规则编辑、启停、实时匹配日志，达到类似 Whistle 的使用体验。

---

## 实施记录

### 任务 3.1-3.6：WebUI 包 + 前端 SPA + embed 嵌入

**状态：✅ 完成**

#### 新增文件

| 文件 | 行数 | 说明 |
|------|------|------|
| `webui/webui.go` | ~273 行 | SSE EventBroadcaster + 路由注册 + StatusProvider 接口 |
| `webui/static/index.html` | ~200 行 | SPA 入口（3 Tab：Rules / Live Logs / Status） |
| `webui/static/style.css` | ~418 行 | 暗色主题（GitHub 配色），响应式布局 |
| `webui/static/app.js` | ~701 行 | 完整前端应用（IIFE，零依赖） |

#### 架构设计

```
┌─────────────────────────────────────────┐
│          webui/webui.go                 │
│  ┌──────────────────────────────────┐   │
│  │ EventBroadcaster                 │   │
│  │  - Ring buffer (200 events)      │   │
│  │  - Multi-client SSE broadcast    │   │
│  │  - Non-blocking writes           │   │
│  │  - History replay on reconnect   │   │
│  └──────────────────────────────────┘   │
│  ┌──────────────────────────────────┐   │
│  │ StatusProvider (interface)       │   │
│  │  - Injected by main.go          │   │
│  │  - GetStatus() map[string]any   │   │
│  └──────────────────────────────────┘   │
│  ┌──────────────────────────────────┐   │
│  │ Routes                           │   │
│  │  GET /ui/*       → 静态资源       │   │
│  │  GET /api/events → SSE 实时推送   │   │
│  │  GET /api/status → 系统状态       │   │
│  │  GET /api/rules/:id/content      │   │
│  └──────────────────────────────────┘   │
│                                         │
│  //go:embed static/*                    │
│  → 单一二进制，零外部依赖              │
└─────────────────────────────────────────┘
```

#### 关键设计决策

| # | 决策 | 理由 |
|---|------|------|
| 1 | `embed.FS` 嵌入静态资源 | 单一二进制输出，部署简单 |
| 2 | 开发模式优先本地文件 | `webui/static/` 本地文件优先于 embed，支持前端热更新 |
| 3 | SSE 而非 WebSocket | 单向数据流足够，实现简单，自动重连 |
| 4 | 环形缓冲区 (200 events) | 控制内存，重连时回放最近 50 条 |
| 5 | 非阻塞广播 | 慢客户端不阻塞其他客户端 |
| 6 | StatusProvider 接口注入 | webui 包不依赖 main 包的具体实现 |

### 任务 3.7：dispatch.go 事件广播集成

**状态：✅ 完成**

#### 修改文件

| 文件 | 修改 |
|------|------|
| `dispatch.go` | 添加 `broadcaster` 字段 + `broadcastEvent()` 方法 |
| `main.go` | 创建 `EventBroadcaster` + 注入到 dispatcher + 注册 WebUI 路由 |

#### dispatch.go 改动详情

1. **添加 `webui` 导入**
2. **结构体新增 `broadcaster *webui.EventBroadcaster` 字段**
3. **构造函数签名变更**：`newProcessorDispatcher(cfg, broadcaster)` 
4. **Process 方法增加计时和事件广播**：
   - 在方法入口记录 `start := time.Now()`
   - 统一通过 `broadcastEvent()` 广播匹配事件
   - 包含：事件类型、时间戳、HTTP 方法、URL、状态码、耗时、脚本名、规则类型、目标地址、错误信息
5. **新增 `broadcastEvent()` 辅助方法**：
   - nil 安全（broadcaster 为 nil 时静默跳过）
   - 将 `RuleType` 枚举转为可读字符串

#### main.go 改动详情

1. 创建 `eventBroadcaster := webui.NewEventBroadcaster(200)`
2. 传入 dispatcher：`newProcessorDispatcher(cfg, eventBroadcaster)`
3. 注册 WebUI 路由：`webui.RegisterRoutes(ginEngine, ruleManager, eventBroadcaster, statusProvider)`
4. 实现 `proxyStatusProvider` 结构体（`webui.StatusProvider` 接口），聚合 DebugCollector + ConnManager 状态

---

## 前端功能清单

### Rules Tab（规则管理）

| 功能 | 状态 |
|------|------|
| 脚本列表展示（名称、状态、优先级、规则数、拦截域名数） | ✅ |
| 脚本启停（Toggle 开关） | ✅ |
| 单脚本重载（↻ 按钮） | ✅ |
| 脚本删除（✕ 按钮，带确认对话框） | ✅ |
| 全部重载（Reload All 按钮） | ✅ |
| 新建脚本（模态对话框） | ✅ |
| 代码编辑器（行号 + Tab 支持 + 单色主题） | ✅ |
| Ctrl+S / Cmd+S 保存 | ✅ |
| 规则校验（Validate 按钮，错误行号显示） | ✅ |
| 未保存提醒（切换脚本时弹窗确认） | ✅ |
| 列表自动刷新（10s 间隔） | ✅ |

### Live Logs Tab（实时日志）

| 功能 | 状态 |
|------|------|
| SSE 实时日志推送 | ✅ |
| 连接状态指示器（绿点/红点） | ✅ |
| 按类型过滤（Match / Forward / Tunnel / Error） | ✅ |
| 按脚本名过滤 | ✅ |
| 按 URL 文本过滤 | ✅ |
| 暂停/恢复日志流 | ✅ |
| 清空日志 | ✅ |
| 自动滚动到底部 | ✅ |
| DOM 行数限制（300 行防内存泄漏） | ✅ |
| 断开自动重连 | ✅ |

### Status Tab（系统状态）

| 功能 | 状态 |
|------|------|
| 运行时间（Uptime） | ✅ |
| 总请求数 / 总错误数 | ✅ |
| MITM 活跃连接 / 已驱逐连接 | ✅ |
| 规则脚本数 / 总规则数 / 总命中数 | ✅ |
| Top Paths 表格（路径、次数、平均耗时、最大耗时、错误数） | ✅ |
| 自动刷新（5s 间隔，仅 Status tab 激活时） | ✅ |

---

## 编译验证

```bash
$ go build ./...   # ✅ 通过
$ go vet ./...     # ✅ 通过
```

---

## 验收标准对照

| # | 验收项 | 状态 | 说明 |
|---|--------|------|------|
| 1 | Web UI 可访问 | ✅ | `http://localhost:8082/ui/` |
| 2 | 规则列表展示 | ✅ | 显示所有脚本 + 启用状态 + 命中统计 |
| 3 | 在线编辑规则 | ✅ | 编辑 → 保存 → 热加载生效 |
| 4 | 实时校验 | ✅ | Validate 按钮，错误行号显示 |
| 5 | 启停控制 | ✅ | Toggle 开关即时生效 |
| 6 | 实时日志 | ✅ | SSE 推送匹配事件 |
| 7 | 日志过滤 | ✅ | 按类型/脚本/URL 过滤 |
| 8 | 单一二进制 | ✅ | `go:embed` 嵌入所有静态资源 |

---

## Bug 修复：规则解析器正则截断 + ParseError 一刀切

> 修复时间：2026-06-30

### 问题描述

端到端验证时发现代理行为不符合预期——规则引擎完全无效，所有请求走 hardcoded fallback 逻辑。

### 根因分析

**Bug 1：正则解析器截断（`parser.go: parseRegexRule`）**

旧逻辑用 `strings.Index(line[1:], "/")` 从左到右找正则结束的 `/`，遇到正则内部的 `/`（如 `\/cgi\/capi`）就会提前截断。

```
输入: /\/cgi\/capi\?.*action=delegate&.*serviceType=apm/  pipeline://yunapi
旧解析: patternStr = "\"  （只取到第一个 /，即转义的 \/）
结果: regex 编译失败 → 5 条正则规则全部失败
```

**Bug 2：ParseError 一刀切（`engine.go: Compile`）**

`Compile()` 过滤条件 `s.Enabled && s.ParseError == nil`，当脚本有任何解析错误时整个脚本被跳过——包括成功解析的 @intercept 域名和 hosts 规则。

两个 bug 叠加：5 条正则规则解析失败 → `ParseError` 被设置 → 整个脚本被引擎排除 → @intercept 域名丢失 → MITM 不工作 → 规则引擎完全无效。

### 修复方案（第一性原理设计）

**核心洞察**：我们是规则语法的定义者，应该让语法对用户友好而非对解析器方便。

**1. 右侧定位 target 策略（`parser.go`）**

不再从左到右找 `/`，而是：
- 从右侧找 target（`http://`、`https://`、`pipeline://`），要求 target 前有空白字符
- target 左侧的最后一个 `/` 就是正则结尾分隔符
- 正则内部的 `/` 无需转义（但转义了也兼容：`\/` → `/`）

**2. 行级容错（`script.go` + `engine.go`）**

- `ParseError error` → `ParseErrors []ParseError`：记录所有解析错误，不阻止其他行
- `Compile()` 只检查 `s.Enabled`，不再因 `ParseErrors` 跳过整个脚本

### 修改文件

| 文件 | 修改 |
|------|------|
| `rule/parser.go` | 重写 `parseRegexRule` + 新增 `findTargetIndex` 辅助方法 |
| `rule/script.go` | `ParseError error` → `ParseErrors []ParseError` |
| `rule/engine.go` | `Compile()` 去掉 `s.ParseError == nil` 条件 |
| `rule/manager.go` | 适配 `ParseErrors` 字段引用 |
| `ruleapi.go` | `HasError` 改用 `len(s.ParseErrors) > 0` |

### 验证结果

```
# 修复前
[RuleManager] Loaded apm-dev (with parse errors: 5 parse errors ...)
→ rules=2（仅 hosts），正则规则全部失败

# 修复后
[RuleManager] Loaded apm-dev: name="APM 本地开发" priority=100 rules=7 intercepts=3
→ rules=7（2 hosts + 5 正则），0 个解析错误
```

API 验证：`curl http://localhost:8082/api/rules` → `has_error: false, rule_count: 7`

---

## Bug 修复：Pipeline 处理器 501 Not Implemented

> 修复时间：2026-06-30

### 问题描述

Live Logs 中所有 pipeline 匹配的请求返回 501 Not Implemented。

### 根因分析

规则解析器修复后，正则规则生效，pipeline 请求被规则引擎正确匹配，走了新的 dispatch 路径 → `processPipeline()`，但该方法是 Sprint 2 遗留的 TODO 桩，直接返回 501。

```
修复前：规则引擎坏 → 走 fallback 硬编码逻辑（classifyRequest → processIntercept）→ pipeline 正常
修复后：规则引擎好 → 走新 dispatch 路径（requestProcessor.Process → processPipeline）→ 501
```

### 修复方案

`processorDispatcher` 注入 `*handler`，`processPipeline` 调用 `handler.processRequest()` 复用完整管道（拆包→转发→组包）。

### 修改文件

| 文件 | 修改 |
|------|------|
| `dispatch.go` | 结构体添加 `handler *handler` 字段，`processPipeline` 调用 `handler.processRequest()` |
| `main.go` | `newProcessorDispatcher(cfg, h, eventBroadcaster)` 传入 handler |

### 验证结果

```
# 修复后
total_hits: 10, total_errors: 0
Backend response (268 bytes): {"Response":{"Data":{...}}}   ← pipeline 管道正常工作
Backend response (118339 bytes): {"Response":{...}}          ← 业务数据正确返回
```

---

## 遗留事项

| # | 事项 | 优先级 | 说明 |
|---|------|--------|------|
| 1 | 语法高亮 | P2 | 当前使用纯文本编辑器，后续可引入 CodeMirror/Monaco 的轻量版本 |
| 2 | 诊断层迁入 WebUI | P2 | Sprint 4 任务：`debug/` 包精简后部分功能迁入 |
| 3 | ~~端到端功能验证~~ | ~~P1~~ | ✅ 已完成，规则解析器 + pipeline 处理器均已修复并验证 |
