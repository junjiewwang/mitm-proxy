# yunapi-proxy - 本地联调代理服务

## 需求背景

前端页面和 tapm-api 后端之间有 yunapi 网关服务，导致本地无法直接用前端页面联调后端。
本服务模拟 yunapi 的行为，配合 Whistle 代理实现本地前后端联调。

## 架构

```
浏览器(Whistle代理) → Whistle拦截 → localhost:8080(本服务) → localhost:16686(tapm-api)
```

## 核心功能

1. **请求格式转换**：前端格式 `{cmd, serviceType, data}` → 后端格式 `{Action, Uin, ...data字段}`
2. **认证信息注入**：模拟 yunapi 注入 Uin、SubAccountUin、AppId
3. **Region 映射**：regionId(数字) → Region(字符串)
4. **响应包装**：后端响应 → 前端期望的嵌套格式

## 使用方式

### 1. 启动服务

```bash
cd ~/GolandProjects/apm/yunapi-proxy
# 修改 config.yaml 中的 mock_auth 信息
go run . 
# 或
./yunapi-proxy
```

### 2. 配置 Whistle 规则

```
console.cloud.tencent.com/cgi/capi http://127.0.0.1:8080/cgi/capi
```

### 3. 浏览器使用 Whistle 代理访问控制台页面即可

## 配置说明

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| server.listen | 代理服务监听地址 | :8080 |
| backend.url | tapm-api 后端地址 | http://127.0.0.1:16686 |
| backend.api_path | 后端统一入口路径 | /api/v1 |
| backend.timeout | 转发超时时间 | 30s |
| mock_auth.uin | 主账号 UIN | - |
| mock_auth.sub_account_uin | 子账号 UIN | - |
| mock_auth.app_id | AppId | - |
| region_map | regionId→Region 映射 | 见 config.yaml |

## 实施进展

- [x] 项目结构和配置文件
- [x] 请求转换和转发逻辑
- [x] 主入口和服务启动
- [x] 编译验证通过

## 遗留问题

- 响应包装格式可能需要根据实际联调情况微调（不同接口的后端返回格式可能有细微差异）
- 如遇到特殊接口需要额外处理，可在 handler.go 中按 Action 分支处理
