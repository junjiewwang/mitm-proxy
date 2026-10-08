package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"mitm-proxy/core/ca"
	"mitm-proxy/core/dispatch"
	"mitm-proxy/core/metrics"
	"mitm-proxy/core/replay"
	"mitm-proxy/core/script"
	"mitm-proxy/rule"
	"mitm-proxy/webui"
)

// Config 代理服务配置（纯框架配置，不含业务字段）
type Config struct {
	Server struct {
		Listen string `yaml:"listen"`
	} `yaml:"server"`
	Proxy struct {
		Enable           bool              `yaml:"enable"`
		Listen           string            `yaml:"listen"`
		CertDir          string            `yaml:"cert_dir"`
		HostsFile        string            `yaml:"hosts_file"`
		InterceptDomains []string          `yaml:"intercept_domains"`
		UpstreamMap      map[string]string `yaml:"upstream_map"`
	} `yaml:"proxy"`
	Rules struct {
		Dir           string        `yaml:"dir"`
		HotReload     bool          `yaml:"hot_reload"`
		WatchInterval time.Duration `yaml:"watch_interval"`
	} `yaml:"rules"`
	WebUI struct {
		Enable bool `yaml:"enable"`
	} `yaml:"webui"`
	Debug struct {
		Enable     bool `yaml:"enable"`
		BufferSize int  `yaml:"buffer_size"`
	} `yaml:"debug"`
	Observability struct {
		PprofAddr         string        `yaml:"pprof_addr"`
		MonitorInterval   time.Duration `yaml:"monitor_interval"`
		GoroutineThreshold int          `yaml:"goroutine_threshold"`
		DumpDir           string        `yaml:"dump_dir"`
	} `yaml:"observability"`
}

