package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ========================================
// 实时诊断：请求追踪 + 统计 + HTML 面板
// ========================================

// RequestTrace 单次请求追踪记录
type RequestTrace struct {
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	Host       string    `json:"host"`
	Path       string    `json:"path"`
	StatusCode int       `json:"status"`
	LatencyMs  float64   `json:"latency_ms"`
	Cached     bool      `json:"cached"` // 连接池命中
	Intercept  bool      `json:"intercept"`
	Error      string    `json:"error,omitempty"`
}

// DebugCollector 诊断数据收集器
type DebugCollector struct {
	mu         sync.Mutex
	ring       []*RequestTrace
	pos        int
	totalReqs  int64
	totalErrs  int64
	startTime  time.Time
}

func NewDebugCollector(size int) *DebugCollector {
	if size <= 0 {
		size = 100
	}
	return &DebugCollector{
		ring:      make([]*RequestTrace, size),
		startTime: time.Now(),
	}
}

// Record 记录一条请求
func (d *DebugCollector) Record(t *RequestTrace) {
	d.mu.Lock()
	d.ring[d.pos] = t
	d.pos = (d.pos + 1) % len(d.ring)
	d.totalReqs++
	if t.Error != "" || t.StatusCode >= 400 {
		d.totalErrs++
	}
	d.mu.Unlock()
}

// Snapshot 获取当前快照
func (d *DebugCollector) Snapshot() (recent []*RequestTrace, totalReqs, totalErrs int64, uptime time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 从 ring buffer 提取最近记录（已排序）
	count := 0
	for _, t := range d.ring {
		if t != nil {
			count++
		}
	}
	recent = make([]*RequestTrace, 0, count)
	// 按时间排序
	all := make([]*RequestTrace, 0, count)
	for _, t := range d.ring {
		if t != nil {
			all = append(all, t)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Time.Before(all[j].Time)
	})
	// 取最近 50 条
	start := 0
	if len(all) > 50 {
		start = len(all) - 50
	}
	recent = all[start:]

	return recent, d.totalReqs, d.totalErrs, time.Since(d.startTime)
}

// ========================================
// Gin Handler: /debug
// ========================================

func registerDebugRoutes(engine *gin.Engine, collector *DebugCollector, connMgr *ConnManager, cfg *Config) {
	engine.GET("/debug", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(debugHTML))
	})

	engine.GET("/debug/status", func(c *gin.Context) {
		recent, total, errs, uptime := collector.Snapshot()
		var active, evicted int64
		var maxConns int
		if connMgr != nil {
			active, evicted = connMgr.Stats()
			maxConns = connMgr.MaxConns
		}

		// 按路径聚合延迟
		pathStats := make(map[string]struct {
			Count int
			Total float64
			Max   float64
			Errs  int
		})
		collector.mu.Lock()
		for _, t := range collector.ring {
			if t == nil {
				continue
			}
			key := t.Method + " " + t.Path
			s := pathStats[key]
			s.Count++
			s.Total += t.LatencyMs
			if t.LatencyMs > s.Max {
				s.Max = t.LatencyMs
			}
			if t.StatusCode >= 400 || t.Error != "" {
				s.Errs++
			}
			pathStats[key] = s
		}
		collector.mu.Unlock()

		type PathStat struct {
			Path  string  `json:"path"`
			Count int     `json:"count"`
			Avg   float64 `json:"avg_ms"`
			Max   float64 `json:"max_ms"`
			Errs  int     `json:"errors"`
		}
		var topPaths []PathStat
		for k, v := range pathStats {
			topPaths = append(topPaths, PathStat{
				Path:  k,
				Count: v.Count,
				Avg:   float64(int(v.Total/float64(v.Count)*10)) / 10,
				Max:   v.Max,
				Errs:  v.Errs,
			})
		}
		sort.Slice(topPaths, func(i, j int) bool {
			return topPaths[i].Count > topPaths[j].Count
		})

		c.JSON(http.StatusOK, gin.H{
			"uptime_sec":    int(uptime.Seconds()),
			"total_requests": total,
			"total_errors":   errs,
			"conn_pool": gin.H{
				"active_mitm":  active,
				"total_evicted": evicted,
				"max":          maxConns,
			},
			"backend":     "managed by scripts (@config)",
			"top_paths":   topPaths,
			"recent_reqs": recent,
		})
	})
}

// ========================================
// HTML 诊断面板
// ========================================

