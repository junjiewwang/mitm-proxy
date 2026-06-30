# Sprint 5：适配器插件化 + Module 重命名

> 创建时间：2026-06-30  
> 状态：✅ 全部完成

---

## 目标

将残留在 main 包中的 yunapi 业务代码完全迁移到 `adapter/yunapi/` 包，实现适配器插件化；
删除 proxy.go 中的硬编码 fallback 路径；module 名从 `yunapi-proxy` 改为 `mitm-proxy`。

---

## 完成任务

| # | 任务 | 状态 | 说明 |
|---|------|------|------|
| 5.1 | 将 handler.go 业务代码迁移到 adapter/yunapi/ | ✅ | 创建 config.go / transformer.go / auth.go / adapter.go |
| 5.2 | dispatch.go 按 match.Adapter 查找适配器 | ✅ | 使用 adapter.Registry 替代直接依赖 handler |
| 5.3 | 删除 proxy.go 硬编码 fallback | ✅ | 删除 classifyRequest / processIntercept / processDirectAPI（~113行） |
| 5.4 | MockAuth/RegionMap 配置迁移 | ✅ | 从顶层 Config 迁移到 adapters.yunapi 配置段 |
| 5.5 | main.go 路由去业务化 | ✅ | /cgi/capi 改为通过 Registry 查找适配器；新增 /pipeline/:adapter 通用入口 |
| 5.6 | go.mod module 重命名 | ✅ | yunapi-proxy → mitm-proxy，所有 import 同步更新 |
| 5.7 | Sprint 5 文档 | ✅ | 本文档 |

---

## 变更清单

### 新增文件

| 文件 | 说明 |
|------|------|
| `adapter/yunapi/config.go` | yunapi 适配器配置结构体（BackendConfig, AuthConfig） |
| `adapter/yunapi/transformer.go` | RequestTransformer（拆包）+ ResponseTransformer（组包） |
| `adapter/yunapi/auth.go` | AuthInjector（Uin/SubAccountUin/AppId 注入） |
| `adapter/yunapi/adapter.go` | 适配器入口，组装 Pipeline 各阶段，实现 Adapter 接口 |

### 删除文件

| 文件 | 说明 |
|------|------|
| `handler.go` | 全部业务逻辑已迁移到 adapter/yunapi/（~397行） |

### 修改文件

| 文件 | 变更 |
|------|------|
| `dispatch.go` | 移除 `*handler` 字段 → 改用 `*adapter.Registry`；processPipeline 通过 Registry.Get 查找适配器 |
| `proxy.go` | 移除 `handler` 字段和 `NewForwardProxy` 的 handler 参数；删除 classifyRequest/processIntercept/processDirectAPI |
| `main.go` | 移除 handler/MockAuth/RegionMap；新增 AdapterConfig 结构体 + adapter.Registry 初始化；路由通过 Registry |
| `mcp.go` | 移除 `handler` 字段 |
| `config.yaml` | mock_auth/region_map → adapters.yunapi 配置段 |
| `go.mod` | module yunapi-proxy → module mitm-proxy |
| 所有 .go 文件 | import 路径 yunapi-proxy/ → mitm-proxy/ |
| `examples/pipeline/config.yaml` | mock_auth/region_map → adapters.yunapi |

---

## 架构变化

### Before (Sprint 4)

```
main.go
  ├─ Config { MockAuth, RegionMap }     ← 业务配置混在顶层
  ├─ handler { processRequest() }       ← 业务逻辑在 main 包
  ├─ dispatch.go → handler.processRequest()  ← 直接耦合
  └─ proxy.go
       ├─ classifyRequest()             ← 硬编码 apm 判断
       ├─ processIntercept() → handler  ← 硬编码 fallback
       └─ processDirectAPI() → handler  ← 硬编码 fallback
```

### After (Sprint 5)

```
main.go
  ├─ Config { Adapters map[string]AdapterConfig }  ← 适配器配置隔离
  ├─ adapter.Registry                               ← 适配器注册表
  ├─ dispatch.go → registry.Get(match.Adapter)      ← 按名称查找
  └─ proxy.go
       └─ requestMatcher.Match() → requestProcessor.Process()  ← 纯接口驱动，无 fallback

adapter/yunapi/
  ├─ adapter.go          ← 实现 Adapter 接口
  ├─ transformer.go      ← Pipeline 拆包/组包阶段
  ├─ auth.go             ← Pipeline 认证注入阶段
  └─ config.go           ← 适配器独立配置
```

---

## 验证结果

| 验证项 | 结果 |
|--------|------|
| `go build ./...` | ✅ 编译通过 |
| `go vet ./...` | ✅ 无警告 |
| 敏感信息扫描 | ✅ 源码中无业务域名/账号 |
| handler.go 已删除 | ✅ |
| proxy.go 无 classifyRequest | ✅ |
| proxy.go 无 processIntercept | ✅ |
| proxy.go 无 processDirectAPI | ✅ |
| dispatch.go 不依赖 handler | ✅ 改用 adapter.Registry |
| main.go 无 MockAuth/RegionMap | ✅ 迁移到 adapters 配置段 |
| module 名 mitm-proxy | ✅ |

---

## 遗留项

| # | 项目 | 说明 |
|---|------|------|
| 1 | 性能基准测试 | 规则匹配 100 条规则 < 1ms（Sprint 4 遗留 4.9） |
| 2 | 集成测试 | 端到端测试用例（Sprint 4 遗留 4.10） |
| 3 | transport.go 清理 | `proxyRequest`/`streamRequest` 与 processor 包有重复，可考虑统一 |
| 4 | 二进制文件名 | 仓库目录名仍为 yunapi-proxy，可考虑重命名仓库 |
