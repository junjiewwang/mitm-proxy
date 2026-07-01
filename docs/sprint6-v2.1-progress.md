# Sprint V2.1：JS 运行时 + Parser 扩展 + ScriptProcessor

> 创建时间：2026-06-30  
> 状态：✅ 全部完成  
> 前置：V1 Sprint 1~5（adapter 插件化 + module 重命名）

---

## 完成的任务

| # | 任务 | 状态 |
|---|------|------|
| 1.1 | 创建 `core/script/runtime.go` — goja VM 池 + 沙箱 + 超时保护 | ✅ |
| 1.2 | 创建 `core/script/api.go` — ctx/console/JSON 注入到 goja VM | ✅ |
| 1.3 | 创建 `core/script/loader.go` — 脚本编译 + 内容哈希缓存 | ✅ |
| 1.4 | 扩展 `rule/types.go` + `rule/script.go` — Config/Scripts/RuleTypeScript | ✅ |
| 1.5 | 扩展 `rule/parser.go` — @config...@end / @script...@end / script:// | ✅ |
| 1.6 | 扩展 `rule/engine.go` — RuleTypeScript 匹配 | ✅ |
| 1.7 | 扩展 `proxy/interfaces.go` — ScriptRef/RuleTypeScript | ✅ |
| 1.8 | 重写 `dispatch.go` — processScript + ScriptAccessor + executePhase | ✅ |
| 1.9 | 简化 `main.go` — 删除 adapter 注册/AdapterConfig/registerPipelineRoute | ✅ |
| 1.10 | 简化 `config.yaml` — 删除 backend/adapters 段 | ✅ |
| 1.11 | 重写 `rules/apm-dev.rules` — @config + @script onRequest/onResponse | ✅ |
| 1.12 | 删除 `adapter/` 目录（~467 行） | ✅ |
| 1.13 | 删除 `pipeline/` 目录（~223 行） | ✅ |
| 1.14 | 删除 `processor/pipeline.go`（引用已删除的 pipeline 包） | ✅ |
| 1.15 | 更新 `debug.go`/`mcp.go` — 移除 cfg.Backend 引用 | ✅ |
| 1.16 | 更新 `interfaces.go` — 添加 RuleTypeScript 别名 | ✅ |
| 1.17 | go mod tidy + go build ./... + go vet ./... | ✅ |
| 1.18 | 添加 `rule/parser_v2_test.go` — 新语法解析测试 | ✅ |

---

## 验收标准

| # | 验收项 | 结果 |
|---|--------|------|
| ✅ | go build ./... 编译通过 | PASS |
| ✅ | go vet ./... 无警告 | PASS |
| ✅ | adapter/ 目录已删除 | PASS |
| ✅ | pipeline/ 目录已删除 | PASS |
| ✅ | config.yaml 不含 adapters/backend 段 | PASS |
| ✅ | apm-dev.rules 包含 @config + @script 段 | PASS |
| ✅ | @config 段解析为 map[string]any | 测试通过 |
| ✅ | @script 段解析为 JS 源码字符串 | 测试通过 |
| ✅ | script://self 规则正确匹配 | 测试通过 |
| ✅ | 现有 hosts/proxy/stream 规则不受影响 | 编译通过 |

---

## 代码变更

| 操作 | 文件 | 行数 |
|------|------|------|
| 新增 | `core/script/runtime.go` | ~160 |
| 新增 | `core/script/api.go` | ~220 |
| 新增 | `core/script/loader.go` | ~95 |
| 修改 | `rule/types.go` | +2 |
| 修改 | `rule/script.go` | +6 |
| 修改 | `rule/parser.go` | +90 |
| 修改 | `rule/engine.go` | +6 |
| 修改 | `proxy/interfaces.go` | +3 |
| 修改 | `dispatch.go` | 重写 (169→250) |
| 修改 | `main.go` | 删除 ~120 行 |
| 修改 | `config.yaml` | 删除 ~30 行 |
| 修改 | `debug.go` | -1 |
| 修改 | `mcp.go` | -1 |
| 修改 | `interfaces.go` | +1 |
| 修改 | `go.mod` | +4 deps |
| 删除 | `adapter/` (6 文件) | -467 |
| 删除 | `pipeline/` (3 文件) | -223 |
| 删除 | `processor/pipeline.go` | -1 |
| 新增 | `rules/apm-dev.rules` | 重写 (25→94) |
| 新增 | `rule/parser_v2_test.go` | ~95 |

**净变更：~+300 行 Go，净删除 ~690 行 Go**

---

## 架构变化

```
V1: main → adapter.Registry → yunapi.Adapter → Pipeline → Go Transformer/Auth/Forwarder
V2: main → ScriptRuntime + ScriptLoader → rule.Parser → JS @script onRequest/onResponse
```

---

## 技术要点

