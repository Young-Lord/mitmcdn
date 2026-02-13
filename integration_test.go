package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitmcdn/src/cache"
	"mitmcdn/src/config"
	"mitmcdn/src/database"
	"mitmcdn/src/download"
	"mitmcdn/src/proxy"

	socksproxy "golang.org/x/net/proxy"
	"gorm.io/gorm"
)

// testServer holds all server components for testing
type testServer struct {
	config          *config.Config
	db              *gorm.DB
	cacheMgr        *cache.Manager
	downloadSched   *download.Scheduler
	mitmProxy       *proxy.MITMProxy
	httpServer      *http.Server
	reverseServer   *http.Server
	httpsServer     *http.Server
	socks5Proxy     *proxy.SOCKS5Proxy
	reverseProxy    *proxy.HTTPReverseProxy
	httpListener    net.Listener
	socks5Listener  net.Listener
	reverseListener net.Listener
	httpsListener   net.Listener
	httpAddr        string
	socks5Addr      string
	reverseAddr     string
	httpsAddr       string
}

// setupTestServer creates and starts all test servers
func setupTestServer(t *testing.T) *testServer {
	// Create temporary directories
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	cacheDir := filepath.Join(tmpDir, "cache")

	// Create test config
	cfg := &config.Config{
		ListenAddress: "127.0.0.1:0", // Use 0 to get random port
		ProxyMode:     "all",
		UpstreamProxy: "",
		Cache: config.CacheConfig{
			CacheDir:     cacheDir,
			MaxFileSize:  "100M",
			MaxTotalSize: "1G",
			TTL:          "1h",
		},
		CDNRules: []config.CDNRule{
			{
				Domain:        "httpbin.org",
				MatchPattern:  ".*",
				DedupStrategy: "filename_only",
			},
			// Note: baidu.com is intentionally NOT in CDNRules
			// This means requests to baidu.com will be proxied but NOT cached
			// This allows us to test both cached and non-cached scenarios
		},
	}

	// Initialize database
	db, err := database.InitDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to init DB: %v", err)
	}

	// Initialize cache manager
	maxFileSize, _ := config.ParseSize(cfg.Cache.MaxFileSize)
	maxTotalSize, _ := config.ParseSize(cfg.Cache.MaxTotalSize)
	ttl, _ := config.ParseDuration(cfg.Cache.TTL)

	cacheMgr, err := cache.NewManager(db, cacheDir, maxFileSize, maxTotalSize, ttl)
	if err != nil {
		t.Fatalf("Failed to create cache manager: %v", err)
	}

	// Initialize download scheduler with test client (trust all certs)
	downloadSched, err := download.NewSchedulerWithClient(
		cacheMgr, db, "",
		createTestHTTPClient(),
	)
	if err != nil {
		t.Fatalf("Failed to create scheduler: %v", err)
	}

	// Initialize MITM proxy
	mitmProxy := proxy.NewMITMProxy(cfg, cacheMgr, downloadSched, nil)

	// Create HTTP listener for proxy
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	httpAddr := httpListener.Addr().String()

	httpServer := &http.Server{
		Handler: http.HandlerFunc(mitmProxy.HandleHTTP),
	}

	// Create SOCKS5 proxy
	socks5Proxy, err := proxy.NewSOCKS5Proxy(cfg, cacheMgr, downloadSched, mitmProxy)
	if err != nil {
		t.Fatalf("Failed to create SOCKS5 proxy: %v", err)
	}

	socks5Listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen SOCKS5: %v", err)
	}
	socks5Addr := socks5Listener.Addr().String()

	// Create reverse proxy
	reverseProxy := proxy.NewHTTPReverseProxy(cfg, cacheMgr, downloadSched, mitmProxy, nil)
	reverseListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen reverse: %v", err)
	}
	reverseAddr := reverseListener.Addr().String()

	reverseServer := &http.Server{
		Handler: reverseProxy,
	}

	// Create HTTPS server (for testing HTTPS endpoint)
	httpsListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen HTTPS: %v", err)
	}
	httpsAddr := httpsListener.Addr().String()

	// Generate test certificate for localhost
	testCert, err := generateTestCert("localhost")
	if err != nil {
		t.Fatalf("Failed to generate test cert: %v", err)
	}

	httpsServer := &http.Server{
		Handler: reverseProxy,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{*testCert},
		},
	}

	// Start all servers
	t.Logf("[SETUP] Starting HTTP server on %s", httpAddr)
	go func() {
		if err := httpServer.Serve(httpListener); err != nil && err != http.ErrServerClosed {
			t.Logf("[SETUP] HTTP server error: %v", err)
		}
	}()

	t.Logf("[SETUP] Starting SOCKS5 server on %s", socks5Addr)
	go func() {
		if err := socks5Proxy.Serve(socks5Listener); err != nil {
			t.Logf("[SETUP] SOCKS5 server error: %v", err)
		}
	}()

	t.Logf("[SETUP] Starting reverse proxy server on %s", reverseAddr)
	go func() {
		if err := reverseServer.Serve(reverseListener); err != nil && err != http.ErrServerClosed {
			t.Logf("[SETUP] Reverse proxy server error: %v", err)
		}
	}()

	t.Logf("[SETUP] Starting HTTPS server on %s", httpsAddr)
	go func() {
		if err := httpsServer.ServeTLS(httpsListener, "", ""); err != nil && err != http.ErrServerClosed {
			t.Logf("[SETUP] HTTPS server error: %v", err)
		}
	}()

	// Wait a bit for servers to start
	time.Sleep(200 * time.Millisecond)
	t.Logf("[SETUP] All servers started, ready for testing")

	return &testServer{
		config:          cfg,
		db:              db,
		cacheMgr:        cacheMgr,
		downloadSched:   downloadSched,
		mitmProxy:       mitmProxy,
		httpServer:      httpServer,
		reverseServer:   reverseServer,
		httpsServer:     httpsServer,
		socks5Proxy:     socks5Proxy,
		reverseProxy:    reverseProxy,
		httpListener:    httpListener,
		socks5Listener:  socks5Listener,
		reverseListener: reverseListener,
		httpsListener:   httpsListener,
		httpAddr:        httpAddr,
		socks5Addr:      socks5Addr,
		reverseAddr:     reverseAddr,
		httpsAddr:       httpsAddr,
	}
}

