package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"

	"mitmcdn/src/cache"
	"mitmcdn/src/config"
	"mitmcdn/src/download"

	"gorm.io/gorm"
)

// UnifiedServer handles multiple protocols on a single port
type UnifiedServer struct {
	config        *config.Config
	cacheManager  *cache.Manager
	downloadSched *download.Scheduler
	mitmProxy     *MITMProxy
	reverseProxy  *HTTPReverseProxy
	socks5Proxy   *SOCKS5Proxy
	statusHandler *StatusHandler
}

// NewUnifiedServer creates a unified server that can handle multiple protocols
func NewUnifiedServer(cfg *config.Config, cacheMgr *cache.Manager, sched *download.Scheduler, db *gorm.DB) (*UnifiedServer, error) {
	mitmProxy := NewMITMProxy(cfg, cacheMgr, sched)
	reverseProxy := NewHTTPReverseProxy(cfg, cacheMgr, sched, mitmProxy)

	var socks5Proxy *SOCKS5Proxy
	var err error
	if cfg.ProxyMode == "all" || cfg.ProxyMode == "socks5" {
		socks5Proxy, err = NewSOCKS5Proxy(cfg, cacheMgr, sched, mitmProxy)
		if err != nil {
			return nil, fmt.Errorf("failed to create SOCKS5 proxy: %w", err)
		}
	}

	// Create status handler
	var statusHandler *StatusHandler
	if db != nil {
		statusHandler = NewStatusHandler(db, cacheMgr, sched)
	}

	return &UnifiedServer{
		config:        cfg,
		cacheManager:  cacheMgr,
		downloadSched: sched,
		mitmProxy:     mitmProxy,
		reverseProxy:  reverseProxy,
		socks5Proxy:   socks5Proxy,
		statusHandler: statusHandler,
	}, nil
}

// ServeHTTP handles HTTP requests (both proxy and reverse proxy)
func (s *UnifiedServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Handle status endpoints
	if path == "/api/status" || path == "/api/status/" {
		if s.statusHandler != nil {
			s.statusHandler.HandleAPIStatus(w, r)
			return
		}
		http.Error(w, "Status handler not available", http.StatusServiceUnavailable)
		return
	}

	if path == "/status" || path == "/status/" {
		if s.statusHandler != nil {
			s.statusHandler.HandleStatusPage(w, r)
			return
		}
		http.Error(w, "Status handler not available", http.StatusServiceUnavailable)
		return
	}

	// Check if this is a reverse proxy request (URL path mode)
	// Format: /https://target.com/file or /http://target.com/file
	if strings.HasPrefix(path, "/http://") || strings.HasPrefix(path, "/https://") {
		// This is a reverse proxy request
		s.reverseProxy.ServeHTTP(w, r)
		return
	}

	// Otherwise, handle as HTTP proxy
	s.mitmProxy.HandleHTTP(w, r)
}

// detectProtocol detects the protocol by peeking at the first bytes
func detectProtocol(conn *peekConn) (string, error) {
	// Read first byte to detect protocol
	buf := make([]byte, 1)
	n, err := conn.Conn.Read(buf)
	if err != nil && n == 0 {
		return "", err
	}

	// Store peeked byte
	conn.buffer = buf[:n]

	// SOCKS5 starts with 0x05 (version byte)
	if buf[0] == 0x05 {
		return "socks5", nil
	}

	// Try to read more bytes for HTTP detection
	if n == 1 {
		moreBuf := make([]byte, 3)
		moreN, _ := conn.Conn.Read(moreBuf)
		if moreN > 0 {
			conn.buffer = append(conn.buffer, moreBuf[:moreN]...)
		}
	}

	// Check for HTTP methods
	if len(conn.buffer) >= 4 {
		method := string(conn.buffer[:4])
		if method == "GET " || method == "POST" || method == "CONN" || method == "HEAD" || method == "PUT " {
			return "http", nil
		}
	}

	// Check for TLS handshake (HTTPS)
	if len(conn.buffer) >= 1 && buf[0] == 0x16 { // TLS handshake record type
		return "https", nil
	}

	return "http", nil // Default to HTTP
}

