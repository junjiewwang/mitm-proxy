# 可观测性方案

## 背景

生产环境出现「运行约一天后代理异常、页面白屏、重启恢复」的问题，当前缺乏观测手段，只能重启恢复，无法定位根因。根据已有证据（历史 goroutine dump），推断为 goroutine 泄漏 / 内存持续增长。

## 方案概览

分五层，递进式覆盖：

| 层 | 手段 | 解决的问题 |
|---|---|---|
| 1 | pprof 诊断端口 | 出问题时抓 goroutine/heap 现场 |
| 2 | monitor 指标日志 | 运行时 goroutine/内存趋势，留痕 |
| 3 | 自动 goroutine dump | 阈值触发，自动留现场，无需人工 |
| 4 | /api/status 增强 | Web UI 可视化，提前看到异常 |
| 5 | Prometheus 自监控 | 长期趋势 + Grafana 告警 |

## 实施内容

### 1. pprof 诊断端口

独立端口 `:6060`（不干扰业务 gin），标准库 `net/http/pprof`：
- `GET /debug/pprof/goroutine?debug=2` — 抓所有 goroutine 栈
- `GET /debug/pprof/heap` — 内存
- `GET /debug/pprof/allocs` — 累积分配（定位泄漏对象）

### 2. monitor goroutine

每 30s 采集并打印到日志：
- `runtime.NumGoroutine()`
- `runtime.MemStats`（HeapAlloc / HeapInuse）
- ConnMgr active 连接数

### 3. 自动 dump

goroutine 数超过阈值（默认 1000）时，`runtime.Stack(buf, true)` dump 到 `/tmp/mitm-proxy-goroutine-<时间戳>.txt`。

### 4. /api/status 增强

新增字段：`goroutines`、`heap_alloc_bytes`、`heap_inuse_bytes`。

### 5. Prometheus 自监控

引入 `prometheus/client_golang`，暴露 `/metrics` 端点。

Runtime 指标（GoCollector + ProcessCollector 自带）：
- `go_goroutines`、`go_memstats_*`、`process_resident_memory_bytes` 等

业务指标（自定义，`mitm_proxy_` 前缀）：
- `mitm_proxy_requests_total{type}` — 总请求数
- `mitm_proxy_request_duration_seconds` — 请求延迟 Histogram
- `mitm_proxy_upstream_duration_seconds` — 上游转发延迟 Histogram
- `mitm_proxy_mitm_connections{state}` — MITM 连接数 Gauge
- `mitm_proxy_rule_hits_total{script}` — 规则命中数 Counter
- `mitm_proxy_errors_total` — 错误数 Counter

## 变更清单

| 文件 | 改动 |
|---|---|
| `go.mod` / `go.sum` | 引入 prometheus/client_golang v1.24.1 |
| `core/metrics/metrics.go` | 业务指标定义 + 记录方法 + Handler |
| `cmd/mitm-proxy/monitor.go` | pprof 端口 + monitor goroutine + 自动 dump |
| `cmd/mitm-proxy/main.go` | 初始化 + /metrics 端点 + config + status 增强 |
| `core/proxy/proxy.go` | processForward/handleTunnel 请求+延迟+错误埋点 |
| `core/proxy/connmgr.go` | 连接数 active/evicted/max 埋点 |
| `core/dispatch/processor.go` | script 命中 + 规则命中埋点 |
| `config.yaml` | observability 配置段 |

## 未完成任务

- [x] 创建方案文档
- [x] 引入依赖
- [x] core/metrics 模块
- [x] pprof 端口
- [x] monitor goroutine + 自动 dump
- [x] 埋点
- [x] status 增强

## 验证

- `go build ./...` 通过
- `go test ./...` 全量通过
- 零 lint 错误

## 使用方式

- `/metrics` — Prometheus 抓取端点（业务 gin 端口 8082）
- `:6060/debug/pprof/goroutine?debug=2` — 抓 goroutine 栈
- 日志每 30s 打印 goroutine/内存
- goroutine 超 1000 自动 dump 到 /tmp