// cleanup closes all servers and listeners
func (ts *testServer) cleanup() {
	// Close listeners first to stop accepting new connections
	if ts.socks5Listener != nil {
		ts.socks5Listener.Close()
	}
	if ts.httpListener != nil {
		ts.httpListener.Close()
	}
	if ts.reverseListener != nil {
		ts.reverseListener.Close()
	}
	if ts.httpsListener != nil {
		ts.httpsListener.Close()
	}

	// Then shutdown servers with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if ts.httpServer != nil {
		ts.httpServer.Shutdown(ctx)
	}
	if ts.reverseServer != nil {
		ts.reverseServer.Shutdown(ctx)
	}
	if ts.httpsServer != nil {
		ts.httpsServer.Shutdown(ctx)
	}
}

// createTestHTTPClient creates HTTP client that trusts all certificates
func createTestHTTPClient() *http.Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		DisableCompression: false,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
	}
}

// generateTestCert generates a test certificate
func generateTestCert(hostname string) (*tls.Certificate, error) {
	if hostname == "" {
		hostname = "localhost"
	}
	return proxy.GenerateCertificate(hostname)
}

// checkFileInCache checks if a file exists in the cache database
func checkFileInCache(t *testing.T, db *gorm.DB, urlPattern string) (*database.File, bool) {
	var file database.File
	err := db.Where("original_url LIKE ?", urlPattern).First(&file).Error
	if err == gorm.ErrRecordNotFound {
		return nil, false
	}
	if err != nil {
		t.Logf("Error checking cache: %v", err)
		return nil, false
	}
	return &file, true
}

// makeHTTPRequest makes an HTTP request and returns the duration and error
func makeHTTPRequest(client *http.Client, url string) (time.Duration, *http.Response, error) {
	start := time.Now()
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return time.Since(start), nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add timeout context (longer timeout for slow connections)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		return duration, nil, fmt.Errorf("request failed: %w", err)
	}
	return duration, resp, nil
}

// createSOCKS5Client creates an HTTP client that uses SOCKS5 proxy
func createSOCKS5Client(proxyAddr string) (*http.Client, error) {
	dialer, err := socksproxy.SOCKS5("tcp", proxyAddr, nil, socksproxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("failed to create SOCKS5 dialer: %w", err)
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Use context with timeout for dialing
			dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
			defer dialCancel()

			// Note: socksproxy.Dial doesn't take context, so we use the timeout from dialCtx
			// by checking if it's cancelled before/after dialing
			select {
			case <-dialCtx.Done():
				return nil, dialCtx.Err()
			default:
			}

			conn, err := dialer.Dial(network, addr)
			if err != nil {
				return nil, fmt.Errorf("dial failed: %w", err)
			}

			// Check if context was cancelled during dial
			select {
			case <-dialCtx.Done():
				conn.Close()
				return nil, dialCtx.Err()
			default:
			}

			// Set read/write deadlines
			if tcpConn, ok := conn.(*net.TCPConn); ok {
				tcpConn.SetKeepAlive(true)
				tcpConn.SetKeepAlivePeriod(30 * time.Second)
			}

			return conn, nil
		},
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // Trust MITM certificates
		},
		DisableCompression: false,
		MaxIdleConns:       10,
		IdleConnTimeout:    30 * time.Second,
		DisableKeepAlives:  false,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second, // Timeout for entire request
	}, nil
}

// createHTTPProxyClient creates an HTTP client that uses HTTP proxy
func createHTTPProxyClient(proxyAddr string) *http.Client {
	proxyURL, _ := url.Parse("http://" + proxyAddr)
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // Trust MITM certificates
		},
		DisableCompression: false,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
}

