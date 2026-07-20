package replay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mitm-proxy/core/security"
)

// Request 重放请求参数
type Request struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Response 重放响应
type Response struct {
	StatusCode  int               `json:"status"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
	LatencyMs   int64             `json:"latency_ms"`
	ContentType string            `json:"content_type,omitempty"`
}

// Replayer 请求重放接口
type Replayer interface {
	// Replay 通过代理发送请求并返回响应。
	// 返回的 *http.Response.Body 已被完整读取，调用方无需再 Close。
	Replay(ctx context.Context, req Request) (*Response, error)
}

// ProxyReplayer 通过正向代理发送重放请求的实现
type ProxyReplayer struct {
	client *http.Client
}

// NewProxyReplayer 创建通过指定代理地址发送请求的重放器。
// proxyAddr 格式如 ":8081"、"127.0.0.1:8081"、"0.0.0.0:8081"。
func NewProxyReplayer(proxyAddr string) (*ProxyReplayer, error) {
	addr := normalizeProxyAddr(proxyAddr)
	proxyURL, err := url.Parse("http://" + addr)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy address %q: %w", proxyAddr, err)
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
	}

	return &ProxyReplayer{
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}, nil
}

// Replay 通过代理发送请求并返回响应
func (r *ProxyReplayer) Replay(ctx context.Context, req Request) (*Response, error) {
	if req.URL == "" {
		return nil, fmt.Errorf("url is required")
	}
	// SSRF 防护：拒绝内网地址和非 http/https scheme
	if err := security.ValidateExternalURL(req.URL); err != nil {
		return nil, err
	}
	if req.Method == "" {
		req.Method = "GET"
	}

	start := time.Now()

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, strings.NewReader(req.Body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	httpReq.Header.Set("X-Replay", "true") // 标记为重放，供代理引擎识别

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	respHeaders := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	return &Response{
		StatusCode:  resp.StatusCode,
		Headers:     respHeaders,
		Body:        string(bodyBytes),
		LatencyMs:   time.Since(start).Milliseconds(),
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

// normalizeProxyAddr 规范化代理地址，使 ":8081" → "127.0.0.1:8081"
func normalizeProxyAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	if !strings.Contains(addr, "://") && !strings.Contains(addr, ":") {
		return addr + ":8081"
	}
	return addr
}
