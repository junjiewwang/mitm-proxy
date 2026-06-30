package processor

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"time"

	"mitm-proxy/proxy"
)

// ProxyProcessor 正则匹配 → URL 重写 → 反向代理
// 实现 RequestProcessor 接口
type ProxyProcessor struct {
	client *http.Client
}

// NewProxyProcessor 创建正则代理处理器
func NewProxyProcessor(timeout time.Duration) *ProxyProcessor {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &ProxyProcessor{
		client: &http.Client{Timeout: timeout},
	}
}

// Process 处理被拦截的请求
// match.Target 已经是完成捕获组替换后的目标 URL
func (p *ProxyProcessor) Process(req *http.Request, body []byte, match *proxy.MatchResult) *http.Response {
	targetURL := match.Target
	log.Printf("[ProxyProcessor] %s %s → %s", req.Method, req.URL.String(), targetURL)

	// 构建代理请求
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	proxyReq, err := http.NewRequest(req.Method, targetURL, bodyReader)
	if err != nil {
		log.Printf("[ProxyProcessor] create request failed: %v", err)
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

	// Content-Length
	if bodyReader != nil && len(body) > 0 {
		proxyReq.ContentLength = int64(len(body))
	}

	// 转发
	resp, err := p.client.Do(proxyReq)
	if err != nil {
		log.Printf("[ProxyProcessor] forward failed: %v", err)
		return buildErrorResp(http.StatusBadGateway, "upstream request failed: "+err.Error())
	}
	defer resp.Body.Close()

	// 读取响应体
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[ProxyProcessor] read response failed: %v", err)
		return buildErrorResp(http.StatusBadGateway, "read upstream response failed")
	}

	// 构建 HTTP 响应
	respHeader := copyHeaders(resp.Header)
	return &http.Response{
		StatusCode:    resp.StatusCode,
		Status:        resp.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        respHeader,
		Body:          io.NopCloser(bytes.NewReader(respBody)),
		ContentLength: int64(len(respBody)),
	}
}
