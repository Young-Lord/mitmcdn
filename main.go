package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mitmcdn/src/cache"
	"mitmcdn/src/config"
	"mitmcdn/src/database"
	"mitmcdn/src/download"
	"mitmcdn/src/htmlplugin"
	"mitmcdn/src/proxy"
)

var (
	configPath = flag.String("config", "config.toml", "Path to configuration file")
	dbPath     = flag.String("db", "mitmcdn.db", "Path to SQLite database")
)

func main() {
	flag.Parse()

	// Configure log format to include filename and line number
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Load configuration
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Initialize database
	db, err := database.InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Parse cache configuration
	maxFileSize, err := config.ParseSize(cfg.Cache.MaxFileSize)
	if err != nil {
		log.Fatalf("Invalid max_file_size: %v", err)
	}

	maxTotalSize, err := config.ParseSize(cfg.Cache.MaxTotalSize)
	if err != nil {
		log.Fatalf("Invalid max_total_size: %v", err)
	}

	ttl, err := config.ParseDuration(cfg.Cache.TTL)
	if err != nil {
		log.Fatalf("Invalid ttl: %v", err)
	}

	// Initialize cache manager
	cacheMgr, err := cache.NewManager(db, cfg.Cache.CacheDir, maxFileSize, maxTotalSize, ttl)
	if err != nil {
		log.Fatalf("Failed to initialize cache manager: %v", err)
	}

	// Initialize download scheduler
	downloadSched, err := download.NewScheduler(cacheMgr, db, cfg.UpstreamProxy)
	if err != nil {
		log.Fatalf("Failed to initialize download scheduler: %v", err)
	}

	// Initialize HTML rewrite plugins
	htmlPluginManager, err := htmlplugin.NewManager("plugins", "configs", cacheMgr, downloadSched)
	if err != nil {
		log.Fatalf("Failed to initialize HTML plugins: %v", err)
	}

	// Start servers based on proxy mode
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start cleanup goroutine
	go startCleanup(ctx, cacheMgr, maxTotalSize)

	// Start unified server that handles all protocols on a single port
	unifiedServer, err := proxy.NewUnifiedServer(cfg, cacheMgr, downloadSched, htmlPluginManager, db)
	if err != nil {
		log.Fatalf("Failed to create unified server: %v", err)
	}

	go func() {
		log.Printf("Starting unified server on %s", cfg.ListenAddress)
		log.Printf("  - HTTP Proxy: http://%s", cfg.ListenAddress)
		log.Printf("  - SOCKS5 Proxy: socks5://%s", cfg.ListenAddress)
		log.Printf("  - HTTP Reverse Proxy: http://%s/https://target.com/file", cfg.ListenAddress)
		log.Printf("  - HTTPS Server: https://%s", cfg.ListenAddress)
		log.Printf("  - Status API: http://%s/api/status", cfg.ListenAddress)
		log.Printf("  - Status Page: http://%s/status", cfg.ListenAddress)
		if err := unifiedServer.ListenAndServe(cfg.ListenAddress); err != nil {
			log.Fatalf("Unified server error: %v", err)
		}
	}()

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	cancel()
}

// startCleanup runs periodic cleanup tasks
func startCleanup(ctx context.Context, cacheMgr *cache.Manager, maxTotalSize int64) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Cleanup expired files
			if err := cacheMgr.CleanupExpiredFiles(); err != nil {
				log.Printf("Error cleaning up expired files: %v", err)
			}

			// LRU eviction if needed
			if err := cacheMgr.LRUEvict(maxTotalSize); err != nil {
				log.Printf("Error during LRU eviction: %v", err)
			}
		}
	}
}
