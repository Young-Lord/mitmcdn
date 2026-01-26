package download

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"mitmcdn/src/cache"
	"mitmcdn/src/database"

	"gorm.io/gorm"
)

type Scheduler struct {
	cacheManager *cache.Manager
	db           *gorm.DB
	httpClient   *http.Client
	mu           sync.RWMutex
	tasks        map[string]*Task // fileHash -> task
	priorityChan chan *Task       // Priority queue
}

type Task struct {
	FileHash   string
	URL        string
	Cookie     string
	Priority   int    // Higher = more priority
	Status     string // pending, downloading, paused, complete, failed
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	pauseChan  chan struct{}
	resumeChan chan struct{}
	file       *database.File
	dataChan   chan []byte // Channel for streaming data to clients
	streamMu   sync.RWMutex
	streamers  []io.Writer // Active streamers (clients receiving data)
}

func NewScheduler(cacheManager *cache.Manager, db *gorm.DB, upstreamProxy string) (*Scheduler, error) {
	// Create HTTP client with upstream proxy if configured
	transport := &http.Transport{
		MaxIdleConns:       100,
		IdleConnTimeout:    90 * time.Second,
		DisableCompression: false,
	}

	// TODO: Configure upstream proxy if provided
	// if upstreamProxy != "" {
	//     // Parse and configure proxy
	// }

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Minute, // Long timeout for large files
	}

	return NewSchedulerWithClient(cacheManager, db, upstreamProxy, client)
}

// NewSchedulerWithClient creates a scheduler with a custom HTTP client (useful for testing)
func NewSchedulerWithClient(cacheManager *cache.Manager, db *gorm.DB, upstreamProxy string, httpClient *http.Client) (*Scheduler, error) {
	return &Scheduler{
		cacheManager: cacheManager,
		db:           db,
		httpClient:   httpClient,
		tasks:        make(map[string]*Task),
		priorityChan: make(chan *Task, 100),
	}, nil
}

// GetActiveTaskCount returns the number of active download tasks
func (s *Scheduler) GetActiveTaskCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, task := range s.tasks {
		if task.Status == "downloading" {
			count++
		}
	}
	return count
}

// StartDownload starts or resumes downloading a file
func (s *Scheduler) StartDownload(file *database.File, url, cookie string, priority int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if task already exists
	if task, exists := s.tasks[file.FileHash]; exists {
		if task.Status == "downloading" {
			return nil // Already downloading
		}
		// Resume paused task
		task.Priority = priority
		task.resumeChan <- struct{}{}
		return nil
	}

	// Create new task
	ctx, cancel := context.WithCancel(context.Background())
	task := &Task{
		FileHash:   file.FileHash,
		URL:        url,
		Cookie:     cookie,
		Priority:   priority,
		Status:     "pending",
		ctx:        ctx,
		cancel:     cancel,
		pauseChan:  make(chan struct{}),
		resumeChan: make(chan struct{}),
		file:       file,
		dataChan:   make(chan []byte, 10), // Buffered channel for streaming
		streamers:  make([]io.Writer, 0),
	}

	s.tasks[file.FileHash] = task

	// Start download in goroutine
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC in downloadTask: %v", r)
			}
		}()
		s.downloadTask(task)
	}()

	return nil
}

// PauseLowPriorityTasks pauses all tasks with lower priority
func (s *Scheduler) PauseLowPriorityTasks(minPriority int) {
	s.mu.RLock()
	tasks := make([]*Task, 0)
	for _, task := range s.tasks {
		if task.Status == "downloading" && task.Priority < minPriority {
			tasks = append(tasks, task)
		}
	}
	s.mu.RUnlock()

	// Pause tasks outside of the lock to avoid deadlock
	for _, task := range tasks {
		task.mu.Lock()
		if task.Status == "downloading" {
			task.Status = "paused"
			// Non-blocking send to pauseChan
			select {
			case task.pauseChan <- struct{}{}:
			default:
			}
		}
		task.mu.Unlock()
	}
}

