# JS 脚本 API 参考

> 适用于 `@script onRequest` / `@script onResponse` 块中的 JavaScript 代码
> 引擎: goja (ES5.1+ with partial ES6: const/let, arrow functions, template literals)

## ctx 对象

框架在每个请求阶段注入的上下文对象，读/写字段决定了请求处理流程。

### 只读字段（输入）

| 字段 | 类型 | 说明 |
|------|------|------|
| `ctx.rawBody` | `string` | 原始请求体 |
| `ctx.method` | `string` | HTTP 方法（如 POST） |
| `ctx.url` | `string` | 完整请求 URL |
| `ctx.path` | `string` | URL 路径 |
| `ctx.query` | `string` | URL 查询字符串 |
| `ctx.headers` | `object` | 请求头（key → value） |

### 私有配置（只读）

| 字段 | 类型 | 说明 |
|------|------|------|
| `ctx.config` | `object` | `@config...@end` 段内容，框架透传，由脚本自行管理 |

### 转发阶段（onRequest 可写）

| 字段 | 类型 | 说明 |
|------|------|------|
| `ctx.backendURL` | `string` | **[可写]** 转发目标 URL，onRequest 必须设置 |
| `ctx.backendRequest` | `object` | **[可写]** 转发请求体（JSON 对象） |
| `ctx.backendHeaders` | `object` | **[可写]** 转发请求头 |

### 响应阶段（onResponse 输入/输出）

| 字段 | 类型 | 说明 |
|------|------|------|
| `ctx.backendResp` | `string` | **[只读]** 后端响应体（JSON 字符串） |
| `ctx.backendStatus` | `number` | **[只读]** 后端响应状态码 |
| `ctx.finalResp` | `string` | **[可写]** 最终返回给客户端的响应体 |
| `ctx.finalStatus` | `number` | **[可写]** 最终响应状态码（默认 200） |
| `ctx.finalHeaders` | `object` | **[可写]** 最终响应头 |

### 跨阶段传递

| 字段 | 类型 | 说明 |
|------|------|------|
| `ctx.metadata` | `object` | onRequest → onResponse 间传递临时数据 |

## console 对象

日志输出到 Web UI 调试面板的控制台区域。

```javascript
console.log("info message");
console.warn("warning message");
console.error("error message");
```

## JSON 处理

goja 内置完整的 `JSON.parse()` 和 `JSON.stringify()`。

```javascript
var body = JSON.parse(ctx.rawBody);
ctx.backendRequest = { Action: body.cmd };
ctx.finalResp = JSON.stringify({ code: 0, data: resp });
```

## 完整示例

### onRequest: yunapi → TencentCloud API 3.0

```javascript
var body = JSON.parse(ctx.rawBody);
var cfg = ctx.config;

ctx.backendURL = cfg.backend.url + cfg.backend.api_path;
ctx.backendRequest = {
  Action: body.cmd,
  ApiModule: body.serviceType,
  Region: cfg.region_map[String(body.regionId)] || "ap-guangzhou"
};

// 认证注入
ctx.backendRequest.Uin = body.ownerUin || cfg.auth.uin;
ctx.backendRequest.SubAccountUin = body.uin || cfg.auth.sub_account_uin;
ctx.backendRequest.AppId = cfg.auth.app_id;

// 扁平化 data 字段
var protectedKeys = { Uin: true, SubAccountUin: true, AppId: true, Action: true };
if (body.data) {
  var keys = Object.keys(body.data);
  for (var i = 0; i < keys.length; i++) {
    var k = keys[i];
    if (!protectedKeys[k]) ctx.backendRequest[k] = body.data[k];
  }
}

console.log("Action: " + ctx.backendRequest.Action);
```

### onResponse: TencentCloud API 3.0 → yunapi

```javascript
var resp = JSON.parse(ctx.backendResp);

var innerCode = 0;
if (resp.Response && resp.Response.Error) {
  innerCode = resp.Response.Error.Code || -1;
}

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
```

## 限制

- 单次执行超时 5 秒，超时后中断执行
- 无 `async/await`、无 `Promise`（goja ES5.1+ 限制）
- 无文件系统访问、无网络访问（沙箱安全）
- 变量声明用 `var`（ES5）或 `let`/`const`（goja 部分支持）
- 模板字符串 `` `hello ${name}` `` 支持