- **goja**: 纯 Go ES5.1+ 引擎，零 CGO，VM 池大小 4-16
- **两阶段执行**: onRequest（acquire → setup → execute → sync → release）→ HTTP forward → onResponse（同上）
- **ScriptAccessor 接口**: dispatch.go 定义，rule.Manager 已有 Get(id) 方法自动实现
- **JSON 处理**: 使用 goja 内置 JSON.parse/JSON.stringify，不覆盖

---

## 遗留问题

1. 需要端到端功能验证（实际启动代理，发送真实请求）
2. processor/ 目录残留（4 文件，死代码），可在后续 Sprint 清理
3. goja 升级 Go 到 1.25（主因 goja 依赖），需确认 CI 环境

---

## V2.1.1 补丁：预编译缓存优化

> 时间：2026-06-30 | 状态：✅ 完成

### 变更

| 文件 | 变更 |
|------|------|
| `rule/script.go` | +`CompiledPrograms map[string]*goja.Program` |
| `rule/manager.go` | +`ScriptLoader` 配置项、`preCompileScripts` 方法、7 处调用点 |
| `dispatch.go` | 移除 `scriptLoader` 字段/参数、使用 `rs.CompiledPrograms[...]` |
| `main.go` | 传递 `scriptLoader` 到 `ManagerConfig` |
| `rule/parser_v2_test.go` | +`TestPreCompileScripts`、`TestManagerPreCompile` |

### 效果

```
优化前:  每请求 → loader.Compile(hash lookup + 首次编译) → VM execute
优化后:  加载时 → loader.CompileMulti(所有@script) → 存入 CompiledPrograms
         每请求 → rs.CompiledPrograms["onRequest"](O(1) map lookup) → VM execute

消除: 每请求的 hash 计算 + 缓存查询 + 条件编译分支
新增: 加载/热更时的批量预编译（一次性, ~0.5ms 每个脚本）
```

---

## V2.1.2 补丁：Bug 修复

> 时间：2026-06-30 | 状态：✅ 完成

### Bug #1: `backendResp` 未注入 JS ctx (CRITICAL)

| 项目 | 说明 |
|------|------|
| **症状** | onResponse 中 `JSON.parse(ctx.backendResp)` = undefined, 执行失败 |
| **根因** | `buildCtxObject` 只注入了 `backendStatus`，遗漏了 `backendResp` |
| **修复** | `core/script/api.go:214` 添加 `obj.Set("backendResp", string(sc.BackendResp))` |
| **验证** | 新增 `core/script/api_test.go` `TestBackendRespInjection` — PASS |

### Bug #2: auth 注入读取层级错误

| 项目 | 说明 |
|------|------|
| **症状** | uin/ownerUin 始终回退到 config，请求中携带的真实值被忽略 |
| **根因** | JS 从 `body.data.ownerUin`（嵌套层）读取，Go 从 `parsed["ownerUin"]`（顶层）读取 |
| **修复** | `rules/apm-dev.rules:53-54` 改为 `body.ownerUin \|\| cfg.auth.uin` |

### 变更文件

| 文件 | 变更 |
|------|------|
| `core/script/api.go` | +1 行：`obj.Set("backendResp", ...)` |
| `rules/apm-dev.rules` | ±2 行：auth 注入层级修正 |
| `core/script/api_test.go` | +95 行：backendResp 注入 + onResponse 流程测试 |

---

## V2.2 实施：调试面板 + Web UI 增强

> 时间：2026-06-30 | 状态：✅ 完成

### 后端变更

| 文件 | 变更 | 行数 |
|------|------|------|
| `core/script/debugger.go` | NEW: 调试执行器（分步执行+日志捕获） | ~195 |
| `webui/webui.go` | +`POST /api/rules/:id/debug` 端点 | +55 |
| `main.go` | 创建 Debugger 实例传递到 WebUI | +2 |

### Debug API

```
POST /api/rules/:id/debug
{ "method":"POST", "path":"/cgi/capi?...", "body":"{...}", "dry_run":true }

Response:
{
  "steps": [
    { "stage":"rule_match", "matched":true },
    { "stage":"onRequest", "output":{backendURL,backendRequest}, "console_logs":[...] },
    { "stage":"forward", "skipped":true, "output":{would_send_to} },
    { "stage":"onResponse", "output":{finalStatus,finalRespPreview} }
  ],
  "total_elapsed_ms": 0.8
}
```

### 前端变更

| 文件 | 变更 |
|------|------|
| `webui/static/index.html` | 新增 Debug Tab（脚本选择+请求表单+结果面板） |
| `webui/static/style.css` | +debug面板、语法高亮、行号着色样式 ~50行 |
| `webui/static/app.js` | +混合语法高亮 + debug交互 + 分步结果渲染 |

### 前端功能

- **语法高亮**: @config行黄色, @script行蓝色, 注释灰色, hosts绿色, regex蓝色, directives紫色
- **行号着色**: @config段内行号黄色, @script段内行号蓝色
- **Debug面板**: 脚本选择器 + method/path/body表单 + Dry Run开关 + 分步结果
- **Console输出**: console.log以颜色编码展示在步骤详情中
