package download

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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
	ytDLPCommand []string
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
	closeOnce  sync.Once
	streamMu   sync.RWMutex
	streamers  []io.Writer // Active streamers (clients receiving data)
	notifyCh   chan struct{}
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
		ytDLPCommand: []string{"yt-dlp"},
	}, nil
}

func (s *Scheduler) HTTPClient() *http.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.httpClient
}

func (s *Scheduler) ConfigureYTDLPCommand(command []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ytDLPCommand = append([]string(nil), command...)
}

func (s *Scheduler) getYTDLPCommand() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return append([]string(nil), s.ytDLPCommand...)
}

// GetActiveTaskCount returns the number of active download tasks
func (s *Scheduler) GetActiveTaskCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, task := range s.tasks {
		task.mu.Lock()
		status := task.Status
		task.mu.Unlock()
		if status == "downloading" {
			count++
		}
	}
	return count
}

// StartDownload starts or resumes downloading a file
func (s *Scheduler) StartDownload(file *database.File, url, cookie string, priority int) error {
	s.mu.Lock()
	task, exists := s.tasks[file.FileHash]
	if exists {
		s.mu.Unlock()

		task.mu.Lock()
		status := task.Status
		task.Priority = priority
		task.mu.Unlock()

		if status == "downloading" || status == "pending" || status == "complete" {
			return nil // Already active or finished
		}

		if status == "paused" {
			// Resume paused task
			task.resumeChan <- struct{}{}
		}
		return nil
	}

	// Create new task
	ctx, cancel := context.WithCancel(context.Background())
	task = &Task{
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
		notifyCh:   make(chan struct{}, 1),
	}

	s.tasks[file.FileHash] = task
	s.mu.Unlock()

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
		task.mu.Lock()
		shouldPause := task.Status == "downloading" && task.Priority < minPriority
		task.mu.Unlock()

		if shouldPause {
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
	task.notify()

	// Update database
	s.db.Model(&database.File{}).Where("file_hash = ?", task.FileHash).Updates(map[string]interface{}{
		"download_status": "downloading",
	})

	if videoID, ok := extractYTDLPVideoID(task.URL); ok {
		s.downloadYTDLPTask(task, videoID)
		return
	}

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

	// Update file size and content type in database and memory
	updates := map[string]interface{}{}
	if totalSize > startOffset {
		updates["file_size"] = totalSize
		// Update in-memory copy for immediate access
		task.mu.Lock()
		task.file.FileSize = totalSize
		task.mu.Unlock()
	}
	if contentType != "" {
		updates["content_type"] = contentType
		// Update in-memory copy for immediate access
		task.mu.Lock()
		task.file.ContentType = contentType
		task.mu.Unlock()
	}
	if len(updates) > 0 {
		s.db.Model(&database.File{}).Where("file_hash = ?", task.FileHash).Updates(updates)
	}
	task.notify()

	if task.file.MaxSize > 0 && totalSize > 0 && totalSize > task.file.MaxSize {
		s.handleDownloadError(task, fmt.Errorf("file size %d exceeds max size %d", totalSize, task.file.MaxSize))
		_ = os.Remove(task.file.SavedPath)
		return
	}

	// Download with pause/resume support
	buffer := make([]byte, 32*1024) // 32KB buffer
	downloaded := startOffset

	for {
		select {
		case <-task.ctx.Done():
			return
		default:
		}

		n, err := resp.Body.Read(buffer)
		if n > 0 {
			if task.file.MaxSize > 0 && downloaded+int64(n) > task.file.MaxSize {
				s.handleDownloadError(task, fmt.Errorf("download exceeded max size %d", task.file.MaxSize))
				_ = os.Remove(task.file.SavedPath)
				return
			}
			data := make([]byte, n)
			copy(data, buffer[:n])

			if _, writeErr := file.Write(data); writeErr != nil {
				s.handleDownloadError(task, writeErr)
				return
			}

			sent := false
			for !sent {
				select {
				case task.dataChan <- data:
					sent = true
				case <-task.ctx.Done():
					return
				case <-task.pauseChan:
					task.mu.Lock()
					task.Status = "paused"
					task.mu.Unlock()
					task.notify()
					<-task.resumeChan
					task.mu.Lock()
					task.Status = "downloading"
					task.mu.Unlock()
					task.notify()
				}
			}

			downloaded += int64(n)

			if downloaded%(1024*1024) == 0 { // Every MB
				s.db.Model(&database.File{}).Where("file_hash = ?", task.FileHash).Update("downloaded_bytes", downloaded)
			}
		}
		if err == io.EOF {
			now := time.Now()
			s.db.Model(&database.File{}).Where("file_hash = ?", task.FileHash).Updates(map[string]interface{}{
				"download_status":  "complete",
				"downloaded_bytes": downloaded,
				"completed_at":     &now,
			})

			task.mu.Lock()
			task.Status = "complete"
			task.mu.Unlock()
			task.notify()

			s.closeTaskDataChan(task)
			return
		}
		if err != nil {
			s.handleDownloadError(task, err)
			return
		}
	}
}

func extractYTDLPVideoID(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "yt-dlp" {
		return "", false
	}

	videoID := parsed.Host
	if videoID == "" {
		videoID = strings.TrimPrefix(parsed.Path, "/")
	}
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return "", false
	}

	return videoID, true
}

