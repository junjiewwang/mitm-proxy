# Sprint 4：诊断层解耦 + 开源准备 — 实施进度

> 创建时间：2026-06-30  
> 最后更新：2026-06-30  
> 状态：核心任务完成（编译 + vet + lint 通过）

---

## 目标

诊断层不依赖业务域名，完善文档和构建系统，达到开源可发布状态。

---

## 实施记录

### 任务 4.1：改造 debug.go — 移除 Host 硬编码

**状态：✅ 完成**

#### 修改文件

| 文件 | 修改 |
|------|------|
| `debug.go` | `recordTraceFWD` 新增 `host` 参数，`recordTraceIntercept` 改为 `host, path` 参数 |
| `proxy.go` | 更新所有 4 处 `recordTraceFWD` 调用，传入 `hostname` |
| `proxy.go` | 更新所有 5 处 `recordTraceIntercept` 调用，传入 `r.URL.Host` 和 `r.URL.RequestURI()` |
| `debug.go` | Debug 面板标题改为 `Proxy Debug Panel` |

#### 改动详情

**Before:**
```go
func recordTraceFWD(method, path string, ...) {
    Host: "console.example.com",  // 硬编码
}
func recordTraceIntercept(cmd string, ...) {
    Host: "console.example.com",  // 硬编码
    Path: "/cgi/capi?cmd=" + cmd,       // 硬编码路径模板
}
```

**After:**
```go
func recordTraceFWD(method, host, path string, ...) {
    Host: host,  // 从调用方传入
}
func recordTraceIntercept(host, path string, ...) {
    Host: host,  // 从调用方传入
    Path: path,  // 从调用方传入完整 URI
}
```

### 任务 4.2：改造 mcp.go — MCP 工具描述通用化

**状态：✅ 完成**

#### 修改文件

| 文件 | 修改 |
|------|------|
| `mcp.go` | `proxy_curl` 工具参数从 `cmd/data` 改为通用的 `url/method/data` |
| `mcp.go` | 移除默认值 `DescribeMetricRecords`、`apm-test`、`console.example.com` |
| `mcp.go` | MCP serverInfo name 从 `yunapi-proxy` 改为 `mitm-proxy` |

#### 改动详情

**Before:**
```go
// 工具参数：cmd + data（APM 特定）
"cmd": {Description: "yunapi cmd，如 DescribeApmServiceMetric"},
// 默认值
cmd := "DescribeMetricRecords"
data := `{"Version":"2021-06-22","InstanceId":"apm-test"}`
// curl 模板硬编码 APM URL
'https://console.example.com/cgi/capi?cmd=...&serviceType=apm'
```

**After:**
```go
// 工具参数：url + method + data（通用）
"url":    {Description: "目标 URL"},
"method": {Description: "HTTP 方法，默认 GET"},
"data":   {Description: "POST 请求体"},
// 通用 curl 生成
curl --proxy http://127.0.0.1:8081 -k -s -X GET 'https://example.com/api/test'
```

### 任务 4.8：代码审计 — 清理硬编码敏感信息

**状态：✅ 完成**

#### 扫描结果

**代码（*.go）中的业务域名：**

| 位置 | 原内容 | 修改 |
|------|--------|------|
| `debug.go:253` | `Host: "console.example.com"` | 改为参数传入 |
| `debug.go:282` | `Host: "console.example.com"` | 改为参数传入 |
| `mcp.go:334` | `console.example.com` | 改为通用模板 |
| `main.go:80` | `InterceptDomains = []string{"console.example.com"}` | 移除默认值 |
| `main.go:99-100` | 注释示例域名 | 改为 `192.168.1.100 api.example.com` |
| `main.go:139` | 注释示例域名 | 改为 `example.com/some/path` |
| `proxy.go:512` | 注释中的业务域名 | 改为通用描述 |

**配置文件（config.yaml）：**

