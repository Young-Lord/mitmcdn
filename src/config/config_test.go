package config

import (
	"os"
	"testing"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"5G", 5 * 1024 * 1024 * 1024, false},
		{"100M", 100 * 1024 * 1024, false},
		{"512K", 512 * 1024, false},
		{"1024B", 1024, false},
		{"1024", 1024, false},
		{"2T", 2 * 1024 * 1024 * 1024 * 1024, false},
		{"invalid", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseSize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && result != tt.expected {
				t.Errorf("ParseSize(%q) = %d, want %d", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input    string
		wantErr  bool
	}{
		{"72h", false},
		{"24h", false}, // 1 day as 24h
		{"30m", false},
		{"1h30m", false},
		{"1d", true}, // Go's time.ParseDuration doesn't support "d"
		{"invalid", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := ParseDuration(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseDuration(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	// Create a temporary config file
	tmpFile, err := os.CreateTemp("", "test-config-*.toml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	configContent := `
listen_address = "127.0.0.1:8081"
proxy_mode = "http"

[cache]
cache_dir = "/tmp/test-cache"
max_file_size = "1G"
max_total_size = "10G"
ttl = "24h"

[[cdn_rules]]
domain = "test-cdn.com"
match_pattern = "\\.mp4$"
dedup_strategy = "filename_only"
`

	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	tmpFile.Close()

	// Test loading config
	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.ListenAddress != "127.0.0.1:8081" {
		t.Errorf("ListenAddress = %q, want %q", cfg.ListenAddress, "127.0.0.1:8081")
	}

	if cfg.ProxyMode != "http" {
		t.Errorf("ProxyMode = %q, want %q", cfg.ProxyMode, "http")
	}

	if len(cfg.CDNRules) != 1 {
		t.Errorf("CDNRules length = %d, want 1", len(cfg.CDNRules))
	}

	if cfg.CDNRules[0].Domain != "test-cdn.com" {
		t.Errorf("CDNRules[0].Domain = %q, want %q", cfg.CDNRules[0].Domain, "test-cdn.com")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	// Create a minimal config file
	tmpFile, err := os.CreateTemp("", "test-config-minimal-*.toml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	configContent := `# Minimal config`

	if _, err := tmpFile.WriteString(configContent); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}
	tmpFile.Close()

	// Test loading config with defaults
	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	// Check defaults
	if cfg.ListenAddress != "0.0.0.0:8081" {
		t.Errorf("ListenAddress default = %q, want %q", cfg.ListenAddress, "0.0.0.0:8081")
	}

	if cfg.ProxyMode != "all" {
		t.Errorf("ProxyMode default = %q, want %q", cfg.ProxyMode, "all")
	}

	if cfg.Cache.MaxFileSize != "5G" {
		t.Errorf("MaxFileSize default = %q, want %q", cfg.Cache.MaxFileSize, "5G")
	}
}

func TestLoadConfigNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/file.toml")
	if err == nil {
		t.Error("LoadConfig() should return error for nonexistent file")
	}
}
