# Hosts 规则冲突检测机制

## 背景

`hosts.rules` 文件中 `cloudcache.tencent-cloud.com` 被定义了两次（第13行 `139.199.214.200` 和第302行 `9.223.72.139`），由于同一脚本内无检测机制，第302行静默覆盖第13行，导致代理用 HTTPS 连接一个只支持 HTTP 的内网地址，产生 `http: server gave HTTP response to HTTPS client` 错误。

## 实施方案：双层防护 + 明确覆盖语义

### 1. 解析期检测（同脚本内）

在 `parser.go` 的 `ParseContent()` 中维护 `seenHosts` 映射，对同一脚本内重复定义的 hosts 域名产生 **warning** 级别 ParseError。

- Severity: `warning`（不阻止加载）
- 精确标记到行号和列号
- 提供 Suggestion 引导用户正确处理

### 2. 编译期检测（跨脚本）

在 `engine.go` 的 `Compile()` 中记录每个域名的来源（脚本ID、行号、IP），检测到覆盖时输出详细日志并通过 API 暴露。

- 输出格式：`[RuleEngine] ⚠ hosts conflict: domain (winner) overrides (loser)`
- 跨脚本覆盖遵循 `@priority` 高优先级覆盖低优先级
- 同脚本内覆盖遵循行号"后来居上"

### 3. 运行时快照 API

- `HostsSnapshot()`: 返回当前生效的 hosts 映射
- `HostsConflicts()`: 返回最近一次编译检测到的冲突列表
- 通过 `/api/diagnostics` 端点暴露给 Web UI

### 4. 覆盖语义规则

- 同一脚本内：后面的定义覆盖前面的（last-write-wins）
- 跨脚本：`@priority` 高优先级的脚本覆盖低优先级
- 同优先级跨脚本：按脚本加载顺序，后加载的覆盖先加载的

## 变更清单

| 文件 | 改动 |
|---|---|
| `rule/parser.go` | ParseContent 中新增 seenHosts 去重检测 |
| `rule/engine.go` | Compile 中新增冲突检测+日志，新增 HostsSnapshot/HostsConflicts 方法，新增 HostsConflict 结构体 |
| `rule/manager.go` | List() 稳定排序：同 priority 按 ID 字母序升序 |
| `cmd/mitm-proxy/ruleapi.go` | /api/rules 接口新增 hosts_conflicts/hosts_count 字段 |
| `webui/webui.go` | diagnostics API 中新增 hosts_conflicts 字段 |
| `webui/static/index.html` | Rules 页新增可折叠 hosts 冲突 banner，移除 Status 页旧面板 |
| `webui/static/app.js` | renderHostsConflicts() 迁至 Rules 页，loadScripts() 绑定冲突数据，新增折叠交互 |
| `webui/static/style.css` | hosts-conflicts-banner 样式替换旧 card 样式 |
| `rules/hosts.rules` | 删除第302行重复映射，添加覆盖语义注释 |

## 未完成任务

- [x] 解析期检测
- [x] 编译期检测+日志
- [x] 运行时 API 暴露
- [x] 规则文件注释 + 清理重复条目
- [x] 前端冲突面板从 Status 页迁至 Rules 页
- [x] 规则列表稳定排序

## 验证结果

使用以下测试内容验证了三层检测均正常工作：

```
@name "Test"
@priority 50
139.199.214.200 cloudcache.tencent-cloud.com
9.223.72.139 cloudcache.tencent-cloud.com
```

**解析期**：产生 1 条 warning，精确标记到行号和 IP：
```
severity: warning
message: duplicate hosts mapping for "cloudcache.tencent-cloud.com"
  (first defined at line 3 with IP 139.199.214.200,
   will be overridden by line 4 with IP 9.223.72.139)
suggestion: Remove the duplicate mapping for cloudcache.tencent-cloud.com
  at line 3, or use separate rule scripts with @priority to manage precedence explicitly
```

**编译期**：日志输出 1 条冲突：
```
[RuleEngine] ⚠ 1 hosts conflict(s) detected:
[RuleEngine]   cloudcache.tencent-cloud.com: line 4 (9.223.72.139:443)
  overrides line 3 (139.199.214.200:443) [same script: ]
```

**运行时**：`HostsSnapshot()` 返回最终生效映射 `cloudcache.tencent-cloud.com → 9.223.72.139:443`，`HostsConflicts()` 正确返回冲突详情，覆盖语义确认为 last-write-wins。
