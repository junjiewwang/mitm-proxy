# Sprint 2 实施进展

> 创建时间：2026-06-30  
> 最后更新：2026-06-30  
> 状态：✅ 核心代码完成，编译 + vet 通过

---

## 目标

在 Sprint 1 通用架构基础上，完善规则管理能力（CRUD + 启停 + 热加载），
创建处理器调度层，实现静态资源代理和流式传输支持。

**核心约束**：所有代码为通用架构层，不包含具体业务逻辑（如 yunapi）。

---

## 已完成

### 1. rule/manager.go — RuleManager 完整实现

| 功能 | 说明 |
|------|------|
| `LoadAll()` | 加载目录下所有 `.rules` 文件，自动创建目录 |
| `StartHotReload()` | 基于 modtime 轮询检测文件变更（非 fsnotify），支持新增文件自动载入 |
| `StopHotReload()` | 优雅停止热加载 goroutine |
| `Create(name, content)` | 创建新脚本 → 写文件 → 解析 → 编译到引擎 |
| `Update(id, content)` | 更新内容 → 写文件 → 重新解析 → 保留统计 → 重编译 |
| `Delete(id)` | 删除文件 + 内存清除 → 重编译 |
| `Enable(id)` / `Disable(id)` | 启停脚本 → 立即重编译引擎 |
| `Reload(id)` / `ReloadAll()` | 从文件重新解析 → 保留启用状态和统计 |
| `Validate(content)` | 语法校验（不保存） |

**热加载机制**：
- 使用 modtime 轮询（默认 2s 间隔），避免 fsnotify 跨平台问题
- 支持**已有文件修改**和**新增文件**检测
- 编译失败不影响已有规则（单脚本解析失败跳过，不回滚）
- 写锁保护 + 批量重编译

### 2. rule/validator.go — 规则深度校验

| 校验项 | 说明 |
|--------|------|
| 正则复杂度 | 检测嵌套量词（`(a+)+` 等灾难性回溯），长度限制 500 字符 |
| 适配器引用 | 检查 `pipeline://name` 中的 name 是否在已知列表中 |
| 超时合法性 | `@timeout` 不低于 100ms |
| 目标 URL 格式 | Proxy 规则必须以 `http://` 或 `https://` 开头 |
| Hosts 格式 | IP 非空 + 至少一个域名 |
| Exact 规则 | Target 非空 |

### 3. dispatch.go — 处理器调度器

- 实现 `RequestProcessor` 接口
- 根据 `MatchResult.RuleType` 分发：
  - `RuleTypeProxy` / `RuleTypeExact` → `processProxy`（反向代理）
  - `RuleTypeProxy` + `@stream` → `processStream`（流式代理）
  - `RuleTypePipeline` → `processPipeline`（暂返回 501，待适配器注册表完善）
- CORS 预检：`OPTIONS + @cors` → 204 + CORS headers

### 4. transport.go — 通用传输层

| 函数 | 职责 |
|------|------|
| `proxyRequest()` | 通用反向代理：Header 复制 → 转发 → 读取响应体 → 返回内存 Response |
| `streamRequest()` | 流式代理：不读完 Body，保持 `io.ReadCloser` 流式传递，`ContentLength: -1` |
| `filterHopByHop()` | 复制 Header 排除 hop-by-hop（复用 `proxy.go` 的 `isHopByHop`） |

### 5. ruleapi.go — REST API

| 端点 | 方法 | 说明 |
|------|------|------|
| `/api/rules` | GET | 列表（含统计信息） |
| `/api/rules/:id` | GET | 详情 |
| `/api/rules` | POST | 创建 |
| `/api/rules/:id` | PUT | 更新 |
| `/api/rules/:id` | DELETE | 删除 |
| `/api/rules/:id/enable` | POST | 启用 |
| `/api/rules/:id/disable` | POST | 停用 |
| `/api/rules/:id/reload` | POST | 重载 |
| `/api/rules/reload-all` | POST | 全部重载 |
| `/api/rules/validate` | POST | 校验（不保存） |

### 6. main.go 改造

- 添加 `Rules` 配置段（Dir, HotReload, WatchInterval）
- 启动流程：`RuleEngine → RuleManager → LoadAll → StartHotReload → processorDispatcher → 注入 ForwardProxy → 注册 REST API`
- `defer ruleManager.StopHotReload()` 优雅关闭

### 7. interfaces.go 类型统一

- 改为使用 `proxy` 包的**类型别名**：`type MatchResult = proxy.MatchResult`
- 消除重复定义导致的类型不匹配编译错误
- 接口方法签名直接使用 `*proxy.MatchResult`

### 8. config.yaml

- 新增 `rules` 配置段：
  ```yaml
  rules:
    dir: "rules"
    hot_reload: true
    watch_interval: 2s
  ```

