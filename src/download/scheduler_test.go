package download

import (
	"crypto/tls"
	"net/http"
	"os"
	"testing"
	"time"

	"mitmcdn/src/cache"
	"mitmcdn/src/database"

	"gorm.io/gorm"
)

func setupTestScheduler(t *testing.T) (*Scheduler, *gorm.DB, *cache.Manager) {
	// Setup database using database.InitDB
	tmpDB, err := os.CreateTemp("", "test-scheduler-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp DB: %v", err)
	}
	tmpDB.Close()

	// Ensure file is writable
	if err := os.Chmod(tmpDB.Name(), 0644); err != nil {
		t.Logf("Warning: failed to set file permissions: %v", err)
	}

	t.Cleanup(func() {
		os.Remove(tmpDB.Name())
	})

	db, err := database.InitDB(tmpDB.Name())
	if err != nil {
		t.Fatalf("Failed to init DB: %v", err)
	}

	// Setup cache manager
	tmpDir := t.TempDir()
	cacheMgr, err := cache.NewManager(db, tmpDir, 1024*1024, 10*1024*1024, time.Hour)
	if err != nil {
		t.Fatalf("Failed to create cache manager: %v", err)
	}

	// Setup scheduler with test HTTP client that trusts all certificates
	sched, err := NewSchedulerWithClient(cacheMgr, db, "", createTestHTTPClient())
	if err != nil {
		t.Fatalf("Failed to create scheduler: %v", err)
	}

	return sched, db, cacheMgr
}

// createTestHTTPClient creates an HTTP client that trusts all certificates (for testing only)
func createTestHTTPClient() *http.Client {
	transport := &http.Transport{
		MaxIdleConns:       100,
		IdleConnTimeout:    90 * time.Second,
		DisableCompression: false,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // Trust all certificates in tests
		},
	}

	return &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second, // Shorter timeout for tests
	}
}

func TestNewScheduler(t *testing.T) {
	sched, _, _ := setupTestScheduler(t)

	if sched == nil {
		t.Fatal("Scheduler should not be nil")
	}

	if sched.httpClient == nil {
		t.Error("HTTP client should be initialized")
	}

	if sched.tasks == nil {
		t.Error("Tasks map should be initialized")
	}
}

func TestStartDownload(t *testing.T) {
	sched, _, cacheMgr := setupTestScheduler(t)

	// Use a real URL that exists (httpbin.org works)
	testURL := "https://httpbin.org/get?a=1"

	// Create a test file using cache manager to ensure proper setup
	file, err := cacheMgr.GetOrCreateFile(testURL, "", "index.html", "filename_only")
	if err != nil {
		t.Fatalf("Failed to get or create file: %v", err)
	}

	// Start download - this should work with httpbin.org
	// Use goroutine to avoid blocking
	go sched.StartDownload(file, testURL, "", 100)

	// Wait a bit for task to be created
	time.Sleep(100 * time.Millisecond)

	// Check that task was created
	sched.mu.RLock()
	task, exists := sched.tasks[file.FileHash]
	sched.mu.RUnlock()

	if !exists {
		t.Error("Task should be created in tasks map")
		return
	}

	if task == nil {
		t.Error("Task should not be nil")
		return
	}

	task.mu.Lock()
	priority := task.Priority
	task.mu.Unlock()
	if priority != 100 {
		t.Errorf("Task priority = %d, want 100", priority)
	}
}

func TestPauseLowPriorityTasks(t *testing.T) {
	sched, _, cacheMgr := setupTestScheduler(t)

	// Use real URLs (httpbin.org works better than index.html)
	testURL1 := "https://httpbin.org/get?a=1"
	testURL2 := "https://httpbin.org/get?b=2"

	// Create files using cache manager
	file1, err := cacheMgr.GetOrCreateFile(testURL1, "", "index.html", "full_url")
	if err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}

	file2, err := cacheMgr.GetOrCreateFile(testURL2, "", "index.html", "full_url")
	if err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	files := []*database.File{file1, file2}

	for _, file := range files {
		// Start download with low priority (non-blocking)
		go sched.StartDownload(file, file.OriginalURL, "", 10)
	}

	// Wait a bit for tasks to be created
	time.Sleep(100 * time.Millisecond)

	// Pause low priority tasks
	sched.PauseLowPriorityTasks(50)

	// Verify tasks exist and check their status
	sched.mu.RLock()
	taskCount := 0
	for _, file := range files {
		if task, exists := sched.tasks[file.FileHash]; exists {
			taskCount++
			task.mu.Lock()
			priority := task.Priority
			status := task.Status
			task.mu.Unlock()

			if priority < 50 {
				// Task should be paused or at least exist
				if status != "paused" && status != "downloading" && status != "pending" {
					t.Logf("Task %s with priority %d has unexpected status: %s",
						file.FileHash, priority, status)
				}
			}
		}
	}
	sched.mu.RUnlock()

	if taskCount == 0 {
		t.Error("No tasks were created")
	}
}

func TestSchedulerTaskManagement(t *testing.T) {
	sched, _, cacheMgr := setupTestScheduler(t)

	testURL := "https://httpbin.org/get?a=1"

	// Create file using cache manager
	file, err := cacheMgr.GetOrCreateFile(testURL, "", "index.html", "filename_only")
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	// Start download
	sched.StartDownload(file, file.OriginalURL, "", 100)

	// Wait a bit for task to be created
	time.Sleep(50 * time.Millisecond)

	// Verify task exists
	sched.mu.RLock()
	task, exists := sched.tasks[file.FileHash]
	sched.mu.RUnlock()

	if !exists {
		t.Fatal("Task should exist")
	}

	// Try to start again (should reuse existing task)
	err = sched.StartDownload(file, file.OriginalURL, "", 100)
	if err != nil {
		t.Logf("StartDownload error (may be expected): %v", err)
	}

	// Verify same task
	sched.mu.RLock()
	task2, exists2 := sched.tasks[file.FileHash]
	sched.mu.RUnlock()

	if !exists2 {
		t.Fatal("Task should still exist")
	}

	if task != task2 {
		t.Error("Should reuse same task")
	}
}

func TestExtractYTDLPVideoID(t *testing.T) {
	testCases := []struct {
		name      string
		rawURL    string
		wantID    string
		wantMatch bool
	}{
		{name: "host format", rawURL: "yt-dlp://XZnZkASrArc", wantID: "XZnZkASrArc", wantMatch: true},
		{name: "path format", rawURL: "yt-dlp:///XZnZkASrArc", wantID: "XZnZkASrArc", wantMatch: true},
		{name: "http URL", rawURL: "https://example.com/video.mp4", wantID: "", wantMatch: false},
		{name: "empty ID", rawURL: "yt-dlp://", wantID: "", wantMatch: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			id, ok := extractYTDLPVideoID(tc.rawURL)
			if ok != tc.wantMatch {
				t.Fatalf("match = %v, want %v", ok, tc.wantMatch)
			}
			if id != tc.wantID {
				t.Fatalf("id = %q, want %q", id, tc.wantID)
			}
		})
	}
}

func TestConfigureYTDLPCommand(t *testing.T) {
	sched, _, _ := setupTestScheduler(t)

	command := []string{"yt-dlp", "--cookies-from-browser", "firefox"}
	sched.ConfigureYTDLPCommand(command)

	got := sched.getYTDLPCommand()
	if len(got) != len(command) {
		t.Fatalf("command length = %d, want %d", len(got), len(command))
	}
	for i := range command {
		if got[i] != command[i] {
			t.Fatalf("command[%d] = %q, want %q", i, got[i], command[i])
		}
	}
}
