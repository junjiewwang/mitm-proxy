# yunapi-proxy - 本地联调代理服务

## 需求背景

前端页面和 tapm-api 后端之间有 yunapi 网关服务，导致本地无法直接用前端页面联调后端。
本服务作为正向代理 + MITM 中间人，拦截 APM 控制台请求并转发到本地后端，实现本地前后端联调。

## 架构

```
浏览器(SwitchyOmega) → 正向代理(:8081) → MITM 拦截 console.cloud.tencent.com
                                              │
                              ┌────────────────┴────────────────┐
                              │                                 │
                    POST /cgi/capi?serviceType=apm        其他请求
                    (INTERCEPT → 本地后端:16686)       (FWD → 真实云端后端)
```

## 核心功能

1. **正向代理 + MITM**：对 `console.cloud.tencent.com` 和 `console-hc.cloud.tencent.com` 进行 HTTPS 拦截解密
2. **请求分流**：APM API 请求拦截转发到本地后端，其他请求透传到真实后端
3. **双格式拦截**：
   - **yunapi 格式**：`POST /cgi/capi?action=delegate&serviceType=apm` — 经 yunapi 格式转换后转发
   - **直连 API 格式**：`/_api/apm/{Action}` — 直接转发到后端，响应原样返回
4. **请求格式转换**（yunapi 格式）：前端格式 `{cmd, serviceType, data}` → 后端格式 `{Action, Uin, ...data字段}`
4. **认证信息注入**：模拟 yunapi 注入 Uin、SubAccountUin、AppId
5. **Region 映射**：regionId(数字) → Region(字符串)
6. **响应包装**：后端响应 → 前端期望的嵌套格式
7. **并发请求处理**：MITM 拦截的请求并发处理 + 有序响应写回，避免 HTTP/1.1 串行瓶颈
8. **诊断面板**：`/debug` HTML 仪表盘 + `/debug/status` JSON API
9. **MCP Server**：JSON-RPC 2.0 诊断工具服务器

## 使用方式

### 1. 启动服务

```bash
cd ~/GolandProjects/apm/yunapi-proxy
# 修改 config.yaml 中的 mock_auth 信息
go run . 
# 或
go build -o yunapi-proxy . && ./yunapi-proxy
```

### 2. 配置 SwitchyOmega

浏览器安装 SwitchyOmega 插件，配置 HTTP 代理指向 `127.0.0.1:8081`。

### 3. 安装 CA 证书

首次运行会自动生成 CA 证书，需要安装到系统信任链：

```bash
# macOS
security add-trusted-cert -d -r trustRoot -k ~/Library/Keychains/login.keychain certs/ca.crt
```

### 4. 浏览器通过 SwitchyOmega 代理访问 APM 控制台即可

## 配置说明

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| server.listen | Gin 服务监听地址 | :8082 |
| backend.url | tapm-api 后端地址 | http://127.0.0.1:16686 |
| backend.api_path | 后端统一入口路径 | /api/v1 |
| backend.timeout | 转发超时时间 | 30s |
| proxy.enable | 是否启用正向代理 | true |
| proxy.listen | 正向代理监听地址 | :8081 |
| proxy.cert_dir | CA 证书目录 | certs |
| proxy.hosts_file | Whistle 格式 hosts 映射文件 | hosts.txt |
| proxy.intercept_domains | MITM 拦截域名列表 | console.cloud.tencent.com |
| mock_auth.uin | 主账号 UIN | - |
| mock_auth.sub_account_uin | 子账号 UIN | - |
| mock_auth.app_id | AppId | - |
| region_map | regionId→Region 映射 | 见 config.yaml |

## 实施进展

- [x] 项目结构和配置文件
- [x] 请求转换和转发逻辑
- [x] 正向代理 + MITM 拦截
- [x] TCP_NODELAY 修复 CDN 隧道卡死问题
- [x] 诊断面板 + MCP Server
- [x] **MITM 请求并发处理优化**（2026-06-17）
- [x] **支持 console-hc 直连 API 拦截**（2026-06-17）
- [x] **直连 API 复用完整拆包组包 pipeline**（2026-06-17）
- [x] **CORS 预检（OPTIONS）支持**（2026-06-17）
- [x] **死代码清理**：删除 `handler.processDirectRequest`、移除 `proxy.go` 多余的 `encoding/json` 导入（2026-06-17）

## 性能优化记录

### MITM 并发请求处理（2026-06-17）

**问题**：通过代理访问 APM 控制台页面加载慢，约 5-10 秒。

**根因**：MITM 模式下请求处理是串行的（读一个请求 → 等待响应 → 写回 → 读下一个），
而浏览器直连使用 HTTP/2 可以并行发送 20+ 个请求。10 个 API 各 500ms：
- 直连（HTTP/2 并行）：~500ms
- 代理（HTTP/1.1 串行）：~5000ms

**解决方案**：reader-worker-writer 三级管线并发架构

```
┌─ reader ─┐    ┌─ worker pool ─┐    ┌─ writer ─┐
│ ReadReq  │───>│ go process(1) │───>│          │
│ ReadReq  │───>│ go process(2) │───>│ 按序号   │──> tlsConn
│ ReadReq  │───>│ go process(3) │───>│ 顺序写回 │
└──────────┘    └───────────────┘    └──────────┘
```

- **reader**：主循环串行读取 HTTP 请求（HTTP/1.1 协议要求），预读 Body 到内存
- **worker pool**：每个请求用 goroutine 并发处理（转发到上游或本地后端）
- **writer**：按请求序号顺序写回响应，保证 HTTP/1.1 管线化语义

**预期效果**：10x 提速，从串行 ~5000ms 降至并行 ~500ms（受限于最慢单个请求）。

## 遗留问题

- 响应包装格式可能需要根据实际联调情况微调（不同接口的后端返回格式可能有细微差异）
- 如遇到特殊接口需要额外处理，可在 handler.go 中按 Action 分支处理
- 可进一步优化：SwitchyOmega 配置精准代理规则，让无关域名完全绕过代理

## 通用化重构计划

项目计划从"腾讯云 APM 联调专用工具"重构为通用的 MITM 正向代理框架，
详见 [通用化重构方案 — Roadmap](refactor-roadmap.md)。