// proxyConfig 将 Config.Proxy 转换为 ProxyConfig
func (c *Config) proxyConfig() *ProxyConfig {
	return &ProxyConfig{
		CertDir:          c.Proxy.CertDir,
		InterceptDomains: c.Proxy.InterceptDomains,
		UpstreamMap:      c.Proxy.UpstreamMap,
	}
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
		cfg.Server.Listen = ":8082"
	}
	if cfg.Proxy.Listen == "" {
		cfg.Proxy.Listen = ":8081"
	}
	if cfg.Proxy.CertDir == "" {
		cfg.Proxy.CertDir = "certs"
	}
	if cfg.Rules.Dir == "" {
		cfg.Rules.Dir = "rules"
	}
	if cfg.Rules.WatchInterval == 0 {
		cfg.Rules.WatchInterval = 2 * time.Second
	}
	// 可观测性默认值
	if cfg.Observability.PprofAddr == "" {
		cfg.Observability.PprofAddr = ":6060"
	}
	if cfg.Observability.MonitorInterval == 0 {
		cfg.Observability.MonitorInterval = 30 * time.Second
	}
	if cfg.Observability.GoroutineThreshold == 0 {
		cfg.Observability.GoroutineThreshold = 1000
	}
	if cfg.Observability.DumpDir == "" {
		cfg.Observability.DumpDir = "/tmp"
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

// loadHostsFile 解析 Whistle 格式的 hosts 文件
func loadHostsFile(path string, upstreamMap map[string]string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open hosts file: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if idx := strings.Index(line, "#"); idx > 0 {
			line = strings.TrimSpace(line[:idx])
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ip := fields[0]
		if !strings.Contains(ip, ".") && !strings.Contains(ip, ":") {
			continue
		}
		for _, domain := range fields[1:] {
			if strings.Contains(domain, "/") || strings.HasPrefix(domain, "proxy://") {
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

	// 初始化诊断收集器
	initDebugCollector()

	// 可观测性：pprof 诊断端口 + 运行时指标监控
	startPprofServer(cfg.Observability.PprofAddr)
	startRuntimeMonitor(cfg.Observability.MonitorInterval, cfg.Observability.GoroutineThreshold, cfg.Observability.DumpDir)

	// ========================================
	// JS 脚本运行时初始化
	// ========================================
	scriptRuntime := script.NewRuntime(
		script.WithTimeout(5*time.Second),
		script.WithMaxVMs(16),
	)
	scriptLoader := script.NewLoader()
	debugger := script.NewDebugger(scriptRuntime)
	log.Printf("  Script runtime: goja (JS engine), max VMs=16, timeout=5s")

	// ========================================
	// 规则引擎初始化
	// ========================================
	ruleEngine := rule.NewRuleEngine()
	ruleManager := rule.NewRuleManager(rule.ManagerConfig{
		Dir:           cfg.Rules.Dir,
		HotReload:     cfg.Rules.HotReload,
		WatchInterval: cfg.Rules.WatchInterval,
		ScriptLoader:  scriptLoader,
	}, ruleEngine)

	// 加载规则文件
	if err := ruleManager.LoadAll(); err != nil {
		log.Printf("[Main] WARNING: load rules failed: %v", err)
	}

	// 启动热加载
	ruleManager.StartHotReload()
	defer ruleManager.StopHotReload()

	// ========================================
	// 创建事件广播器
	// ========================================
	eventBroadcaster := webui.NewEventBroadcaster(2000)

	// ========================================
	// 创建处理器调度（使用 JS 脚本运行时）
	// JS 脚本的编译已在 RuleManager 加载时预编译完成，调度器直接使用预编译的 Program
	// ========================================
	dispatcher := dispatch.NewProcessorDispatcher(scriptRuntime, ruleManager, eventBroadcaster)

	// 启动正向代理
	var fwdProxy *ForwardProxy
	if cfg.Proxy.Enable {
		var proxyErr error
		fwdProxy, proxyErr = NewForwardProxy(cfg.proxyConfig(),
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

	ginEngine.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Prometheus 自监控端点
	ginEngine.GET("/metrics", gin.WrapH(metrics.Handler()))

	// 规则管理 REST API
	registerRuleAPI(ginEngine, ruleManager)

	// Web UI 管理面板
	var connMgr *ConnManager
	if fwdProxy != nil {
		connMgr = fwdProxy.ConnMgr
	}
	replayer, err := replay.NewProxyReplayer(cfg.Proxy.Listen)
	if err != nil {
		log.Printf("[Main] WARNING: replay disabled: %v", err)
		replayer = nil
	}
	caMgr, caErr := ca.NewManager(cfg.Proxy.CertDir)
	if caErr != nil {
		log.Printf("[Main] WARNING: CA not loaded: %v", caErr)
	}
	webui.RegisterRoutes(ginEngine, ruleManager, eventBroadcaster,
		&proxyStatusProvider{collector: defaultCollector, connMgr: connMgr, cfg: cfg}, debugger, replayer, caMgr)

	// 诊断面板 + MCP Server
	if fwdProxy != nil {
		registerDebugRoutes(ginEngine, defaultCollector, fwdProxy.ConnMgr, cfg)
		registerMCPRoutes(ginEngine, &mcpServer{
			collector: defaultCollector,
			connMgr:   fwdProxy.ConnMgr,
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

// proxyStatusProvider 实现 webui.StatusProvider
type proxyStatusProvider struct {
	collector *DebugCollector
	connMgr   *ConnManager
	cfg       *Config
}

func (p *proxyStatusProvider) GetStatus() map[string]interface{} {
	status := make(map[string]interface{})

	// 运行时指标：goroutine 数 + 内存（定位泄漏的关键）
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	status["runtime"] = map[string]interface{}{
		"goroutines":        runtime.NumGoroutine(),
		"heap_alloc_bytes":  ms.HeapAlloc,
		"heap_inuse_bytes":  ms.HeapInuse,
		"heap_sys_bytes":    ms.HeapSys,
		"num_gc":            ms.NumGC,
	}

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
			"max":           p.connMgr.MaxConns,
		}
	}

	if p.cfg != nil {
		status["backend"] = "managed by scripts (@config)"
		status["proxy_listen"] = p.cfg.Proxy.Listen
	}

	return status
}
