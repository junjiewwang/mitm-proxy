package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// ForwardProxy 正向代理服务，支持 CONNECT 隧道和 MITM 拦截
type ForwardProxy struct {
	cfg          *Config
	handler      *handler
	caCert       *CACert
	connMgr      *ConnManager // MITM 连接管理器（有界 + LRU 淘汰）
	httpClient   *http.Client // 普通 HTTP 代理转发
	pooledClient *http.Client // MITM 透传（连接池复用 TLS）
}

// NewForwardProxy 创建正向代理实例
func NewForwardProxy(cfg *Config, h *handler) (*ForwardProxy, error) {
	ca, err := LoadOrGenerateCA(cfg.Proxy.CertDir)
	if err != nil {
		return nil, fmt.Errorf("load/generate CA: %w", err)
	}

	p := &ForwardProxy{
		cfg:     cfg,
		handler: h,
		caCert:  ca,
	}

	// 普通 HTTP 客户端
	p.httpClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}

	// 连接池客户端 — 自定义 Dialer 走 upstream_map
	p.pooledClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(addr)
				upstream := p.getUpstreamAddr(host)
				dialer := &net.Dialer{Timeout: 10 * time.Second}
				return tls.DialWithDialer(dialer, "tcp", upstream, &tls.Config{
					ServerName:         host, // SNI 用原始域名
					InsecureSkipVerify: true,
				})
			},
			MaxIdleConnsPerHost:   50, // 连接池 — 关键优化
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true, // 启用 HTTP/2
		},
	}

	// 连接管理器 — 有界 MITM 连接 + LRU 淘汰
	p.connMgr = NewConnManager(100, 10*time.Second)

	return p, nil
}

// ListenAndServe 启动正向代理监听
func (p *ForwardProxy) ListenAndServe(addr string) error {
	server := &http.Server{
		Addr:    addr,
		Handler: p,
	}

	// 定期打印连接池统计
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			active, evicted := p.connMgr.Stats()
			log.Printf("[ConnMgr] Stats: active_mitm=%d, total_evicted=%d", active, evicted)
		}
	}()

	log.Printf("[Proxy] Forward proxy listening on %s", addr)
	log.Printf("[Proxy] Configure SwitchyOmega: HTTP proxy → 127.0.0.1%s", addr)
	return server.ListenAndServe()
}

// ServeHTTP 处理代理请求
func (p *ForwardProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
	} else {
		p.handleHTTP(w, r)
	}
}

// handleConnect 处理 HTTPS CONNECT 隧道
func (p *ForwardProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if !strings.Contains(host, ":") {
		host += ":443"
	}

	hostname := strings.Split(host, ":")[0]

	// 判断是否需要 MITM 拦截（只对目标域名做 MITM）
	if p.shouldIntercept(hostname) {
		p.handleMITM(w, r, hostname)
		return
	}

	// 不需要拦截的域名，直接透传隧道
	p.handleTunnel(w, r, host)
}

// shouldIntercept 判断是否需要 MITM 拦截
func (p *ForwardProxy) shouldIntercept(hostname string) bool {
	for _, domain := range p.cfg.Proxy.InterceptDomains {
		if hostname == domain || strings.HasSuffix(hostname, "."+domain) {
			return true
		}
	}
	return false
}

// handleTunnel 直接透传隧道（不解密）
func (p *ForwardProxy) handleTunnel(w http.ResponseWriter, r *http.Request, targetHost string) {
	recordTraceTunnel(targetHost)

	// 解析上游地址（优先使用配置的映射）
	hostname, port := splitHostPort(targetHost)
	upstreamAddr := p.getUpstreamAddr(hostname)

	// 如果没有配置映射，保留原始端口
	if upstreamAddr == hostname+":443" && port != "" && port != "443" {
		upstreamAddr = hostname + ":" + port
	}

	log.Printf("[Proxy] Tunnel: %s -> %s", targetHost, upstreamAddr)

	// 连接上游服务器
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	destConn, err := dialer.Dial("tcp", upstreamAddr)
	if err != nil {
		http.Error(w, "Failed to connect to target", http.StatusBadGateway)
		log.Printf("[Proxy] Tunnel dial failed: %s -> %v", upstreamAddr, err)
		return
	}
	defer destConn.Close()

	// TCP 优化：禁用 Nagle + keep-alive（否则 TLS 小包被延迟合并，CDN 判定超时）
	if tcpConn, ok := destConn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(30 * time.Second)
	}

	// 回复 200 Connection Established
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "Hijack failed", http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	// TCP_NODELAY: 浏览器端 TLS 小包也立即发送
	if tcpConn, ok := clientConn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
	}

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// 双向透传
	go io.Copy(destConn, clientConn)
	io.Copy(clientConn, destConn)
}