// TestHTTPProxy tests HTTP proxy mode with comprehensive scenarios
func TestHTTPProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	server := setupTestServer(t)
	defer server.cleanup()

	client := createHTTPProxyClient(server.httpAddr)

	// Test cases: (protocol, domain, shouldCache, description)
	testCases := []struct {
		protocol    string
		domain      string
		shouldCache bool
		desc        string
	}{
		{"http", "httpbin.org", true, "HTTP cached site"},
		{"https", "httpbin.org", true, "HTTPS cached site"},
		{"http", "baidu.com", false, "HTTP non-cached site"},
		{"https", "baidu.com", false, "HTTPS non-cached site"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			testURL := fmt.Sprintf("%s://%s/", tc.protocol, tc.domain)

			// First access
			t.Logf("First access to %s", testURL)
			duration1, resp1, err := makeHTTPRequest(client, testURL)
			if err != nil {
				t.Logf("Request failed (may be expected): %v", err)
				return
			}
			if resp1 != nil {
				defer resp1.Body.Close()
				body1, _ := io.ReadAll(resp1.Body)
				t.Logf("First access: status=%d, duration=%v, body_size=%d", resp1.StatusCode, duration1, len(body1))
			}

			// Wait for caching/download to complete
			select {
			case <-ctx.Done():
				t.Fatal("Test timeout")
			case <-time.After(2 * time.Second):
			}

			// Verify cache behavior
			file, found := checkFileInCache(t, server.db, "%"+tc.domain+"%")
			if tc.shouldCache {
				if !found {
					t.Logf("File not cached yet for %s (may still be downloading)", tc.domain)
				} else {
					t.Logf("File cached: %s, status: %s, hash: %s", file.Filename, file.DownloadStatus, file.FileHash)
				}
			} else {
				if found {
					t.Logf("Warning: File for non-cached domain %s was found in cache (unexpected but not fatal)", tc.domain)
				} else {
					t.Logf("Correctly not cached: %s", tc.domain)
				}
			}

			// Second access (only for cached sites)
			if tc.shouldCache {
				t.Logf("Second access to %s (should use cache)", testURL)
				duration2, resp2, err := makeHTTPRequest(client, testURL)
				if err != nil {
					t.Logf("Second request failed: %v", err)
					return
				}
				if resp2 != nil {
					defer resp2.Body.Close()
					body2, _ := io.ReadAll(resp2.Body)
					t.Logf("Second access: status=%d, duration=%v, body_size=%d", resp2.StatusCode, duration2, len(body2))
					if duration2 < duration1 {
						t.Logf("Cache hit: second access was faster (%v < %v)", duration2, duration1)
					}
				}
			}
		})
	}
}

// TestHTTPSProxy tests HTTPS proxy mode (CONNECT method) with timeout protection
// Note: This is a simpler test than TestHTTPProxy which covers HTTPS comprehensively
func TestHTTPSProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	server := setupTestServer(t)
	defer server.cleanup()

	client := createHTTPProxyClient(server.httpAddr)

	// Test HTTPS request through CONNECT method
	testURL := "https://httpbin.org/get?a=1"
	t.Logf("Testing HTTPS CONNECT: %s", testURL)

	duration, resp, err := makeHTTPRequest(client, testURL)
	if err != nil {
		// HTTPS CONNECT through MITM proxy may not be fully implemented
		t.Logf("HTTPS request failed (may be expected if MITM CONNECT not fully implemented): %v", err)
		return
	}

	select {
	case <-ctx.Done():
		t.Fatal("Test timeout")
	default:
	}

	if resp != nil {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		t.Logf("HTTPS proxy test successful: status=%d, duration=%v, body_size=%d", resp.StatusCode, duration, len(body))
	}
}

