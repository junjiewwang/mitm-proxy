package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
)

// ========================================
// 数据结构定义
// ========================================

// frontendRequest 前端发送给 yunapi 的请求格式
type frontendRequest struct {
	Cmd         string                 `json:"cmd"`
	ServiceType string                 `json:"serviceType"`
	Data        map[string]interface{} `json:"data"`
	RegionId    interface{}            `json:"regionId"` // 可能是 int 或 string
	Uin         string                 `json:"uin"`
	OwnerUin    string                 `json:"ownerUin"`
}

// PipelineError 内部错误类型
type PipelineError struct {
	StatusCode int
	Message    string
}

func (e *PipelineError) Error() string { return e.Message }

// ========================================
// 保护字段集合（data 中的字段不能覆盖这些）
// ========================================

var protectedFields = map[string]bool{
	"Uin":           true,
	"SubAccountUin": true,
	"AppId":         true,
	"Action":        true,
	"ApiModule":     true,
}

// ========================================
// RequestPipeline：请求处理流水线
// ========================================

// handler 包含流水线所需的所有依赖
type handler struct {
	cfg        *Config
	httpClient *http.Client
}

func newHandler(cfg *Config) *handler {
	return &handler{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: cfg.Backend.Timeout,
		},
	}
}

// ========================================
// HandleCAPI（Gin 入口）
// ========================================

// HandleCAPI 处理 /cgi/capi 请求（Gin handler 模式，配合 Whistle 使用）
func (h *handler) HandleCAPI(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": -1, "message": "read request body failed: " + err.Error()})
		return
	}

	result, pipeErr := h.runPipeline("[CAPI]", body)
	if pipeErr != nil {
		c.JSON(pipeErr.StatusCode, gin.H{"code": -1, "message": pipeErr.Message})
		return
	}

	c.Data(http.StatusOK, "application/json; charset=utf-8", result)
}

// ========================================
// ProcessRequest（正向代理入口，proxy.go 调用）
// ========================================

// proxyResponse handler 处理结果（供 proxy.go 使用）
type proxyResponse struct {
	statusCode int
	body       []byte
}

// processRequest 处理正向代理拦截的请求（yunapi 格式）
func (h *handler) processRequest(body []byte) *proxyResponse {
	result, err := h.runPipeline("[Proxy]", body)
	if err != nil {
		return &proxyResponse{
			statusCode: err.StatusCode,
			body:       []byte(fmt.Sprintf(`{"code":-1,"message":"%s"}`, err.Message)),
		}
	}
	return &proxyResponse{statusCode: http.StatusOK, body: result}
}

// ========================================
// Pipeline 核心流程
// ========================================

// runPipeline 执行完整流水线：解析 → 构建 → 转发 → 包装
func (h *handler) runPipeline(logPrefix string, rawBody []byte) ([]byte, *PipelineError) {
	// 1. 解析前端请求
	req, err := h.parseRequest(rawBody)
	if err != nil {
		return nil, err
	}

	log.Printf("%s >>> Received: cmd=%s, serviceType=%s, uin=%s, ownerUin=%s, regionId=%v",
		logPrefix, req.Cmd, req.ServiceType, req.Uin, req.OwnerUin, req.RegionId)

	// 2. 转换为后端请求格式
	backendBody := h.buildBackendRequest(req)

	// 3. 转发到后端
	backendResp, err := h.forwardToBackend(logPrefix, backendBody)
	if err != nil {
		log.Printf("%s !!! Forward failed: %v", logPrefix, err)
		return nil, err
	}

	log.Printf("%s <<< Backend response (%d bytes): %s", logPrefix, len(backendResp), truncate(backendResp, 500))

	// 4. 包装响应为前端格式
	return h.wrapResponse(backendResp), nil
}

// parseRequest 解析前端请求体（支持 JSON 和 form-urlencoded）
func (h *handler) parseRequest(body []byte) (*frontendRequest, *PipelineError) {
	var req frontendRequest

	if err := parseJSON(body, &req); err != nil {
		return nil, &PipelineError{
			StatusCode: http.StatusBadRequest,
			Message:    "parse request failed: " + err.Error(),
		}
	}

	if req.Cmd == "" {
		return nil, &PipelineError{
			StatusCode: http.StatusBadRequest,
			Message:    "cmd is required",
		}
	}

	return &req, nil
}

// ========================================
// 请求转换（Step 2: 拆包）
// ========================================

// buildBackendRequest 将前端请求转换为后端期望的扁平格式
//
// 转换规则：
//   1. cmd → Action
//   2. serviceType → ApiModule
//   3. uin/ownerUin → Uin/SubAccountUin（优先 URL 值，fallback mock_auth）
//   4. regionId → Region（映射表转换）
//   5. data.* → 扁平化到顶层（保护字段不可覆盖）
func (h *handler) buildBackendRequest(req *frontendRequest) map[string]interface{} {
	result := make(map[string]interface{})

	// TODO 1: 注入 Action
	result["Action"] = req.Cmd

	// TODO 2: 注入 ApiModule
	if req.ServiceType != "" {
		result["ApiModule"] = req.ServiceType
	}

	// TODO 3: 注入认证信息
	h.injectAuth(req, result)

	// TODO 4: 注入 Region
	result["Region"] = h.resolveRegion(req.RegionId)

	// TODO 5: 扁平化 data 字段
	h.flattenData(req.Data, result)

	return result
}