---

## 架构变更总结

```
main.go
  ├── loadConfig() → Config.Rules{Dir, HotReload, WatchInterval}
  │
  ├── rule.NewRuleEngine()           ← 匹配引擎
  ├── rule.NewRuleManager(cfg, eng)  ← 管理器（持有引擎引用）
  │     ├── LoadAll()                ← 加载 rules/ 目录
  │     └── StartHotReload()         ← 启动文件监听
  │
  ├── newProcessorDispatcher(cfg)    ← 处理器调度
  │     ├── processProxy()   → proxyRequest()   [transport.go]
  │     ├── processStream()  → streamRequest()  [transport.go]
  │     └── processPipeline()  → TODO(适配器注册)
  │
  ├── NewForwardProxy(cfg, handler,
  │     WithInterceptMatcher(engine),     ← 域名拦截
  │     WithUpstreamResolver(engine),     ← hosts 解析
  │     WithRequestMatcher(engine),       ← 请求匹配
  │     WithRequestProcessor(dispatcher), ← 处理器调度
  │   )
  │
  └── registerRuleAPI(gin, manager)  ← REST API [ruleapi.go]
```

---

## 文件清单

| 文件 | 状态 | 说明 |
|------|------|------|
| `rule/manager.go` | 新建 | RuleManager 完整实现 |
| `rule/validator.go` | 新建 | 规则深度校验 |
| `dispatch.go` | 新建 | 处理器调度器 |
| `transport.go` | 新建 | 通用传输层（proxy + stream） |
| `ruleapi.go` | 新建 | REST API 路由 |
| `interfaces.go` | 重写 | 类型别名统一 |
| `main.go` | 修改 | 集成 RuleManager + 调度器 |
| `config.yaml` | 修改 | 新增 rules 配置段 |

---

## 设计决策

| # | 决策 | 理由 |
|---|------|------|
| 1 | 热加载用 modtime 轮询，非 fsnotify | 跨平台一致性好，macOS/Linux 行为一致，且间隔 2s 对文件监听场景足够 |
| 2 | processorDispatcher 放在根包，非 processor 包 | 避免循环依赖（需要访问 Config 和 transport），且是 main 级组装逻辑 |
| 3 | transport.go 独立于 proxy.go | 职责分离：transport 负责"转发到目标 URL"，proxy.go 负责"MITM session 管理" |
| 4 | interfaces.go 用类型别名而非独立类型 | 别名 `type X = proxy.X` 使 `rule.RuleEngine` 的返回值可直接赋给 `main.RequestMatcher` 接口 |
| 5 | Pipeline 调度暂返回 501 | Sprint 2 不实现具体适配器，Pipeline 在适配器注册表完善后可用 |

---

## 验收状态

| # | 验收项 | 状态 | 验证方式 |
|---|--------|------|---------|
| 1 | 编译通过 | ✅ | `go build ./...` |
| 2 | vet 通过 | ✅ | `go vet ./...` |
| 3 | RuleManager CRUD | ✅ | REST API 代码完整 |
| 4 | 热加载机制 | ✅ | modtime 轮询 + 新增文件检测 |
| 5 | 处理器调度（Proxy/Stream） | ✅ | dispatch.go 分发逻辑 |
| 6 | CORS 处理 | ✅ | OPTIONS 预检 + @cors 头注入 |
| 7 | REST API | ✅ | 10 个端点全部注册 |
| 8 | config.yaml rules 段 | ✅ | 新增配置 |

---

## 待完成（后续 Sprint）

1. **rules/cls-dev.rules** — 验证多脚本隔离（与 apm-dev 共存）
2. **rules/test-env.rules** — 迁移 hosts.txt 中的测试环境映射
3. **adapter/generic/** — 通用适配器（URL 改写 + Header 注入）
4. **adapter/template/** — 模板驱动适配器（配置化 Pipeline）
5. **端到端验证** — APM 前端资源走本地 dev-server + HMR + API 拦截
6. **hosts.txt 迁移** — 全部由 rules/ 目录管理后删除 hosts.txt

---

## 遗留问题

1. **transport.go 的 `isHopByHop` 依赖**：复用 `proxy.go` 中的同名函数（同包 main），无编译问题，但物理迁移 proxy.go 到 proxy/ 包时需要同步处理
2. **Pipeline 适配器未注册**：`processPipeline()` 返回 501，需要在适配器层实现后接入
3. **Validator 未集成到 Manager**：当前 `Manager.Validate()` 只做 Parser 级校验，Validator 的深度校验（适配器引用等）需要在适配器注册表完善后接入
4. **httpClient 复用**：transport.go 中每次请求都创建新的 `http.Client`，应改为复用共享 client（性能优化，Sprint 3 处理）
