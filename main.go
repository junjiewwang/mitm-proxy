package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"mitm-proxy/adapter"
	yunapiadapter "mitm-proxy/adapter/yunapi"
	"mitm-proxy/rule"
	"mitm-proxy/webui"
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
	Proxy struct {
		Enable           bool              `yaml:"enable"`
		Listen           string            `yaml:"listen"`
		CertDir          string            `yaml:"cert_dir"`
		HostsFile        string            `yaml:"hosts_file"`
		InterceptDomains []string          `yaml:"intercept_domains"`
		UpstreamMap      map[string]string `yaml:"upstream_map"`
	} `yaml:"proxy"`
	// 规则引擎配置
	Rules struct {
		Dir           string        `yaml:"dir"`
		HotReload     bool          `yaml:"hot_reload"`
		WatchInterval time.Duration `yaml:"watch_interval"`
	} `yaml:"rules"`

	// 适配器配置（按名称索引）
	Adapters map[string]AdapterConfig `yaml:"adapters"`
}

// AdapterConfig 适配器配置（通用结构）
type AdapterConfig struct {
	Type    string                 `yaml:"type"`
	Backend yunapiadapter.BackendConfig `yaml:"backend"`
	Auth    yunapiadapter.AuthConfig    `yaml:"auth"`
	// RegionMap regionId(int) → Region 字符串的映射
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
	// 注意：intercept_domains 现在主要由规则脚本的 @intercept 指令管理
	// 配置文件中的列表作为额外补充（规则引擎与配置列表取并集）
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
//	192.168.1.100 api.example.com
//	192.168.1.100 www.example.com  # 注释
func loadHostsFile(path string, upstreamMap map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open hosts file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
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
		// 支持多个域名用空格分隔: 192.168.1.100 a.com b.com
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
			// 跳过包含路径的规则（如 example.com/some/path）
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

	log.Printf("Starting mitm-proxy...")
	log.Printf("  Listen: %s", cfg.Server.Listen)
	log.Printf("  Backend: %s%s", cfg.Backend.URL, cfg.Backend.APIPath)

	// 初始化诊断收集器
	initDebugCollector()

	// ========================================
	// 适配器注册表初始化
	// ========================================
	adapterRegistry := adapter.NewRegistry()

	// 注册适配器：优先从 adapters 配置段加载，fallback 到顶层 backend 配置
	for name, adapterCfg := range cfg.Adapters {
		switch adapterCfg.Type {
		case "yunapi", "":
			yunapiCfg := &yunapiadapter.Config{
				Backend:   adapterCfg.Backend,
				Auth:      adapterCfg.Auth,
				RegionMap: adapterCfg.RegionMap,
			}
			// 默认值
			if yunapiCfg.Backend.URL == "" {
				yunapiCfg.Backend.URL = cfg.Backend.URL
			}
			if yunapiCfg.Backend.APIPath == "" {
				yunapiCfg.Backend.APIPath = cfg.Backend.APIPath
			}
			if yunapiCfg.Backend.Timeout == 0 {
				yunapiCfg.Backend.Timeout = cfg.Backend.Timeout
			}
			a := yunapiadapter.New(yunapiCfg)
			if err := adapterRegistry.Register(a); err != nil {
				log.Fatalf("Failed to register adapter %s: %v", name, err)
			}
			log.Printf("  Adapter: %s (type=%s, backend=%s%s)", name, adapterCfg.Type, yunapiCfg.Backend.URL, yunapiCfg.Backend.APIPath)
		default:
			log.Printf("  WARNING: unknown adapter type: %s (name=%s)", adapterCfg.Type, name)
		}
	}

	// 如果没有配置任何适配器，自动注册一个默认的 yunapi 适配器（向后兼容）
	if len(cfg.Adapters) == 0 {
		defaultCfg := &yunapiadapter.Config{
			Backend: yunapiadapter.BackendConfig{
				URL:     cfg.Backend.URL,
				APIPath: cfg.Backend.APIPath,
				Timeout: cfg.Backend.Timeout,
			},
		}
		a := yunapiadapter.New(defaultCfg)
		if err := adapterRegistry.Register(a); err != nil {
			log.Printf("  WARNING: failed to register default yunapi adapter: %v", err)
		} else {
			log.Printf("  Adapter: yunapi (default, backend=%s%s)", cfg.Backend.URL, cfg.Backend.APIPath)
		}
	}

	// ========================================
	// 规则引擎初始化
	// ========================================
	ruleEngine := rule.NewRuleEngine()
	ruleManager := rule.NewRuleManager(rule.ManagerConfig{
		Dir:           cfg.Rules.Dir,
		HotReload:     cfg.Rules.HotReload,
		WatchInterval: cfg.Rules.WatchInterval,
	}, ruleEngine)

	// 加载规则文件
	if err := ruleManager.LoadAll(); err != nil {
		log.Printf("[Main] WARNING: load rules failed: %v", err)
	}

	// 启动热加载
	ruleManager.StartHotReload()
	defer ruleManager.StopHotReload()

	// ========================================
	// 创建事件广播器（SSE 实时日志推送）
	// ========================================
	eventBroadcaster := webui.NewEventBroadcaster(200)

	// ========================================
	// 创建处理器调度（供规则引擎匹配后分发）
	// ========================================
	dispatcher := newProcessorDispatcher(cfg, adapterRegistry, eventBroadcaster)

	// 启动正向代理（如果启用）
	var fwdProxy *ForwardProxy
	if cfg.Proxy.Enable {
		var proxyErr error
		// 使用 RuleEngine 适配器注入到 ForwardProxy
		fwdProxy, proxyErr = NewForwardProxy(cfg,
			WithInterceptMatcher(ruleEngine),
			WithUpstreamResolver(ruleEngine),
			WithRequestMatcher(ruleEngine),
			WithRequestProcessor(dispatcher),
		)
		if proxyErr != nil {
			log.Fatalf("Failed to create forward proxy: %v", proxyErr)
		}
		go func() {
			if err := fwdProxy.ListenAndServe(cfg.Proxy.Listen); err != nil {
				log.Fatalf("Forward proxy failed: %v", err)
			}
		}()
		log.Printf("  Proxy: %s (intercept: %v)", cfg.Proxy.Listen, cfg.Proxy.InterceptDomains)
	}

	// 启动 Gin 服务
	gin.SetMode(gin.DebugMode)
	ginEngine := gin.New()
	ginEngine.Use(gin.Logger(), gin.Recovery())

	// 注册路由
	// 通用 Pipeline 入口（根据 Content-Type 和请求体格式自动路由到适配器）
	registerPipelineRoute(ginEngine, adapterRegistry)
	ginEngine.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// 规则管理 REST API
	registerRuleAPI(ginEngine, ruleManager)

	// Web UI 管理面板
	var connMgr *ConnManager
	if fwdProxy != nil {
		connMgr = fwdProxy.connMgr
	}
	webui.RegisterRoutes(ginEngine, ruleManager, eventBroadcaster,
		&proxyStatusProvider{collector: defaultCollector, connMgr: connMgr, cfg: cfg})

	// 诊断面板 + MCP Server
	if fwdProxy != nil {
		registerDebugRoutes(ginEngine, defaultCollector, fwdProxy.connMgr, cfg)
		registerMCPRoutes(ginEngine, &mcpServer{
			collector: defaultCollector,
			connMgr:   fwdProxy.connMgr,
			cfg:       cfg,
		})
	} else {
		registerDebugRoutes(ginEngine, defaultCollector, nil, cfg)
		registerMCPRoutes(ginEngine, &mcpServer{
			collector: defaultCollector,
			cfg:       cfg,
		})
	}

	if err := ginEngine.Run(cfg.Server.Listen); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

// registerPipelineRoute 注册通用 Pipeline 入口路由
func registerPipelineRoute(engine *gin.Engine, registry *adapter.Registry) {
	// 通用 Pipeline 入口：POST /pipeline/:adapter
	engine.POST("/pipeline/:adapter", func(c *gin.Context) {
		adapterName := c.Param("adapter")

		a, ok := registry.Get(adapterName)
		if !ok {
			c.JSON(400, gin.H{"code": -1, "message": "adapter not found: " + adapterName})
			return
		}

		// 预读请求体
		reqBody, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, gin.H{"code": -1, "message": "read request body failed"})
			return
		}

		resp := a.GetPipeline().Execute(c.Request, reqBody)
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.Data(resp.StatusCode, "application/json; charset=utf-8", respBody)
	})

	// 兼容旧路由 /cgi/capi → 路由到默认 yunapi 适配器
	engine.POST("/cgi/capi", func(c *gin.Context) {
		a, ok := registry.Get("yunapi")
		if !ok {
			c.JSON(500, gin.H{"code": -1, "message": "yunapi adapter not registered"})
			return
		}

		reqBody, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, gin.H{"code": -1, "message": "read request body failed"})
			return
		}

		resp := a.GetPipeline().Execute(c.Request, reqBody)
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		c.Data(resp.StatusCode, "application/json; charset=utf-8", respBody)
	})
}

// proxyStatusProvider 实现 webui.StatusProvider，聚合系统运行时状态
type proxyStatusProvider struct {
	collector *DebugCollector
	connMgr   *ConnManager
	cfg       *Config
}

func (p *proxyStatusProvider) GetStatus() map[string]interface{} {
	status := make(map[string]interface{})

	if p.collector != nil {
		_, totalReqs, totalErrs, uptime := p.collector.Snapshot()
		status["uptime_sec"] = int(uptime.Seconds())
		status["total_requests"] = totalReqs
		status["total_errors"] = totalErrs
	}

	if p.connMgr != nil {
		active, evicted := p.connMgr.Stats()
		status["conn_pool"] = map[string]interface{}{
			"active_mitm":   active,
			"total_evicted": evicted,
			"max":           p.connMgr.maxConns,
		}
	}

	if p.cfg != nil {
		status["backend"] = p.cfg.Backend.URL + p.cfg.Backend.APIPath
		status["proxy_listen"] = p.cfg.Proxy.Listen
	}

	return status
}
