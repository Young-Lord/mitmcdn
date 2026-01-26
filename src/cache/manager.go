package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mitmcdn/src/database"

	"gorm.io/gorm"
)

type Manager struct {
	db              *gorm.DB
	cacheDir        string
	maxFileSize     int64
	maxTotalSize    int64
	ttl             time.Duration
	mu              sync.RWMutex
	activeDownloads map[string]*DownloadTask // fileHash -> task
}

type DownloadTask struct {
	FileHash   string
	URL        string
	Cookie     string
	Status     string // downloading, paused, complete, failed
	Priority   int    // higher = more priority
	Downloaded int64
	TotalSize  int64
	mu         sync.Mutex
	cancel     func() // context cancel function
	pauseChan  chan struct{}
	resumeChan chan struct{}
}

func NewManager(db *gorm.DB, cacheDir string, maxFileSize, maxTotalSize int64, ttl time.Duration) (*Manager, error) {
	// Create cache directory if not exists
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	return &Manager{
		db:              db,
		cacheDir:        cacheDir,
		maxFileSize:     maxFileSize,
		maxTotalSize:    maxTotalSize,
		ttl:             ttl,
		activeDownloads: make(map[string]*DownloadTask),
	}, nil
}

// CacheDir returns the cache directory path
func (m *Manager) CacheDir() string {
	return m.cacheDir
}

// ComputeFileHash computes deduplication hash based on strategy
func (m *Manager) ComputeFileHash(url, cookie, strategy string) string {
	var hashInput string

	switch strategy {
	case "filename_only":
		// Extract filename from URL
		parts := strings.Split(url, "/")
		filename := parts[len(parts)-1]
		// Remove query parameters
		if idx := strings.Index(filename, "?"); idx != -1 {
			filename = filename[:idx]
		}
		hashInput = filename
		if cookie != "" {
			hashInput += "|" + cookie
		}
	case "full_url":
		hashInput = url
		if cookie != "" {
			hashInput += "|" + cookie
		}
	default:
		hashInput = url
	}

	hash := sha256.Sum256([]byte(hashInput))
	return hex.EncodeToString(hash[:])
}

// GetOrCreateFile gets existing file or creates a new entry
func (m *Manager) GetOrCreateFile(url, cookie, filename, strategy string) (*database.File, error) {
	fileHash := m.ComputeFileHash(url, cookie, strategy)

	var file database.File
	err := m.db.Where("file_hash = ?", fileHash).First(&file).Error
	if err == nil {
		// Update last accessed time
		file.LastAccessedAt = time.Now()
		m.db.Save(&file)
		return &file, nil
	}

	if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	// Create new file entry
	file = database.File{
		FileHash:       fileHash,
		OriginalURL:    url,
		RequestCookie:  cookie,
		Filename:       filename,
		FileSize:       0, // Unknown initially
		SavedPath:      filepath.Join(m.cacheDir, fileHash),
		DownloadStatus: "pending",
		LastAccessedAt: time.Now(),
	}

	if err := m.db.Create(&file).Error; err != nil {
		return nil, err
	}

	return &file, nil
}

// MatchCDNRule checks if URL matches any CDN rule
func MatchCDNRule(url string, domain, pattern string) bool {
	// Check domain
	if !strings.Contains(url, domain) {
		return false
	}

	// Check pattern if provided
	if pattern != "" {
		matched, err := regexp.MatchString(pattern, url)
		if err != nil || !matched {
			return false
		}
	}

	return true
}

// CleanupExpiredFiles removes files older than TTL
func (m *Manager) CleanupExpiredFiles() error {
	cutoff := time.Now().Add(-m.ttl)

	var files []database.File
	if err := m.db.Where("last_accessed_at < ? AND download_status = ?", cutoff, "complete").Find(&files).Error; err != nil {
		return err
	}

	for _, file := range files {
		os.Remove(file.SavedPath)
		m.db.Delete(&file)
	}

	return nil
}

// LRUEvict removes least recently used files when cache is full
func (m *Manager) LRUEvict(targetSize int64) error {
	var totalSize int64
	var files []database.File

	if err := m.db.Order("last_accessed_at ASC").Find(&files).Error; err != nil {
		return err
	}

	// Calculate total size
	for _, file := range files {
		if file.DownloadStatus == "complete" {
			info, err := os.Stat(file.SavedPath)
			if err == nil {
				totalSize += info.Size()
			}
		}
	}

	// Evict until we're under target
	for _, file := range files {
		if totalSize <= targetSize {
			break
		}

		if file.DownloadStatus == "complete" {
			info, err := os.Stat(file.SavedPath)
			if err == nil {
				os.Remove(file.SavedPath)
				totalSize -= info.Size()
				m.db.Delete(&file)
			}
		}
	}

	return nil
}
