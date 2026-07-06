package dispatch

import (
	"net/http"
	"strings"
	"time"

	"mitm-proxy/core/event"
)

// EventTag 事件元数据标签（替代魔数 X-Replay 头检查）
type EventTag string

const (
	TagReplay EventTag = "replay" // 请求重放
)

// MatchInfo 规则匹配摘要（dispatch 层内部使用，不依赖 proxy.MatchResult）
type MatchInfo struct {
	ScriptName string
	RuleType   string // proxy | exact | script | pipeline
	Target     string
}

// Broadcaster 事件广播器接口
type Broadcaster interface {
	Broadcast(evt *event.Event)
}

// BuildEvent 构建 SSE 事件（纯函数，便于测试）
// tags: 可选的标签列表（如 TagReplay 表示重放请求）
func BuildEvent(opts BuildOptions) *event.Event {
	reqHeaders := make(map[string]string)
	for k, v := range opts.Req.Header {
		reqHeaders[k] = strings.Join(v, ", ")
	}
	respHeaders := make(map[string]string)
	var contentType string
	var contentLength int64
	if opts.Resp != nil {
		for k, v := range opts.Resp.Header {
			respHeaders[k] = strings.Join(v, ", ")
		}
		contentType = opts.Resp.Header.Get("Content-Type")
		contentLength = opts.Resp.ContentLength
		if contentLength <= 0 {
			contentLength = int64(len(opts.RespBody))
		}
	}

	eventType := "match"
	for _, t := range opts.Tags {
		if t == TagReplay {
			eventType = "replay"
			break
		}
	}

	statusCode := 0
	errMsg := ""
	if opts.Resp != nil {
		statusCode = opts.Resp.StatusCode
		if opts.Resp.StatusCode >= 400 {
			errMsg = opts.Resp.Status
		}
	}

	return &event.Event{
		Type:          eventType,
		Time:          time.Now().Format(time.RFC3339),
		Method:        opts.Req.Method,
		URL:           opts.Req.URL.String(),
		Status:        statusCode,
		LatencyMs:     opts.Elapsed.Milliseconds(),
		Script:        opts.Match.ScriptName,
		RuleType:      opts.Match.RuleType,
		Target:        opts.Match.Target,
		Error:         errMsg,
		ReqHeaders:    reqHeaders,
		ReqBody:       truncateBody(opts.ReqBody),
		RespHeaders:   respHeaders,
		RespBody:      truncateBody(opts.RespBody),
		ContentType:   contentType,
		ContentLength: contentLength,
	}
}

// truncateBody 截断 body 到 4KB，超出追加省略标记
func truncateBody(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if len(data) > 4096 {
		return string(data[:4096]) + "\n...(truncated)"
	}
	return string(data)
}

// BuildOptions 构建事件所需的全部输入
type BuildOptions struct {
	Req      *http.Request
	ReqBody  []byte
	Resp     *http.Response
	RespBody []byte
	Match    MatchInfo
	Elapsed  time.Duration
	Tags     []EventTag
}
