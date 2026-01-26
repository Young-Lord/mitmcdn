package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mitmcdn/src/cache"
	"mitmcdn/src/config"
	"mitmcdn/src/download"
)

type MITMProxy struct {
	config        *config.Config
	cacheManager  *cache.Manager
	downloadSched *download.Scheduler
	certCache     sync.Map // host -> *tls.Certificate
}

func NewMITMProxy(cfg *config.Config, cacheMgr *cache.Manager, sched *download.Scheduler) *MITMProxy {
	return &MITMProxy{
		config:        cfg,
		cacheManager:  cacheMgr,
		downloadSched: sched,
	}
}

// HandleHTTP handles HTTP CONNECT requests (HTTPS tunneling)
func (p *MITMProxy) HandleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleHTTPS(w, r)
	} else {
		p.handleHTTPRequest(w, r)
	}
}

// handleHTTPS handles HTTPS CONNECT requests
func (p *MITMProxy) handleHTTPS(w http.ResponseWriter, r *http.Request) {
	// Extract target host
	host := r.Host
	if !strings.Contains(host, ":") {
		host += ":443"
	}

	// Check if this is a CDN we should intercept
	if !p.shouldIntercept(r.Host) {
		// Forward to upstream proxy or direct connection
		p.forwardConnect(w, r, host)
		return
	}

	// Hijack connection for MITM
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Send 200 Connection Established
	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// Generate or get certificate for this host
	cert, err := p.getCertificate(r.Host)
	if err != nil {
		clientConn.Close()
		return
	}

	// Create TLS connection with client
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*cert},
	}
	tlsConn := tls.Server(clientConn, tlsConfig)

	// Handle TLS connection
	go p.handleTLSConnection(tlsConn, r.Host)
}

// handleTLSConnection handles the TLS connection after handshake
func (p *MITMProxy) handleTLSConnection(conn *tls.Conn, host string) {
	defer conn.Close()

	// Read HTTP request from TLS connection
	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}

	// Reconstruct URL
	req.URL.Scheme = "https"
	req.URL.Host = host
	if req.URL.Path == "" {
		req.URL.Path = "/"
	}

	// Process request
	p.processRequest(req, conn)
}

