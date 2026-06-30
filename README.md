# mitm-proxy

一个基于规则脚本的通用 MITM 正向代理框架，支持 HTTPS 拦截、请求改写、API Pipeline 拆包/组包，提供类似 [Whistle](https://wproxy.org/whistle/) 的使用体验。

## ✨ 特性

- **规则脚本引擎** — Whistle 兼容语法，支持 hosts 映射、正则 URL 代理、Pipeline 处理
- **多脚本隔离** — 每个 `.rules` 文件独立管理，互不干扰，支持优先级控制
- **MITM 透明代理** — 自动签发 TLS 证书，HTTPS 请求解密 + 拦截 + 修改
- **Pipeline 拆包/组包** — 可插拔适配器，支持 API 格式转换（`pipeline://` 扩展语法）
- **Web UI 管理面板** — 规则编辑器 + 实时日志 SSE + 系统状态仪表盘
- **热加载** — 修改规则文件自动重载，无需重启
- **SSE 流式代理** — `@stream` 标记支持 Webpack HMR、SSE 等长连接
- **MCP Server** — 内置 JSON-RPC 2.0 接口，支持 AI 助手集成
- **单一二进制** — Go embed 嵌入所有静态资源，零外部依赖

## 🚀 快速开始

### 1. 编译

```bash
git clone <repo-url>
cd mitm-proxy
go build -o mitm-proxy .
```

### 2. 创建规则脚本

创建 `rules/my-dev.rules`：

```bash
# 声明需要 MITM 拦截的域名
@name "本地开发"
@priority 100

@intercept api.example.com

# Hosts 映射
192.168.1.100 api.example.com

# 正则匹配 → URL 重写（反向代理到本地 dev-server）
/\/static\/(app[\w-]*)\.js$/  http://127.0.0.1:3000/$1.js

# Webpack HMR 流式代理
/\/__webpack_hmr$/  http://127.0.0.1:3000/__webpack_hmr  @stream
```

### 3. 配置 `config.yaml`

```yaml
server:
  listen: ":8082"       # 管理面板 + REST API

proxy:
  enable: true
  listen: ":8081"       # 代理端口
  cert_dir: "certs"

rules:
  dir: "rules"
  hot_reload: true
  watch_interval: 2s
```

### 4. 启动

```bash
./mitm-proxy
# 或指定配置文件路径
./mitm-proxy /path/to/config.yaml
```

### 5. 安装 CA 证书

首次启动会在 `certs/` 目录生成 CA 证书，需要安装并信任：

```bash
# macOS
security add-trusted-cert -d -r trustRoot -k ~/Library/Keychains/login.keychain certs/ca.crt

# Linux (Debian/Ubuntu)
sudo cp certs/ca.crt /usr/local/share/ca-certificates/mitm-proxy.crt
sudo update-ca-certificates
```

### 6. 配置浏览器代理

使用 [SwitchyOmega](https://chrome.google.com/webstore/detail/proxy-switchyomega) 等插件：

- 代理协议：`HTTP`
- 代理服务器：`127.0.0.1`
- 代理端口：`8081`

### 7. 打开管理面板

浏览器访问 `http://localhost:8082/ui/`

## 📖 规则语法参考

### Hosts 映射

```bash
# IP domain1 domain2 ...（兼容 Whistle / /etc/hosts 格式）
192.168.1.100 api.example.com www.example.com
```

### 正则 URL 代理

```bash
# /regex/  target_url  [@options]
# 支持捕获组 $1~$9
/\/api\/(v\d+)\/users/  http://127.0.0.1:3000/api/$1/users

# 带 @cors 自动处理跨域
/\/api\/data/  http://127.0.0.1:3000/api/data  @cors

# 带 @stream 支持 SSE/chunked 长连接
/\/__webpack_hmr$/  http://127.0.0.1:3000/__webpack_hmr  @stream
```

### Pipeline 代理

```bash
# /regex/  pipeline://adapter_name  [@options]
# 匹配的请求经过 Pipeline 适配器处理（拆包→认证→转发→组包）
/\/cgi\/api\?.*serviceType=myapp/  pipeline://yunapi  @cors
```

### MITM 拦截域名

```bash
# @intercept domain — 声明需要 MITM 解密的域名
@intercept api.example.com
@intercept cdn.example.com
```

### 元指令

```bash
@name "脚本名称"       # 在 Web UI 中显示的名称
@priority 100          # 优先级（越高越先匹配）
```

### Options

| Option | 说明 |
|--------|------|
| `@stream` | SSE/chunked 流式代理（不读完 Body） |
| `@cors` | 自动处理 OPTIONS 预检 + CORS 响应头 |

### 注释

```bash
# 以 # 开头的行是注释
```

## 🏗 架构总览

```
                          ┌─────────────────┐
                          │    Web UI        │
                          │  localhost:8082  │
                          └────────┬────────┘
                                   │ REST API / SSE
┌──────────┐    CONNECT    ┌───────▼────────┐
│  浏览器   │ ──────────→ │  ForwardProxy   │
│(代理模式) │ ←────────── │  :8081 (MITM)   │
└──────────┘              └───────┬────────┘
                                   │
                    ┌──────────────┼──────────────┐
                    ▼              ▼              ▼
             ┌───────────┐ ┌───────────┐ ┌───────────┐
             │ RuleEngine │ │ Processor │ │ Pipeline  │
             │  匹配规则   │ │ URL 重写   │ │ 拆包/组包  │
             └───────────┘ └───────────┘ └───────────┘
```

### 目录结构

```
mitm-proxy/
├── main.go              # 启动入口：配置 → 规则引擎 → 代理 → 启动
├── config.yaml          # 系统级配置
├── proxy.go             # 代理核心：MITM / Tunnel / 并发处理 / 有序响应
├── dispatch.go          # 处理器调度：根据 MatchResult 分发到 Proxy/Pipeline/Stream
├── transport.go         # 通用反向代理 + 流式代理
├── handler.go           # Pipeline 处理引擎（拆包 → 认证 → 转发 → 组包）
├── debug.go             # 诊断收集器 + Debug 面板
├── mcp.go               # MCP Server（JSON-RPC 2.0）
│
├── rule/                # 规则引擎包
│   ├── parser.go        #   规则文件解析器
│   ├── engine.go        #   匹配引擎（ShouldIntercept / ResolveUpstream / Match）
│   ├── manager.go       #   规则管理（CRUD / 启停 / 热加载）
│   ├── script.go        #   脚本数据结构
│   └── types.go         #   Rule / RuleType / MatchResult 类型
│
├── proxy/               # 代理核心类型
│   └── types.go         #   MatchResult / RuleType 等核心类型定义
│
├── processor/           # 处理器集合
│   ├── proxy.go         #   ProxyProcessor（正则 → URL 重写）
│   ├── pipeline.go      #   PipelineProcessor（调用适配器）
│   └── stream.go        #   StreamProcessor（SSE 流式代理）
│
├── pipeline/            # Pipeline 处理引擎
│   ├── interfaces.go    #   阶段接口定义
│   ├── context.go       #   PipelineContext 上下文
│   └── pipeline.go      #   编排器
│
├── adapter/             # 可插拔适配器
│   └── interfaces.go    #   Adapter 接口
│
├── webui/               # Web UI 管理面板
│   ├── webui.go         #   SSE 广播 + 路由注册
│   └── static/          #   前端静态资源（embed 嵌入）
│
├── rules/               # 规则脚本目录
│   └── *.rules          #   规则脚本文件
│
└── examples/            # 示例配置和规则
```

## ⚙️ 配置参考

### config.yaml 完整配置

```yaml
# 管理服务配置
server:
  listen: ":8082"          # 管理面板 + REST API + MCP Server

# 后端服务（Pipeline 适配器转发目标）
backend:
  url: "http://127.0.0.1:16686"
  api_path: "/api/v1"
  timeout: 30s

# Pipeline 适配器认证（可选）
mock_auth:
  uin: "your-uin"
  sub_account_uin: "your-sub-uin"
  app_id: 0

# 正向代理
proxy:
  enable: true
  listen: ":8081"          # 代理端口
  cert_dir: "certs"        # CA 证书目录
  hosts_file: ""           # 额外的 hosts 文件（可选）
  intercept_domains: []    # 额外的 MITM 域名（通常由 @intercept 管理）
  upstream_map: {}         # 额外的上游映射

# 规则引擎
rules:
  dir: "rules"             # 规则脚本目录
  hot_reload: true         # 文件变化自动重载
  watch_interval: 2s

# Region 映射（Pipeline 适配器使用，可选）
region_map:
  1: "ap-guangzhou"
  4: "ap-shanghai"
```

## 🔌 REST API

```bash
# 规则管理
GET    /api/rules              # 列表
POST   /api/rules              # 创建 {name, content}
GET    /api/rules/:id          # 详情
PUT    /api/rules/:id          # 更新 {content}
DELETE /api/rules/:id          # 删除

# 启停控制
POST   /api/rules/:id/enable   # 启用
POST   /api/rules/:id/disable  # 禁用
POST   /api/rules/:id/reload   # 重载

# 批量操作
POST   /api/rules/reload-all   # 全部重载
POST   /api/rules/validate     # 校验 {content}

# 系统状态
GET    /api/status             # 运行状态
GET    /api/events             # SSE 实时日志
```

## 🔧 MCP 工具

内置 MCP Server 支持 AI 助手集成（如 Claude、CodeBuddy）：

| 工具 | 说明 |
|------|------|
| `proxy_status` | 获取代理状态：连接数、请求统计、后端地址 |
| `proxy_requests` | 获取最近请求列表（支持过滤） |
| `proxy_errors` | 获取错误请求列表 |
| `proxy_connections` | 获取 MITM 连接池状态 |
| `proxy_curl` | 生成 curl 测试命令 |

MCP 端点：`POST /mcp`（JSON-RPC 2.0）

## 🏗 Pipeline 适配器开发

要支持新的 API 格式转换，实现 `Adapter` 接口：

```go
type Adapter interface {
    Name() string                           // 适配器名称（被 pipeline:// 引用）
    Init(rawConfig map[string]any) error    // 初始化
    GetPipeline() *Pipeline                 // 返回处理管道
    Close() error                           // 清理资源
}
```

Pipeline 由四个阶段组成：

1. **RequestTransformer** — 拆包：提取/重命名/扁平化请求字段
2. **AuthInjector** — 认证注入（可选）
3. **Forwarder** — 转发到后端
4. **ResponseTransformer** — 组包：包装/重命名/嵌套响应结构

在规则脚本中通过 `pipeline://adapter_name` 引用。

## 📄 License

MIT License
