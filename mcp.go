package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// ========================================
// MCP Server (JSON-RPC 2.0 over HTTP)
// ========================================

type mcpServer struct {
	collector *DebugCollector
	connMgr   *ConnManager
	cfg       *Config
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP 工具定义
type mcpTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

type property struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

var mcpTools = []mcpTool{
	{
		Name:        "proxy_status",
		Description: "获取代理状态：连接数、请求统计、后端地址、运行时间",
		InputSchema: inputSchema{Type: "object", Properties: map[string]property{}},
	},
	{
		Name:        "proxy_requests",
		Description: "获取最近的请求列表，包含路径、状态码、延迟",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]property{
				"limit": {Type: "number", Description: "返回条数，默认 20"},
				"filter": {Type: "string", Description: "过滤路径关键词，如 'capi'、'apm'、'err'"},
			},
		},
	},
	{
		Name:        "proxy_errors",
		Description: "获取最近的错误请求列表",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]property{
				"limit": {Type: "number", Description: "返回条数，默认 20"},
			},
		},
	},
	{
		Name:        "proxy_connections",
		Description: "获取当前活跃的 MITM 连接数和历史淘汰统计",
		InputSchema: inputSchema{Type: "object", Properties: map[string]property{}},
	},
	{
		Name:        "proxy_curl",
		Description: "生成 curl 测试命令用于验证代理转发",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]property{
				"url":    {Type: "string", Description: "目标 URL（完整路径，如 https://example.com/api/test）"},
				"method": {Type: "string", Description: "HTTP 方法，默认 GET"},
				"data":   {Type: "string", Description: "POST 请求体（JSON 格式）"},
			},
		},
	},
}

func registerMCPRoutes(engine *gin.Engine, server *mcpServer) {
	engine.POST("/mcp", func(c *gin.Context) {
		var req jsonRPCRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, jsonRPCResponse{
				JSONRPC: "2.0", ID: nil,
				Error: &rpcError{Code: -32700, Message: "Parse error"},
			})
			return
		}

		resp := server.handleRPC(&req)
		c.JSON(http.StatusOK, resp)
	})

	// MCP SSE endpoint (for stdio-free clients)
	engine.GET("/mcp/sse", func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.String(http.StatusOK, "event: endpoint\ndata: /mcp\n\n")
	})
}

func (s *mcpServer) handleRPC(req *jsonRPCRequest) *jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return s.handleInit(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	default:
		return &jsonRPCResponse{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: -32601, Message: "Method not found: " + req.Method},
		}
	}
}

func (s *mcpServer) handleInit(req *jsonRPCRequest) *jsonRPCResponse {
	return &jsonRPCResponse{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "mitm-proxy",
				"version": "2.0.0",
			},
		},
	}
}

func (s *mcpServer) handleToolsList(req *jsonRPCRequest) *jsonRPCResponse {
	return &jsonRPCResponse{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]interface{}{
			"tools": mcpTools,
		},
	}
}

