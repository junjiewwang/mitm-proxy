# mitm-proxy

一个基于规则脚本的通用 MITM 正向代理框架，支持 HTTPS 拦截、JS 脚本请求改写、API 格式转换，提供类似 [Whistle](https://wproxy.org/whistle/) 的使用体验。

## ✨ 特性

- **规则脚本引擎** — Whistle 兼容语法 + JS 脚本扩展，支持 hosts 映射、正则 URL 代理、脚本处理
- **JS 脚本化业务逻辑** — `@script onRequest/onResponse` 内嵌 JavaScript，热更新无需重启
- **MITM 透明代理** — 自动签发 TLS 证书，HTTPS 请求解密 + 拦截 + 改写
- **配置自管理** — 业务配置（backend/auth/region_map）通过 `@config` 段内聚在规则文件中
- **Web UI 管理面板** — 规则编辑器 + 语法高亮 + Debug 调式面板 + SSE 实时日志
- **热加载** — 修改规则文件自动重载，文件删除/新增/修改全部检测
- **hosts 冲突检测** — 同一域名映射到不同 IP 时告警，编辑器内联提示 + 行号跳转
- **请求对比排查** — Requests 详情页展示脚本重写前后的请求体（两栏对比）
- **MCP Server** — 内置 JSON-RPC 2.0 接口，支持 AI 助手集成
- **可复用的核心框架** — `core/proxy`/`core/rule`/`core/script` 可被外部项目 import

## 🚀 快速开始

### 1. 编译

```bash
git clone <repo-url>
cd mitm-proxy
make build
```

### 2. 创建规则脚本

创建 `rules/my-dev.rules`：

```bash
@name "本地开发"
@priority 100

# MITM 拦截域名
@intercept api.example.com

# Hosts 映射
192.168.1.100 api.example.com

# 正则代理
/\/static\/(app[\w-]*)\.js$/  http://127.0.0.1:3000/$1.js

# JS 脚本处理（API 格式转换）
/\/cgi\/api\?.*/  script://self

@config
backend:
  url: "http://127.0.0.1:16686"
  api_path: "/api/v1"
@end

@script onRequest
  var body = JSON.parse(ctx.rawBody);
  ctx.backendURL = ctx.config.backend.url + ctx.config.backend.api_path;
  ctx.backendRequest = { Action: body.cmd };
@end
```

### 3. 启动

```bash
cp config.yaml.example config.yaml
./mitm-proxy
```

浏览器配置代理 `127.0.0.1:8081`，安装并信任 CA 证书。

### 4. 打开管理面板

访问 `http://localhost:8082/ui/`

## 📖 规则语法参考

### Hosts 映射

```bash
192.168.1.100 api.example.com www.example.com
```

### 正则 URL 代理

```bash
# /regex/  target_url  [@options]
/\/api\/(v\d+)\/users/  http://127.0.0.1:3000/api/$1/users
/\/__webpack_hmr$/  http://127.0.0.1:3000/__webpack_hmr  @stream
```

### JS 脚本处理 (V2)

```bash
# /regex/  script://self  [@options]
/\/api\/gateway/  script://self  @cors
```

### 脚本私有配置

```bash
@config
backend:
  url: "http://127.0.0.1:16686"
  api_path: "/api/v1"
@end
```

### JS 脚本块

```bash
@script onRequest
  ctx.backendURL = ctx.config.backend.url;
  ctx.backendRequest = { Action: "Test" };
@end

@script onResponse
  ctx.finalResp = ctx.backendResp;
@end
```

### MITM 拦截 + 元指令

```bash
@intercept example.com
@name "我的规则"
@priority 100
```

### Options

| Option | 说明 |
|--------|------|
| `@stream` | SSE/chunked 流式代理 |
| `@cors` | 自动处理 CORS 预检 + 响应头 |

## 🐞 调试

访问 Web UI 的 **Debug** Tab：

1. 选择目标脚本
2. 输入 Method / Path / Body（可选：Headers、Cookie）
3. 勾选 Dry Run 跳过实际后端转发
4. 点击 **Run Debug** 查看分步执行结果和 console.log 输出

**curl 一键导入**：直接粘贴浏览器复制的 curl 命令，点击 **Parse curl** 自动解析并填充 Method / Path / Headers / Cookie / Body。

**Requests 详情**：点击命中脚本的请求，Backend Tab 以两栏对比展示原始请求体和脚本重写后的请求体，方便排查规则转换是否正确。

详见 [JS API 参考](docs/script-api-reference.md)

## 🏗 架构

```
cmd/mitm-proxy/          ← 应用入口
core/proxy/              ← MITM 代理引擎（可复用）
core/rule/               ← 规则引擎
core/script/             ← JS 脚本运行时
webui/                   ← Web 管理面板
```

### 目录结构

```
mitm-proxy/
├── cmd/mitm-proxy/          ← 应用入口
│   ├── main.go              ←    启动 + Config
│   ├── dispatch.go          ←    处理器调度
│   ├── debug.go             ←    诊断收集器
│   ├── mcp.go               ←    MCP Server
│   ├── ruleapi.go           ←    REST API
│   └── interfaces.go        ←    类型别名
│
├── core/                     ← 通用框架核心
│   ├── proxy/               ← MITM 正向代理引擎
│   │   ├── proxy.go          ←    ForwardProxy + ProxyConfig
│   │   ├── cert.go           ←    CA 证书管理
│   │   ├── connmgr.go        ←    LRU 连接管理器
│   │   ├── transport.go      ←    请求转发
│   │   ├── helpers.go        ←    公共工具函数
│   │   └── interfaces.go     ←    核心接口 + MatchResult
│   │
│   ├── rule/                ← 规则引擎
│   │   ├── parser.go         ←    解析器 (@config/@script/@end)
│   │   ├── engine.go         ←    匹配引擎
│   │   ├── manager.go        ←    CRUD + 热加载
│   │   └── types.go          ←    Rule/RuleScript 类型
│   │
│   └── script/              ← JS 脚本运行时 (goja)
│       ├── runtime.go        ←    VM 池 + 沙箱
│       ├── api.go            ←    ctx/console 注入
│       ├── loader.go         ←    预编译 + 缓存
│       └── debugger.go       ←    调试执行器
│
├── webui/                    ← Web UI
│   ├── webui.go              ←   REST API + SSE
│   └── static/               ←   前端静态资源
│
├── config.yaml               ← 框架配置
├── rules/                    ← 规则脚本（gitignored）
└── examples/                 ← 示例
```

## ⚙️ 配置参考

```yaml
server:
  listen: ":8082"

proxy:
  enable: true
  listen: ":8081"
  cert_dir: "certs"
  hosts_file: ""
  intercept_domains: []
  upstream_map: {}

rules:
  dir: "rules"
  hot_reload: true
  watch_interval: 2s

webui:
  enable: true

debug:
  enable: true
  buffer_size: 200
```

注意：**业务配置**（backend URL、auth、region_map）不在 `config.yaml` 中，而是在规则脚本的 `@config` 段内由脚本自管理。

## 🔌 REST API

```bash
# 规则管理
GET    /api/rules              # 列表
POST   /api/rules              # 创建
GET    /api/rules/:id/content  # 获取文件内容
PUT    /api/rules/:id          # 更新
DELETE /api/rules/:id          # 删除
POST   /api/rules/:id/enable   # 启用
POST   /api/rules/:id/disable  # 禁用

# 调试
POST   /api/rules/:id/debug    # 分步执行调试

# 系统
GET    /api/events             # SSE 实时日志
GET    /api/status             # 运行状态
GET    /health                 # 健康检查
```

## 🔧 MCP 工具

| 工具 | 说明 |
|------|------|
| `proxy_status` | 代理状态：连接数、请求统计 |
| `proxy_requests` | 最近请求列表（支持过滤） |
| `proxy_errors` | 错误请求列表 |
| `proxy_connections` | MITM 连接池状态 |
| `proxy_curl` | 生成 curl 测试命令 |

MCP 端点：`POST /mcp`（JSON-RPC 2.0）

## 📄 License

MIT License
