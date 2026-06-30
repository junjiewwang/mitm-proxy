package processor

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// buildErrorResp 构建 JSON 错误 HTTP 响应
func buildErrorResp(statusCode int, message string) *http.Response {
	body := fmt.Sprintf(`{"code":-1,"message":"%s"}`, message)
	return &http.Response{
		StatusCode:    statusCode,
		Status:        http.StatusText(statusCode),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

// isHopByHop 判断是否为 hop-by-hop 头（不应转发）
func isHopByHop(header string) bool {
	switch strings.ToLower(header) {
	case "connection", "keep-alive", "proxy-connection",
		"proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

// copyHeaders 复制 HTTP Header（排除 hop-by-hop）
func copyHeaders(src http.Header) http.Header {
	dst := make(http.Header)
	for k, vv := range src {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
	return dst
}
