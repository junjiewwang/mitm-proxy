package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// HTTPForwarder 默认 HTTP 转发器实现
// 将 BackendRequest 以 POST JSON 转发到 BackendURL
type HTTPForwarder struct {
	Client *http.Client
}

// NewHTTPForwarder 创建 HTTP 转发器
func NewHTTPForwarder(timeout time.Duration) *HTTPForwarder {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTPForwarder{
		Client: &http.Client{Timeout: timeout},
	}
}

// Forward 实现 Forwarder 接口
func (f *HTTPForwarder) Forward(ctx *PipelineContext) error {
	if ctx.BackendURL == "" {
		return fmt.Errorf("backend URL not set")
	}

	bodyBytes, err := json.Marshal(ctx.BackendRequest)
	if err != nil {
		return fmt.Errorf("marshal backend request: %w", err)
	}

	log.Printf("[Pipeline] --> Forward to: %s, body: %s", ctx.BackendURL, truncateBytes(bodyBytes, 1000))

	req, err := http.NewRequest(http.MethodPost, ctx.BackendURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.Client.Do(req)
	if err != nil {
		return fmt.Errorf("forward request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	log.Printf("[Pipeline] <-- Response (%d bytes): %s", len(respBody), truncateBytes(respBody, 500))

	ctx.BackendResp = respBody
	return nil
}

// truncateBytes 截断字节切片用于日志打印
func truncateBytes(data []byte, maxLen int) string {
	if len(data) <= maxLen {
		return string(data)
	}
	return string(data[:maxLen]) + "...(truncated)"
}
