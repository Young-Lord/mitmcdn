package proxy

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"mitmcdn/src/cache"
	"mitmcdn/src/database"
	"mitmcdn/src/download"

	"gorm.io/gorm"
)

// StatusHandler handles status API and HTML page
type StatusHandler struct {
	db            *gorm.DB
	cacheManager  *cache.Manager
	downloadSched *download.Scheduler
	startTime     time.Time
	version       string
}

// NewStatusHandler creates a new status handler
func NewStatusHandler(db *gorm.DB, cacheMgr *cache.Manager, sched *download.Scheduler) *StatusHandler {
	return &StatusHandler{
		db:            db,
		cacheManager:  cacheMgr,
		downloadSched: sched,
		startTime:     time.Now(),
		version:       getVersion(),
	}
}

// getVersion returns the version string
func getVersion() string {
	// Try to read from version file or use default
	if data, err := os.ReadFile("version.json"); err == nil {
		var v struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &v) == nil && v.Version != "" {
			return v.Version
		}
	}
	return "1.0.0"
}

// StatusResponse represents the status API response
type StatusResponse struct {
	Version      string                 `json:"version"`
	Uptime       string                 `json:"uptime"`
	UptimeSeconds float64               `json:"uptime_seconds"`
	Cache        CacheStatus            `json:"cache"`
	Downloads    DownloadStatus         `json:"downloads"`
	Files        []FileInfo             `json:"files"`
}

// CacheStatus represents cache statistics
type CacheStatus struct {
	TotalFiles      int64   `json:"total_files"`
	CompleteFiles   int64   `json:"complete_files"`
	DownloadingFiles int64   `json:"downloading_files"`
	TotalSize       int64   `json:"total_size"`
	TotalSizeHuman  string  `json:"total_size_human"`
	CacheDir        string  `json:"cache_dir"`
}

// DownloadStatus represents download statistics
type DownloadStatus struct {
	ActiveTasks    int     `json:"active_tasks"`
	CompletedTasks int     `json:"completed_tasks"`
	FailedTasks    int     `json:"failed_tasks"`
	TotalDownloaded int64  `json:"total_downloaded"`
	TotalDownloadedHuman string `json:"total_downloaded_human"`
}

// FileInfo represents file information
type FileInfo struct {
	Hash           string    `json:"hash"`
	URL            string    `json:"url"`
	Filename       string    `json:"filename"`
	Size           int64     `json:"size"`
	SizeHuman      string    `json:"size_human"`
	Status         string    `json:"status"`
	Downloaded     int64     `json:"downloaded"`
	DownloadedHuman string   `json:"downloaded_human"`
	Progress       float64   `json:"progress"`
	CreatedAt      time.Time `json:"created_at"`
	LastAccessed   time.Time `json:"last_accessed"`
}

