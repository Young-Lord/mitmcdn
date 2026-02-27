package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mitmcdn/src/cache"
	"mitmcdn/src/config"
	"mitmcdn/src/database"
	"mitmcdn/src/download"

	"golang.org/x/net/proxy"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSOCKS5ProxyMITMCachesHTTPRequests(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("http-socks5-body"))
	}))
	defer origin.Close()

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatalf("failed to parse origin URL: %v", err)
	}

	proxyAddr, db, cleanup := setupSOCKS5ProxyForTest(t, []config.CDNRule{{
		Domain:        originURL.Hostname(),
		MatchPattern:  ".*",
		DedupStrategy: "full_url",
	}})
	defer cleanup()

	client := newSOCKS5HTTPClient(t, proxyAddr)
	targetURL := origin.URL + "/assets/http.bin?token=1"

	resp, err := client.Get(targetURL)
	if err != nil {
		t.Fatalf("SOCKS5 HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read HTTP response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected HTTP status: %d", resp.StatusCode)
	}
	if len(body) == 0 {
		t.Fatal("expected non-empty HTTP response body")
	}

	if _, ok := waitForCachedFileByURL(t, db, targetURL, 5*time.Second); !ok {
		t.Fatalf("expected cached file record for %s", targetURL)
	}
}

func TestSOCKS5ProxyMITMCachesHTTPSRequests(t *testing.T) {
	tmpHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", tmpHome); err != nil {
		t.Fatalf("failed to set HOME: %v", err)
	}
	t.Cleanup(func() {
		if originalHome != "" {
			_ = os.Setenv("HOME", originalHome)
		} else {
			_ = os.Unsetenv("HOME")
		}
	})

	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("https-socks5-body"))
	}))
	defer origin.Close()

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatalf("failed to parse origin URL: %v", err)
	}

	proxyAddr, db, cleanup := setupSOCKS5ProxyForTest(t, []config.CDNRule{{
		Domain:        originURL.Hostname(),
		MatchPattern:  ".*",
		DedupStrategy: "full_url",
	}})
	defer cleanup()

	client := newSOCKS5HTTPClient(t, proxyAddr)
	targetURL := origin.URL + "/assets/https.bin?token=2"

	resp, err := client.Get(targetURL)
	if err != nil {
		t.Fatalf("SOCKS5 HTTPS request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read HTTPS response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected HTTPS status: %d", resp.StatusCode)
	}
	if len(body) == 0 {
		t.Fatal("expected non-empty HTTPS response body")
	}

	if _, ok := waitForCachedFileByURL(t, db, targetURL, 5*time.Second); !ok {
		t.Fatalf("expected cached file record for %s", targetURL)
	}
}

func setupSOCKS5ProxyForTest(t *testing.T, rules []config.CDNRule) (string, *gorm.DB, func()) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "proxy-test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}

	if err := db.AutoMigrate(&database.File{}, &database.Log{}); err != nil {
		t.Fatalf("failed to migrate database: %v", err)
	}

	cacheDir := filepath.Join(t.TempDir(), "cache")
	cacheMgr, err := cache.NewManager(db, cacheDir, 50*1024*1024, 200*1024*1024, time.Hour)
	if err != nil {
		t.Fatalf("failed to create cache manager: %v", err)
	}

	downloadClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 15 * time.Second,
	}

	sched, err := download.NewSchedulerWithClient(cacheMgr, db, "", downloadClient)
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}

	cfg := &config.Config{
		CDNRules: rules,
	}

	mitm := NewMITMProxy(cfg, cacheMgr, sched, nil)
	socksProxy, err := NewSOCKS5Proxy(cfg, cacheMgr, sched, mitm)
	if err != nil {
		t.Fatalf("failed to create SOCKS5 proxy: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on test socket: %v", err)
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = socksProxy.Serve(listener)
	}()

	cleanup := func() {
		_ = listener.Close()
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for SOCKS5 proxy shutdown")
		}
	}

	return listener.Addr().String(), db, cleanup
}

func newSOCKS5HTTPClient(t *testing.T, proxyAddr string) *http.Client {
	t.Helper()

	dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
	if err != nil {
		t.Fatalf("failed to create SOCKS5 dialer: %v", err)
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			conn, err := dialer.Dial(network, addr)
			if err != nil {
				return nil, err
			}

			select {
			case <-ctx.Done():
				_ = conn.Close()
				return nil, ctx.Err()
			default:
			}

			return conn, nil
		},
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
	}
}

func waitForCachedFileByURL(t *testing.T, db *gorm.DB, originalURL string, timeout time.Duration) (*database.File, bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var file database.File
		err := db.Where("original_url = ?", originalURL).First(&file).Error
		if err == nil {
			return &file, true
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("failed querying cache record: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	return nil, false
}
