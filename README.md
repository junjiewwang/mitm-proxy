# yunapi-proxy

本地模拟 yunapi 网关的代理服务，用于前端页面与本地 tapm-api 后端服务联调。

## 背景

前端控制台页面和 tapm-api 后端之间有 yunapi 网关服务（`console.cloud.tencent.com/cgi/capi`），导致本地开发时无法直接用前端页面联调后端接口。

本服务模拟 yunapi 的请求转换行为，配合 Whistle 代理实现完全透明的本地前后端联调。

## 架构

```
┌──────────────┐                    ┌─────────────┐                   ┌────────────────┐                  ┌──────────────┐
│  浏览器       │  HTTPS请求         │  Whistle     │  HTTP转发          │  yunapi-proxy   │  POST /api/v1    │  tapm-api    │
│ (Whistle代理) │ ───────────────→  │  拦截&转发    │ ──────────────→   │  localhost:8080 │ ──────────────→  │  :16686      │
│              │ ←───────────────  │             │ ←──────────────   │  格式转换+包装   │ ←──────────────  │              │
└──────────────┘  包装后的响应       └─────────────┘                   └────────────────┘                  └──────────────┘
```

## 核心功能

| 功能 | 说明 |
|------|------|
| 请求格式转换 | 前端 `{cmd, serviceType, data}` → 后端 `{Action, Uin, SubAccountUin, AppId, Region, ...data}` |
| 认证信息注入 | 模拟 yunapi 注入 Uin、SubAccountUin、AppId 等鉴权字段 |
| Region 映射 | 前端 regionId (数字) → 后端 Region (字符串，如 `ap-guangzhou`) |
| 响应包装 | 后端响应 → 前端期望的三层嵌套格式 |
| CORS 透明 | 通过 Whistle 代理，域名不变，无跨域问题 |

## 快速开始

### 前置条件

- Go 1.21+
- [Whistle](https://wproxy.org/whistle/) 已安装并运行
- 浏览器已配置 Whistle 代理
- 本地 tapm-api 服务已启动（默认 `:16686`）

### 1. 克隆并编译

```bash
git clone git@git.woa.com:junjiewwang/yunapi-proxy.git
cd yunapi-proxy
go build -o yunapi-proxy .
```

### 2. 修改配置

编辑 `config.yaml`，根据你的环境修改：

```yaml
server:
  listen: ":8080"

backend:
  url: "http://127.0.0.1:16686"
  api_path: "/api/v1"
  timeout: 30s

# 修改为你自己的账号信息
mock_auth:
  uin: "1500000686"              # 主账号 UIN (对应前端 ownerUin)
  sub_account_uin: "700001199694" # 子账号 UIN (对应前端 uin)
  app_id: 251005942

region_map:
  1: "ap-guangzhou"
  4: "ap-shanghai"
  8: "ap-beijing"
  # ... 更多 region 映射见 config.yaml
```

### 3. 启动代理服务

```bash
# 方式一：直接运行
go run .

# 方式二：运行编译产物
./yunapi-proxy

# 方式三：指定配置文件路径
./yunapi-proxy /path/to/your/config.yaml
```

启动后会输出：
```
Starting yunapi-proxy...
  Listen: :8080
  Backend: http://127.0.0.1:16686/api/v1
  MockAuth: Uin=1500000686, SubAccountUin=700001199694, AppId=251005942
```

### 4. 配置 Whistle 规则

在 Whistle 的 Rules 面板中添加：

```
console.cloud.tencent.com/cgi/capi http://127.0.0.1:8080/cgi/capi
```

### 5. 开始联调

浏览器通过 Whistle 代理访问控制台页面，所有发往 yunapi 的请求会被自动拦截并转发到本地后端。

## 配置说明

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `server.listen` | 代理服务监听地址 | `:8080` |
| `backend.url` | tapm-api 后端地址 | `http://127.0.0.1:16686` |
| `backend.api_path` | 后端统一入口路径 | `/api/v1` |
| `backend.timeout` | 转发超时时间 | `30s` |
| `mock_auth.uin` | 主账号 UIN | - |
| `mock_auth.sub_account_uin` | 子账号 UIN | - |
| `mock_auth.app_id` | AppId | - |
| `region_map` | regionId → Region 映射 | 见 config.yaml |

## 请求转换示例

### 前端发送（原始请求）

```json
{
  "cmd": "DescribeApmServiceMetric",
  "serviceType": "apm",
  "data": {
    "Version": "2021-06-22",
    "Language": "zh-CN",
    "InstanceId": "apm-eDyXPD6FF",
    "StartTime": 1779432660,
    "EndTime": 1779433560,
    "OrderBy": {"Key": "model_request_count", "Value": "desc"},
    "Filters": [{"Key": "span.kind", "Type": "in", "Value": "server"}]
  },
  "regionId": 1
}
```

### 转换后发给后端

```json
{
  "Action": "DescribeApmServiceMetric",
  "Uin": "1500000686",
  "SubAccountUin": "700001199694",
  "AppId": 251005942,
  "Region": "ap-guangzhou",
  "Version": "2021-06-22",
  "Language": "zh-CN",
  "InstanceId": "apm-eDyXPD6FF",
  "StartTime": 1779432660,
  "EndTime": 1779433560,
  "OrderBy": {"Key": "model_request_count", "Value": "desc"},
  "Filters": [{"Key": "span.kind", "Type": "in", "Value": "server"}]
}
```

## 健康检查

```bash
curl http://127.0.0.1:8080/health
# 返回: {"status":"ok"}
```

## 项目结构

```
yunapi-proxy/
├── main.go          # 入口、配置加载、Gin 服务启动
├── handler.go       # 请求转换、转发、响应包装核心逻辑
├── config.yaml      # 配置文件
├── go.mod / go.sum  # Go 模块依赖
└── docs/
    └── yunapi-proxy.md  # 详细需求文档
```

## 常见问题

### Q: 联调时前端报跨域错误？

A: 确保浏览器走的是 Whistle 代理。Whistle 转发后前端看到的域名仍是 `console.cloud.tencent.com`，不会有跨域问题。

### Q: 响应格式不对，前端解析失败？

A: 不同接口的后端返回格式可能有细微差异，可以在 `handler.go` 的 `wrapResponse` 方法中按 Action 做特殊处理。

### Q: 如何查看转发的详细日志？

A: 服务默认使用 Gin 的 Debug 模式，所有请求都会打印在终端。也可以在 Whistle 中查看请求/响应详情。

### Q: 如何支持新的 region？

A: 在 `config.yaml` 的 `region_map` 中添加对应的 regionId → Region 字符串映射即可。

## License

Internal use only.
