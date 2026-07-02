# 规则脚本语法参考手册

> 版本：V2 | 适用于 yunapi-proxy MITM 代理引擎

---

## 1. 概述

**规则脚本**（`.rules` 文件）是 yunapi-proxy 的核心配置单元。每个 `.rules` 文件描述一组完整的 MITM 代理行为，包括：

- 需要拦截的 HTTPS 域名
- URL 到目标的映射规则（正则代理 / 精确路径代理 / hosts 映射）
- 请求/响应改写逻辑（通过内嵌 JavaScript 脚本实现）
- 私有配置数据（YAML 格式，框架透传给脚本）

规则引擎受 [Whistle](https://wproxy.org/whistle/) 启发，扩展了 `@config` / `@script` 等 V2 语法，支持在同文件中将业务配置与转换逻辑内聚在一起。

---

## 2. 文件结构

一个 `.rules` 文件由以下部分组成：

| 部分 | 说明 |
|------|------|
| **文件头** | `@name` 声明名称、`@priority` 声明优先级 |
| **注释** | 以 `#` 开头的行，可出现在文件任意位置 |
| **指令** | `@intercept` 等单行指令 |
| **配置块** | `@config ... @end` 多行 YAML 配置 |
| **脚本块** | `@script ... @end` 多行 JavaScript 脚本 |
| **规则行** | 正则规则、hosts 映射、精确路径代理 |

**基础示例：**

```plaintext
# =============================================
# 示例：本地开发联调规则
# =============================================
@name "本地开发"
@priority 100

# --- MITM 拦截域名 ---
@intercept api.example.com

# --- Hosts 映射 ---
127.0.0.1 api.example.com

# --- 正则代理 ---
/\/static\/(app[\w-]*)\.js$/  http://127.0.0.1:3000/$1.js
```

---

## 3. 指令参考

指令以 `@` 开头，作用于整个规则脚本。

### 3.1 `@name` — 脚本名称

```plaintext
@name "名称"
```

声明规则脚本的显示名称（可选，建议填写）。支持引号包裹（含空格时建议使用）。

### 3.2 `@priority` — 优先级

```plaintext
@priority 100
```

声明脚本的优先级。数值越大优先级越高（默认 `0`）。当多个脚本之间存在规则冲突时，高优先级脚本的规则优先匹配。

### 3.3 `@intercept` — MITM 拦截域名

```plaintext
@intercept example.com
@intercept console.cloud.tencent.com
```

声明需要通过 MITM 代理拦截的 HTTPS 域名。只有被 `@intercept` 声明的域名，其加密流量才会被代理解密，从而进行 URL 匹配和改写。

一条 `@intercept` 可声明多个域名（空格分隔），也可以多次使用。

### 3.4 `@config ... @end` — YAML 配置块

```plaintext
@config
backend:
  url: "http://127.0.0.1:16686"
  api_path: "/api/v1"
  timeout: 30
auth:
  uin: "1500000686"
  app_id: 251005942
@end
```

声明脚本私有配置。`@config` 和 `@end` 之间的内容以 **YAML** 格式编写。

> **重要：** 框架不解析配置内容的语义，仅做 YAML 反序列化后透传给 JavaScript 脚本（通过 `ctx.config` 访问）。这允许每个脚本自定义任意配置结构。

### 3.5 `@script ... @end` — JavaScript 脚本块

```plaintext
@script onRequest
  var body = JSON.parse(ctx.rawBody);
  ctx.backendURL = "http://127.0.0.1:16686/api/v1";
  ctx.backendRequest = { Action: body.cmd };
@end

@script onResponse
  var resp = JSON.parse(ctx.backendResp);
  ctx.finalResp = JSON.stringify({ code: 0, data: resp });
@end
```

声明请求/响应处理脚本。脚本名（如 `onRequest`、`onResponse`）用于标识脚本的执行阶段。`@script` 和 `@end` 之间的内容为 JavaScript（ECMAScript 5.1 兼容，由 [goja](https://github.com/dop251/goja) 引擎执行）。

**外部脚本引用：**

```plaintext
@script onRequest file:///path/to/script.js
```

通过 `file://` 协议引用外部 JS 文件，脚本内容从指定路径读取。

---

## 4. 正则规则

正则规则是规则脚本中最常用的规则类型，通过正则表达式匹配 URL 路径并转发到目标。

### 4.1 基本语法

```plaintext
/regex/ target [@stream] [@cors] [@timeout=Xs]
```

- **`/regex/`**：URL 路径匹配正则，使用 `/` 作为定界符（正则内部的 `/` 可写为 `\/`，也可以直接写 `/`——解析器会智能处理）
- **`target`**：转发目标，支持以下协议：
  - `http://` / `https://` — 直接代理转发
  - `script://` — 交给 JS 脚本处理（V2）
  - `pipeline://` — 交给 Pipeline 适配器处理（V1 兼容，已废弃）

### 4.2 Target 类型详解

#### 4.2.1 `http://` / `https://` — 直接代理

```plaintext
/\/qcloud\/tea\/app\/(tapm[\w-]*)\.js$/  http://127.0.0.1:8322/$1.js
```

匹配的请求直接转发到目标 URL。目标 URL 中可使用 `$1`~`$9` 引用正则捕获组。

#### 4.2.2 `script://` — JS 脚本处理

```plaintext
/\/cgi\/capi\?.*action=delegate&.*serviceType=apm/  script://self
```

匹配的请求交给 JS 脚本处理，不直接转发。`self` 表示使用同文件中的 `@script onRequest` / `@script onResponse`。

#### 4.2.3 `pipeline://` — Pipeline 适配器（V1 兼容）

```plaintext
/\/cgi\/api\?.*action=delegate/  pipeline://yunapi
```

匹配的请求通过指定的 Pipeline 适配器转换后转发。V2 推荐使用 `script://self` + `@script` 替代。

### 4.3 选项

| 选项 | 说明 |
|------|------|
| `@stream` | 流式代理模式，适用于 SSE / WebSocket / chunked 传输场景（如 webpack HMR）。不会缓冲完整响应。 |
| `@cors` | 自动处理 CORS 预检请求（OPTIONS），返回允许跨域的响应头。 |
| `@timeout=Xs` | 单条规则超时时间（如 `@timeout=30s`）。 |

### 4.4 捕获组

正则中的捕获组 `(...)` 可以在目标 URL 中通过 `$1`~`$9` 引用：

```plaintext
# 将 /static/app-name.hash.js 代理到 http://127.0.0.1:3000/app-name.js
/\/static\/(app[\w-]*)(\.\w{10})?\.js$/  http://127.0.0.1:3000/$1.js
```

### 4.5 完整示例

```plaintext
# CDN JS/CSS → 本地 webpack-dev-server
/\/qcloud\/tea\/app\/(tapm[\w-]*)(\.(zh|en|jp|ko|dev))?(\.\w{10})?\.(js|css)(\.map)?$/  http://127.0.0.1:8322/$1.$5$6

# Webpack HMR 流式代理
/\/qcloud\/tea\/app\/(.+\/)?__webpack_hmr$/  http://127.0.0.1:8322/__webpack_hmr  @stream

# Hot update 文件
/\/qcloud\/tea\/app\/(.+\.)?(\w+)\.(hot-update\.(js|json))$/  http://127.0.0.1:8322/$1$2.$3

# API 请求 → JS 脚本处理
/\/cgi\/capi\?.*action=delegate&.*serviceType=apm/  script://self

# 带 CORS 的 API 请求
/\/_api\/apm\//  script://self  @cors
```

---

## 5. Hosts 映射

### 5.1 语法

```plaintext
IP_ADDRESS domain1 domain2 ...
```

将指定域名解析到目标 IP。一条规则可映射多个域名到同一 IP。

### 5.2 示例

```plaintext
# 单域名映射
192.168.1.100 api.example.com

# 多域名映射到同一 IP
9.223.72.139 console.cloud.tencent.com console-hc.cloud.tencent.com

# 多个 hosts 规则
9.223.72.139 eb.qcloud.com
9.223.72.139 www.qcloud.com
139.199.214.200 cloudcache.tencent-cloud.com
```

> **注意：** hosts 映射生效的前提是目标域名已在 `@intercept` 中声明。

---

## 6. 精确路径代理

### 6.1 语法

```plaintext
domain/path target
```

对指定域名的精确路径进行代理转发。与正则规则不同，精确路径代理不使用正则，直接匹配完整的 domain + path。

### 6.2 示例

```plaintext
# 将 example.com 的 /api/v1 路径代理到本地
example.com/api/v1 http://127.0.0.1:3000/api/v1
```

> **注意：** 精确路径代理不支持 `@stream`、`@cors` 等选项，也不支持捕获组。对于复杂匹配场景，请使用正则规则。

---

## 7. 脚本块详解

脚本块是 V2 语法核心，用于实现请求/响应的自定义转换逻辑。脚本运行在 [goja](https://github.com/dop251/goja) JavaScript 引擎中，兼容 ECMAScript 5.1。

### 7.1 执行阶段

| 脚本名 | 执行时机 | 典型用途 |
|--------|----------|----------|
| `onRequest` | 收到客户端请求后，转发到后端前 | 修改后端目标 URL、构造后端请求体、注入认证参数 |
| `onResponse` | 收到后端响应后，返回客户端前 | 解包后端响应、重新组装响应格式 |

### 7.2 ctx 对象 API

脚本通过全局 `ctx` 对象访问请求上下文：

#### 输入字段（只读）

| 字段 | 类型 | 说明 |
|------|------|------|
| `ctx.rawBody` | `string` | 客户端请求体原始字符串 |
| `ctx.method` | `string` | HTTP 方法（GET / POST / ...） |
| `ctx.url` | `string` | 完整请求 URL |
| `ctx.path` | `string` | URL 路径部分 |
| `ctx.query` | `string` | URL 查询字符串 |
| `ctx.headers` | `object` | 请求头键值对 |

#### 配置字段（只读）

| 字段 | 类型 | 说明 |
|------|------|------|
| `ctx.config` | `object` | `@config ... @end` 中定义的 YAML 配置，框架原样透传 |

#### 转发阶段（`onRequest` 可写）

| 字段 | 类型 | 说明 |
|------|------|------|
| `ctx.backendURL` | `string` | 后端目标 URL（如 `http://127.0.0.1:16686/api/v1`） |
| `ctx.backendRequest` | `object` | 转发到后端的请求体（JSON 对象，自动序列化） |
| `ctx.backendHeaders` | `object` | 转发到后端的自定义请求头 |

#### 响应阶段（框架写入，`onResponse` 读取/改写）

| 字段 | 类型 | 说明 |
|------|------|------|
| `ctx.backendResp` | `string` | 后端返回的原始响应体字符串 |
| `ctx.backendStatus` | `number` | 后端返回的 HTTP 状态码 |

#### 最终响应（`onResponse` 可写）

| 字段 | 类型 | 说明 |
|------|------|------|
| `ctx.finalResp` | `string` | 最终返回给客户端的响应体（JSON 字符串） |
| `ctx.finalStatus` | `number` | 最终返回给客户端的 HTTP 状态码 |
| `ctx.finalHeaders` | `object` | 最终返回给客户端的自定义响应头 |

#### 跨阶段数据

| 字段 | 类型 | 说明 |
|------|------|------|
| `ctx.metadata` | `object` | `onRequest` 和 `onResponse` 之间共享的元数据（自由使用） |

### 7.3 调试输出

使用 `console.log()` 输出调试信息，日志会被框架捕获并展示在 Web UI 中：

```javascript
console.log("Action: " + ctx.backendRequest.Action);
console.log("Region: " + ctx.backendRequest.Region);
```

### 7.4 完整示例

以下是一个完整的 V2 规则脚本，演示了 yunapi 格式与腾讯云 API 3.0 格式之间的双向转换：

```plaintext
# =============================================
# APM 本地联调规则（V2 完整示例）
# 包含：hosts 映射 + API 拦截 + 请求/响应转换
# =============================================
@name "APM 本地开发"
@priority 100

# ═══ 脚本私有配置 ═══
@config
backend:
  url: "http://127.0.0.1:16686"
  api_path: "/api/v1"
  timeout: 30
auth:
  uin: "1500000686"
  sub_account_uin: "700001199694"
  app_id: 251005942
region_map:
  1: "ap-guangzhou"
  4: "ap-shanghai"
  5: "ap-hongkong"
  8: "ap-beijing"
  9: "ap-singapore"
@end

# ═══ 请求转换（yunapi → TencentCloud API 3.0）═══
@script onRequest
  var body = JSON.parse(ctx.rawBody);
  var cfg = ctx.config;

  // 设置后端目标
  ctx.backendURL = cfg.backend.url + cfg.backend.api_path;

  // 构造后端请求体
  ctx.backendRequest = {
    Action: body.cmd,
    ApiModule: body.serviceType,
    Region: cfg.region_map[String(body.regionId)] || "ap-guangzhou"
  };

  // 注入认证信息
  ctx.backendRequest.Uin = body.ownerUin || cfg.auth.uin;
  ctx.backendRequest.SubAccountUin = body.uin || cfg.auth.sub_account_uin;
  ctx.backendRequest.AppId = cfg.auth.app_id;

  // 扁平化 data 字段（保护字段不可覆盖）
  var protectedKeys = {
    Uin: true, SubAccountUin: true, AppId: true,
    Action: true, ApiModule: true, Region: true
  };
  if (body.data) {
    var keys = Object.keys(body.data);
    for (var i = 0; i < keys.length; i++) {
      var k = keys[i];
      if (!protectedKeys[k]) {
        ctx.backendRequest[k] = body.data[k];
      }
    }
  }

  console.log("Action: " + ctx.backendRequest.Action +
              ", Region: " + ctx.backendRequest.Region);
@end

# ═══ 响应转换（TencentCloud API 3.0 → yunapi 三层嵌套）═══
@script onResponse
  var resp = JSON.parse(ctx.backendResp);

  // 提取错误码
  var innerCode = 0;
  if (resp.Response && resp.Response.Error) {
    innerCode = resp.Response.Error.Code || -1;
  }

  // 组装 yunapi 三层嵌套格式
  ctx.finalResp = JSON.stringify({
    code: 0,
    data: {
      code: innerCode,
      cgwerrorCode: 0,
      data: { Response: resp.Response || resp }
    },
    mccode: 0,
    errObj: {},
    seqId: "proxy-" + Date.now()
  });
@end

# ═══ MITM 拦截域名 ═══
@intercept console.cloud.tencent.com
@intercept console-hc.cloud.tencent.com
@intercept cloudcache.tencent-cloud.com

# ═══ Hosts 映射 ═══
9.223.72.139 console.cloud.tencent.com console-hc.cloud.tencent.com
139.199.214.200 cloudcache.tencent-cloud.com

# ═══ API 拦截（走 JS 脚本处理）═══
/\/cgi\/capi\?.*action=delegate&.*serviceType=apm/  script://self
/\/_api\/apm\//  script://self  @cors
```

---

## 8. 常见错误和修复

| 错误信息 | 原因 | 修复方法 |
|----------|------|----------|
| `unknown directive: @xxx` | 使用了不支持的 `@` 指令 | 检查指令名拼写，当前支持 `@name`、`@priority`、`@intercept`、`@config`、`@script` |
| `@priority requires a value` | `@priority` 后缺少数值 | 添加整数参数，如 `@priority 100` |
| `@priority value must be integer: xxx` | `@priority` 参数不是整数 | 确保传入合法整数 |
| `@intercept requires at least one domain` | `@intercept` 后缺少域名 | 添加至少一个域名参数 |
| `config block opened at line X but never closed (missing @end)` | `@config` 块缺少 `@end` | 在配置块末尾添加 `@end` |
| `script block opened at line X but never closed (missing @end)` | `@script` 块缺少 `@end` | 在脚本块末尾添加 `@end` |
| `parse @config YAML: ...` | `@config` 块中 YAML 格式错误 | 检查 YAML 缩进和语法 |
| `regex rule requires a target` | 正则规则缺少目标 | 在正则后添加 target（如 `http://...`、`script://self`） |
| `regex not properly delimited (expected /regex/ before target)` | 正则定界符 `/` 不完整 | 确保正则以 `/` 开头并以 `/` 结尾 |
| `empty regex pattern` | 正则内容为空 | 在 `/` 之间填写有效的正则表达式 |
| `invalid regex: ...` | 正则语法错误 | 检查正则表达式语法 |
| `proxy target should start with http:// or https://, got: xxx` | 代理目标 URL 格式不正确 | 目标必须以 `http://` 或 `https://` 开头 |
| `proxy rule target is empty` | 代理规则目标为空 | 添加有效的目标 URL |
| `hosts rule requires at least one domain` | hosts 规则只写了 IP，没有域名 | 在 IP 后添加至少一个域名 |
| `unrecognized rule format: xxx` | 行首内容不是 IP、域名/路径、正则或注释 | 检查行格式是否正确 |
| `regex complexity warning: nested quantifiers detected` | 正则中存在嵌套量词（如 `(a+)+`），可能导致灾难性回溯 | 简化正则结构，避免嵌套量词 |
| `regex complexity warning: pattern too long` | 正则长度超过 500 字符 | 缩短正则或拆分为多条规则 |

---

## 9. 最佳实践

### 9.1 优先级策略

```plaintext
# 高优先级：项目专用规则（如 APM 本地开发）
@priority 100

# 中优先级：通用测试环境
@priority 50

# 低优先级：兜底规则
@priority 0
```

建议：
- 项目专用规则设置较高优先级（100+）
- 通用环境规则设置中等优先级（50）
- 兜底/默认规则使用低优先级（0 或不设置）

### 9.2 注释规范

```plaintext
# ═══ 使用分隔线区分逻辑区块 ═══

# --- 子区块使用短分隔线 ---

# 行内注释说明单条规则的用途
/\/api\/v1\//  http://127.0.0.1:3000/api/v1/  @cors
```

建议：
- 用 `# ═══` 双线分隔主要区块
- 用 `# ---` 单线分隔子区块
- 复杂正则添加行内注释说明匹配意图

### 9.3 调试技巧

1. **使用 `console.log()` 打印关键变量**，日志会显示在 Web UI 的诊断面板中。

2. **分阶段验证**：先写 `onRequest`，确认后端请求正确；再写 `onResponse`，确认响应格式正确。

3. **利用 Web UI 的 Debug 面板**：可以输入模拟请求路径和请求体，逐步观察脚本执行结果。

4. **从简到繁**：先用最简单的正则规则验证连通性，再逐步添加 JS 脚本逻辑。

5. **关注解析错误**：Web UI 会展示每行解析错误的具体位置和修复建议，优先处理这些错误。

6. **正则调试**：正则内部的 `/` 无需强制转义（解析器会智能处理），但如果遇到匹配异常，可尝试显式写 `\/`。
