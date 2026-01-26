package database

import (
	"log"
	"os"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// File represents a cached file metadata
type File struct {
	ID             uint      `gorm:"primaryKey"`
	FileHash       string    `gorm:"uniqueIndex;not null"` // Deduplication fingerprint
	OriginalURL    string    `gorm:"not null"`
	RequestCookie  string    `gorm:"type:text"` // Cookie used for authentication
	Filename       string    `gorm:"not null"`
	FileSize       int64     `gorm:"not null"`
	SavedPath      string    `gorm:"not null"`
	ContentType    string    `gorm:"type:text"` // MIME type from upstream
	DownloadStatus string    `gorm:"not null;default:'pending'"` // pending, downloading, complete, failed
	CreatedAt      time.Time `gorm:"autoCreateTime"`
	LastAccessedAt time.Time `gorm:"autoUpdateTime"`
	CompletedAt    *time.Time // nil if not completed
	DownloadedBytes int64     `gorm:"default:0"` // For resume support
}

// Log represents system logs
type Log struct {
	ID        uint      `gorm:"primaryKey"`
	Level     string    `gorm:"not null"` // info, warn, error
	Message   string    `gorm:"type:text;not null"`
	URL       string    `gorm:"type:text"`
	FileHash  string
	CreatedAt time.Time `gorm:"autoCreateTime"`
}

// InitDB initializes the database and runs migrations
func InitDB(dbPath string) (*gorm.DB, error) {
	// Configure GORM logger with filename and line number
	// Use a custom writer that includes file and line info
	gormLogger := logger.New(
		log.New(os.Stderr, "", log.LstdFlags|log.Lshortfile),
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Info,
			IgnoreRecordNotFoundError: false,
			Colorful:                  false,
		},
	)

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		return nil, err
	}

	// Auto migrate
	if err := db.AutoMigrate(&File{}, &Log{}); err != nil {
		return nil, err
	}

	return db, nil
}
