package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// Config 代理服务配置
type Config struct {
	Server struct {
		Listen string `yaml:"listen"`
	} `yaml:"server"`
	Backend struct {
		URL     string        `yaml:"url"`
		APIPath string        `yaml:"api_path"`
		Timeout time.Duration `yaml:"timeout"`
	} `yaml:"backend"`
	MockAuth struct {
		Uin           string `yaml:"uin"`
		SubAccountUin string `yaml:"sub_account_uin"`
		AppId         int    `yaml:"app_id"`
	} `yaml:"mock_auth"`
	RegionMap map[int]string `yaml:"region_map"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// 默认值
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = ":8080"
	}
	if cfg.Backend.URL == "" {
		cfg.Backend.URL = "http://127.0.0.1:16686"
	}
	if cfg.Backend.APIPath == "" {
		cfg.Backend.APIPath = "/api/v1"
	}
	if cfg.Backend.Timeout == 0 {
		cfg.Backend.Timeout = 30 * time.Second
	}
	return &cfg, nil
}

func main() {
	// 加载配置
	configPath := "config.yaml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Starting yunapi-proxy...")
	log.Printf("  Listen: %s", cfg.Server.Listen)
	log.Printf("  Backend: %s%s", cfg.Backend.URL, cfg.Backend.APIPath)
	log.Printf("  MockAuth: Uin=%s, SubAccountUin=%s, AppId=%d",
		cfg.MockAuth.Uin, cfg.MockAuth.SubAccountUin, cfg.MockAuth.AppId)

	// 创建 handler
	h := newHandler(cfg)

	// 启动 Gin 服务
	gin.SetMode(gin.DebugMode)
	engine := gin.New()
	engine.Use(gin.Logger(), gin.Recovery())

	// 注册路由 - 匹配前端发送的路径
	engine.POST("/cgi/capi", h.HandleCAPI)

	// 健康检查
	engine.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	if err := engine.Run(cfg.Server.Listen); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