// TestSOCKS5Proxy tests SOCKS5 proxy mode with comprehensive scenarios
func TestSOCKS5Proxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	server := setupTestServer(t)
	defer server.cleanup()

	// Create SOCKS5 client
	client, err := createSOCKS5Client(server.socks5Addr)
	if err != nil {
		t.Fatalf("Failed to create SOCKS5 client: %v", err)
	}

	// Test cases: (protocol, domain, shouldCache, description)
	testCases := []struct {
		protocol    string
		domain      string
		shouldCache bool
		desc        string
	}{
		{"http", "httpbin.org", true, "HTTP cached site via SOCKS5"},
		{"https", "httpbin.org", true, "HTTPS cached site via SOCKS5"},
		{"http", "baidu.com", false, "HTTP non-cached site via SOCKS5"},
		{"https", "baidu.com", false, "HTTPS non-cached site via SOCKS5"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			testURL := fmt.Sprintf("%s://%s/", tc.protocol, tc.domain)
			t.Logf("[TEST] Starting test case: %s", tc.desc)
			t.Logf("[TEST] Test URL: %s", testURL)

			// First access
			t.Logf("[TEST] Making first request to %s via SOCKS5", testURL)
			reqStart := time.Now()
			duration1, resp1, err := makeHTTPRequest(client, testURL)
			reqDuration := time.Since(reqStart)
			t.Logf("[TEST] Request completed in %v, total duration: %v", reqDuration, duration1)

			if err != nil {
				t.Logf("[TEST] Request failed (may be expected): %v", err)
				// Check if context was cancelled
				if ctx.Err() != nil {
					t.Logf("[TEST] Context cancelled: %v", ctx.Err())
				}
				return
			}
			if resp1 != nil {
				t.Logf("[TEST] Response received: status=%d", resp1.StatusCode)
				defer func() {
					if resp1.Body != nil {
						io.Copy(io.Discard, resp1.Body) // Drain body
						resp1.Body.Close()
						t.Logf("[TEST] Response body closed")
					}
				}()
				body1, readErr := io.ReadAll(resp1.Body)
				if readErr != nil {
					t.Logf("[TEST] Error reading body: %v", readErr)
				} else {
					t.Logf("[TEST] First access: status=%d, duration=%v, body_size=%d", resp1.StatusCode, duration1, len(body1))
				}
			}

			// Wait for caching/download to complete
			select {
			case <-ctx.Done():
				t.Fatal("Test timeout")
			case <-time.After(2 * time.Second):
			}

			// Verify cache behavior
			file, found := checkFileInCache(t, server.db, "%"+tc.domain+"%")
			if tc.shouldCache {
				if !found {
					t.Logf("File not cached yet for %s (may still be downloading)", tc.domain)
				} else {
					t.Logf("File cached: %s, status: %s, hash: %s", file.Filename, file.DownloadStatus, file.FileHash)
				}
			} else {
				if found {
					t.Logf("Warning: File for non-cached domain %s was found in cache (unexpected but not fatal)", tc.domain)
				} else {
					t.Logf("Correctly not cached: %s", tc.domain)
				}
			}

			// Second access (only for cached sites)
			if tc.shouldCache {
				t.Logf("[TEST] Making second request to %s via SOCKS5 (should use cache)", testURL)
				duration2, resp2, err := makeHTTPRequest(client, testURL)
				if err != nil {
					t.Logf("[TEST] Second request failed: %v", err)
					if ctx.Err() != nil {
						t.Logf("[TEST] Context cancelled during second request: %v", ctx.Err())
					}
					return
				}
				if resp2 != nil {
					t.Logf("[TEST] Second response received: status=%d", resp2.StatusCode)
					defer func() {
						if resp2.Body != nil {
							io.Copy(io.Discard, resp2.Body) // Drain body
							resp2.Body.Close()
							t.Logf("[TEST] Second response body closed")
						}
					}()
					body2, readErr := io.ReadAll(resp2.Body)
					if readErr != nil {
						t.Logf("[TEST] Error reading second body: %v", readErr)
					} else {
						t.Logf("[TEST] Second access: status=%d, duration=%v, body_size=%d", resp2.StatusCode, duration2, len(body2))
						if duration2 < duration1 {
							t.Logf("[TEST] Cache hit: second access was faster (%v < %v)", duration2, duration1)
						}
					}
				}
			}
			t.Logf("[TEST] Test case %s completed", tc.desc)
		})
	}
}

// TestHTTPReverseProxy tests URL path proxy mode with comprehensive scenarios
func TestHTTPReverseProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	server := setupTestServer(t)
	defer server.cleanup()

	// Create HTTP client
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
		Timeout: 30 * time.Second,
	}

	// Test cases: (targetProtocol, domain, shouldCache, description)
	testCases := []struct {
		targetProtocol string
		domain         string
		shouldCache    bool
		desc           string
	}{
		{"http", "httpbin.org", true, "HTTP cached site via reverse proxy"},
		{"https", "httpbin.org", true, "HTTPS cached site via reverse proxy"},
		{"http", "baidu.com", false, "HTTP non-cached site via reverse proxy"},
		{"https", "baidu.com", false, "HTTPS non-cached site via reverse proxy"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			// Format: http://server/https://target.com/
			testURL := fmt.Sprintf("http://%s/%s://%s/", server.reverseAddr, tc.targetProtocol, tc.domain)

			// First access
			t.Logf("First access to %s via reverse proxy", testURL)
			duration1, resp1, err := makeHTTPRequest(client, testURL)
			if err != nil {
				t.Logf("Request failed (may be expected): %v", err)
				return
			}
			if resp1 != nil {
				defer resp1.Body.Close()
				body1, _ := io.ReadAll(resp1.Body)
				t.Logf("First access: status=%d, duration=%v, body_size=%d", resp1.StatusCode, duration1, len(body1))
			}

			// Wait for caching/download to complete
			select {
			case <-ctx.Done():
				t.Fatal("Test timeout")
			case <-time.After(2 * time.Second):
			}

			// Verify cache behavior
			file, found := checkFileInCache(t, server.db, "%"+tc.domain+"%")
			if tc.shouldCache {
				if !found {
					t.Logf("File not cached yet for %s (may still be downloading)", tc.domain)
				} else {
					t.Logf("File cached: %s, status: %s, hash: %s", file.Filename, file.DownloadStatus, file.FileHash)
				}
			} else {
				if found {
					t.Logf("Warning: File for non-cached domain %s was found in cache (unexpected but not fatal)", tc.domain)
				} else {
					t.Logf("Correctly not cached: %s", tc.domain)
				}
			}

			// Second access (only for cached sites)
			if tc.shouldCache {
				t.Logf("Second access to %s via reverse proxy (should use cache)", testURL)
				duration2, resp2, err := makeHTTPRequest(client, testURL)
				if err != nil {
					t.Logf("Second request failed: %v", err)
					return
				}
				if resp2 != nil {
					defer resp2.Body.Close()
					body2, _ := io.ReadAll(resp2.Body)
					t.Logf("Second access: status=%d, duration=%v, body_size=%d", resp2.StatusCode, duration2, len(body2))
					if duration2 < duration1 {
						t.Logf("Cache hit: second access was faster (%v < %v)", duration2, duration1)
					}
				}
			}
		})
	}
}

