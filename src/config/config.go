package config

import (
	"fmt"
	"os"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	ListenAddress string      `toml:"listen_address"`
	ProxyMode     string      `toml:"proxy_mode"` // http, socks5, url_path, or all
	UpstreamProxy string      `toml:"upstream_proxy"`
	AssetsDir     string      `toml:"assets_dir"` // Fallback assets directory
	Cache         CacheConfig `toml:"cache"`
	CDNRules      []CDNRule   `toml:"cdn_rules"`
}

type CacheConfig struct {
	CacheDir     string `toml:"cache_dir"`
	MaxFileSize  string `toml:"max_file_size"`
	MaxTotalSize string `toml:"max_total_size"`
	TTL          string `toml:"ttl"`
}

type CDNRule struct {
	Domain         string `toml:"domain"`
	MatchPattern   string `toml:"match_pattern"`   // URL regex pattern
	DedupStrategy  string `toml:"dedup_strategy"`  // full_url or filename_only
	RequestCookie  string `toml:"request_cookie,omitempty"` // optional cookie for dedup
}

// LoadConfig loads configuration from a TOML file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Validate and set defaults
	if config.ListenAddress == "" {
		config.ListenAddress = "0.0.0.0:8081"
	}
	if config.ProxyMode == "" {
		config.ProxyMode = "all"
	}
	if config.Cache.CacheDir == "" {
		config.Cache.CacheDir = "/var/lib/mitmcdn/data"
	}
	if config.Cache.MaxFileSize == "" {
		config.Cache.MaxFileSize = "5G"
	}
	if config.Cache.MaxTotalSize == "" {
		config.Cache.MaxTotalSize = "100G"
	}
	if config.Cache.TTL == "" {
		config.Cache.TTL = "72h"
	}
	if config.AssetsDir == "" {
		config.AssetsDir = "./assets"
	}

	return &config, nil
}

// ParseSize parses size string like "5G", "100M" to bytes
func ParseSize(sizeStr string) (int64, error) {
	if sizeStr == "" {
		return 0, fmt.Errorf("invalid size format: empty string")
	}

	var size int64
	var unit string
	n, err := fmt.Sscanf(sizeStr, "%d%s", &size, &unit)
	// n == 0 means we couldn't parse the number, which is an error
	// err == EOF is OK if we parsed at least one value (the number)
	if n == 0 {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}
	// If we got an error other than EOF, it's a problem
	if err != nil && err.Error() != "EOF" {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}

	// If no unit provided, treat as bytes
	if unit == "" {
		return size, nil
	}

	switch unit {
	case "B":
		return size, nil
	case "K", "KB":
		return size * 1024, nil
	case "M", "MB":
		return size * 1024 * 1024, nil
	case "G", "GB":
		return size * 1024 * 1024 * 1024, nil
	case "T", "TB":
		return size * 1024 * 1024 * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("unknown size unit: %s", unit)
	}
}

// ParseDuration parses duration string like "72h", "1d"
func ParseDuration(durStr string) (time.Duration, error) {
	return time.ParseDuration(durStr)
}