func (s *Scheduler) downloadYTDLPTask(task *Task, videoID string) {
	command := s.getYTDLPCommand()
	if len(command) == 0 {
		s.handleDownloadError(task, fmt.Errorf("yt-dlp command is empty"))
		return
	}

	if err := os.MkdirAll(filepath.Dir(task.file.SavedPath), 0755); err != nil {
		s.handleDownloadError(task, err)
		return
	}

	tempPath := task.file.SavedPath + ".part"
	_ = os.Remove(tempPath)

	args := append([]string{}, command[1:]...)
	sourceURL := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoID)
	args = append(args,
		"--no-part",
		"--no-continue",
		"--no-playlist",
		"-o", tempPath,
		sourceURL,
	)

	cmd := exec.CommandContext(task.ctx, command[0], args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		s.handleDownloadError(task, fmt.Errorf("yt-dlp failed: %w: %s", err, strings.TrimSpace(string(output))))
		return
	}

	// yt-dlp may append a different extension after merging formats
	// (e.g. ".part.webm" instead of ".part"), so glob for the actual output file.
	actualPath := tempPath
	if _, statErr := os.Stat(tempPath); os.IsNotExist(statErr) {
		matches, _ := filepath.Glob(tempPath + ".*")
		if len(matches) == 1 {
			actualPath = matches[0]
		} else if len(matches) > 1 {
			// Pick the largest file (the merged output)
			var best string
			var bestSize int64
			for _, m := range matches {
				if fi, e := os.Stat(m); e == nil && fi.Size() > bestSize {
					best = m
					bestSize = fi.Size()
				}
			}
			actualPath = best
		}
	}

	if err := os.Rename(actualPath, task.file.SavedPath); err != nil {
		s.handleDownloadError(task, fmt.Errorf("failed to finalize yt-dlp output: %w", err))
		return
	}

	info, err := os.Stat(task.file.SavedPath)
	if err != nil {
		s.handleDownloadError(task, err)
		return
	}

	// Determine content type from the actual file extension yt-dlp produced.
	contentType := "video/mp4"
	if actualPath != tempPath {
		switch strings.ToLower(filepath.Ext(actualPath)) {
		case ".webm":
			contentType = "video/webm"
		case ".mkv":
			contentType = "video/x-matroska"
		case ".mp4":
			contentType = "video/mp4"
		}
	}

	now := time.Now()
	s.db.Model(&database.File{}).Where("file_hash = ?", task.FileHash).Updates(map[string]interface{}{
		"download_status":  "complete",
		"downloaded_bytes": info.Size(),
		"file_size":        info.Size(),
		"content_type":     contentType,
		"completed_at":     &now,
	})

	task.mu.Lock()
	task.Status = "complete"
	task.file.ContentType = contentType
	task.file.FileSize = info.Size()
	task.mu.Unlock()
	task.notify()

	s.closeTaskDataChan(task)
}

