package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"mitm-proxy/rule"
)

//go:embed static/*
var staticFS embed.FS

// ========================================
// SSE 事件广播器
// ========================================

// Event SSE 推送的事件
type Event struct {
	Type      string `json:"type"`       // match | forward | tunnel | error | reload
	Time      string `json:"time"`       // ISO8601 时间戳
	Method    string `json:"method"`     // HTTP 方法
	URL       string `json:"url"`        // 请求 URL
	Status    int    `json:"status"`     // 响应状态码
	LatencyMs int64  `json:"latency_ms"` // 耗时（毫秒）
	Script    string `json:"script"`     // 匹配的脚本名
	RuleType  string `json:"rule_type"`  // 规则类型
	Target    string `json:"target"`     // 目标地址
	Error     string `json:"error,omitempty"`
}

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

// RegisterRoutes 注册 Web UI 路由
func RegisterRoutes(engine *gin.Engine, manager *rule.RuleManager, broadcaster *EventBroadcaster, statusProvider StatusProvider) {
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
			totalHits += s.Stats.TotalHits
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
		c.JSON(http.StatusOK, gin.H{
			"id":      id,
			"content": string(content),
		})
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