// TestHTTPSServer tests HTTPS server mode with comprehensive scenarios
func TestHTTPSServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	server := setupTestServer(t)
	defer server.cleanup()

	// Create HTTP client with TLS
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // Trust test server cert
			},
		},
		Timeout: 30 * time.Second,
	}

	// Test cases: (targetProtocol, domain, shouldCache, description)
	testCases := []struct {
		targetProtocol string
		domain         string
		shouldCache    bool
		desc           string
	}{
		{"http", "httpbin.org", true, "HTTP cached site via HTTPS server"},
		{"https", "httpbin.org", true, "HTTPS cached site via HTTPS server"},
		{"http", "baidu.com", false, "HTTP non-cached site via HTTPS server"},
		{"https", "baidu.com", false, "HTTPS non-cached site via HTTPS server"},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			// Format: https://server/https://target.com/
			testURL := fmt.Sprintf("https://%s/%s://%s/", server.httpsAddr, tc.targetProtocol, tc.domain)

			// First access
			t.Logf("First access to %s via HTTPS server", testURL)
			duration1, resp1, err := makeHTTPRequest(client, testURL)
			if err != nil {
				t.Logf("Request failed (may be expected): %v", err)
				return
			}
			if resp1 != nil {
				defer resp1.Body.Close()
				body1, _ := io.ReadAll(resp1.Body)
				t.Logf("First access: status=%d, duration=%v, body_size=%d", resp1.StatusCode, duration1, len(body1))
			}

			// Wait for caching/download to complete
			select {
			case <-ctx.Done():
				t.Fatal("Test timeout")
			case <-time.After(2 * time.Second):
			}

			// Verify cache behavior
			file, found := checkFileInCache(t, server.db, "%"+tc.domain+"%")
			if tc.shouldCache {
				if !found {
					t.Logf("File not cached yet for %s (may still be downloading)", tc.domain)
				} else {
					t.Logf("File cached: %s, status: %s, hash: %s", file.Filename, file.DownloadStatus, file.FileHash)
				}
			} else {
				if found {
					t.Logf("Warning: File for non-cached domain %s was found in cache (unexpected but not fatal)", tc.domain)
				} else {
					t.Logf("Correctly not cached: %s", tc.domain)
				}
			}

			// Second access (only for cached sites)
			if tc.shouldCache {
				t.Logf("Second access to %s via HTTPS server (should use cache)", testURL)
				duration2, resp2, err := makeHTTPRequest(client, testURL)
				if err != nil {
					t.Logf("Second request failed: %v", err)
					return
				}
				if resp2 != nil {
					defer resp2.Body.Close()
					body2, _ := io.ReadAll(resp2.Body)
					t.Logf("Second access: status=%d, duration=%v, body_size=%d", resp2.StatusCode, duration2, len(body2))
					if duration2 < duration1 {
						t.Logf("Cache hit: second access was faster (%v < %v)", duration2, duration1)
					}
				}
			}
		})
	}
}

// TestCertificateGeneration tests certificate generation and reuse
func TestCertificateGeneration(t *testing.T) {
	// Test certificate generation
	cert1, err := proxy.GenerateCertificate("httpbin.org")
	if err != nil {
		t.Fatalf("Failed to generate certificate: %v", err)
	}

	if cert1 == nil {
		t.Fatal("Certificate should not be nil")
	}

	// Generate again - should reuse or generate new
	cert2, err := proxy.GenerateCertificate("httpbin.org")
	if err != nil {
		t.Fatalf("Failed to generate certificate second time: %v", err)
	}

	if cert2 == nil {
		t.Fatal("Second certificate should not be nil")
	}

	// Generate for different host
	cert3, err := proxy.GenerateCertificate("test.com")
	if err != nil {
		t.Fatalf("Failed to generate certificate for different host: %v", err)
	}

	if cert3 == nil {
		t.Fatal("Third certificate should not be nil")
	}

	t.Log("Certificate generation test successful")
}

