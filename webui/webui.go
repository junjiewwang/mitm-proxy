package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"mitm-proxy/core/event"
	"mitm-proxy/core/ca"
	"mitm-proxy/core/replay"
	"mitm-proxy/core/script"
	"mitm-proxy/core/security"
	"mitm-proxy/rule"
)

//go:embed static/*
var staticFS embed.FS

// ========================================
// SSE 事件广播器
// ========================================

// Event 代理请求事件（类型别名，指向 core/event.Event）
type Event = event.Event

// EventBroadcaster SSE 事件广播器（多客户端）
type EventBroadcaster struct {
	mu      sync.RWMutex
	clients map[chan *Event]struct{}
	buffer  []*Event // 环形缓冲区
	pos     int
	size    int
}

// NewEventBroadcaster 创建事件广播器
func NewEventBroadcaster(bufferSize int) *EventBroadcaster {
	if bufferSize <= 0 {
		bufferSize = 200
	}
	return &EventBroadcaster{
		clients: make(map[chan *Event]struct{}),
		buffer:  make([]*Event, bufferSize),
		size:    bufferSize,
	}
}

// Subscribe 注册 SSE 客户端
func (b *EventBroadcaster) Subscribe() chan *Event {
	ch := make(chan *Event, 64)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe 注销 SSE 客户端
func (b *EventBroadcaster) Unsubscribe(ch chan *Event) {
	b.mu.Lock()
	delete(b.clients, ch)
	close(ch)
	b.mu.Unlock()
}

// Broadcast 广播事件到所有客户端
func (b *EventBroadcaster) Broadcast(evt *Event) {
	b.mu.Lock()
	// 写入环形缓冲区
	b.buffer[b.pos] = evt
	b.pos = (b.pos + 1) % b.size

	// 广播到所有客户端（非阻塞写入，满则丢弃）
	for ch := range b.clients {
		select {
		case ch <- evt:
		default:
			// 客户端消费太慢，丢弃事件
		}
	}
	b.mu.Unlock()
}

// RecentEvents 获取最近的事件列表
func (b *EventBroadcaster) RecentEvents(limit int) []*Event {
	b.mu.RLock()
	defer b.mu.RUnlock()

	events := make([]*Event, 0, limit)
	for i := 0; i < b.size; i++ {
		idx := (b.pos - 1 - i + b.size) % b.size
		if b.buffer[idx] == nil {
			break
		}
		events = append(events, b.buffer[idx])
	}
	// 反转为时间正序
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events
}

// ClientCount 当前连接的 SSE 客户端数
func (b *EventBroadcaster) ClientCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

// ========================================
// WebUI 注册
// ========================================

// StatusProvider 系统状态提供者接口（由 main.go 注入）
type StatusProvider interface {
	GetStatus() map[string]interface{}
}

// RequestFilter 请求过滤参数
type RequestFilter struct {
	Type     string `form:"type"`      // 事件类型：match/forward/tunnel/error/script
	Script   string `form:"script"`    // 脚本名称
	RuleType string `form:"rule_type"` // 规则类型：proxy/exact/script/hosts
	URL      string `form:"url"`       // URL 关键词（大小写不敏感）
	Status   string `form:"status"`    // 状态码范围：2xx/3xx/4xx/5xx 或具体状态码
	Limit    int    `form:"limit"`     // 分页大小，默认 100
	Offset   int    `form:"offset"`    // 偏移量，默认 0
}

// matchFilter 判断事件是否匹配过滤条件
func (f *RequestFilter) matchFilter(evt *Event) bool {
	if evt == nil {
		return false
	}
	if f.Type != "" && evt.Type != f.Type {
		return false
	}
	if f.Script != "" && evt.Script != f.Script {
		return false
	}
	if f.RuleType != "" && evt.RuleType != f.RuleType {
		return false
	}
	if f.URL != "" && !strings.Contains(strings.ToLower(evt.URL), strings.ToLower(f.URL)) {
		return false
	}
	if f.Status != "" && !matchStatusFilter(evt.Status, f.Status) {
		return false
	}
	return true
}

// matchStatusFilter 判断状态码是否匹配过滤条件
func matchStatusFilter(status int, filter string) bool {
	switch filter {
	case "2xx":
		return status >= 200 && status < 300
	case "3xx":
		return status >= 300 && status < 400
	case "4xx":
		return status >= 400 && status < 500
	case "5xx":
		return status >= 500 && status < 600
	default:
		// 尝试解析为具体状态码
		var code int
		if _, err := fmt.Sscanf(filter, "%d", &code); err == nil {
			return status == code
		}
		return true // 无法解析则不过滤
	}
}

// RegisterRoutes 注册 Web UI 路由
func RegisterRoutes(engine *gin.Engine, manager *rule.RuleManager, broadcaster *EventBroadcaster, statusProvider StatusProvider, debugger *script.Debugger, replayer replay.Replayer, caManager *ca.Manager) {
	// 静态资源（embed.FS）
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Printf("[WebUI] WARNING: embed static fs failed: %v", err)
		return
	}

	// UI 入口路由
	engine.GET("/ui", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/ui/")
	})
	engine.GET("/ui/*filepath", func(c *gin.Context) {
		filepath := c.Param("filepath")
		if filepath == "/" || filepath == "" {
			filepath = "/index.html"
		}

		// 优先从本地 webui/static 读取（开发模式热更新）
		localPath := "webui/static" + filepath
		if _, err := os.Stat(localPath); err == nil {
			c.File(localPath)
			return
		}

		// 回退到 embed.FS
		http.StripPrefix("/ui/", http.FileServer(http.FS(staticSub))).ServeHTTP(c.Writer, c.Request)
	})

	// SSE 实时事件推送
	engine.GET("/api/events", func(c *gin.Context) {
		handleSSE(c, broadcaster)
	})

	// 系统状态 API
	engine.GET("/api/status", func(c *gin.Context) {
		status := make(map[string]interface{})

		// 注入的系统状态
		if statusProvider != nil {
			for k, v := range statusProvider.GetStatus() {
				status[k] = v
			}
		}

		// 规则引擎状态
		scripts := manager.List()
		enabledCount := 0
		totalRules := 0
		totalHits := int64(0)
		for _, s := range scripts {
			if s.Enabled {
				enabledCount++
			}
			totalRules += len(s.Rules)
			totalHits += s.Stats.Hits()
		}
		status["rules"] = map[string]interface{}{
			"total_scripts":   len(scripts),
			"enabled_scripts": enabledCount,
			"total_rules":     totalRules,
			"total_hits":      totalHits,
		}

		// SSE 客户端数
		status["sse_clients"] = broadcaster.ClientCount()

		c.JSON(http.StatusOK, status)
	})

	// 获取规则脚本文件内容（供编辑器使用）
	engine.GET("/api/rules/:id/content", func(c *gin.Context) {
		id := c.Param("id")
		script, ok := manager.Get(id)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "script not found"})
			return
		}
		// 读取原始文件内容
		content, err := os.ReadFile(script.FilePath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "read file failed"})
			return
		}

		// 分类 parse 错误/警告
		var parseErrors, parseWarnings []rule.ParseError
		for _, pe := range script.ParseErrors {
			if pe.Severity == "warning" {
				parseWarnings = append(parseWarnings, pe)
			} else {
				parseErrors = append(parseErrors, pe)
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"id":             id,
			"content":        string(content),
			"parse_errors":   parseErrors,
			"parse_warnings": parseWarnings,
		})
	})

	// JS 脚本调试端点
	engine.POST("/api/rules/:id/debug", func(c *gin.Context) {
		if debugger == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "debugger not available"})
			return
		}

		id := c.Param("id")
		var req script.DebugRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// 获取脚本
		rs, ok := manager.Get(id)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "script not found"})
			return
		}

		// 执行规则匹配（仅作为提示，不阻止后续执行：用户手动指定 script 即按指定执行）
		matched := false
		engine := manager.GetEngine()
		if engine != nil && req.Path != "" {
			// SSRF 防护：调试端点允许内网（dry run），仅校验 scheme
			if err := security.RequireExternal(req.Path); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path: " + err.Error()})
				return
			}
			// 构建假请求用于规则匹配
			fakeReq, _ := http.NewRequest(req.Method, req.Path, nil)
			if fakeReq == nil {
				fakeReq, _ = http.NewRequest("POST", req.Path, nil)
			}
			if fakeReq != nil && fakeReq.URL != nil {
				if fakeReq.URL.RawQuery == "" && strings.Contains(req.Path, "?") {
					idx := strings.Index(req.Path, "?")
					pathPart := req.Path[:idx]
					queryPart := req.Path[idx+1:]
					fakeReq.URL.Path = pathPart
					fakeReq.URL.RawQuery = queryPart
				}
				if _, m := engine.Match(fakeReq); m {
					matched = true
				}
			}
		}

		// 注意：用户手动选了 script id，意图就是跑这个 script（即使 disabled 或不被 match 命中）
		// 这里的 matched 仅作后续步骤的提示，不再作为是否执行的硬性条件

		// 执行调试
		hasOnReq := rs.Scripts["onRequest"] != ""
		hasOnResp := rs.Scripts["onResponse"] != ""
		result := debugger.Execute(req, id, rs.Config, rs.CompiledPrograms, hasOnReq, hasOnResp, matched)
		c.JSON(http.StatusOK, result)
	})

	// 诊断端点：一目了然的代理状态 + 静态资源代理验证
	engine.GET("/api/diagnostics", func(c *gin.Context) {
		result := make(map[string]interface{})

		// 规则引擎状态
		scripts := manager.List()
		enabledCount := 0
		totalRules := 0
		totalHits := int64(0)
		scriptDetails := make([]map[string]interface{}, 0)
		for _, s := range scripts {
			if s.Enabled { enabledCount++ }
			totalRules += len(s.Rules)
			totalHits += s.Stats.Hits()
			scriptDetails = append(scriptDetails, map[string]interface{}{
				"id":       s.ID,
				"name":     s.Name,
				"enabled":  s.Enabled,
				"priority": s.Priority,
				"rules":    len(s.Rules),
				"intercepts": len(s.Intercepts),
				"has_js":   len(s.Scripts) > 0,
				"hits":     s.Stats.Hits(),
			})
		}
		result["scripts"] = scriptDetails
		result["summary"] = map[string]interface{}{
			"total_scripts":   len(scripts),
			"enabled_scripts": enabledCount,
			"total_rules":     totalRules,
			"total_hits":      totalHits,
		}

		// 最近代理匹配事件（从 broadcaster 环形缓冲区取最新 1000 条）
		// 按 rule_type 分类，避免高频 cgi/capi 淹没低频前端资源事件
		var proxyEvents, scriptEvents []*Event
		for _, evt := range broadcaster.RecentEvents(1000) {
			if evt.Type != "match" { continue }
			if evt.RuleType == "script" {
				scriptEvents = append(scriptEvents, evt)
			} else {
				proxyEvents = append(proxyEvents, evt)
			}
		}
		// 各取最新 20 条
		toResult := func(events []*Event) []map[string]interface{} {
			if len(events) > 20 { events = events[len(events)-20:] }
			out := make([]map[string]interface{}, 0, len(events))
			for _, evt := range events {
				m := map[string]interface{}{
					"time":       evt.Time,
					"method":     evt.Method,
					"url":        evt.URL,
					"script":     evt.Script,
					"target":     evt.Target,
					"rule_type":  evt.RuleType,
					"status":     evt.Status,
					"latency_ms": evt.LatencyMs,
				}
				if evt.Error != "" { m["error"] = evt.Error }
				out = append(out, m)
			}
			return out
		}
		result["proxy_events"] = toResult(proxyEvents)
		result["script_events"] = toResult(scriptEvents)
		result["recent_matches"] = toResult(append(proxyEvents, scriptEvents...))

		// hosts 冲突检测结果
		ruleEngine := manager.GetEngine()
		if ruleEngine != nil {
			conflicts := ruleEngine.HostsConflicts()
			result["hosts_conflicts"] = conflicts
			result["hosts_count"] = len(ruleEngine.HostsSnapshot())
		}

		c.JSON(http.StatusOK, result)
	})

	// 请求追踪列表 API（支持过滤+分页）
	engine.GET("/api/requests", func(c *gin.Context) {
		var filter RequestFilter
		if err := c.ShouldBindQuery(&filter); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid query params"})
			return
		}

		// 默认值
		if filter.Limit <= 0 || filter.Limit > 1000 {
			filter.Limit = 100
		}
		if filter.Offset < 0 {
			filter.Offset = 0
		}

		// 从 broadcaster 环形缓冲区获取所有事件
		allEvents := broadcaster.RecentEvents(2000)

		// 过滤
		filtered := make([]*Event, 0, len(allEvents))
		for _, evt := range allEvents {
			if filter.matchFilter(evt) {
				filtered = append(filtered, evt)
			}
		}

		total := len(filtered)

		// 分页
		if filter.Offset >= total {
			filtered = nil
		} else {
			end := filter.Offset + filter.Limit
			if end > total {
				end = total
			}
			filtered = filtered[filter.Offset:end]
		}

		c.JSON(http.StatusOK, gin.H{
			"total":   total,
			"limit":   filter.Limit,
			"offset":  filter.Offset,
			"results": filtered,
		})
	})

	// 脚本列表（供前端过滤下拉使用）
	engine.GET("/api/scripts", func(c *gin.Context) {
		scripts := manager.List()
		type scriptInfo struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		}
		result := make([]scriptInfo, 0, len(scripts))
		for _, s := range scripts {
			result = append(result, scriptInfo{
				ID:      s.ID,
				Name:    s.Name,
				Enabled: s.Enabled,
			})
		}
		c.JSON(http.StatusOK, gin.H{"scripts": result})
	})

	// 请求重放：通过代理重新发送编辑后的请求（MITM → 规则匹配 → JS 脚本全部生效）
	engine.POST("/api/replay", func(c *gin.Context) {
		if replayer == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "replay is not configured"})
			return
		}
		var req replay.Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.URL == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "url is required"})
			return
		}
		if req.Method == "" {
			req.Method = "GET"
		}

		resp, err := replayer.Replay(c.Request.Context(), req)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, resp)
	})

	// CA 管理 API
	engine.GET("/api/ca/status", func(c *gin.Context) {
		if caManager == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CA not loaded"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"fingerprint": caManager.Fingerprint(),
			"stores":      caManager.CheckTrust(),
		})
	})
	engine.GET("/api/ca/download", func(c *gin.Context) {
		if caManager == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "CA not loaded"})
			return
		}
		format := c.Query("format")
		switch format {
		case "mobileconfig":
			data, err := caManager.MobileConfig()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			c.Header("Content-Type", "application/x-apple-aspen-config")
			c.Header("Content-Disposition", "attachment; filename=mitm-proxy-ca.mobileconfig")
			c.Data(http.StatusOK, "application/x-apple-aspen-config", data)
		case "crt":
			c.Header("Content-Type", "application/x-x509-ca-cert")
			c.Header("Content-Disposition", "attachment; filename=mitm-proxy-ca.crt")
			c.Data(http.StatusOK, "application/x-x509-ca-cert", caManager.PEM())
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid format, use mobileconfig or crt"})
		}
	})

	log.Printf("[WebUI] Registered at /ui/ (SSE at /api/events)")
}

// handleSSE 处理 SSE 连接
func handleSSE(c *gin.Context, b *EventBroadcaster) {
	// 设置 SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

	// 订阅事件
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	// 发送最近的历史事件（客户端重连时恢复上下文）
	recent := b.RecentEvents(50)
	for _, evt := range recent {
		data, _ := json.Marshal(evt)
		fmt.Fprintf(c.Writer, "data: %s\n\n", data)
	}
	c.Writer.Flush()

	// 客户端断开检测
	clientGone := c.Request.Context().Done()

	// 心跳定时器
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-clientGone:
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			c.Writer.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(c.Writer, ": heartbeat\n\n")
			c.Writer.Flush()
		}
	}
}