// handleMITM 对需要拦截的域名做 MITM 解密
func (p *ForwardProxy) handleMITM(w http.ResponseWriter, r *http.Request, hostname string) {
	startTime := time.Now()

	// 生成该域名的 TLS 证书（首次生成耗时可忽略，后续缓存命中 <1ms）
	tlsCert, err := p.caCert.GenerateCert(hostname)
	if err != nil {
		http.Error(w, "Generate cert failed", http.StatusInternalServerError)
		log.Printf("[Proxy] MITM cert generation failed for %s: %v", hostname, err)
		return
	}

	// Hijack 连接
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Printf("[Proxy] Hijack failed: %v", err)
		return
	}
	defer clientConn.Close()

	// 回复 200 Connection Established
	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// 用生成的证书与客户端建立 TLS 连接
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*tlsCert},
	}
	// 设置 TLS 握手超时
	clientConn.SetDeadline(time.Now().Add(5 * time.Second))
	tlsConn := tls.Server(clientConn, tlsConfig)
	if err := tlsConn.Handshake(); err != nil {
		log.Printf("[Proxy] TLS handshake failed with client: %v", err)
		return
	}
	// 握手成功，清除 deadline，后续用 ReadDeadline 控制
	clientConn.SetDeadline(time.Time{})

	// 生成连接 ID 并注册到连接管理器
	connID := fmt.Sprintf("mitm-%s-%d", hostname, time.Now().UnixNano())
	if !p.connMgr.Register(connID, clientConn) {
		log.Printf("[Proxy] ConnMgr full, rejecting MITM: %s", hostname)
		clientConn.Close()
		return
	}

	log.Printf("[Proxy] MITM session established: %s (%.0fms)", hostname, time.Since(startTime).Seconds()*1000)

	defer func() {
		p.connMgr.Unregister(connID)
		tlsConn.Close()
		log.Printf("[Proxy] MITM session closed: %s (alive %.1fs)", hostname, time.Since(startTime).Seconds())
	}()

	// 从 TLS 连接读取 HTTP 请求
	reader := bufio.NewReader(tlsConn)
	requestCount := 0

	for {
		// 首个请求：5s 超时（快速关闭空闲连接）
		// 后续请求：30s 超时（保持 keep-alive 复用）
		timeout := 5 * time.Second
		if requestCount > 0 {
			timeout = 30 * time.Second
		}
		tlsConn.SetReadDeadline(time.Now().Add(timeout))

		req, err := http.ReadRequest(reader)
		if err != nil {
			return // EOF 或超时，正常退出
		}
		requestCount++

		// 标记连接活跃（LRU 提升）
		p.connMgr.Touch(connID)

		// 设置完整 URL
		req.URL.Scheme = "https"
		req.URL.Host = hostname

		// 判断是否需要拦截这个请求
		if p.shouldInterceptRequest(req) {
			log.Printf("[Proxy] *** INTERCEPTED: %s %s", req.Method, req.URL.String())
			p.handleInterceptedRequest(tlsConn, req)
			// 继续读取下一个请求，支持多路复用
		} else {
			// 透传到原始目标
			p.forwardToOriginal(tlsConn, req, hostname)
		}
	}
}

// shouldInterceptRequest 判断是否拦截此 HTTP 请求
func (p *ForwardProxy) shouldInterceptRequest(r *http.Request) bool {
	// 只拦截 POST /cgi/capi?action=delegate&serviceType=apm
	if r.Method != http.MethodPost {
		return false
	}
	if r.URL.Path != "/cgi/capi" {
		return false
	}
	query := r.URL.Query()
	return query.Get("serviceType") == "apm" && query.Get("action") == "delegate"
}

// handleInterceptedRequest 处理被拦截的请求（转发到本地后端）
func (p *ForwardProxy) handleInterceptedRequest(conn net.Conn, r *http.Request) {
	startTime := time.Now()

	// 读取请求体
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErrorResponse(conn, http.StatusBadRequest, "read body failed")
		return
	}
	r.Body.Close()

	// 使用现有 handler 逻辑处理
	resp := p.handler.processRequest(body)

	// 写回响应
	// 根据客户端 Connection 头决定是否保持连接（浏览器 keep-alive 复用，curl close 立即断开）
	closeConn := strings.EqualFold(r.Header.Get("Connection"), "close")
	httpResp := &http.Response{
		StatusCode: resp.statusCode,
		Status:     http.StatusText(resp.statusCode),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(resp.body))),
		Close:      closeConn,
	}
	httpResp.Header.Set("Content-Type", "application/json; charset=utf-8")
	httpResp.Header.Set("Content-Length", fmt.Sprintf("%d", len(resp.body)))
	httpResp.Header.Set("Access-Control-Allow-Origin", "*")
	httpResp.Write(conn)

	// 诊断追踪
	recordTraceIntercept(r.URL.Query().Get("cmd"), resp.statusCode, time.Since(startTime))
}