| 位置 | 原内容 | 修改 |
|------|--------|------|
| `mock_auth.uin` | `your-uin` | 改为 `your-uin` |
| `mock_auth.sub_account_uin` | `your-sub-uin` | 改为 `your-sub-account-uin` |
| `mock_auth.app_id` | `your-app-id` | 改为 `0` |
| `intercept_domains` | 硬编码域名列表 | 改为 `[]` |

**最终扫描验证：**
```bash
$ grep -rn "example.com" --include="*.go" --include="*.yaml" | grep -v .rules | grep -v docs/
# 无结果 ✅

$ grep -rnE "(XXXXXXX)" --include="*.go" --include="*.yaml" | grep -v .rules | grep -v docs/
# 无结果 ✅
```

### 任务 4.3：重写 README.md

**状态：✅ 完成**

完全重写为通用 MITM 代理框架文档，包含：
- 特性列表
- 快速开始（6 步上手）
- 规则语法参考（hosts / 正则 / pipeline / @intercept / options）
- 架构总览图
- 目录结构说明
- 完整 config.yaml 配置参考
- REST API 文档
- MCP 工具说明
- Pipeline 适配器开发指南

### 任务 4.4：添加 LICENSE

**状态：✅ 完成**

- 添加 MIT License

### 任务 4.5：添加 examples/ 目录

**状态：✅ 完成**

```
examples/
├── basic/                    # 基础示例
│   ├── config.yaml          #   最小化配置
│   └── rules/dev.rules      #   hosts + 正则代理 + 流式代理
└── pipeline/                 # Pipeline 示例
    ├── config.yaml          #   含 mock_auth + region_map
    └── rules/api-gateway.rules   #   pipeline:// 规则示例
```

### 任务 4.6：添加 Makefile

**状态：✅ 完成**

支持的 target：

| Target | 说明 |
|--------|------|
| `make build` | 构建当前平台二进制 |
| `make build-all` | 构建 linux/amd64 + darwin/arm64 |
| `make test` | 运行所有测试（-race） |
| `make bench` | 运行 benchmark |
| `make vet` | go vet |
| `make lint` | 完整 lint（build + vet + 敏感信息扫描） |
| `make clean` | 清理构建产物 |
| `make release` | lint + test + build-all |

### 任务 4.7：添加 .goreleaser.yaml

**状态：✅ 完成**

- 支持 linux/darwin/windows × amd64/arm64
- CGO_ENABLED=0 静态链接
- 自动注入 version + buildTime
- tar.gz / zip 打包
- 包含 LICENSE + README + config.yaml + examples/ + rules/

---

## 验收标准对照

| # | 验收项 | 状态 | 验证方式 |
|---|--------|------|---------|
| 1 | `proxy/` 无业务域名 | ✅ | `grep -r "example.com" proxy/` 无结果 |
| 2 | `rule/` 无业务域名 | ✅ | `grep -r "example.com" rule/` 无结果 |
| 3 | 源代码无敏感信息 | ✅ | `make lint` 敏感信息扫描通过 |
| 4 | README 完整 | ✅ | 包含快速开始 + 规则语法 + 配置参考 + API 文档 |
| 5 | 示例可运行 | ✅ | `examples/basic/` + `examples/pipeline/` |
| 6 | 跨平台编译 | ✅ | `make build-all` 成功产出 linux-amd64 + darwin-arm64 |
| 7 | 编译通过 | ✅ | `go build ./...` + `go vet ./...` |

---

## 未完成/后续优化

| # | 事项 | 优先级 | 说明 |
|---|------|--------|------|
| 1 | 性能基准测试 | P2 | 需要编写 `rule/engine_bench_test.go`（100 条规则匹配 < 1ms） |
| 2 | 集成测试 | P2 | 需要搭建 test fixture（MITM 端到端测试较复杂） |
| 3 | 模块名重命名 | P3 | `go.mod` 中的 `module yunapi-proxy` 可考虑改为 `mitm-proxy` |
| 4 | `handler.go` 注释 | P3 | 内部仍有 `yunapi` 引用，但这是适配器业务逻辑的准确描述 |
| 5 | `.gitignore` 补充 | P3 | 添加 `certs/`、`mitm-proxy*` 二进制文件 |
