package database

import (
	"os"
	"testing"
	"time"
)

func TestInitDB(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-init-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp DB: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}

	if db == nil {
		t.Fatal("InitDB() returned nil")
	}

	// Test that tables were created
	if !db.Migrator().HasTable(&File{}) {
		t.Error("files table was not created")
	}

	if !db.Migrator().HasTable(&Log{}) {
		t.Error("logs table was not created")
	}
}

func TestFileModel(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-file-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp DB: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}

	// Create a file record
	file := File{
		FileHash:       "test-hash-123",
		OriginalURL:    "https://cdn.com/video.mp4",
		RequestCookie:  "session=abc",
		Filename:       "video.mp4",
		FileSize:       1024 * 1024,
		SavedPath:      "/tmp/video.mp4",
		DownloadStatus: "pending",
		DownloadedBytes: 0,
	}

	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	if file.ID == 0 {
		t.Error("File ID should be set after creation")
	}

	// Retrieve file
	var retrieved File
	if err := db.Where("file_hash = ?", "test-hash-123").First(&retrieved).Error; err != nil {
		t.Fatalf("Failed to retrieve file: %v", err)
	}

	if retrieved.OriginalURL != file.OriginalURL {
		t.Errorf("OriginalURL = %q, want %q", retrieved.OriginalURL, file.OriginalURL)
	}

	// Update file
	retrieved.DownloadStatus = "complete"
	now := time.Now()
	retrieved.CompletedAt = &now
	if err := db.Save(&retrieved).Error; err != nil {
		t.Fatalf("Failed to update file: %v", err)
	}

	// Verify update
	var updated File
	db.First(&updated, retrieved.ID)
	if updated.DownloadStatus != "complete" {
		t.Errorf("DownloadStatus = %q, want %q", updated.DownloadStatus, "complete")
	}
}

func TestLogModel(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-log-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp DB: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}

	// Create a log entry
	log := Log{
		Level:    "info",
		Message:  "Test log message",
		URL:      "https://cdn.com/file.mp4",
		FileHash: "test-hash",
	}

	if err := db.Create(&log).Error; err != nil {
		t.Fatalf("Failed to create log: %v", err)
	}

	if log.ID == 0 {
		t.Error("Log ID should be set after creation")
	}

	// Retrieve log
	var retrieved Log
	if err := db.Where("file_hash = ?", "test-hash").First(&retrieved).Error; err != nil {
		t.Fatalf("Failed to retrieve log: %v", err)
	}

	if retrieved.Message != log.Message {
		t.Errorf("Message = %q, want %q", retrieved.Message, log.Message)
	}
}

func TestFileUniqueConstraint(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-unique-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp DB: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := InitDB(tmpFile.Name())
	if err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}

	// Create first file
	file1 := File{
		FileHash:       "unique-hash",
		OriginalURL:    "https://cdn.com/video1.mp4",
		Filename:       "video.mp4",
		FileSize:       1024,
		SavedPath:      "/tmp/video1.mp4",
		DownloadStatus: "pending",
	}

	if err := db.Create(&file1).Error; err != nil {
		t.Fatalf("Failed to create first file: %v", err)
	}

	// Try to create duplicate file with same hash
	file2 := File{
		FileHash:       "unique-hash",
		OriginalURL:    "https://cdn.com/video2.mp4",
		Filename:       "video.mp4",
		FileSize:       2048,
		SavedPath:      "/tmp/video2.mp4",
		DownloadStatus: "pending",
	}

	err = db.Create(&file2).Error
	if err == nil {
		t.Error("Should not allow duplicate file_hash")
	}
}