func (s *Scheduler) handleDownloadError(task *Task, err error) {
	task.mu.Lock()
	task.Status = "failed"
	task.mu.Unlock()
	s.closeTaskDataChan(task)
	task.notify()

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

func (s *Scheduler) closeTaskDataChan(task *Task) {
	task.closeOnce.Do(func() {
		close(task.dataChan)
	})
}

func (t *Task) notify() {
	select {
	case t.notifyCh <- struct{}{}:
	default:
	}
}

func (t *Task) waitUntil(timeout time.Duration, check func() bool) bool {
	if check() {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return check()
		case <-t.notifyCh:
			if check() {
				return true
			}
		}
	}
}

// StreamFile streams a file to client while downloading (if not complete)
// Implements "stream tapping" - downloads from upstream while streaming to client
func (s *Scheduler) StreamFile(file *database.File, w http.ResponseWriter, r *http.Request, priority int) error {
	// If file is complete, serve directly
	if file.DownloadStatus == "complete" {
		http.ServeFile(w, r, file.SavedPath)
		return nil
	}

	// If file download failed, return error to client
	if file.DownloadStatus == "failed" {
		return s.writeDownloadError(w, file.FileHash)
	}

	// Get or start download task
	// Check if task exists (without holding lock during StartDownload to avoid deadlock)
	s.mu.RLock()
	task, exists := s.tasks[file.FileHash]
	s.mu.RUnlock()

	needsStart := !exists
	if exists && task != nil {
		task.mu.Lock()
		status := task.Status
		task.mu.Unlock()
		needsStart = status != "downloading" && status != "complete"
	}

	if needsStart {
		if priority == 0 {
			priority = 100
		}
		// Start download with high priority (don't hold lock to avoid deadlock)
		s.PauseLowPriorityTasks(priority)
		if err := s.StartDownload(file, file.OriginalURL, file.RequestCookie, priority); err != nil {
			return fmt.Errorf("failed to start download: %w", err)
		}
		// Get the task we just created
		s.mu.RLock()
		task = s.tasks[file.FileHash]
		s.mu.RUnlock()

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

	// Wait for task to be ready (downloading or complete)
	task.mu.Lock()
	status := task.Status
	task.mu.Unlock()
	if status != "downloading" && status != "complete" && status != "failed" {
		if !task.waitUntil(5*time.Second, func() bool {
			task.mu.Lock()
			s := task.Status
			task.mu.Unlock()
			return s == "downloading" || s == "complete" || s == "failed"
		}) && currentSize == 0 {
			return fmt.Errorf("timeout waiting for download to start")
		}
	}

	task.mu.Lock()
	status = task.Status
	task.mu.Unlock()
	if status == "failed" && currentSize == 0 {
		return s.writeDownloadError(w, file.FileHash)
	}

	// Wait for ContentType and FileSize to be set by download task
	task.waitUntil(time.Second, func() bool {
		task.mu.Lock()
		ct := task.file.ContentType
		task.mu.Unlock()
		return ct != ""
	})

	task.mu.Lock()
	status = task.Status
	ct := task.file.ContentType
	fs := task.file.FileSize
	task.mu.Unlock()

	if status == "failed" && currentSize == 0 {
		return s.writeDownloadError(w, file.FileHash)
	}
	if ct != "" {
		file.ContentType = ct
		file.FileSize = fs
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

func (s *Scheduler) writeDownloadError(w http.ResponseWriter, fileHash string) error {
	// Get error message from logs
	var logEntry database.Log
	s.db.Where("file_hash = ? AND level = ?", fileHash, "error").
		Order("created_at DESC").
		First(&logEntry)

	// Extract HTTP status code from error message if available
	statusCode := http.StatusBadGateway
	errorMsg := "Download failed"

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