// downloadTask performs the actual download
func (s *Scheduler) downloadTask(task *Task) {
	task.mu.Lock()
	task.Status = "downloading"
	task.mu.Unlock()

	// Update database
	s.db.Model(&database.File{}).Where("file_hash = ?", task.FileHash).Updates(map[string]interface{}{
		"download_status": "downloading",
	})

	// Check if file already exists and get current size
	fileInfo, err := os.Stat(task.file.SavedPath)
	var startOffset int64 = 0
	if err == nil {
		startOffset = fileInfo.Size()
	}

	// Create request with Range header for resume
	req, err := http.NewRequestWithContext(task.ctx, "GET", task.URL, nil)
	if err != nil {
		s.handleDownloadError(task, err)
		return
	}

	if task.Cookie != "" {
		req.Header.Set("Cookie", task.Cookie)
	}

	if startOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startOffset))
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.handleDownloadError(task, err)
		return
	}
	defer resp.Body.Close()

	// Handle partial content (206) or full content (200)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		s.handleDownloadError(task, fmt.Errorf("unexpected status code: %d", resp.StatusCode))
		return
	}

	// Get Content-Type from response (keep full header including charset)
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Open file for writing (append if resuming)
	file, err := os.OpenFile(task.file.SavedPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		s.handleDownloadError(task, err)
		return
	}
	defer file.Close()

	// Get total size if available from Content-Length header
	totalSize := startOffset
	if resp.ContentLength > 0 {
		totalSize = startOffset + resp.ContentLength
	}

	// Update file size and content type in database
	updates := map[string]interface{}{}
	if totalSize > startOffset {
		updates["file_size"] = totalSize
	}
	if contentType != "" {
		updates["content_type"] = contentType
	}
	if len(updates) > 0 {
		s.db.Model(&database.File{}).Where("file_hash = ?", task.FileHash).Updates(updates)
	}

	// Download with pause/resume support
	buffer := make([]byte, 32*1024) // 32KB buffer
	downloaded := startOffset

	for {
		select {
		case <-task.ctx.Done():
			return
		case <-task.pauseChan:
			task.mu.Lock()
			task.Status = "paused"
			task.mu.Unlock()
			// Wait for resume
			<-task.resumeChan
			task.mu.Lock()
			task.Status = "downloading"
			task.mu.Unlock()
		default:
			n, err := resp.Body.Read(buffer)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buffer[:n])

				// Write to file
				if _, writeErr := file.Write(data); writeErr != nil {
					s.handleDownloadError(task, writeErr)
					return
				}

				// Send to channel for all streamers
				select {
				case task.dataChan <- data:
				default:
					// Channel full, skip (streamers will read from file via ticker)
				}

				downloaded += int64(n)

				// Update progress in database periodically
				if downloaded%1024*1024 == 0 { // Every MB
					s.db.Model(&database.File{}).Where("file_hash = ?", task.FileHash).Update("downloaded_bytes", downloaded)
				}
			}
			if err == io.EOF {
				// Download complete
				now := time.Now()
				s.db.Model(&database.File{}).Where("file_hash = ?", task.FileHash).Updates(map[string]interface{}{
					"download_status":  "complete",
					"downloaded_bytes": downloaded,
					"completed_at":     &now,
				})

				task.mu.Lock()
				task.Status = "complete"
				task.mu.Unlock()

				// Close data channel to signal streamers
				close(task.dataChan)
				return
			}
			if err != nil {
				s.handleDownloadError(task, err)
				return
			}
		}
	}
}

func (s *Scheduler) handleDownloadError(task *Task, err error) {
	task.mu.Lock()
	task.Status = "failed"
	task.mu.Unlock()

	s.db.Model(&database.File{}).Where("file_hash = ?", task.FileHash).Update("download_status", "failed")

	// Log error with stack trace
	errorMsg := fmt.Sprintf("Download failed: %v", err)
	log.Printf("Download error for %s: %v\nStack trace:\n%s", task.URL, err, string(debug.Stack()))

	// Log error to database
	s.db.Create(&database.Log{
		Level:    "error",
		Message:  errorMsg,
		URL:      task.URL,
		FileHash: task.FileHash,
	})
}

