package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// frontendRequest 前端发送给 yunapi 的请求格式
type frontendRequest struct {
	Cmd         string                 `json:"cmd"`
	ServiceType string                 `json:"serviceType"`
	Data        map[string]interface{} `json:"data"`
	RegionId    interface{}            `json:"regionId"` // 可能是 int 或 string
	Uin         string                 `json:"uin"`
	OwnerUin    string                 `json:"ownerUin"`
}

// handler 处理前端请求，转换格式后转发到后端
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

// HandleCAPI 处理 /cgi/capi 请求
func (h *handler) HandleCAPI(c *gin.Context) {
	// 1. 读取前端请求体
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": -1, "message": "read request body failed: " + err.Error()})
		return
	}

	// 2. 解析前端请求
	var req frontendRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": -1, "message": "parse request failed: " + err.Error()})
		return
	}

	if req.Cmd == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": -1, "message": "cmd is required"})
		return
	}

	// 3. 构建后端请求体
	backendBody := h.buildBackendRequest(&req)

	// 4. 转发到后端
	backendResp, err := h.forwardToBackend(backendBody)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"code": -1, "message": "forward to backend failed: " + err.Error()})
		return
	}

	// 5. 包装响应返回给前端
	frontendResp := h.wrapResponse(backendResp)

	c.Data(http.StatusOK, "application/json; charset=utf-8", frontendResp)
}

// buildBackendRequest 将前端请求转换为后端期望的扁平格式
func (h *handler) buildBackendRequest(req *frontendRequest) map[string]interface{} {
	result := make(map[string]interface{})

	// 注入 Action（从 cmd 字段）
	result["Action"] = req.Cmd

	// 注入认证信息
	// 前端 ownerUin = 后端 Uin（主账号）
	// 前端 uin = 后端 SubAccountUin（操作者）
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

	// 解析 Region
	region := h.resolveRegion(req.RegionId)
	result["Region"] = region

	// 将 data 中的所有字段展开到顶层
	if req.Data != nil {
		for k, v := range req.Data {
			// data 中的字段优先级高于外层注入的字段（除了认证字段）
			if k == "Uin" || k == "SubAccountUin" || k == "AppId" || k == "Action" {
				continue
			}
			result[k] = v
		}
	}

	// 如果 data 中有 InstanceId，使用它；否则不注入
	// InstanceId 通常在 data 中传递

	return result
}

// resolveRegion 将 regionId 转换为 Region 字符串
func (h *handler) resolveRegion(regionId interface{}) string {
	if regionId == nil {
		return "ap-guangzhou" // 默认广州
	}

	var id int
	switch v := regionId.(type) {
	case float64:
		id = int(v)
	case int:
		id = v
	case string:
		// 如果已经是 region 字符串格式，直接返回
		if len(v) > 3 {
			return v
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

// forwardToBackend 将转换后的请求转发到后端
func (h *handler) forwardToBackend(body map[string]interface{}) ([]byte, error) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal backend request: %w", err)
	}

	url := h.cfg.Backend.URL + h.cfg.Backend.APIPath
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return respBody, nil
}

// wrapResponse 将后端响应包装为前端期望的嵌套格式
// 后端返回: {"code":0, "data":{...}}
// 前端期望: {"code":0, "data":{"code":0, "cgwerrorCode":0, "data":{"Response":{...}}}, "seqId":"...", "reqId":"..."}
func (h *handler) wrapResponse(backendResp []byte) []byte {
	var backendResult map[string]interface{}
	if err := json.Unmarshal(backendResp, &backendResult); err != nil {
		// 解析失败，原样返回
		return backendResp
	}

	// 获取后端的 code
	code, _ := backendResult["code"]
	backendData := backendResult["data"]

	// 构建内层 data（模拟 yunapi 的包装）
	innerData := map[string]interface{}{
		"Response": backendData,
	}

	// 构建中间层
	middleLayer := map[string]interface{}{
		"code":         code,
		"cgwerrorCode": 0,
		"data":         innerData,
	}

	// 构建最终响应
	finalResp := map[string]interface{}{
		"code":  0,
		"data":  middleLayer,
		"seqId": fmt.Sprintf("proxy-%d", time.Now().UnixNano()),
		"reqId": fmt.Sprintf("proxy-%d", time.Now().UnixMilli()),
	}

	result, _ := json.Marshal(finalResp)
	return result
}
