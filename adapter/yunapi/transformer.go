package yunapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"mitm-proxy/pipeline"
)

// protectedFields data 中的字段不能覆盖这些保护字段
var protectedFields = map[string]bool{
	"Uin":           true,
	"SubAccountUin": true,
	"AppId":         true,
	"Action":        true,
	"ApiModule":     true,
}

// ========================================
// RequestTransformer: 拆包阶段
// ========================================

// RequestTransformer 将 yunapi 前端请求转换为后端期望的扁平格式
//
// 转换规则：
//  1. cmd → Action
//  2. serviceType → ApiModule
//  3. regionId → Region（映射表转换）
//  4. data.* → 扁平化到顶层（保护字段不可覆盖）
type RequestTransformer struct {
	regionMap map[int]string
}

// NewRequestTransformer 创建请求转换器
func NewRequestTransformer(regionMap map[int]string) *RequestTransformer {
	if regionMap == nil {
		regionMap = map[int]string{1: "ap-guangzhou"}
	}
	return &RequestTransformer{regionMap: regionMap}
}

// TransformRequest 实现 pipeline.RequestTransformer 接口
func (t *RequestTransformer) TransformRequest(ctx *pipeline.PipelineContext) error {
	parsed := ctx.ParsedRequest
	if parsed == nil {
		return fmt.Errorf("parsed request is nil")
	}

	// 支持 form-urlencoded 格式（ParsedRequest 可能为空，需从 RawBody 解析）
	if len(parsed) == 0 && len(ctx.RawBody) > 0 && ctx.RawBody[0] != '{' {
		if err := parseFormURLEncoded(ctx.RawBody, parsed); err != nil {
			return fmt.Errorf("parse form body: %w", err)
		}
	}

	result := make(map[string]any)

	// 1. cmd → Action
	cmd, _ := parsed["cmd"].(string)
	if cmd == "" {
		return fmt.Errorf("cmd is required")
	}
	result["Action"] = cmd

	// 2. serviceType → ApiModule
	if serviceType, _ := parsed["serviceType"].(string); serviceType != "" {
		result["ApiModule"] = serviceType
	}

	// 3. regionId → Region
	result["Region"] = t.resolveRegion(parsed["regionId"])

	// 4. 扁平化 data 字段
	if data, ok := parsed["data"].(map[string]any); ok {
		for k, v := range data {
			if protectedFields[k] {
				continue
			}
			result[k] = v
		}
	}

	ctx.BackendRequest = result

	// 将 uin/ownerUin 存入 Metadata 供 AuthInjector 使用
	if uin, _ := parsed["uin"].(string); uin != "" {
		ctx.Metadata["uin"] = uin
	}
	if ownerUin, _ := parsed["ownerUin"].(string); ownerUin != "" {
		ctx.Metadata["ownerUin"] = ownerUin
	}

	return nil
}

// resolveRegion 将 regionId 转换为 Region 字符串
func (t *RequestTransformer) resolveRegion(regionId any) string {
	if regionId == nil {
		return "ap-guangzhou"
	}

	var id int
	switch v := regionId.(type) {
	case float64:
		id = int(v)
	case int:
		id = v
	case string:
		if len(v) > 3 {
			return v // 已经是 region 名称
		}
		fmt.Sscanf(v, "%d", &id)
	default:
		return "ap-guangzhou"
	}

	if region, ok := t.regionMap[id]; ok {
		return region
	}
	return "ap-guangzhou"
}

// parseFormURLEncoded 解析 form-urlencoded 格式到 map
func parseFormURLEncoded(body []byte, result map[string]any) error {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return err
	}
	if v := values.Get("cmd"); v != "" {
		result["cmd"] = v
	}
	if v := values.Get("serviceType"); v != "" {
		result["serviceType"] = v
	}
	if v := values.Get("uin"); v != "" {
		result["uin"] = v
	}
	if v := values.Get("ownerUin"); v != "" {
		result["ownerUin"] = v
	}
	return nil
}

// ========================================
// ResponseTransformer: 组包阶段
// ========================================

// ResponseTransformer 将后端响应包装为 yunapi 前端期望的三层嵌套格式
//
// 后端格式：
//
//	成功: {"Response":{"Data":{...},"RequestId":"..."}}
//	失败: {"Response":{"Error":{"Code":"...","Message":"..."},"RequestId":"..."}}
//
// 前端格式：
//
//	{"code":0, "data":{"code":0, "cgwerrorCode":0, "data":{"Response":{...}}}, "mccode":0, "errObj":{}, ...}
type ResponseTransformer struct{}

// NewResponseTransformer 创建响应转换器
func NewResponseTransformer() *ResponseTransformer {
	return &ResponseTransformer{}
}

// TransformResponse 实现 pipeline.ResponseTransformer 接口
func (t *ResponseTransformer) TransformResponse(ctx *pipeline.PipelineContext) error {
	var backend map[string]any
	if err := json.Unmarshal(ctx.BackendResp, &backend); err != nil {
		// 解析失败，原样返回
		ctx.FinalResp = ctx.BackendResp
		return nil
	}

	// 提取后端 Response 内容
	responseContent, innerCode := extractResponse(backend)

	// 构建 yunapi 三层嵌套格式
	final := map[string]any{
		"code": 0,
		"data": map[string]any{
			"code":         innerCode,
			"cgwerrorCode": 0,
			"data": map[string]any{
				"Response": responseContent,
			},
		},
		"mccode": 0,
		"errObj": map[string]any{},
		"seqId":  fmt.Sprintf("proxy-%d", time.Now().UnixNano()),
		"reqId":  fmt.Sprintf("proxy-%d", time.Now().UnixMilli()),
	}

	result, err := json.Marshal(final)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	ctx.FinalResp = result
	return nil
}

// extractResponse 从后端响应中提取 Response 内容和业务状态码
func extractResponse(backend map[string]any) (any, any) {
	// 格式: {"Response": {...}}
	if resp, ok := backend["Response"]; ok {
		code := extractErrorCode(resp)
		return resp, code
	}
	// 兼容旧格式: {"code":0, "data":{...}}
	code, _ := backend["code"]
	return backend["data"], code
}

// extractErrorCode 从 Response 中提取错误码
func extractErrorCode(resp any) any {
	respMap, ok := resp.(map[string]any)
	if !ok {
		return 0
	}
	errObj, hasError := respMap["Error"]
	if !hasError {
		return 0
	}
	if errMap, ok := errObj.(map[string]any); ok {
		if code, ok := errMap["Code"]; ok {
			return code
		}
	}
	return -1
}