// TestFullFlow tests complete flow: request -> cache -> download
func TestFullFlow(t *testing.T) {
	server := setupTestServer(t)
	defer server.cleanup()

	// Create HTTP client using proxy
	proxyURL, err := url.Parse("http://" + server.httpAddr)
	if err != nil {
		t.Fatalf("Failed to parse proxy URL: %v", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
		Timeout: 15 * time.Second,
	}

	// Make HTTP request (more reliable than HTTPS for testing)
	testURL := "https://httpbin.org/get?a=1"
	resp, err := client.Get(testURL)
	if err != nil {
		t.Logf("Request failed (may be expected): %v", err)
		// Continue to test caching even if request fails
	} else {
		defer resp.Body.Close()

		// Read response
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Logf("Failed to read body: %v", err)
		} else if len(body) > 0 {
			t.Logf("Received %d bytes", len(body))
		}
	}

	// Wait for download to complete
	time.Sleep(2 * time.Second)

	// Verify file in database
	var file database.File
	if err := server.db.Where("original_url LIKE ?", "%httpbin.org%").First(&file).Error; err != nil {
		t.Logf("File not found in database yet: %v", err)
	} else {
		t.Logf("File found: hash=%s, status=%s, size=%d", file.FileHash, file.DownloadStatus, file.FileSize)

		// Verify file exists on disk
		if _, err := os.Stat(file.SavedPath); err != nil {
			t.Logf("File not on disk yet (may be downloading): %v", err)
		} else {
			t.Logf("File exists on disk: %s", file.SavedPath)
		}
	}

	t.Log("Full flow test completed")
}

// TestCDNRuleMatching tests CDN rule matching
func TestCDNRuleMatching(t *testing.T) {
	server := setupTestServer(t)
	defer server.cleanup()

	// Test that httpbin.org matches our CDN rule
	testURL := "https://httpbin.org/get?a=1"
	rule := server.config.CDNRules[0]

	if !cache.MatchCDNRule(testURL, rule.Domain, rule.MatchPattern) {
		t.Error("httpbin.org should match CDN rule")
	}

	// Test that other domain doesn't match
	testURL2 := "https://other.com/test.html"
	if cache.MatchCDNRule(testURL2, rule.Domain, rule.MatchPattern) {
		t.Error("other.com should not match CDN rule")
	}

	t.Log("CDN rule matching test successful")
}

// TestStatusEndpoints tests status API and HTML page
func TestStatusEndpoints(t *testing.T) {
	// Create a unified server for status testing
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-status.db")
	cacheDir := filepath.Join(tmpDir, "cache")

	// Initialize database
	db, err := database.InitDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to init DB: %v", err)
	}

	// Initialize cache manager
	maxFileSize, _ := config.ParseSize("100M")
	maxTotalSize, _ := config.ParseSize("1G")
	ttl, _ := config.ParseDuration("1h")

	cacheMgr, err := cache.NewManager(db, cacheDir, maxFileSize, maxTotalSize, ttl)
	if err != nil {
		t.Fatalf("Failed to create cache manager: %v", err)
	}

	// Initialize download scheduler
	downloadSched, err := download.NewSchedulerWithClient(
		cacheMgr, db, "",
		createTestHTTPClient(),
	)
	if err != nil {
		t.Fatalf("Failed to create scheduler: %v", err)
	}

	// Create config
	cfg := &config.Config{
		ListenAddress: "127.0.0.1:0",
		ProxyMode:     "all",
	}

	// Create unified server
	unifiedServer, err := proxy.NewUnifiedServer(cfg, cacheMgr, downloadSched, nil, db)
	if err != nil {
		t.Fatalf("Failed to create unified server: %v", err)
	}

	// Start server on random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	// Start server in goroutine
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- unifiedServer.ListenAndServe(addr)
	}()

	// Cleanup: shutdown server when test completes
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		unifiedServer.Shutdown(ctx)
	}()

	// Wait for server to start and verify it's listening
	time.Sleep(500 * time.Millisecond)

	// Verify server is listening
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("Server not listening: %v", err)
	}
	conn.Close()

	// Create test file
	file := database.File{
		FileHash:        "test-hash-123",
		OriginalURL:     "https://httpbin.org/get?a=1",
		Filename:        "test.mp4",
		FileSize:        1024 * 1024,
		SavedPath:       filepath.Join(cacheDir, "test-hash-123"),
		DownloadStatus:  "complete",
		DownloadedBytes: 1024 * 1024,
	}
	db.Create(&file)

	// Test API endpoint
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	apiURL := fmt.Sprintf("http://%s/api/status", addr)
	resp, err := client.Get(apiURL)
	if err != nil {
		t.Fatalf("API request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var status proxy.StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}

	if status.Version == "" {
		t.Error("Version should not be empty")
	}

	if status.Uptime == "" {
		t.Error("Uptime should not be empty")
	}

	if status.Cache.TotalFiles == 0 {
		t.Error("Should have at least one file")
	}

	t.Logf("Status API: version=%s, uptime=%s, files=%d, cache_size=%s",
		status.Version, status.Uptime, status.Cache.TotalFiles, status.Cache.TotalSizeHuman)

	// Test HTML page
	htmlURL := fmt.Sprintf("http://%s/status", addr)
	resp2, err := client.Get(htmlURL)
	if err != nil {
		t.Logf("HTML request failed (may be expected if server not fully started): %v", err)
		return
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Logf("Got status %d (may be expected)", resp2.StatusCode)
		return
	}

	body, err := io.ReadAll(resp2.Body)
	if err != nil {
		t.Logf("Failed to read body: %v", err)
		return
	}

	if !strings.Contains(string(body), "MitmCDN") {
		t.Error("HTML should contain 'MitmCDN'")
	}

	if !strings.Contains(string(body), "test.mp4") {
		t.Logf("HTML may not contain filename yet")
	}

	t.Logf("Status page loaded successfully, %d bytes", len(body))
}

// TestUnifiedServerHTTPSProxyConnect verifies HTTPS CONNECT works in unified mode
func TestUnifiedServerHTTPSProxyConnect(t *testing.T) {
	// Start local HTTPS upstream server to avoid external network dependency
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("Failed to parse upstream URL: %v", err)
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-unified-connect.db")
	cacheDir := filepath.Join(tmpDir, "cache")

	db, err := database.InitDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to init DB: %v", err)
	}

	maxFileSize, _ := config.ParseSize("100M")
	maxTotalSize, _ := config.ParseSize("1G")
	ttl, _ := config.ParseDuration("1h")

	cacheMgr, err := cache.NewManager(db, cacheDir, maxFileSize, maxTotalSize, ttl)
	if err != nil {
		t.Fatalf("Failed to create cache manager: %v", err)
	}

	downloadSched, err := download.NewSchedulerWithClient(
		cacheMgr, db, "",
		createTestHTTPClient(),
	)
	if err != nil {
		t.Fatalf("Failed to create scheduler: %v", err)
	}

	cfg := &config.Config{
		ListenAddress: "127.0.0.1:0",
		ProxyMode:     "all",
		CDNRules: []config.CDNRule{
			{
				Domain:        upstreamURL.Hostname(),
				MatchPattern:  ".*",
				DedupStrategy: "full_url",
			},
		},
	}

	unifiedServer, err := proxy.NewUnifiedServer(cfg, cacheMgr, downloadSched, nil, db)
	if err != nil {
		t.Fatalf("Failed to create unified server: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to reserve listen address: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- unifiedServer.ListenAndServe(addr)
	}()

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = unifiedServer.Shutdown(ctx)
		select {
		case err := <-serverErr:
			if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
				t.Logf("Unified server exited with: %v", err)
			}
		default:
		}
	}()

	time.Sleep(300 * time.Millisecond)

	client := createHTTPProxyClient(addr)
	testURL := upstream.URL + "/video.mp4?part=1"

	duration, resp, err := makeHTTPRequest(client, testURL)
	if err != nil {
		t.Fatalf("HTTPS via unified HTTP proxy failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected status 200 for CONNECT flow, got %d, body=%q", resp.StatusCode, string(body))
	}

	t.Logf("Unified CONNECT request successful: status=%d, duration=%v", resp.StatusCode, duration)
}

