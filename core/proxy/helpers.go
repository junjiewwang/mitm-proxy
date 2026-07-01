package proxy

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// BuildErrorResponse 构建错误 HTTP 响应
func BuildErrorResponse(statusCode int, message string) *http.Response {
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

// SetCORSHeaders 设置 CORS 响应头
func SetCORSHeaders(h http.Header, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "*"
	}
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	h.Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
	if h.Get("Access-Control-Allow-Headers") == "" {
		h.Set("Access-Control-Allow-Headers", "Content-Type, X-Lid, X-Life, Authorization")
	}
}

// IsHopByHop 判断是否为 hop-by-hop 头（不应转发）
func IsHopByHop(header string) bool {
	switch strings.ToLower(header) {
	case "connection", "keep-alive", "proxy-connection",
		"proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

// FilterHopByHop 复制 Header 排除 hop-by-hop
func FilterHopByHop(src http.Header) http.Header {
	dst := make(http.Header)
	for k, vv := range src {
		if IsHopByHop(k) {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
	return dst
}
