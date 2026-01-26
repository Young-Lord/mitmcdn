package cache

import (
	"os"
	"testing"

	"mitmcdn/src/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	tmpFile, err := os.CreateTemp("", "test-db-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp DB: %v", err)
	}
	tmpFile.Close()

	db, err := gorm.Open(sqlite.Open(tmpFile.Name()), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}

	if err := db.AutoMigrate(&database.File{}, &database.Log{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	t.Cleanup(func() {
		os.Remove(tmpFile.Name())
	})

	return db
}

func TestComputeFileHash(t *testing.T) {
	mgr := &Manager{}

	tests := []struct {
		name     string
		url      string
		cookie   string
		strategy string
		wantSame bool // Whether same inputs should produce same hash
	}{
		{
			name:     "filename_only same filename",
			url:      "https://cdn.com/video1.mp4",
			cookie:   "",
			strategy: "filename_only",
			wantSame: true,
		},
		{
			name:     "filename_only different path same filename",
			url:      "https://cdn.com/path/video1.mp4",
			cookie:   "",
			strategy: "filename_only",
			wantSame: true,
		},
		{
			name:     "full_url different URLs",
			url:      "https://cdn.com/video1.mp4",
			cookie:   "",
			strategy: "full_url",
			wantSame: false,
		},
		{
			name:     "filename_only with cookie",
			url:      "https://cdn.com/video1.mp4",
			cookie:   "session=abc",
			strategy: "filename_only",
			wantSame: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1 := mgr.ComputeFileHash(tt.url, tt.cookie, tt.strategy)
			hash2 := mgr.ComputeFileHash(tt.url, tt.cookie, tt.strategy)

			if hash1 != hash2 {
				t.Errorf("Same inputs produced different hashes: %q != %q", hash1, hash2)
			}

			if hash1 == "" {
				t.Error("Hash should not be empty")
			}
		})
	}
}

func TestMatchCDNRule(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		domain  string
		pattern string
		want    bool
	}{
		{
			name:    "matches domain and pattern",
			url:     "https://cdn.example.com/video.mp4",
			domain:  "cdn.example.com",
			pattern: "\\.mp4$",
			want:    true,
		},
		{
			name:    "matches domain but not pattern",
			url:     "https://cdn.example.com/page.html",
			domain:  "cdn.example.com",
			pattern: "\\.mp4$",
			want:    false,
		},
		{
			name:    "does not match domain",
			url:     "https://other.com/video.mp4",
			domain:  "cdn.example.com",
			pattern: "\\.mp4$",
			want:    false,
		},
		{
			name:    "matches domain without pattern",
			url:     "https://cdn.example.com/anyfile",
			domain:  "cdn.example.com",
			pattern: "",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchCDNRule(tt.url, tt.domain, tt.pattern)
			if got != tt.want {
				t.Errorf("MatchCDNRule() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewManager(t *testing.T) {
	db := setupTestDB(t)
	tmpDir := t.TempDir()

	mgr, err := NewManager(db, tmpDir, 1024*1024, 10*1024*1024, 3600)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if mgr == nil {
		t.Fatal("NewManager() returned nil")
	}

	if mgr.cacheDir != tmpDir {
		t.Errorf("cacheDir = %q, want %q", mgr.cacheDir, tmpDir)
	}

	// Check directory was created
	if _, err := os.Stat(tmpDir); err != nil {
		t.Errorf("Cache directory was not created: %v", err)
	}
}

func TestGetOrCreateFile(t *testing.T) {
	db := setupTestDB(t)
	tmpDir := t.TempDir()

	mgr, err := NewManager(db, tmpDir, 1024*1024, 10*1024*1024, 3600)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	url := "https://cdn.com/video.mp4"
	cookie := ""
	filename := "video.mp4"
	strategy := "filename_only"

	// First call should create
	file1, err := mgr.GetOrCreateFile(url, cookie, filename, strategy)
	if err != nil {
		t.Fatalf("GetOrCreateFile() error = %v", err)
	}

	if file1.FileHash == "" {
		t.Error("FileHash should not be empty")
	}

	if file1.OriginalURL != url {
		t.Errorf("OriginalURL = %q, want %q", file1.OriginalURL, url)
	}

	// Second call should return same file
	file2, err := mgr.GetOrCreateFile(url, cookie, filename, strategy)
	if err != nil {
		t.Fatalf("GetOrCreateFile() error = %v", err)
	}

	if file1.FileHash != file2.FileHash {
		t.Errorf("FileHash changed: %q != %q", file1.FileHash, file2.FileHash)
	}

	if file1.ID != file2.ID {
		t.Errorf("File ID changed: %d != %d", file1.ID, file2.ID)
	}
}

func TestGetOrCreateFileDifferentStrategies(t *testing.T) {
	db := setupTestDB(t)
	tmpDir := t.TempDir()

	mgr, err := NewManager(db, tmpDir, 1024*1024, 10*1024*1024, 3600)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	url1 := "https://cdn.com/path1/video.mp4"
	url2 := "https://cdn.com/path2/video.mp4"
	filename := "video.mp4"

	// filename_only strategy should produce same hash
	file1, _ := mgr.GetOrCreateFile(url1, "", filename, "filename_only")
	file2, _ := mgr.GetOrCreateFile(url2, "", filename, "filename_only")

	if file1.FileHash != file2.FileHash {
		t.Error("filename_only strategy should produce same hash for same filename")
	}

	// full_url strategy should produce different hash
	file3, _ := mgr.GetOrCreateFile(url1, "", filename, "full_url")
	file4, _ := mgr.GetOrCreateFile(url2, "", filename, "full_url")

	if file3.FileHash == file4.FileHash {
		t.Error("full_url strategy should produce different hash for different URLs")
	}
}