// HandleAPIStatus handles /api/status JSON endpoint
func (h *StatusHandler) HandleAPIStatus(w http.ResponseWriter, r *http.Request) {
	status := h.getStatus()
	
	w.Header().Set("Content-Type", "application/json")
	
	// Encode to buffer first to get size, then set Content-Length
	data, err := json.Marshal(status)
	if err != nil {
		http.Error(w, "Failed to encode status", http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// HandleStatusPage handles /status HTML page
func (h *StatusHandler) HandleStatusPage(w http.ResponseWriter, r *http.Request) {
	status := h.getStatus()
	
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	
	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>MitmCDN Status</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            padding: 20px;
        }
        .container {
            max-width: 1200px;
            margin: 0 auto;
        }
        .header {
            background: white;
            border-radius: 10px;
            padding: 30px;
            margin-bottom: 20px;
            box-shadow: 0 4px 6px rgba(0,0,0,0.1);
        }
        .header h1 {
            color: #333;
            margin-bottom: 10px;
        }
        .header .meta {
            color: #666;
            font-size: 14px;
        }
        .stats-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
            gap: 20px;
            margin-bottom: 20px;
        }
        .stat-card {
            background: white;
            border-radius: 10px;
            padding: 20px;
            box-shadow: 0 4px 6px rgba(0,0,0,0.1);
        }
        .stat-card h3 {
            color: #667eea;
            margin-bottom: 10px;
            font-size: 14px;
            text-transform: uppercase;
        }
        .stat-card .value {
            font-size: 32px;
            font-weight: bold;
            color: #333;
        }
        .stat-card .label {
            color: #666;
            font-size: 12px;
            margin-top: 5px;
        }
        .files-section {
            background: white;
            border-radius: 10px;
            padding: 30px;
            box-shadow: 0 4px 6px rgba(0,0,0,0.1);
        }
        .files-section h2 {
            color: #333;
            margin-bottom: 20px;
        }
        table {
            width: 100%;
            border-collapse: collapse;
        }
        th, td {
            padding: 12px;
            text-align: left;
            border-bottom: 1px solid #eee;
        }
        th {
            background: #f8f9fa;
            color: #667eea;
            font-weight: 600;
        }
        tr:hover {
            background: #f8f9fa;
        }
        .status-badge {
            display: inline-block;
            padding: 4px 8px;
            border-radius: 4px;
            font-size: 12px;
            font-weight: 600;
        }
        .status-complete { background: #d4edda; color: #155724; }
        .status-downloading { background: #fff3cd; color: #856404; }
        .status-failed { background: #f8d7da; color: #721c24; }
        .status-pending { background: #e2e3e5; color: #383d41; }
        .progress-bar {
            width: 100%;
            height: 8px;
            background: #e9ecef;
            border-radius: 4px;
            overflow: hidden;
            margin-top: 5px;
        }
        .progress-fill {
            height: 100%;
            background: linear-gradient(90deg, #667eea 0%, #764ba2 100%);
            transition: width 0.3s;
        }
        .refresh-btn {
            background: #667eea;
            color: white;
            border: none;
            padding: 10px 20px;
            border-radius: 5px;
            cursor: pointer;
            font-size: 14px;
            margin-top: 20px;
        }
        .refresh-btn:hover {
            background: #5568d3;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>🚀 MitmCDN Cache Server</h1>
            <div class="meta">
                <strong>Version:</strong> {{.Version}} | 
                <strong>Uptime:</strong> {{.Uptime}}
            </div>
        </div>

        <div class="stats-grid">
            <div class="stat-card">
                <h3>Cache Files</h3>
                <div class="value">{{.Cache.TotalFiles}}</div>
                <div class="label">Total: {{.Cache.CompleteFiles}} complete</div>
            </div>
            <div class="stat-card">
                <h3>Cache Size</h3>
                <div class="value">{{.Cache.TotalSizeHuman}}</div>
                <div class="label">{{.Cache.CacheDir}}</div>
            </div>
            <div class="stat-card">
                <h3>Active Downloads</h3>
                <div class="value">{{.Downloads.ActiveTasks}}</div>
                <div class="label">{{.Downloads.CompletedTasks}} completed</div>
            </div>
            <div class="stat-card">
                <h3>Total Downloaded</h3>
                <div class="value">{{.Downloads.TotalDownloadedHuman}}</div>
                <div class="label">{{.Downloads.FailedTasks}} failed</div>
            </div>
        </div>

        <div class="files-section">
            <h2>📁 Cached Files</h2>
            <table>
                <thead>
                    <tr>
                        <th>Filename</th>
                        <th>Size</th>
                        <th>Status</th>
                        <th>Progress</th>
                        <th>Last Accessed</th>
                    </tr>
                </thead>
                <tbody>
                    {{range .Files}}
                    <tr>
                        <td><strong>{{.Filename}}</strong><br><small style="color:#666;">{{.URL}}</small></td>
                        <td>{{.SizeHuman}}</td>
                        <td><span class="status-badge status-{{.Status}}">{{.Status}}</span></td>
                        <td>
                            <div class="progress-bar">
                                <div class="progress-fill" style="width: {{.Progress}}%"></div>
                            </div>
                            <small>{{.DownloadedHuman}} / {{.SizeHuman}}</small>
                        </td>
                        <td>{{.LastAccessed.Format "2006-01-02 15:04:05"}}</td>
                    </tr>
                    {{else}}
                    <tr>
                        <td colspan="5" style="text-align:center;color:#666;">No files cached yet</td>
                    </tr>
                    {{end}}
                </tbody>
            </table>
            <button class="refresh-btn" onclick="location.reload()">🔄 Refresh</button>
        </div>
    </div>
</body>
</html>`

	t, err := template.New("status").Parse(tmpl)
	if err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}
	
	// Execute template to buffer first to get size, then set Content-Length
	var buf strings.Builder
	if err := t.Execute(&buf, status); err != nil {
		http.Error(w, "Template execution error", http.StatusInternalServerError)
		return
	}
	
	html := buf.String()
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(html)))
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(html))
}

// getStatus collects all status information
func (h *StatusHandler) getStatus() StatusResponse {
	uptime := time.Since(h.startTime)
	
	// Get cache statistics
	cacheStats := h.getCacheStats()
	
	// Get download statistics
	downloadStats := h.getDownloadStats()
	
	// Get file list
	files := h.getFileList()
	
	return StatusResponse{
		Version:       h.version,
		Uptime:        formatDuration(uptime),
		UptimeSeconds: uptime.Seconds(),
		Cache:         cacheStats,
		Downloads:     downloadStats,
		Files:         files,
	}
}

// getCacheStats gets cache statistics
func (h *StatusHandler) getCacheStats() CacheStatus {
	var totalFiles, completeFiles, downloadingFiles int64
	var totalSize int64
	
	var files []database.File
	h.db.Find(&files)
	
	for _, file := range files {
		totalFiles++
		if file.DownloadStatus == "complete" {
			completeFiles++
			totalSize += file.FileSize
		} else if file.DownloadStatus == "downloading" {
			downloadingFiles++
		}
	}
	
	return CacheStatus{
		TotalFiles:      totalFiles,
		CompleteFiles:   completeFiles,
		DownloadingFiles: downloadingFiles,
		TotalSize:       totalSize,
		TotalSizeHuman:  formatBytes(totalSize),
		CacheDir:        h.cacheManager.CacheDir(),
	}
}

// getDownloadStats gets download statistics
func (h *StatusHandler) getDownloadStats() DownloadStatus {
	var completedTasks, failedTasks int64
	var totalDownloaded int64
	
	var files []database.File
	h.db.Find(&files)
	
	for _, file := range files {
		if file.DownloadStatus == "complete" {
			completedTasks++
			totalDownloaded += file.DownloadedBytes
		} else if file.DownloadStatus == "failed" {
			failedTasks++
		}
	}
	
	// Get active tasks from scheduler
	activeTasks := h.downloadSched.GetActiveTaskCount()
	
	return DownloadStatus{
		ActiveTasks:      activeTasks,
		CompletedTasks:   int(completedTasks),
		FailedTasks:      int(failedTasks),
		TotalDownloaded:  totalDownloaded,
		TotalDownloadedHuman: formatBytes(totalDownloaded),
	}
}

// getFileList gets list of files with details
func (h *StatusHandler) getFileList() []FileInfo {
	var dbFiles []database.File
	h.db.Order("last_accessed_at DESC").Limit(50).Find(&dbFiles)
	
	files := make([]FileInfo, 0, len(dbFiles))
	for _, file := range dbFiles {
		progress := 0.0
		if file.FileSize > 0 {
			progress = float64(file.DownloadedBytes) / float64(file.FileSize) * 100
		}
		
		files = append(files, FileInfo{
			Hash:            file.FileHash,
			URL:             file.OriginalURL,
			Filename:        file.Filename,
			Size:            file.FileSize,
			SizeHuman:       formatBytes(file.FileSize),
			Status:          file.DownloadStatus,
			Downloaded:      file.DownloadedBytes,
			DownloadedHuman: formatBytes(file.DownloadedBytes),
			Progress:        progress,
			CreatedAt:       file.CreatedAt,
			LastAccessed:    file.LastAccessedAt,
		})
	}
	
	return files
}

// formatDuration formats duration in human-readable format
func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60
	
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, minutes, seconds)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// formatBytes formats bytes in human-readable format
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
