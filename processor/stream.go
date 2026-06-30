package processor

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"time"

	"mitm-proxy/proxy"
)

// StreamProcessor SSE/chunked 流式反向代理
// 实现 RequestProcessor 接口
// 与 ProxyProcessor 的区别：不读完响应 Body，保持为流式 io.ReadCloser
type StreamProcessor struct {
	client *http.Client
}

// NewStreamProcessor 创建流式代理处理器
func NewStreamProcessor() *StreamProcessor {
	return &StreamProcessor{
		client: &http.Client{
			// 流式传输不设全局超时（SSE 长连接可能持续数小时）
			Timeout: 0,
			Transport: &http.Transport{
				MaxIdleConnsPerHost:   10,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second, // 只限制获取 header 的时间
			},
		},
	}
}

// Process 流式代理：转发请求，响应 Body 保持为流
func (s *StreamProcessor) Process(req *http.Request, body []byte, match *proxy.MatchResult) *http.Response {
	targetURL := match.Target
	log.Printf("[StreamProcessor] %s %s → %s (stream)", req.Method, req.URL.String(), targetURL)

	// 构建代理请求
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	proxyReq, err := http.NewRequest(req.Method, targetURL, bodyReader)
	if err != nil {
		log.Printf("[StreamProcessor] create request failed: %v", err)
		return buildErrorResp(http.StatusBadGateway, "create upstream request failed")
	}

	// 复制 Header（排除 hop-by-hop）
	for k, vv := range req.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}

	// 转发
	resp, err := s.client.Do(proxyReq)
	if err != nil {
		log.Printf("[StreamProcessor] forward failed: %v", err)
		return buildErrorResp(http.StatusBadGateway, "upstream request failed: "+err.Error())
	}

	// 流式传输：不做 io.ReadAll
	// Body 保持为 io.ReadCloser 流，http.Response.Write 会自动 chunked 写出
	// caller（writer goroutine）负责 Close
	respHeader := copyHeaders(resp.Header)
	return &http.Response{
		StatusCode:    resp.StatusCode,
		Status:        resp.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        respHeader,
		Body:          resp.Body,   // 流式传递，不读入内存
		ContentLength: -1,          // chunked
	}
}
