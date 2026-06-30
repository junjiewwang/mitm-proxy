package yunapi

import "time"

// Config yunapi 适配器配置
type Config struct {
	Backend BackendConfig `yaml:"backend"`
	Auth    AuthConfig    `yaml:"auth"`
	// RegionMap regionId(int) → Region 字符串的映射
	RegionMap map[int]string `yaml:"region_map"`
}

// BackendConfig 后端服务配置
type BackendConfig struct {
	URL     string        `yaml:"url"`
	APIPath string        `yaml:"api_path"`
	Timeout time.Duration `yaml:"timeout"`
}

// AuthConfig 认证信息配置
type AuthConfig struct {
	Uin           string `yaml:"uin"`
	SubAccountUin string `yaml:"sub_account_uin"`
	AppId         int    `yaml:"app_id"`
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		Backend: BackendConfig{
			URL:     "http://127.0.0.1:16686",
			APIPath: "/api/v1",
			Timeout: 30 * time.Second,
		},
		RegionMap: map[int]string{
			1: "ap-guangzhou",
		},
	}
}
