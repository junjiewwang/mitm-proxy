package proxy

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"time"
)

// proxyRequest 通用反向代理：将请求转发到 targetURL 并返回响应
func ProxyRequest(origReq *http.Request, body []byte, targetURL string, timeout time.Duration) *http.Response {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	proxyReq, err := http.NewRequest(origReq.Method, targetURL, bodyReader)
	if err != nil {
		log.Printf("[Transport] create request failed: %v", err)
		return BuildErrorResponse(http.StatusBadGateway, "create upstream request failed")
	}

	// 复制 Header（排除 hop-by-hop）
	for k, vv := range origReq.Header {
		if IsHopByHop(k) {
			continue
		}
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}
	if bodyReader != nil && len(body) > 0 {
		proxyReq.ContentLength = int64(len(body))
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("[Transport] forward failed: %v", err)
		return BuildErrorResponse(http.StatusBadGateway, "upstream request failed: "+err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[Transport] read response failed: %v", err)
		return BuildErrorResponse(http.StatusBadGateway, "read upstream response failed")
	}

	respHeader := FilterHopByHop(resp.Header)
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

// streamRequest 流式代理：不读完 Body，保持为流式 io.ReadCloser
func StreamRequest(origReq *http.Request, body []byte, targetURL string) *http.Response {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	proxyReq, err := http.NewRequest(origReq.Method, targetURL, bodyReader)
	if err != nil {
		log.Printf("[Transport] stream create request failed: %v", err)
		return BuildErrorResponse(http.StatusBadGateway, "create upstream request failed")
	}

	for k, vv := range origReq.Header {
		if IsHopByHop(k) {
			continue
		}
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}

	// 流式：不设全局超时
	client := &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("[Transport] stream forward failed: %v", err)
		return BuildErrorResponse(http.StatusBadGateway, "upstream stream request failed: "+err.Error())
	}

	respHeader := FilterHopByHop(resp.Header)
	return &http.Response{
		StatusCode:    resp.StatusCode,
		Status:        resp.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        respHeader,
		Body:          resp.Body, // 流式传递
		ContentLength: -1,        // chunked
	}
}
