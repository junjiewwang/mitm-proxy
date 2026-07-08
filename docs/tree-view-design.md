# 树状分组设计方案

## 目标
Requests 表格按域名分组折叠，解决平铺列表翻找慢的问题。

## UI 效果

```
[🌲 Tree]  [⏸ Pause]  [↻ Refresh]  [🗑 Clear]  87 events

🔽 console.cloud.tencent.com (12)          avg 312ms
   Timestamp  Type   Method  URL                    Status   CType  Size   Latency  Script    Rule    Target
   17:03:10   match  POST    /cgi/capi?action=del..  200     json   569B   658ms    APM本地开发 script
   17:03:11   match  POST    /cgi/capi?action=get..  200     json   1.2K   234ms    APM本地开发 script
   ...

▶ cloudcache.tencent-cloud.com (45)        avg 8ms

▶ otheve.beacon.qq.com (8)                 avg 3ms
```

- 🔽/▶ 点击展开/折叠
- 分组头显示：域名、请求数、平均延迟
- 组内行完整保留现有 11 列
- SSE 新请求自动追加到对应分组

## 实现

### 新增 Toggle 按钮 (index.html)
```
[🌲 Tree] 切换平面列表/树状视图
```

### 新增函数 (app.js)

1. **`extractHostname(url)`** — 从完整 URL 提取 hostname
2. **`renderGroupHeader(hostname, count, avgMs)`** — 渲染分组折叠头
3. **`renderTreeView()`** — 树状渲染主入口
4. **`toggleTreeView()`** — 切换模式

### 核心逻辑

```
state.treeView = true  // 默认开启

renderTreeView():
  1. 按 hostname 分组 state.requests[]
  2. 按组内数量降序排列组
  3. 渲染组头 + 组内行
  4. 记录已展开的组，恢复状态
```

### SSE 追加 (onReqEvent)

```
新请求到达 → 找到对应组 → 组头已存在则更新计数，否则新建组 → 行插入组内
```

### CSS
```
.tree-group-header: hover 效果 + 展开图标
.tree-group-children: 缩进 0（用背景色区分）
```

## 不改动
- ❌ 不修改后端
- ❌ 不修改过滤逻辑
- ❌ 不修改详情面板
- ❌ SSE 广播器不变

## 预估
0.5d
