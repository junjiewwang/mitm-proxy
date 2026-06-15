# yunapi-proxy

本地模拟 yunapi 网关的代理服务，用于前端页面与本地 tapm-api 后端服务联调。

## 背景

前端控制台页面和 tapm-api 后端之间有 yunapi 网关服务（`console.cloud.tencent.com/cgi/capi`），导致本地开发时无法直接用前端页面联调后端接口。

本服务模拟 yunapi 的请求转换行为，配合 Whistle 代理实现完全透明的本地前后端联调。

## 架构

支持两种模式：

### 模式一：正向代理模式（推荐，无需 Whistle）

```
┌──────────────┐                    ┌──────────────────┐                  ┌──────────────┐
│  浏览器       │  HTTPS CONNECT     │  yunapi-proxy     │  POST /api/v1    │  tapm-api    │
│(SwitchyOmega)│ ───────────────→  │  :8081 (MITM)    │ ──────────────→  │  :16686      │
│              │ ←───────────────  │  拦截 APM 请求    │ ←──────────────  │              │
└──────────────┘                    │  透传其他请求 →    │                  └──────────────┘
                                    │      ↓            │
                                    │  9.223.72.139     │ (测试环境)
                                    └──────────────────┘
```

### 模式二：Whistle 转发模式

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
  Proxy: :8081 (intercept: [console.cloud.tencent.com])
[Proxy] Forward proxy listening on :8081
[Proxy] Configure SwitchyOmega: HTTP proxy → 127.0.0.1:8081
```

### 4. 配置代理

#### 方式 A：正向代理 + SwitchyOmega（推荐）

无需 Whistle，直接通过浏览器插件：

**Step 1**: 启动后会自动生成 CA 证书到 `certs/ca.crt`，安装并信任它：

```bash
# macOS 安装 CA 证书
security add-trusted-cert -d -r trustRoot -k ~/Library/Keychains/login.keychain certs/ca.crt
```

**Step 2**: 在 Chrome 安装 [SwitchyOmega](https://chrome.google.com/webstore/detail/proxy-switchyomega) 插件

**Step 3**: 创建一个代理情景模式：
- 名称: `yunapi-proxy`
- 代理协议: `HTTP`
- 代理服务器: `127.0.0.1`
- 代理端口: `8081`

**Step 4**: 创建一个自动切换模式：
- 规则: `*.cloud.tencent.com` → 使用 `yunapi-proxy` 情景模式
- 默认: 直接连接

**Step 5**: 在 SwitchyOmega 选择你创建的自动切换模式

> 注意：如果你同时还需要走 Whistle 做其他域名的 host 映射，可以把默认代理设为 Whistle（`127.0.0.1:8899`），
> 只有 `console.cloud.tencent.com` 走 yunapi-proxy。

#### 方式 B：Whistle 转发模式

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

### 正向代理配置

| 配置项 | 说明 | 默认值 |
|--------|------|--------|
| `proxy.enable` | 是否启用正向代理 | `false` |
| `proxy.listen` | 正向代理监听地址 | `:8081` |
| `proxy.cert_dir` | CA 证书存储目录 | `certs` |
| `proxy.hosts_file` | Whistle 格式 hosts 文件路径 | - |
| `proxy.intercept_domains` | MITM 拦截域名列表 | `["console.cloud.tencent.com"]` |
| `proxy.upstream_map` | 额外域名 → IP 映射 | `{}` |

### hosts 文件格式

Whistle 标准格式，与 `hosts.txt` 或 `config.yaml` 中的 `upstream_map` 合并生效：

```
# 支持多个域名指向同一 IP
9.223.72.139 console.cloud.tencent.com buy.cloud.tencent.com

# CDN 环境
139.199.214.200 cloudcache.tencent-cloud.com
```

- 非拦截域名的 HTTPS 连接会按此映射透传到对应 IP
- 未在映射表中的域名走正常 DNS 解析
- 只处理域名映射，跳过带路径的规则（如 `cloud.tencent.com/password/recover`）
- 如果你的原始 Whistle 规则有更新，直接编辑 `hosts.txt` 即可，无需改代码

## 请求/响应转换规则

### 前端 → 后端（拆包规则）

前端发送给 `console.cloud.tencent.com/cgi/capi` 的请求包括：

| 来源 | 字段 | 说明 |
|------|------|------|
| URL Query | `action=delegate`、`serviceType=apm`、`uin`、`ownerUin` | 路由与认证参数 |
| Body JSON | `cmd`、`serviceType`、`data`、`regionId` | 业务参数 |

**转换规则（5 条）：**

| # | 规则 | 前端字段 | 后端字段 | 举例 |
|---|------|----------|----------|------|
| 1 | **Action 映射** | `cmd` | → `Action` | `DescribeApmServiceMetric` |
| 2 | **ApiModule 注入** | `serviceType` | → `ApiModule` | `apm` |
| 3 | **认证注入** | `uin` / `ownerUin` | → `Uin` / `SubAccountUin` | 优先用 URL 值，fallback `mock_auth` 配置 |
| 4 | **Region 映射** | `regionId` (数字) | → `Region` (字符串) | `16` → `ap-chongqing` |
| 5 | **data 扁平化** | `data.*` | → 顶层字段 | `data.Version` → `Version` |

**保护字段**（data 中的字段不能覆盖）：`Uin`、`SubAccountUin`、`AppId`、`Action`、`ApiModule`

**示例**：

```
前端发送:
{"cmd":"DescribeApmServiceMetric", "serviceType":"apm",
 "data":{"Version":"2021-06-22","InstanceId":"apm-test"},
 "regionId":1}