// injectAuth 注入认证字段：Uin、SubAccountUin、AppId
func (h *handler) injectAuth(req *frontendRequest, result map[string]interface{}) {
	uin := h.cfg.MockAuth.Uin
	subAccountUin := h.cfg.MockAuth.SubAccountUin

	if req.OwnerUin != "" {
		uin = req.OwnerUin
	}
	if req.Uin != "" {
		subAccountUin = req.Uin
	}

	result["Uin"] = uin
	result["SubAccountUin"] = subAccountUin
	result["AppId"] = h.cfg.MockAuth.AppId
}

// flattenData 将 data 中所有字段扁平化到结果中，保护字段不可被覆盖
func (h *handler) flattenData(data map[string]interface{}, result map[string]interface{}) {
	if data == nil {
		return
	}
	for k, v := range data {
		if protectedFields[k] {
			continue
		}
		result[k] = v
	}
}

// ========================================
// Region 映射
// ========================================

// resolveRegion 将 regionId 转换为 Region 字符串
func (h *handler) resolveRegion(regionId interface{}) string {
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

	if region, ok := h.cfg.RegionMap[id]; ok {
		return region
	}
	return "ap-guangzhou"
}

// ========================================
// 后端转发（Step 3）
// ========================================

// forwardToBackend 将转换后的请求转发到后端
func (h *handler) forwardToBackend(logPrefix string, body map[string]interface{}) ([]byte, *PipelineError) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, &PipelineError{StatusCode: http.StatusInternalServerError, Message: "marshal backend request: " + err.Error()}
	}

	url := h.cfg.Backend.URL + h.cfg.Backend.APIPath
	log.Printf("%s --> Forwarding to: %s, body: %s", logPrefix, url, truncate(bodyBytes, 1000))

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, &PipelineError{StatusCode: http.StatusInternalServerError, Message: "create request: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, &PipelineError{StatusCode: http.StatusBadGateway, Message: "forward request: " + err.Error()}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &PipelineError{StatusCode: http.StatusInternalServerError, Message: "read response: " + err.Error()}
	}

	return respBody, nil
}

// ========================================
// 响应包装（Step 4: 组包）
// ========================================

// wrapResponse 将后端响应包装为前端期望的三层嵌套格式
//
// 后端格式：
//   成功: {"Response":{"Data":{...},"RequestId":"..."}}
//   失败: {"Response":{"Error":{"Code":"...","Message":"..."},"RequestId":"..."}}
//
// 前端格式：
//   {"code":0, "data":{"code":0, "cgwerrorCode":0, "data":{"Response":{...}}}, "mccode":0, "errObj":{}, ...}
func (h *handler) wrapResponse(backendResp []byte) []byte {
	var backend map[string]interface{}
	if err := json.Unmarshal(backendResp, &backend); err != nil {
		return backendResp // 解析失败，原样返回
	}

	// 提取后端 Response 内容
	responseContent, innerCode := h.extractResponse(backend)

	// 构建 yunapi 三层嵌套格式
	final := map[string]interface{}{
		"code":   0,
		"data": map[string]interface{}{
			"code":         innerCode,
			"cgwerrorCode": 0,
			"data": map[string]interface{}{
				"Response": responseContent,
			},
		},
		"mccode": 0,
		"errObj": map[string]interface{}{},
		"seqId":  fmt.Sprintf("proxy-%d", time.Now().UnixNano()),
		"reqId":  fmt.Sprintf("proxy-%d", time.Now().UnixMilli()),
	}

	result, _ := json.Marshal(final)
	return result
}

// extractResponse 从后端响应中提取 Response 内容和业务状态码
func (h *handler) extractResponse(backend map[string]interface{}) (interface{}, interface{}) {
	// 格式: {"Response": {...}}
	if resp, ok := backend["Response"]; ok {
		code := h.extractErrorCode(resp)
		return resp, code
	}

	// 兼容旧格式: {"code":0, "data":{...}}
	code, _ := backend["code"]
	return backend["data"], code
}

// extractErrorCode 从 Response 中提取错误码
func (h *handler) extractErrorCode(resp interface{}) interface{} {
	respMap, ok := resp.(map[string]interface{})
	if !ok {
		return 0
	}
	errObj, hasError := respMap["Error"]
	if !hasError {
		return 0
	}
	// 提取具体错误码
	if errMap, ok := errObj.(map[string]interface{}); ok {
		if code, ok := errMap["Code"]; ok {
			return code
		}
	}
	return -1
}

// ========================================
// 工具函数
// ========================================

// parseJSON 解析请求体（支持 JSON 和 form-urlencoded）
func parseJSON(body []byte, req *frontendRequest) error {
	if len(body) > 0 && body[0] == '{' {
		return json.Unmarshal(body, req)
	}

	values, err := url.ParseQuery(string(body))
	if err != nil {
		return fmt.Errorf("not JSON nor form: %w", err)
	}

	req.Cmd = values.Get("cmd")
	req.ServiceType = values.Get("serviceType")
	req.Uin = values.Get("uin")
	req.OwnerUin = values.Get("ownerUin")

	return nil
}

// truncate 截断字节切片用于日志打印
func truncate(data []byte, maxLen int) string {
	if len(data) <= maxLen {
		return string(data)
	}
	return string(data[:maxLen]) + "...(truncated)"
}
