package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mitmcdn/src/cache"
	"mitmcdn/src/database"
	"mitmcdn/src/download"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupStatusHandler(t *testing.T) (*StatusHandler, *gorm.DB) {
	// Setup database
	tmpDB, err := os.CreateTemp("", "test-status-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp DB: %v", err)
	}
	tmpDB.Close()
	t.Cleanup(func() {
		os.Remove(tmpDB.Name())
	})

	db, err := gorm.Open(sqlite.Open(tmpDB.Name()), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}

	if err := db.AutoMigrate(&database.File{}, &database.Log{}); err != nil {
		t.Fatalf("Failed to migrate: %v", err)
	}

	// Setup cache manager
	tmpDir := t.TempDir()
	cacheMgr, err := cache.NewManager(db, tmpDir, 1024*1024, 10*1024*1024, time.Hour)
	if err != nil {
		t.Fatalf("Failed to create cache manager: %v", err)
	}

	// Setup scheduler
	sched, err := download.NewScheduler(cacheMgr, db, "")
	if err != nil {
		t.Fatalf("Failed to create scheduler: %v", err)
	}

	handler := NewStatusHandler(db, cacheMgr, sched)
	return handler, db
}

func TestStatusHandlerAPI(t *testing.T) {
	handler, db := setupStatusHandler(t)

	// Create a test file
	file := database.File{
		FileHash:       "test-hash",
		OriginalURL:    "https://example.com/test.mp4",
		Filename:       "test.mp4",
		FileSize:       1024 * 1024,
		SavedPath:      "/tmp/test.mp4",
		DownloadStatus: "complete",
		DownloadedBytes: 1024 * 1024,
	}
	db.Create(&file)

	// Test API endpoint
	req := httptest.NewRequest("GET", "/api/status", nil)
	w := httptest.NewRecorder()

	handler.HandleAPIStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var status StatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if status.Version == "" {
		t.Error("Version should not be empty")
	}

	if status.Cache.TotalFiles == 0 {
		t.Error("Should have at least one file")
	}
}

func TestStatusHandlerPage(t *testing.T) {
	handler, db := setupStatusHandler(t)

	// Create a test file
	file := database.File{
		FileHash:       "test-hash",
		OriginalURL:    "https://example.com/test.mp4",
		Filename:       "test.mp4",
		FileSize:       1024 * 1024,
		SavedPath:      "/tmp/test.mp4",
		DownloadStatus: "complete",
		DownloadedBytes: 1024 * 1024,
	}
	db.Create(&file)

	// Test HTML page
	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()

	handler.HandleStatusPage(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "MitmCDN") {
		t.Error("HTML should contain 'MitmCDN'")
	}

	if !strings.Contains(body, "test.mp4") {
		t.Error("HTML should contain filename")
	}
}
