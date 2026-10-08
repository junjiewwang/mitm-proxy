package main

import (
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof" // 注册 pprof handler 到 DefaultServeMux
	"os"
	"runtime"
	"time"
)

// startPprofServer 启动 pprof 诊断端口（独立于业务 gin 服务，避免相互干扰）
func startPprofServer(addr string) {
	if addr == "" {
		return
	}
	go func() {
		// 使用 DefaultServeMux，_ "net/http/pprof" 已注册 /debug/pprof/ 路由
		log.Printf("[Monitor] pprof listening on %s", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Printf("[Monitor] pprof server error: %v", err)
		}
	}()
}

// startRuntimeMonitor 启动运行时指标监控：
//   - 定时打印 goroutine 数 / 内存，形成趋势日志
//   - goroutine 数超过阈值时自动 dump 全量 goroutine 栈到文件
func startRuntimeMonitor(interval time.Duration, goroutineThreshold int, dumpDir string) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			numGoroutine := runtime.NumGoroutine()
			log.Printf("[Monitor] goroutines=%d heap_alloc=%dKB heap_inuse=%dKB sys=%dKB",
				numGoroutine, ms.HeapAlloc/1024, ms.HeapInuse/1024, ms.Sys/1024)

			if goroutineThreshold > 0 && numGoroutine >= goroutineThreshold {
				dumpGoroutines(dumpDir, numGoroutine)
			}
		}
	}()
}

// dumpGoroutines 将全量 goroutine 栈 dump 到文件（用于离线分析泄漏现场）
func dumpGoroutines(dir string, numGoroutine int) {
	if dir == "" {
		dir = "/tmp"
	}
	// 16MB 缓冲足够容纳上万 goroutine 的完整栈
	buf := make([]byte, 16*1024*1024)
	n := runtime.Stack(buf, true)
	filename := fmt.Sprintf("%s/mitm-proxy-goroutine-%d-%d.txt", dir, numGoroutine, time.Now().Unix())
	if err := os.WriteFile(filename, buf[:n], 0644); err != nil {
		log.Printf("[Monitor] dump goroutines failed: %v", err)
		return
	}
	log.Printf("[Monitor] ⚠ goroutine threshold exceeded (%d), dumped stack to %s", numGoroutine, filename)
}