func (s *mcpServer) handleToolsCall(req *jsonRPCRequest) *jsonRPCResponse {
	var params struct {
		Name      string                 `json:"name"`
		Arguments map[string]interface{} `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &jsonRPCResponse{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: -32602, Message: "Invalid params"},
		}
	}

	var result interface{}
	var err *rpcError

	switch params.Name {
	case "proxy_status":
		result = s.toolStatus()
	case "proxy_requests":
		result = s.toolRequests(params.Arguments)
	case "proxy_errors":
		result = s.toolErrors(params.Arguments)
	case "proxy_connections":
		result = s.toolConnections()
	case "proxy_curl":
		result = s.toolCurl(params.Arguments)
	default:
		err = &rpcError{Code: -32602, Message: "Unknown tool: " + params.Name}
	}

	return &jsonRPCResponse{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": toJSON(result)},
			},
		},
		Error: err,
	}
}

// ========================================
// 工具实现
// ========================================

func (s *mcpServer) toolStatus() interface{} {
	_, total, errs, uptime := s.collector.Snapshot()
	var active, evicted int64
	if s.connMgr != nil {
		active, evicted = s.connMgr.Stats()
	}

	return map[string]interface{}{
		"uptime_sec":    int(uptime.Seconds()),
		"total_requests": total,
		"total_errors":   errs,
		"error_rate_pct": float64(errs) / float64(max(total, 1)) * 100,
		"connections": map[string]interface{}{
			"active_mitm":  active,
			"total_evicted": evicted,
		},
		"backend": s.cfg.Backend.URL + s.cfg.Backend.APIPath,
		"proxy_port": s.cfg.Proxy.Listen,
	}
}

func (s *mcpServer) toolRequests(args map[string]interface{}) interface{} {
	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}
	filter := ""
	if f, ok := args["filter"].(string); ok {
		filter = f
	}

	recent, _, _, _ := s.collector.Snapshot()

	var filtered []*RequestTrace
	for i := len(recent) - 1; i >= 0 && len(filtered) < limit; i-- {
		r := recent[i]
		if filter == "" || contains(r.Path, filter) || contains(r.Host, filter) {
			filtered = append(filtered, r)
		}
	}

	// 转换为可读格式
	var list []map[string]interface{}
	for _, r := range filtered {
		status := fmt.Sprintf("%d", r.StatusCode)
		if r.Error != "" {
			status = "ERR: " + r.Error
		}
		list = append(list, map[string]interface{}{
			"time":      r.Time.Format("15:04:05"),
			"method":    r.Method,
			"path":      r.Path,
			"status":    status,
			"latency_ms": r.LatencyMs,
			"intercept": r.Intercept,
		})
	}
	return map[string]interface{}{
		"count":   len(list),
		"filter":  filter,
		"requests": list,
	}
}

func (s *mcpServer) toolErrors(args map[string]interface{}) interface{} {
	limit := 20
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	recent, _, _, _ := s.collector.Snapshot()

	var errors []map[string]interface{}
	for i := len(recent) - 1; i >= 0 && len(errors) < limit; i-- {
		r := recent[i]
		if r.StatusCode >= 400 || r.Error != "" {
			errors = append(errors, map[string]interface{}{
				"time":      r.Time.Format("15:04:05"),
				"method":    r.Method,
				"path":      r.Path,
				"status":    r.StatusCode,
				"latency_ms": r.LatencyMs,
				"error":     r.Error,
			})
		}
	}

	_, _, errs, _ := s.collector.Snapshot()
	return map[string]interface{}{
		"count":      len(errors),
		"total_errors": errs,
		"errors":     errors,
	}
}

func (s *mcpServer) toolConnections() interface{} {
	if s.connMgr == nil {
		return map[string]interface{}{"enabled": false}
	}
	active, evicted := s.connMgr.Stats()
	return map[string]interface{}{
		"active_mitm":   active,
		"total_evicted": evicted,
		"max_connections": s.connMgr.maxConns,
	}
}

func (s *mcpServer) toolCurl(args map[string]interface{}) interface{} {
	targetURL := "https://example.com/api/test"
	method := "GET"
	data := ""

	if u, ok := args["url"].(string); ok && u != "" {
		targetURL = u
	}
	if m, ok := args["method"].(string); ok && m != "" {
		method = m
	}
	if d, ok := args["data"].(string); ok && d != "" {
		data = d
	}

	var curl string
	if data != "" {
		curl = fmt.Sprintf(
			`curl --proxy http://127.0.0.1%s -k -s -X %s \
  '%s' \
  -H 'Content-Type: application/json' \
  --data-raw '%s'`,
			s.cfg.Proxy.Listen, method, targetURL, data,
		)
	} else {
		curl = fmt.Sprintf(
			`curl --proxy http://127.0.0.1%s -k -s -X %s '%s'`,
			s.cfg.Proxy.Listen, method, targetURL,
		)
	}

	return map[string]interface{}{
		"curl":   curl,
		"method": method,
		"url":    targetURL,
		"proxy":  "http://127.0.0.1" + s.cfg.Proxy.Listen,
	}
}

// ========================================
// 辅助函数
// ========================================

func toJSON(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && len(sub) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