↓ 转换后发给后端:

{"Action":"DescribeApmServiceMetric","ApiModule":"apm",
 "Uin":"1500000686","SubAccountUin":"700001199694","AppId":251005942,
 "Region":"ap-guangzhou","Version":"2021-06-22","InstanceId":"apm-test"}
```

### 后端 → 前端（组包规则）

后端返回格式：`{"Response": {实际数据, "RequestId": "..."}}`

| 情况 | 后端返回 | 说明 |
|------|----------|------|
| 成功 | `{"Response":{"Data":{...},"RequestId":"..."}}` | `Data` 包含业务结果 |
| 失败 | `{"Response":{"Error":{"Code":"...","Message":"..."},"RequestId":"..."}}` | `Error` 包含错误信息 |

**包装逻辑**：后端 `Response.*` → 前端 `data.data.Response.*` 的三层嵌套：

```
后端响应:
{"Response":{"Instances":[...],"RequestId":"abc-123"}}

↓ 包装后返回前端:

{"code":0, "data":{"code":0, "cgwerrorCode":0,
  "data":{"Response":{"Instances":[...],"RequestId":"abc-123"}}},
 "mccode":0, "errObj":{}, "reqId":"...", "seqId":"..."}
```

**各层字段含义**：

| 层级 | 字段 | 值规则 |
|------|------|--------|
| 顶层 | `code` | **始终 0** |
| 顶层 | `mccode` | **始终 0** |
| 顶层 | `errObj` | **始终 {}** |
| 顶层 | `seqId` / `reqId` | 代理生成的追踪 ID |
| 中间层 | `data.code` | 成功=0，失败=错误码 |
| 中间层 | `data.cgwerrorCode` | **始终 0** |
| 中间层 | `data.data` | 内层容器 |
| 内层 | `data.data.Response` | **后端原始响应内容**（`{"Data":...}` 或 `{"Error":...}`）

### 总结公式

```
拆包：{cmd, serviceType, data, regionId}
    → {Action=cmd, ApiModule=serviceType, +auth, Region, ...flatten(data)}

组包：{"Response": 内容}
    → {code:0, data:{code:0, data:{Response: 内容}}, mccode:0, errObj:{}}
```

## 健康检查

```bash
curl http://127.0.0.1:8080/health
# 返回: {"status":"ok"}
```

## 项目结构

```
yunapi-proxy/
├── main.go          # 入口、配置加载、Whistle hosts 解析
├── handler.go       # 请求转换、转发、响应包装核心逻辑
├── proxy.go         # 正向代理（HTTP CONNECT + MITM 拦截）
├── cert.go          # CA 证书生成与域名证书动态签发
├── config.yaml      # 配置文件
├── hosts.txt        # Whistle 格式 hosts 映射（IP domain）
├── certs/           # 自动生成的 CA 证书（首次启动生成）
│   ├── ca.crt       # CA 证书（需安装信任）
│   └── ca.key       # CA 私钥
├── go.mod / go.sum  # Go 模块依赖
└── docs/
    └── yunapi-proxy.md  # 详细需求文档
```

## 连接管理架构

### ConnManager（LRU 淘汰机制）

MITM 连接由 `ConnManager` 管理，采用**有界槽位 + 最近最少使用淘汰**策略：

```
[活跃端]                                     [淘汰端]
 slot[0] ➤ slot[1] ➤ slot[2] ... slot[97] ➤ slot[98] ➤ slot[99]  (max=100)

新连接 → 插入 [0]，现有后移            满时扫描淘汰端
活跃请求 → Touch 提升到 [0]           闲置 > 10s → 踢除
连接关闭 → 移出槽位
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `maxConns` | 100 | 最大 MITM 连接数 |
| `idleTimeout` | 10s | 闲置超时（槽位满时触发淘汰） |

每 30s 统计日志：`[ConnMgr] Stats: active_mitm=12, total_evicted=3`

### 请求处理优先级

```
新请求到达 → MITM 管道
  ├── 槽位未满   → 立即处理（热烈路径）   ← 新 session 秒级响应
  ├── 已满+闲置  → 踢旧换新（🔄 淘汰）    ← 旧 session 让位
  └── 已满+活跃  → 拒绝（🚫 保护）        ← 防止雪崩
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
