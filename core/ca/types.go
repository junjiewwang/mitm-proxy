package ca

// TrustStatus 单个信任存储的检测结果
type TrustStatus struct {
	Name    string `json:"name"`              // "System Keychain" / "Firefox"
	Trusted bool   `json:"trusted"`
	Error   string `json:"error,omitempty"`   // 检测失败原因
}

// Detector CA 信任检测器接口（允许不同平台实现）
type Detector interface {
	CheckAll(fingerprint string) []TrustStatus
}

// Distributor CA 分发文件生成器
type Distributor interface {
	MobileConfig(caPEM []byte) ([]byte, error) // macOS 配置描述文件
}
