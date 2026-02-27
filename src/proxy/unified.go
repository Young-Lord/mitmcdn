package proxy

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"mitmcdn/src/cache"
	"mitmcdn/src/config"
	"mitmcdn/src/database"
	"mitmcdn/src/download"
	"mitmcdn/src/htmlplugin"

	"gorm.io/gorm"
)

// UnifiedServer handles multiple protocols on a single port
type UnifiedServer struct {
	config        *config.Config
	db            *gorm.DB
	cacheManager  *cache.Manager
	downloadSched *download.Scheduler
	mitmProxy     *MITMProxy
	reverseProxy  *HTTPReverseProxy
	socks5Proxy   *SOCKS5Proxy
	statusHandler *StatusHandler
	listener      net.Listener
}

// NewUnifiedServer creates a unified server that can handle multiple protocols
func NewUnifiedServer(cfg *config.Config, cacheMgr *cache.Manager, sched *download.Scheduler, htmlPlugins *htmlplugin.Manager, db *gorm.DB) (*UnifiedServer, error) {
	mitmProxy := NewMITMProxy(cfg, cacheMgr, sched, htmlPlugins)
	reverseProxy := NewHTTPReverseProxy(cfg, cacheMgr, sched, mitmProxy, htmlPlugins)

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
		db:            db,
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

	// Handle cached YouTube video endpoints
	if strings.HasPrefix(path, "/cache/yt/") {
		s.handleCacheYT(w, r, path)
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
			// ServeConn is blocking, but we're already in a goroutine
			if err := s.socks5Proxy.server.ServeConn(peekConn); err != nil {
				// Log error but don't fail - connection might have been closed
			}
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

// handleHTTPConnection handles HTTP connection with keep-alive support
func (s *UnifiedServer) handleHTTPConnection(conn net.Conn) {
	br := bufio.NewReader(conn)

	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			conn.Close()
			return
		}

		w := &responseWriter{
			conn:   conn,
			reader: br,
			header: make(http.Header),
			writer: bufio.NewWriter(conn),
		}

		s.ServeHTTP(w, req)

		if w.hijacked {
			return
		}

		w.Close()
		w.Flush()

		// Close connection if client requested it or HTTP/1.0
		if req.Close || req.Header.Get("Connection") == "close" || req.ProtoMajor < 1 || (req.ProtoMajor == 1 && req.ProtoMinor == 0) {
			conn.Close()
			return
		}
	}
}

// responseWriter implements http.ResponseWriter for raw connections
type responseWriter struct {
	conn        net.Conn
	reader      *bufio.Reader
	header      http.Header
	status      int
	wroteHeader bool
	chunked     bool
	hijacked    bool
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

func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.hijacked {
		return nil, nil, fmt.Errorf("connection already hijacked")
	}
	w.hijacked = true
	return w.conn, bufio.NewReadWriter(w.reader, w.writer), nil
}

// Close closes the response writer and sends the final chunk if using chunked encoding
func (w *responseWriter) Close() error {
	if w.hijacked {
		return nil
	}
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
	s.listener = listener

	// Start accepting connections with protocol detection
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}

		go s.handleConnection(conn)
	}
}

// Shutdown gracefully shuts down the server
func (s *UnifiedServer) Shutdown(ctx context.Context) error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

var ytVideoIDRegex = regexp.MustCompile(`^/cache/yt/([A-Za-z0-9_-]{6,})/(player|video)$`)

// handleCacheYT serves cached YouTube video files and an embedded player page.
func (s *UnifiedServer) handleCacheYT(w http.ResponseWriter, r *http.Request, path string) {
	m := ytVideoIDRegex.FindStringSubmatch(path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	videoID := m[1]
	action := m[2]

	// Look up the cache entry for this video.
	cacheURL := fmt.Sprintf("yt-dlp://%s", videoID)
	hashBytes := sha256.Sum256([]byte(cacheURL))
	fileHash := hex.EncodeToString(hashBytes[:])

	var file database.File
	if err := s.db.Where("file_hash = ?", fileHash).First(&file).Error; err != nil {
		http.Error(w, "Video not cached", http.StatusNotFound)
		return
	}

	switch action {
	case "video":
		s.serveCachedVideo(w, r, &file)
	case "player":
		s.serveVideoPlayer(w, r, videoID, &file)
	}
}

// serveCachedVideo streams the cached video file with Range support.
func (s *UnifiedServer) serveCachedVideo(w http.ResponseWriter, r *http.Request, file *database.File) {
	if file.DownloadStatus != "complete" {
		http.Error(w, "Video still downloading", http.StatusServiceUnavailable)
		return
	}

	f, err := os.Open(file.SavedPath)
	if err != nil {
		http.Error(w, "Cache file not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	ct := file.ContentType
	if ct == "" {
		ct = "video/mp4"
	}

	totalSize := info.Size()
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Accept-Ranges", "bytes")

	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		// Full response
		w.Header().Set("Content-Length", strconv.FormatInt(totalSize, 10))
		w.WriteHeader(http.StatusOK)
		io.CopyN(w, f, totalSize)
		return
	}

	// Parse "bytes=START-END"
	var start, end int64
	end = totalSize - 1
	n, _ := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end)
	if n == 0 {
		// Try "bytes=START-"
		fmt.Sscanf(rangeHeader, "bytes=%d-", &start)
		end = totalSize - 1
	}
	if start < 0 || start >= totalSize || end >= totalSize || start > end {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", totalSize))
		http.Error(w, "Range Not Satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	length := end - start + 1
	f.Seek(start, io.SeekStart)

	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, totalSize))
	w.WriteHeader(http.StatusPartialContent)
	io.CopyN(w, f, length)
}

// serveVideoPlayer returns a minimal HTML5 video player page.
func (s *UnifiedServer) serveVideoPlayer(w http.ResponseWriter, r *http.Request, videoID string, file *database.File) {
	videoSrc := fmt.Sprintf("/cache/yt/%s/video", videoID)

	var statusNote string
	switch file.DownloadStatus {
	case "complete":
		statusNote = ""
	case "downloading":
		statusNote = `<p style="color:#f0ad4e;text-align:center;">Video is still downloading&hellip; Refresh later.</p>`
	default:
		statusNote = fmt.Sprintf(`<p style="color:#d9534f;text-align:center;">Video status: %s</p>`, file.DownloadStatus)
	}

	ct := file.ContentType
	if ct == "" {
		ct = "video/mp4"
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s - Cached Video</title>
<style>*{margin:0;padding:0;box-sizing:border-box}html,body{width:100%%;height:100%%;background:#000;overflow:hidden}
video{width:100%%;height:100%%;object-fit:contain}</style></head>
<body>%s<video controls autoplay><source src="%s" type="%s">Your browser does not support the video tag.</video></body></html>`,
		videoID, statusNote, videoSrc, ct)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(html)))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(html))
}
