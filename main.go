package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
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
	Proxy struct {
		Enable           bool              `yaml:"enable"`
		Listen           string            `yaml:"listen"`
		CertDir          string            `yaml:"cert_dir"`
		HostsFile        string            `yaml:"hosts_file"`
		InterceptDomains []string          `yaml:"intercept_domains"`
		UpstreamMap      map[string]string `yaml:"upstream_map"`
	} `yaml:"proxy"`
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
	// 代理默认值
	if cfg.Proxy.Listen == "" {
		cfg.Proxy.Listen = ":8081"
	}
	if cfg.Proxy.CertDir == "" {
		cfg.Proxy.CertDir = "certs"
	}
	if len(cfg.Proxy.InterceptDomains) == 0 {
		cfg.Proxy.InterceptDomains = []string{"console.cloud.tencent.com"}
	}
	// 加载 hosts 文件（Whistle 格式：IP domain）
	if cfg.Proxy.UpstreamMap == nil {
		cfg.Proxy.UpstreamMap = make(map[string]string)
	}
	if cfg.Proxy.HostsFile != "" {
		if err := loadHostsFile(cfg.Proxy.HostsFile, cfg.Proxy.UpstreamMap); err != nil {
			log.Printf("[Config] WARNING: load hosts file %s: %v", cfg.Proxy.HostsFile, err)
		} else {
			log.Printf("[Config] Loaded %d host mappings from %s", len(cfg.Proxy.UpstreamMap), cfg.Proxy.HostsFile)
		}
	}
	return &cfg, nil
}

// loadHostsFile 解析 Whistle 格式的 hosts 文件（IP domain 格式）
// 支持格式：
//
//	9.223.72.139 console.cloud.tencent.com
//	9.223.72.139 buy.cloud.tencent.com  # 注释
func loadHostsFile(path string, upstreamMap map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open hosts file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())

		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// 去除行内注释
		if idx := strings.Index(line, "#"); idx > 0 {
			line = strings.TrimSpace(line[:idx])
		}

		// 解析 "IP domain" 格式
		// 支持多个域名用空格分隔: 9.223.72.139 a.com b.com
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue // 格式不合法
		}

		ip := fields[0]
		// 判断是否合法 IP（简单判断）
		if !strings.Contains(ip, ".") && !strings.Contains(ip, ":") {
			continue
		}

		// 对每个域名建立映射
		for _, domain := range fields[1:] {
			// 跳过包含路径的规则（如 cloud.tencent.com/password/recover）
			if strings.Contains(domain, "/") {
				continue
			}
			// 跳过 proxy:// 规则
			if strings.HasPrefix(domain, "proxy://") {
				continue
			}
			addr := ip
			if !strings.Contains(addr, ":") {
				addr += ":443"
			}
			upstreamMap[domain] = addr
		}
	}

	return scanner.Err()
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

	// 初始化诊断收集器
	initDebugCollector()

	// 创建 handler
	h := newHandler(cfg)

	// 启动正向代理（如果启用）
	var proxy *ForwardProxy
	if cfg.Proxy.Enable {
		var err error
		proxy, err = NewForwardProxy(cfg, h)
		if err != nil {
			log.Fatalf("Failed to create forward proxy: %v", err)
		}
		go func() {
			if err := proxy.ListenAndServe(cfg.Proxy.Listen); err != nil {
				log.Fatalf("Forward proxy failed: %v", err)
			}
		}()
		log.Printf("  Proxy: %s (intercept: %v)", cfg.Proxy.Listen, cfg.Proxy.InterceptDomains)
	}

	// 启动 Gin 服务
	gin.SetMode(gin.DebugMode)
	engine := gin.New()
	engine.Use(gin.Logger(), gin.Recovery())

	// 注册路由
	engine.POST("/cgi/capi", h.HandleCAPI)
	engine.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// 诊断面板 + MCP Server
	if proxy != nil {
		registerDebugRoutes(engine, defaultCollector, proxy.connMgr, cfg)
		registerMCPRoutes(engine, &mcpServer{
			collector: defaultCollector,
			connMgr:   proxy.connMgr,
			cfg:       cfg,
			handler:   h,
		})
	} else {
		registerDebugRoutes(engine, defaultCollector, nil, cfg)
		registerMCPRoutes(engine, &mcpServer{
			collector: defaultCollector,
			cfg:       cfg,
			handler:   h,
		})
	}

	if err := engine.Run(cfg.Server.Listen); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