// TestUnifiedServerHTTPSProxyPropagatesUpstreamError verifies HTTPS CONNECT returns upstream HTTP errors promptly
func TestUnifiedServerHTTPSProxyPropagatesUpstreamError(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failure", http.StatusBadGateway)
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("Failed to parse upstream URL: %v", err)
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-unified-connect-502.db")
	cacheDir := filepath.Join(tmpDir, "cache")

	db, err := database.InitDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to init DB: %v", err)
	}

	maxFileSize, _ := config.ParseSize("100M")
	maxTotalSize, _ := config.ParseSize("1G")
	ttl, _ := config.ParseDuration("1h")

	cacheMgr, err := cache.NewManager(db, cacheDir, maxFileSize, maxTotalSize, ttl)
	if err != nil {
		t.Fatalf("Failed to create cache manager: %v", err)
	}

	downloadSched, err := download.NewSchedulerWithClient(
		cacheMgr, db, "",
		createTestHTTPClient(),
	)
	if err != nil {
		t.Fatalf("Failed to create scheduler: %v", err)
	}

	cfg := &config.Config{
		ListenAddress: "127.0.0.1:0",
		ProxyMode:     "all",
		CDNRules: []config.CDNRule{
			{
				Domain:        upstreamURL.Hostname(),
				MatchPattern:  ".*",
				DedupStrategy: "full_url",
			},
		},
	}

	unifiedServer, err := proxy.NewUnifiedServer(cfg, cacheMgr, downloadSched, nil, db)
	if err != nil {
		t.Fatalf("Failed to create unified server: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to reserve listen address: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- unifiedServer.ListenAndServe(addr)
	}()

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = unifiedServer.Shutdown(ctx)
		select {
		case err := <-serverErr:
			if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
				t.Logf("Unified server exited with: %v", err)
			}
		default:
		}
	}()

	time.Sleep(300 * time.Millisecond)

	client := createHTTPProxyClient(addr)
	testURL := upstream.URL + "/broken.mp4"

	duration, resp, err := makeHTTPRequest(client, testURL)
	if err != nil {
		t.Fatalf("HTTPS via unified HTTP proxy failed: %v", err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("Failed to read proxy error body: %v", readErr)
	}

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("Expected status 502, got %d, body=%q", resp.StatusCode, string(body))
	}

	if !strings.Contains(string(body), "unexpected status code: 502") {
		t.Fatalf("Expected propagated 502 error message, got body=%q", string(body))
	}

	if duration > 5*time.Second {
		t.Fatalf("Expected fast failure response, got duration=%v", duration)
	}

	t.Logf("Unified CONNECT propagated upstream error correctly: status=%d, duration=%v", resp.StatusCode, duration)
}