const debugHTML = `<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>Proxy Debug</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:Menlo,Monaco,monospace;background:#0d1117;color:#c9d1d9;padding:20px}
h1{font-size:18px;color:#58a6ff;margin-bottom:16px}
h2{font-size:14px;color:#58a6ff;margin:16px 0 8px}
.card{background:#161b22;border:1px solid #30363d;border-radius:6px;padding:12px;margin-bottom:12px}
.stat{display:inline-block;margin:0 24px 8px 0}
.stat .label{font-size:11px;color:#8b949e}
.stat .value{font-size:20px;font-weight:bold;color:#e6edf3}
table{width:100%;border-collapse:collapse;font-size:12px}
th,td{padding:4px 8px;text-align:left;border-bottom:1px solid #21262d}
th{color:#8b949e;font-size:11px}
td .ok{color:#3fb950}td .err{color:#f85149}td .warn{color:#d29922}
td .path{max-width:300px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.flex{display:flex;gap:12px}.flex>*{flex:1}
.footer{font-size:11px;color:#484f58;margin-top:16px;text-align:center}
</style></head><body>
<h1>Proxy Debug Panel</h1>
<div class="flex">
<div class="card"><div class="stat"><div class="label">Total Requests</div><div class="value" id="total">-</div></div><div class="stat"><div class="label">Errors</div><div class="value err" id="errors">-</div></div><div class="stat"><div class="label">Uptime</div><div class="value" id="uptime">-</div></div></div>
<div class="card"><div class="stat"><div class="label">MITM Active</div><div class="value" id="active">-</div></div><div class="stat"><div class="label">MITM Evicted</div><div class="value warn" id="evicted">-</div></div><div class="stat"><div class="label">Backend</div><div class="value" id="backend" style="font-size:12px">-</div></div></div>
</div>
<h2>Top Paths</h2>
<div class="card"><table><thead><tr><th>Path</th><th>#</th><th>Avg ms</th><th>Max ms</th><th>Err</th></tr></thead><tbody id="paths"></tbody></table></div>
<h2>Recent Requests (last 50)</h2>
<div class="card"><table><thead><tr><th>Time</th><th>Method</th><th>Path</th><th>Status</th><th>Latency</th><th></th></tr></thead><tbody id="recent"></tbody></table></div>
<div class="footer">Refresh: 2s | <span id="lastUpdate"></span></div>
<script>
function fmtMs(ms){return ms<1000?ms.toFixed(0)+'ms':(ms/1000).toFixed(1)+'s'}
function fmtAge(t){var s=(Date.now()-new Date(t).getTime())/1000;return s<60?s.toFixed(0)+'s':s<3600?Math.floor(s/60)+'m':Math.floor(s/3600)+'h'}
function render(d){
document.getElementById('total').textContent=d.total_requests||0
document.getElementById('errors').textContent=d.total_errors||0
document.getElementById('uptime').textContent=fmtMs((d.uptime_sec||0)*1000)
document.getElementById('active').textContent=d.conn_pool.active_mitm
document.getElementById('evicted').textContent=d.conn_pool.total_evicted
document.getElementById('backend').textContent=d.backend
var p=''
;(d.top_paths||[]).forEach(function(x){
p+='<tr><td class="path" title="'+x.path+'">'+x.path+'</td><td>'+x.count+'</td><td>'+x.avg_ms+'</td><td class="'+(x.max_ms>3000?'warn':'')+'">'+x.max_ms+'</td><td class="'+(x.errors>0?'err':'ok')+'">'+x.errors+'</td></tr>'
})
document.getElementById('paths').innerHTML=p||'<tr><td colspan=5>No data</td></tr>'
var r=''
;(d.recent_reqs||[]).slice(-20).reverse().forEach(function(x){
var cls=x.error||x.status>=400?'err':x.status==304?'warn':'ok'
r+='<tr><td>'+fmtAge(x.time)+'</td><td>'+x.method+'</td><td class="path" title="'+x.path+'">'+x.path+'</td><td class="'+cls+'">'+x.status+'</td><td class="'+(x.latency_ms>3000?'warn':'')+'">'+fmtMs(x.latency_ms)+'</td><td>'+(x.intercept?'<span style="color:#58a6ff">🎯</span>':'')+(x.cached?'<span style="color:#3fb950">♻</span>':'')+(x.error?'<span class="err" title="'+x.error+'">⚠</span>':'')+'</td></tr>'
})
document.getElementById('recent').innerHTML=r||'<tr><td colspan=6>No requests yet</td></tr>'
document.getElementById('lastUpdate').textContent=new Date().toLocaleTimeString()
}
function poll(){fetch('/debug/status').then(function(r){return r.json()}).then(render).catch(function(e){console.error(e)});setTimeout(poll,2000)}
poll()
</script></body></html>`

// ========================================
// 便捷记录方法（proxy.go 调用）
// ========================================

var defaultCollector *DebugCollector

func initDebugCollector() {
	defaultCollector = NewDebugCollector(200)
}

func recordTraceFWD(method, host, path string, status int, latency time.Duration, cached bool, err string) {
	if defaultCollector == nil {
		return
	}
	defaultCollector.Record(&RequestTrace{
		Time:       time.Now(),
		Method:     method,
		Host:       host,
		Path:       path,
		StatusCode: status,
		LatencyMs:  float64(latency.Microseconds()) / 1000.0,
		Cached:     cached,
	})
}

func recordTraceTunnel(host string) {
	if defaultCollector == nil {
		return
	}
	defaultCollector.Record(&RequestTrace{
		Time:       time.Now(),
		Method:     "TUNNEL",
		Host:       host,
		Path:       host,
		StatusCode: 200,
		LatencyMs:  0,
	})
}

func recordTraceIntercept(host, path string, status int, latency time.Duration) {
	if defaultCollector == nil {
		return
	}
	defaultCollector.Record(&RequestTrace{
		Time:       time.Now(),
		Method:     "POST",
		Host:       host,
		Path:       path,
		StatusCode: status,
		LatencyMs:  float64(latency.Microseconds()) / 1000.0,
		Intercept:  true,
	})
}

// dumpJSON 调试用 JSON 格式化
func dumpJSON(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