// forwardToOriginal 透传请求到原始目标服务器（使用连接池复用 TLS 连接）
func (p *ForwardProxy) forwardToOriginal(conn net.Conn, r *http.Request, hostname string) {
	startTime := time.Now()
	method := r.Method
	path := r.URL.Path
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}

	// 构建上游 URL
	upstreamURL := "https://" + hostname + r.URL.RequestURI()

	// 请求体：nil 表示无 body（GET/HEAD 等），r.Body 表示需要透传的 body
	var body io.Reader
	if r.Body != nil && r.ContentLength != 0 {
		body = r.Body
	}

	proxyReq, err := http.NewRequest(method, upstreamURL, body)
	if err != nil {
		log.Printf("[Proxy] [FWD] %s %s -> CREATE ERR: %v", method, path, err)
		writeErrorResponse(conn, http.StatusBadGateway, "create upstream request failed")
		return
	}

	// 复制 Header（排除 hop-by-hop），保留必要的 Host
	for k, vv := range r.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}

	// Content-Length 需要显式设置（body reader 不会自动设置）
	if body != nil && r.ContentLength > 0 {
		proxyReq.ContentLength = r.ContentLength
	} else if body != nil {
		proxyReq.ContentLength = -1 // chunked
	}

	log.Printf("[Proxy] [FWD] %s %s -> upstream", method, truncateStr(path, 120))

	// 通过连接池客户端转发（自动复用 TLS 连接）
	resp, err := p.pooledClient.Do(proxyReq)
	if err != nil {
		log.Printf("[Proxy] [FWD] %s %s -> ERR (%.0fms): %v", method, path, time.Since(startTime).Seconds()*1000, err)
		writeErrorResponse(conn, http.StatusBadGateway, "upstream request failed")
		return
	}
	defer resp.Body.Close()

	sizeStr := fmt.Sprintf("%d bytes", resp.ContentLength)
	if resp.ContentLength < 0 {
		sizeStr = "chunked"
	}
	log.Printf("[Proxy] [FWD] %s %s -> %d (%s, %.0fms)",
		method, path, resp.StatusCode, sizeStr, time.Since(startTime).Seconds()*1000)

	// 复制响应 Header（排除 hop-by-hop）
	respHeader := make(http.Header)
	for k, vv := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			respHeader.Add(k, v)
		}
	}

	// 构建并写回响应
	httpResp := &http.Response{
		StatusCode:    resp.StatusCode,
		Status:        resp.Status,
		Proto:         resp.Proto,
		ProtoMajor:    resp.ProtoMajor,
		ProtoMinor:    resp.ProtoMinor,
		Header:        respHeader,
		Body:          resp.Body,
		ContentLength: resp.ContentLength, // 关键：否则默认 0 导致大响应截断
		Close:         r.Close,
	}
	httpResp.Write(conn)

	// 诊断追踪
	recordTraceFWD(method, path, resp.StatusCode, time.Since(startTime), true, "")
}

// recordFWDErr 记录错误追踪
func recordFWDErr(method, path string, elapsed time.Duration, errMsg string) {
	recordTraceFWD(method, path, 0, elapsed, false, errMsg)
}

// isHopByHop 判断是否为 hop-by-hop 头（不应转发）
func isHopByHop(header string) bool {
	switch strings.ToLower(header) {
	case "connection", "keep-alive", "proxy-connection",
		"proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

// getUpstreamAddr 获取上游服务器地址
func (p *ForwardProxy) getUpstreamAddr(hostname string) string {
	// 如果配置了 upstream 映射，使用映射地址
	if addr, ok := p.cfg.Proxy.UpstreamMap[hostname]; ok {
		return addr
	}
	// 默认使用原始域名
	return hostname + ":443"
}

// handleHTTP 处理普通 HTTP 代理请求（非 CONNECT）
func (p *ForwardProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	// 构建目标 URL
	targetURL := r.URL.String()
	if !r.URL.IsAbs() {
		targetURL = "http://" + r.Host + r.URL.RequestURI()
	}

	// 转发请求
	proxyReq, err := http.NewRequest(r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, "Create request failed", http.StatusBadGateway)
		return
	}

	// 复制 header
	for k, vv := range r.Header {
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}

	resp, err := p.httpClient.Do(proxyReq)
	if err != nil {
		http.Error(w, "Forward request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 复制响应 header
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// writeErrorResponse 写入错误响应
func writeErrorResponse(conn net.Conn, statusCode int, message string) {
	body := fmt.Sprintf(`{"code":-1,"message":"%s"}`, message)
	resp := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		statusCode, http.StatusText(statusCode), len(body), body)
	conn.Write([]byte(resp))
}

// splitHostPort 分割 host:port，返回 hostname 和 port
func splitHostPort(host string) (string, string) {
	if !strings.Contains(host, ":") {
		return host, ""
	}
	parts := strings.SplitN(host, ":", 2)
	return parts[0], parts[1]
}

// truncateStr 截断字符串
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
