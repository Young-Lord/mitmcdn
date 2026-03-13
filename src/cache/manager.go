package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
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

type FileOptions struct {
	CacheKey      string
	DedupStrategy string
	TTLOverride   time.Duration
	MaxSize       int64
	RuleName      string
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

// ComputeFileHashFromKey computes deduplication hash based on a cache key.
func (m *Manager) ComputeFileHashFromKey(key string) string {
	hash := sha256.Sum256([]byte(key))
	return hex.EncodeToString(hash[:])
}

// GetOrCreateFile gets existing file or creates a new entry
func (m *Manager) GetOrCreateFile(url, cookie, filename, strategy string) (*database.File, error) {
	return m.GetOrCreateFileWithOptions(url, cookie, filename, FileOptions{DedupStrategy: strategy})
}

// GetOrCreateFileWithOptions gets existing file or creates a new entry with options.
func (m *Manager) GetOrCreateFileWithOptions(url, cookie, filename string, opts FileOptions) (*database.File, error) {
	strategy := opts.DedupStrategy
	if strategy == "" {
		strategy = "full_url"
	}

	fileHash := ""
	if opts.CacheKey != "" {
		fileHash = m.ComputeFileHashFromKey(opts.CacheKey)
	} else {
		fileHash = m.ComputeFileHash(url, cookie, strategy)
	}

	var file database.File
	err := m.db.Where("file_hash = ?", fileHash).First(&file).Error
	if err == nil {
		// Update last accessed time and rule fields
		file.LastAccessedAt = time.Now()
		updates := map[string]interface{}{}
		if opts.CacheKey != "" {
			updates["cache_key"] = opts.CacheKey
		}
		if opts.TTLOverride > 0 {
			updates["ttl_override_seconds"] = int64(opts.TTLOverride.Seconds())
		}
		if opts.MaxSize > 0 {
			updates["max_size"] = opts.MaxSize
		}
		if opts.RuleName != "" {
			updates["rule_name"] = opts.RuleName
		}
		if len(updates) > 0 {
			m.db.Model(&database.File{}).Where("file_hash = ?", fileHash).Updates(updates)
		}
		m.db.Save(&file)
		return &file, nil
	}

	if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	// Create new file entry
	file = database.File{
		FileHash:           fileHash,
		CacheKey:           opts.CacheKey,
		OriginalURL:        url,
		RequestCookie:      cookie,
		Filename:           filename,
		FileSize:           0, // Unknown initially
		SavedPath:          filepath.Join(m.cacheDir, fileHash),
		DownloadStatus:     "pending",
		LastAccessedAt:     time.Now(),
		TTLOverrideSeconds: int64(opts.TTLOverride.Seconds()),
		MaxSize:            opts.MaxSize,
		RuleName:           opts.RuleName,
	}

	if err := m.db.Create(&file).Error; err != nil {
		return nil, err
	}

	return &file, nil
}

// CleanupExpiredFiles removes files older than TTL
func (m *Manager) CleanupExpiredFiles() error {
	var files []database.File
	if err := m.db.Where("download_status = ?", "complete").Find(&files).Error; err != nil {
		return err
	}

	now := time.Now()
	for _, file := range files {
		ttl := m.ttl
		if file.TTLOverrideSeconds > 0 {
			ttl = time.Duration(file.TTLOverrideSeconds) * time.Second
		}
		if file.LastAccessedAt.Before(now.Add(-ttl)) {
			os.Remove(file.SavedPath)
			m.db.Delete(&file)
		}
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
