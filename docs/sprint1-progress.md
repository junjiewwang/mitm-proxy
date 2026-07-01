# Sprint 1 实施进展

> 创建时间：2026-06-30  
> 最后更新：2026-06-30  
> 状态：通用架构层已完成，编译通过

---

## 目标

将 yunapi-proxy 从扁平单包架构重构为**分层多包架构**，建立通用代理框架的代码骨架。
Sprint 1 只关注通用架构代码，不包含具体业务逻辑（如 yunapi 转换）。

---

## 已完成

### 1. 通用接口定义（interfaces.go）

根目录 `interfaces.go` 定义了代理核心层的所有接口：
- `InterceptMatcher` — 域名是否需要 MITM
- `UpstreamResolver` — hosts 映射解析
- `RequestMatcher` — 请求规则匹配
- `RequestProcessor` — 匹配后的请求处理
- `TraceRecorder` — 请求追踪记录
- `RuleType` 枚举 + `MatchResult` 结构体

### 2. proxy/ 包 — 代理核心层接口（mirror）

`proxy/interfaces.go` — 与根目录接口保持一致，作为独立包的接口定义。
后续物理迁移 ForwardProxy 时，根目录的 interfaces.go 将删除。

### 3. rule/ 包 — 通用规则引擎

| 文件 | 职责 |
|------|------|
| `rule/types.go` | Rule 结构体、RuleType 枚举、ParseError |
| `rule/script.go` | RuleScript（对应 .rules 文件）、ScriptStats |
| `rule/parser.go` | 规则文件解析器（Whistle 兼容扩展语法） |
| `rule/engine.go` | RuleEngine（ShouldIntercept + ResolveUpstream + Match） |

### 4. pipeline/ 包 — 通用 Pipeline 引擎

| 文件 | 职责 |
|------|------|
| `pipeline/interfaces.go` | PipelineContext + 四阶段接口 |
| `pipeline/pipeline.go` | Pipeline 编排器（链式执行） |
| `pipeline/forwarder.go` | HTTPForwarder 默认转发器 |

### 5. processor/ 包 — 通用处理器

| 文件 | 职责 |
|------|------|
| `processor/registry.go` | 处理器注册表（RuleType → Processor） |
| `processor/helpers.go` | 公共工具函数 |
| `processor/proxy.go` | ProxyProcessor（URL 重写 → 反向代理） |
| `processor/pipeline.go` | PipelineProcessor（调用适配器 Pipeline） |
| `processor/stream.go` | StreamProcessor（SSE/chunked 流式代理） |

### 6. adapter/ 包 — 通用适配器接口

| 文件 | 职责 |
|------|------|
| `adapter/interfaces.go` | Adapter 接口定义 |
| `adapter/registry.go` | 适配器注册表 |

### 7. ForwardProxy 改造（proxy.go）

- 新增 Option 模式依赖注入：`WithInterceptMatcher`/`WithUpstreamResolver`/`WithRequestMatcher`/`WithRequestProcessor`
- `shouldIntercept()` → 优先委托给 InterceptMatcher，回退到配置
- `getUpstreamAddr()` → 优先委托给 UpstreamResolver，回退到配置
- MITM worker → 优先使用 RequestMatcher + RequestProcessor，回退到原有硬编码逻辑

### 8. 规则脚本文件

- `rules/apm-dev.rules` — APM 本地开发规则（声明式，将来由 RuleEngine 加载）

---

## 架构设计原则

1. **通用代码 vs 业务代码分离**：`proxy/`、`rule/`、`pipeline/`、`processor/`、`adapter/` 均为通用架构，不含任何 yunapi 业务逻辑
2. **渐进式改造**：ForwardProxy 通过 Option 注入接口，接口为 nil 时回退到原有逻辑，保证向后兼容
3. **yunapi 作为配置/插件**：yunapi 的拆包/认证/组包逻辑保留在 handler.go，后续通过实现 `adapter.Adapter` 接口注册为插件
4. **编译隔离**：各包之间无循环依赖，`go build ./...` + `go vet ./...` 通过

---

## 依赖关系

```
main (根目录)
  ├── handler.go (yunapi 业务，保持不变)
  ├── proxy.go (ForwardProxy，持有接口)
  ├── interfaces.go (通用接口定义)
  ├── debug.go / mcp.go (诊断)
  │
  ├── rule/       (不依赖 proxy/, processor/, adapter/)
  ├── pipeline/   (不依赖 adapter/, rule/)
  ├── processor/  (依赖 proxy/interfaces, pipeline/)
  └── adapter/    (依赖 pipeline/)
```

---

## 待完成（后续 Sprint）

1. **main.go 组装**：加载 .rules → 创建 RuleEngine → 创建 Processors → 注入到 ForwardProxy
2. **物理迁移文件**：proxy.go/cert.go/connmgr.go → proxy/ 包
3. **handler.go → adapter/yunapi/**：将 yunapi 业务逻辑迁移为适配器插件
4. **rule/manager.go**：规则管理器（CRUD + 热加载）
5. **Web UI**：管理面板

---

## 遗留问题

1. `proxy/interfaces.go` 与根目录 `interfaces.go` 有重复定义（物理迁移后消除）
2. `processor/` 包中的 `Adapter` 接口与 `adapter/` 包中的接口相同（依赖方向问题，需在物理迁移时统一）
3. debug.go 中的 `recordTraceFWD`/`recordTraceIntercept` 仍硬编码 `console.example.com`（Sprint 4 解决）