// handleConnection handles a single connection with protocol detection
func (s *UnifiedServer) handleConnection(conn net.Conn) {
	// Create a peekable connection to detect protocol
	peekConn := &peekConn{Conn: conn, peeked: false}

	// Detect protocol
	protocol, err := detectProtocol(peekConn)
	if err != nil {
		conn.Close()
		return
	}

	switch protocol {
	case "socks5":
		if s.socks5Proxy != nil {
			// Handle SOCKS5 - the connection has been peeked, so we need to handle it
			s.socks5Proxy.server.ServeConn(peekConn)
		} else {
			conn.Close()
		}
	case "http", "https":
		// Handle HTTP/HTTPS (proxy or reverse proxy)
		if protocol == "https" {
			// For HTTPS, we need to do TLS handshake first
			cert, err := GenerateCertificate("localhost")
			if err == nil {
				tlsConfig := &tls.Config{
					Certificates: []tls.Certificate{*cert},
					GetCertificate: func(clientHello *tls.ClientHelloInfo) (*tls.Certificate, error) {
						return GenerateCertificate(clientHello.ServerName)
					},
				}
				// Wrap connection with TLS
				tlsConn := tls.Server(peekConn, tlsConfig)
				s.handleHTTPConnection(tlsConn)
			} else {
				conn.Close()
			}
		} else {
			s.handleHTTPConnection(peekConn)
		}
	default:
		conn.Close()
	}
}

// peekConn wraps a connection to allow peeking without consuming
type peekConn struct {
	net.Conn
	peeked bool
	buffer []byte
}

func (p *peekConn) Read(b []byte) (int, error) {
	if !p.peeked && len(p.buffer) > 0 {
		// Return peeked data first
		n := copy(b, p.buffer)
		if n < len(p.buffer) {
			p.buffer = p.buffer[n:]
		} else {
			p.buffer = nil
		}
		p.peeked = true
		return n, nil
	}
	return p.Conn.Read(b)
}

// handleHTTPConnection handles HTTP connection
func (s *UnifiedServer) handleHTTPConnection(conn net.Conn) {
	// Create a buffered reader to read the request
	br := bufio.NewReader(conn)

	// Read HTTP request
	req, err := http.ReadRequest(br)
	if err != nil {
		conn.Close()
		return
	}

	// Create a response writer
	w := &responseWriter{
		conn:   conn,
		header: make(http.Header),
		writer: bufio.NewWriter(conn),
	}

	// Handle the request
	s.ServeHTTP(w, req)
	
	// Close chunked encoding if used
	w.Close()
	w.Flush()
	
	// Ensure connection is closed after response
	// For HTTP/1.1, if Content-Length is set correctly, connection should close automatically
	// But we'll close it explicitly to be safe
	if req.Close || req.Header.Get("Connection") == "close" {
		conn.Close()
	}
}

// responseWriter implements http.ResponseWriter for raw connections
type responseWriter struct {
	conn        net.Conn
	header      http.Header
	status      int
	wroteHeader bool
	chunked     bool
	writer      *bufio.Writer
}

func (w *responseWriter) Header() http.Header {
	return w.header
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	
	if w.chunked {
		// Write chunk size in hex
		fmt.Fprintf(w.writer, "%x\r\n", len(b))
		// Write chunk data
		n, err := w.writer.Write(b)
		if err != nil {
			return n, err
		}
		// Write chunk trailer
		w.writer.Write([]byte("\r\n"))
		return n, nil
	}
	
	return w.writer.Write(b)
}

func (w *responseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return // Already written
	}
	w.wroteHeader = true
	w.status = statusCode
	
	// Determine if we need chunked encoding
	// Use chunked if no Content-Length is set
	if w.header.Get("Content-Length") == "" && w.header.Get("Transfer-Encoding") == "" {
		w.chunked = true
		w.header.Set("Transfer-Encoding", "chunked")
	}
	
	statusText := http.StatusText(statusCode)
	fmt.Fprintf(w.writer, "HTTP/1.1 %d %s\r\n", statusCode, statusText)
	w.header.Write(w.writer)
	w.writer.Write([]byte("\r\n"))
	w.writer.Flush()
}

func (w *responseWriter) Flush() {
	if w.writer != nil {
		w.writer.Flush()
	}
}

// Close closes the response writer and sends the final chunk if using chunked encoding
func (w *responseWriter) Close() error {
	if w.chunked && w.wroteHeader {
		// Send final chunk (0-sized chunk to signal end)
		w.writer.Write([]byte("0\r\n\r\n"))
		w.writer.Flush()
	}
	return nil
}

// ListenAndServe starts the unified server on the specified address
// It handles HTTP Proxy, HTTP Reverse Proxy, HTTPS Server, and SOCKS5 on the same port
func (s *UnifiedServer) ListenAndServe(addr string) error {
	// Create listener
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	// Start accepting connections with protocol detection
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}

		go s.handleConnection(conn)
	}
}