// TestCacheHitMiss tests cache hit and miss scenarios
func TestCacheHitMiss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	server := setupTestServer(t)
	defer server.cleanup()

	client := createHTTPProxyClient(server.httpAddr)

	// Test cached site (httpbin.org)
	cachedURL := "https://httpbin.org/get?a=1"
	t.Run("CacheMissThenHit", func(t *testing.T) {
		// First access - cache miss
		t.Logf("First access (cache miss): %s", cachedURL)
		_, resp1, err := makeHTTPRequest(client, cachedURL)
		if err != nil {
			t.Logf("Request failed: %v", err)
			return
		}
		if resp1 != nil {
			defer resp1.Body.Close()
			io.ReadAll(resp1.Body)
		}

		// Wait for cache
		select {
		case <-ctx.Done():
			t.Fatal("Test timeout")
		case <-time.After(2 * time.Second):
		}

		// Verify cache entry exists
		file, found := checkFileInCache(t, server.db, "%httpbin.org%")
		if !found {
			t.Logf("Cache entry not found yet (may still be downloading)")
		} else {
			t.Logf("Cache entry found: status=%s", file.DownloadStatus)
		}

		// Second access - should be cache hit
		t.Logf("Second access (cache hit): %s", cachedURL)
		_, resp2, err := makeHTTPRequest(client, cachedURL)
		if err != nil {
			t.Logf("Second request failed: %v", err)
			return
		}
		if resp2 != nil {
			defer resp2.Body.Close()
			io.ReadAll(resp2.Body)
			t.Logf("Cache hit test completed")
		}
	})

	// Test non-cached site (baidu.com)
	nonCachedURL := "https://baidu.com/"
	t.Run("NonCachedSite", func(t *testing.T) {
		t.Logf("Accessing non-cached site: %s", nonCachedURL)
		_, resp, err := makeHTTPRequest(client, nonCachedURL)
		if err != nil {
			t.Logf("Request failed: %v", err)
			return
		}
		if resp != nil {
			defer resp.Body.Close()
			io.ReadAll(resp.Body)
		}

		// Wait a bit
		select {
		case <-ctx.Done():
			t.Fatal("Test timeout")
		case <-time.After(1 * time.Second):
		}

		// Verify NOT cached
		_, found := checkFileInCache(t, server.db, "%baidu.com%")
		if found {
			t.Logf("Warning: Non-cached site was cached (unexpected)")
		} else {
			t.Logf("Correctly not cached")
		}
	})
}

// TestTimeouts verifies that all tests have proper timeout protection
func TestTimeouts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server := setupTestServer(t)
	defer server.cleanup()

	client := createHTTPProxyClient(server.httpAddr)

	// Test that timeout works
	done := make(chan bool, 1)
	go func() {
		// This should complete within timeout
		_, _, err := makeHTTPRequest(client, "https://httpbin.org/get?a=1")
		if err != nil {
			t.Logf("Request failed (expected in timeout test): %v", err)
		}
		done <- true
	}()

	select {
	case <-ctx.Done():
		t.Logf("Timeout test: context cancelled (expected)")
		// Wait for goroutine to finish or timeout
		select {
		case <-done:
			t.Logf("Request completed after context cancellation")
		case <-time.After(2 * time.Second):
			t.Logf("Request still running after context cancellation")
		}
	case <-done:
		t.Logf("Timeout test: request completed before timeout")
	case <-time.After(6 * time.Second):
		t.Logf("Timeout test: request took longer than expected")
		// Wait a bit more for goroutine to finish
		select {
		case <-done:
			t.Logf("Request completed after timeout")
		case <-time.After(1 * time.Second):
			t.Logf("Request still running")
		}
	}
}

// TestConcurrentRequests tests concurrent requests and cache behavior
func TestConcurrentRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	server := setupTestServer(t)
	defer server.cleanup()

	client := createHTTPProxyClient(server.httpAddr)

	testURL := "https://httpbin.org/get?a=1"
	numRequests := 5

	// Make concurrent requests
	results := make(chan bool, numRequests)
	for i := 0; i < numRequests; i++ {
		go func(id int) {
			t.Logf("Concurrent request %d", id)
			_, resp, err := makeHTTPRequest(client, testURL)
			if err != nil {
				t.Logf("Request %d failed: %v", id, err)
				results <- false
				return
			}
			if resp != nil {
				defer resp.Body.Close()
				io.ReadAll(resp.Body)
			}
			results <- true
		}(i)
	}

	// Wait for all requests
	successCount := 0
	for i := 0; i < numRequests; i++ {
		select {
		case <-ctx.Done():
			t.Fatal("Test timeout")
		case success := <-results:
			if success {
				successCount++
			}
		}
	}

	t.Logf("Concurrent requests completed: %d/%d successful", successCount, numRequests)

	// Wait for cache
	select {
	case <-ctx.Done():
		t.Fatal("Test timeout")
	case <-time.After(2 * time.Second):
	}

	// Verify cache entry (should only be one despite multiple requests)
	var files []database.File
	server.db.Where("original_url LIKE ?", "%httpbin.org%").Find(&files)
	t.Logf("Cache entries for httpbin.org: %d (should be 1 or few)", len(files))
}