// handleHTTPRequest handles plain HTTP requests
func (p *MITMProxy) handleHTTPRequest(w http.ResponseWriter, r *http.Request) {
	// Check if URL is invalid (e.g., https:///favicon.ico)
	if r.URL.Host == "" || r.URL.Scheme == "" || strings.HasPrefix(r.URL.String(), "https:///") || strings.HasPrefix(r.URL.String(), "http:///") {
		// Try to serve from assets directory
		if p.serveFromAssets(w, r) {
			return
		}
		// If not found in assets, return 404
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	if !p.shouldIntercept(r.Host) {
		// Forward to upstream
		p.forwardHTTP(w, r)
		return
	}

	// Process intercepted request
	p.processRequest(r, w)
}

// processRequest processes intercepted requests
func (p *MITMProxy) processRequest(r *http.Request, w io.Writer) {
	// Check CDN rules
	rule := p.findMatchingRule(r.URL.String(), r.Host)
	if rule == nil {
		// Not a CDN file, forward normally
		p.forwardHTTP(w.(http.ResponseWriter), r)
		return
	}

	// Extract cookie
	cookie := r.Header.Get("Cookie")

	// Extract filename
	filename := p.extractFilename(r.URL.Path)

	// Get or create file entry
	file, err := p.cacheManager.GetOrCreateFile(
		r.URL.String(),
		cookie,
		filename,
		rule.DedupStrategy,
	)
	if err != nil {
		// Log error with stack trace and forward
		logErrorWithStack(err, "Failed to get or create file: %s", r.URL.String())
		p.forwardHTTP(w.(http.ResponseWriter), r)
		return
	}

	// Check if file is complete
	if file.DownloadStatus == "complete" {
		// Serve from cache
		http.ServeFile(w.(http.ResponseWriter), r, file.SavedPath)
		return
	}

	// Stream file (will trigger download if needed)
	if httpWriter, ok := w.(http.ResponseWriter); ok {
		if err := p.downloadSched.StreamFile(file, httpWriter, r); err != nil {
			logErrorWithStack(err, "Failed to stream file: %s", r.URL.String())
			// Error already sent to client by StreamFile
		}
	}
}

// shouldIntercept checks if host should be intercepted
func (p *MITMProxy) shouldIntercept(host string) bool {
	for _, rule := range p.config.CDNRules {
		if strings.Contains(host, rule.Domain) {
			return true
		}
	}
	return false
}

// findMatchingRule finds matching CDN rule
func (p *MITMProxy) findMatchingRule(urlStr, host string) *config.CDNRule {
	for _, rule := range p.config.CDNRules {
		if cache.MatchCDNRule(urlStr, rule.Domain, rule.MatchPattern) {
			return &rule
		}
	}
	return nil
}

// extractFilename extracts filename from URL path
func (p *MITMProxy) extractFilename(path string) string {
	parts := strings.Split(path, "/")
	filename := parts[len(parts)-1]
	if idx := strings.Index(filename, "?"); idx != -1 {
		filename = filename[:idx]
	}
	return filename
}

// getCertificate generates or retrieves a certificate for the host
func (p *MITMProxy) getCertificate(host string) (*tls.Certificate, error) {
	// Check cache
	if cert, ok := p.certCache.Load(host); ok {
		return cert.(*tls.Certificate), nil
	}

	// Generate new certificate (simplified - in production use proper CA)
	// TODO: Implement proper certificate generation with CA
	cert, err := generateCertificate(host)
	if err != nil {
		return nil, err
	}

	p.certCache.Store(host, cert)
	return cert, nil
}

// generateCertificate is now implemented in cert.go

// forwardConnect forwards CONNECT request to upstream
func (p *MITMProxy) forwardConnect(w http.ResponseWriter, r *http.Request, target string) {
	if p.config.UpstreamProxy == "" {
		http.Error(w, "No upstream proxy configured", http.StatusBadGateway)
		return
	}

	// Parse upstream proxy
	proxyURL, err := url.Parse(p.config.UpstreamProxy)
	if err != nil {
		http.Error(w, "Invalid upstream proxy", http.StatusInternalServerError)
		return
	}

	// Connect to upstream proxy
	conn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer conn.Close()

	// Send CONNECT request to upstream
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)

	// Hijack client connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer clientConn.Close()

	// Send 200 to client
	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// Relay data
	go io.Copy(conn, clientConn)
	io.Copy(clientConn, conn)
}

// serveFromAssets tries to serve file from assets directory
func (p *MITMProxy) serveFromAssets(w http.ResponseWriter, r *http.Request) bool {
	if p.config.AssetsDir == "" {
		return false
	}

	// Extract filename from path
	filename := p.extractFilename(r.URL.Path)
	if filename == "" || filename == "/" {
		filename = "index.html"
	}

	// Build file path
	filePath := filepath.Join(p.config.AssetsDir, filename)
	
	// Security: ensure file is within assets directory
	absAssetsDir, err := filepath.Abs(p.config.AssetsDir)
	if err != nil {
		return false
	}
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}
	if !strings.HasPrefix(absFilePath, absAssetsDir) {
		return false
	}

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return false
	}

	// Serve file
	http.ServeFile(w, r, filePath)
	return true
}

// forwardHTTP forwards HTTP request to upstream
func (p *MITMProxy) forwardHTTP(w http.ResponseWriter, r *http.Request) {
	// Create reverse proxy
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			if req.URL.Scheme == "" {
				req.URL.Scheme = "https"
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logErrorWithStack(err, "HTTP proxy error: %s %s", r.Method, r.URL.String())
			// Try to serve from assets as fallback
			if p.serveFromAssets(w, r) {
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
		},
	}

	// Configure upstream proxy if needed
	if p.config.UpstreamProxy != "" {
		proxyURL, err := url.Parse(p.config.UpstreamProxy)
		if err == nil {
			proxy.Transport = &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			}
		}
	}

	proxy.ServeHTTP(w, r)
}