// StreamFile streams a file to client while downloading (if not complete)
// Implements "stream tapping" - downloads from upstream while streaming to client
func (s *Scheduler) StreamFile(file *database.File, w http.ResponseWriter, r *http.Request) error {
	// If file is complete, serve directly
	if file.DownloadStatus == "complete" {
		http.ServeFile(w, r, file.SavedPath)
		return nil
	}

	// If file download failed, return error to client
	if file.DownloadStatus == "failed" {
		// Get error message from logs
		var logEntry database.Log
		s.db.Where("file_hash = ? AND level = ?", file.FileHash, "error").
			Order("created_at DESC").
			First(&logEntry)

		// Extract HTTP status code from error message if available
		var statusCode int = http.StatusBadGateway
		var errorMsg string = "Download failed"

		if logEntry.Message != "" {
			errorMsg = logEntry.Message
			// Try to extract status code from message like "Download failed: unexpected status code: 404"
			if strings.Contains(logEntry.Message, "unexpected status code:") {
				var code int
				// Try different patterns
				if _, err := fmt.Sscanf(logEntry.Message, "Download failed: unexpected status code: %d", &code); err == nil {
					statusCode = code
				} else if _, err := fmt.Sscanf(logEntry.Message, "unexpected status code: %d", &code); err == nil {
					statusCode = code
				}
			}
		}

		// Remove "Download failed: " prefix if present for cleaner error message
		if after, ok := strings.CutPrefix(errorMsg, "Download failed: "); ok {
			errorMsg = after
		}

		http.Error(w, errorMsg, statusCode)
		return fmt.Errorf("file download failed: %s", errorMsg)
	}

	// Get or start download task
	// Check if task exists (without holding lock during StartDownload to avoid deadlock)
	s.mu.Lock()
	task, exists := s.tasks[file.FileHash]
	needsStart := !exists || (task != nil && task.Status != "downloading" && task.Status != "complete")
	s.mu.Unlock()

	if needsStart {
		// Start download with high priority (don't hold lock to avoid deadlock)
		s.PauseLowPriorityTasks(100)
		if err := s.StartDownload(file, file.OriginalURL, file.RequestCookie, 100); err != nil {
			return fmt.Errorf("failed to start download: %w", err)
		}
		// Get the task we just created
		s.mu.Lock()
		task = s.tasks[file.FileHash]
		s.mu.Unlock()

		if task == nil {
			return fmt.Errorf("task not found after creation")
		}
	}

	// Open file for reading existing content first
	currentSize := int64(0)
	if f, err := os.Open(file.SavedPath); err == nil {
		info, _ := f.Stat()
		currentSize = info.Size()
		f.Close() // Close and reopen later if needed
	}

	// Wait for task to be ready (with timeout)
	timeout := time.After(5 * time.Second)
	ready := false
	for !ready {
		select {
		case <-timeout:
			// Timeout - check if we have any content to serve
			if currentSize > 0 {
				// We have some content, serve it even if download hasn't started
				ready = true
				break
			}
			// No content, return error
			return fmt.Errorf("timeout waiting for download to start")
		default:
			task.mu.Lock()
			status := task.Status
			task.mu.Unlock()
			if status == "downloading" || status == "complete" {
				ready = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Wait a bit for ContentType and FileSize to be set by download task
	for i := 0; i < 20; i++ {
		// Reload file from database to get updated info
		var updatedFile database.File
		if err := s.db.First(&updatedFile, "file_hash = ?", file.FileHash).Error; err == nil {
			file.ContentType = updatedFile.ContentType
			file.FileSize = updatedFile.FileSize
			if file.ContentType != "" {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Set headers for streaming
	contentType := file.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)

	// Set Content-Length if we know the total size (from upstream Content-Length)
	// This allows proper connection termination
	if file.FileSize > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", file.FileSize))
	}
	w.WriteHeader(http.StatusOK)

	// Flush headers to start streaming
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// Stream existing content first (if any)
	if currentSize > 0 {
		if f, err := os.Open(file.SavedPath); err == nil {
			io.Copy(w, f)
			f.Close()
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}

	// Stream new data as it arrives from download task
	for {
		select {
		case <-r.Context().Done():
			// Client disconnected
			return nil
		case data, ok := <-task.dataChan:
			if !ok {
				// Channel closed, download complete
				// Go's http.ResponseWriter will automatically finish the response
				// when this function returns (either close connection or send chunked end marker)
				return nil
			}
			if _, err := w.Write(data); err != nil {
				return err
			}
			currentSize += int64(len(data))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}
}
