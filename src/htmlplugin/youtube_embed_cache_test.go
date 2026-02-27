package htmlplugin

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitmcdn/src/cache"
	"mitmcdn/src/database"
	"mitmcdn/src/download"

	"gorm.io/gorm"
)

func setupPluginTestEnv(t *testing.T) (*cache.Manager, *download.Scheduler, *gorm.DB) {
	t.Helper()

	tmpDB, err := os.CreateTemp("", "htmlplugin-*.db")
	if err != nil {
		t.Fatalf("failed to create temp DB: %v", err)
	}
	_ = tmpDB.Close()
	t.Cleanup(func() { _ = os.Remove(tmpDB.Name()) })

	db, err := database.InitDB(tmpDB.Name())
	if err != nil {
		t.Fatalf("failed to init DB: %v", err)
	}

	cacheMgr, err := cache.NewManager(db, t.TempDir(), 100*1024*1024, 200*1024*1024, time.Hour)
	if err != nil {
		t.Fatalf("failed to create cache manager: %v", err)
	}

	sched, err := download.NewScheduler(cacheMgr, db, "")
	if err != nil {
		t.Fatalf("failed to create scheduler: %v", err)
	}

	return cacheMgr, sched, db
}

func createFakeYTDLPScript(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-yt-dlp.sh")
	content := "#!/bin/sh\nset -eu\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("failed to write fake yt-dlp script: %v", err)
	}
	return path
}

func writePluginConfig(t *testing.T, path string, patterns []string, command []string, priority int) {
	t.Helper()

	quotedPatterns := make([]string, 0, len(patterns))
	for _, p := range patterns {
		quotedPatterns = append(quotedPatterns, fmt.Sprintf("%q", p))
	}

	quotedCommand := make([]string, 0, len(command))
	for _, c := range command {
		quotedCommand = append(quotedCommand, fmt.Sprintf("%q", c))
	}

	content := fmt.Sprintf("url_patterns = [%s]\nyt_dlp_command = [%s]\ndownload_priority = %d\n", strings.Join(quotedPatterns, ", "), strings.Join(quotedCommand, ", "), priority)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write plugin config: %v", err)
	}
}

func waitForStatusByURL(t *testing.T, db *gorm.DB, originalURL string, status string) database.File {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var file database.File
		err := db.Where("original_url = ?", originalURL).First(&file).Error
		if err == nil && file.DownloadStatus == status {
			return file
		}
		time.Sleep(20 * time.Millisecond)
	}

	var file database.File
	_ = db.Where("original_url = ?", originalURL).First(&file).Error
	t.Fatalf("timeout waiting for %s to reach %s, last status=%s", originalURL, status, file.DownloadStatus)
	return database.File{}
}

func TestYouTubeEmbedCachePlugin_ProcessRewritesAndDownloads(t *testing.T) {
	cacheMgr, sched, db := setupPluginTestEnv(t)

	script := createFakeYTDLPScript(t, `out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    out="$2"
    shift 2
    continue
  fi
  shift
done
printf 'cached-video' > "$out"`)

	configPath := filepath.Join(t.TempDir(), "config.toml")
	writePluginConfig(t, configPath, []string{`^https://target\.example/.*$`}, []string{script}, 95)

	plugin, err := NewYouTubeEmbedCachePlugin(configPath, cacheMgr, sched)
	if err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}

	html := `<div><iframe src="https://www.youtube.com/embed/XZnZkASrArc"></iframe></div>` +
		`<section><iframe src="//www.youtube.com/embed/XZnZkASrArc"></iframe></section>` +
		`<p><iframe src="https://www.youtube.com/embed/AbCdEfGhIjk"></iframe></p>`

	modified, changed, err := plugin.Process("https://target.example/page", html)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}
	if !changed {
		t.Fatal("expected html to be changed")
	}

	if strings.Count(modified, `src="//www.youtube.com/embed/XZnZkASrArc"`) != 2 {
		t.Fatalf("expected rewritten iframe for XZnZkASrArc twice, got: %s", modified)
	}
	if !strings.Contains(modified, `src="//www.youtube.com/embed/AbCdEfGhIjk"`) {
		t.Fatalf("expected rewritten iframe for AbCdEfGhIjk, got: %s", modified)
	}

	file1 := waitForStatusByURL(t, db, "yt-dlp://XZnZkASrArc", "complete")
	file2 := waitForStatusByURL(t, db, "yt-dlp://AbCdEfGhIjk", "complete")

	for _, file := range []database.File{file1, file2} {
		data, err := os.ReadFile(file.SavedPath)
		if err != nil {
			t.Fatalf("failed to read cached file %s: %v", file.SavedPath, err)
		}
		if string(data) != "cached-video" {
			t.Fatalf("unexpected cached data for %s: %q", file.OriginalURL, string(data))
		}
	}

	var count int64
	if err := db.Model(&database.File{}).Where("original_url = ?", "yt-dlp://XZnZkASrArc").Count(&count).Error; err != nil {
		t.Fatalf("failed to count dedup records: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected dedup count=1 for XZnZkASrArc, got %d", count)
	}
}

func TestYouTubeEmbedCachePlugin_ProcessSkipsUnmatchedURL(t *testing.T) {
	cacheMgr, sched, db := setupPluginTestEnv(t)

	configPath := filepath.Join(t.TempDir(), "config.toml")
	writePluginConfig(t, configPath, []string{`^https://only\.this\.site/.*$`}, []string{"/bin/true"}, 80)

	plugin, err := NewYouTubeEmbedCachePlugin(configPath, cacheMgr, sched)
	if err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}

	html := `<iframe src="https://www.youtube.com/embed/XZnZkASrArc"></iframe>`
	modified, changed, err := plugin.Process("https://other.site/page", html)
	if err != nil {
		t.Fatalf("process failed: %v", err)
	}
	if changed {
		t.Fatal("expected no change for unmatched URL")
	}
	if modified != html {
		t.Fatalf("html should remain unchanged")
	}

	var count int64
	if err := db.Model(&database.File{}).Count(&count).Error; err != nil {
		t.Fatalf("failed to count files: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no download records, got %d", count)
	}
}

func TestNewYouTubeEmbedCachePlugin_ConfigValidation(t *testing.T) {
	cacheMgr, sched, _ := setupPluginTestEnv(t)

	tests := []struct {
		name    string
		content string
	}{
		{name: "empty patterns", content: "url_patterns=[]\nyt_dlp_command=[\"yt-dlp\"]\n"},
		{name: "invalid regex", content: "url_patterns=[\"([\"]\nyt_dlp_command=[\"yt-dlp\"]\n"},
		{name: "empty command", content: "url_patterns=[\"^https://x\"]\nyt_dlp_command=[]\n"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(cfg, []byte(tc.content), 0644); err != nil {
				t.Fatalf("failed to write config: %v", err)
			}

			if _, err := NewYouTubeEmbedCachePlugin(cfg, cacheMgr, sched); err == nil {
				t.Fatalf("expected config validation error")
			}
		})
	}
}

func TestManager_ModifyResponseHTML(t *testing.T) {
	cacheMgr, sched, db := setupPluginTestEnv(t)

	script := createFakeYTDLPScript(t, `out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    out="$2"
    shift 2
    continue
  fi
  shift
done
printf 'manager-video' > "$out"`)

	pluginsDir := filepath.Join(t.TempDir(), "plugins")
	configsDir := filepath.Join(t.TempDir(), "configs")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("failed to create plugins dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(configsDir, "youtube_embed_cache"), 0755); err != nil {
		t.Fatalf("failed to create configs dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(pluginsDir, "youtube_embed_cache.ts"), []byte("export {}\n"), 0644); err != nil {
		t.Fatalf("failed to write plugin ts file: %v", err)
	}
	writePluginConfig(
		t,
		filepath.Join(configsDir, "youtube_embed_cache", "config.toml"),
		[]string{`^https://manager\.example/.*$`},
		[]string{script},
		90,
	)

	mgr, err := NewManager(pluginsDir, configsDir, cacheMgr, sched)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	reqURL, _ := url.Parse("https://manager.example/page")
	html := `<iframe src="https://www.youtube.com/embed/XZnZkASrArc"></iframe>`
	resp := &http.Response{
		Header: http.Header{
			"Content-Type":     []string{"text/html; charset=utf-8"},
			"Content-Encoding": []string{"gzip"},
		},
		Body:    io.NopCloser(strings.NewReader(html)),
		Request: &http.Request{URL: reqURL},
	}

	if err := mgr.ModifyResponse(resp); err != nil {
		t.Fatalf("modify response failed: %v", err)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed reading modified body: %v", err)
	}
	out := string(body)
	if !strings.Contains(out, `src="//www.youtube.com/embed/XZnZkASrArc"`) {
		t.Fatalf("expected rewritten iframe, got: %s", out)
	}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("content-encoding should be removed")
	}
	if resp.Header.Get("Content-Length") == "" {
		t.Fatalf("content-length should be set")
	}

	_ = waitForStatusByURL(t, db, "yt-dlp://XZnZkASrArc", "complete")
}

func TestManager_ModifyResponseSkipsNonHTML(t *testing.T) {
	mgr := &Manager{plugins: []HTMLPlugin{&YouTubeEmbedCachePlugin{}}}

	reqURL, _ := url.Parse("https://manager.example/page")
	body := `{"ok":true}`
	resp := &http.Response{
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body:    io.NopCloser(strings.NewReader(body)),
		Request: &http.Request{URL: reqURL},
	}

	if err := mgr.ModifyResponse(resp); err != nil {
		t.Fatalf("modify response failed: %v", err)
	}

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed reading response: %v", err)
	}
	if string(out) != body {
		t.Fatalf("non-html response should not change")
	}
}

func TestNewManagerFailsWhenPluginConfigMissing(t *testing.T) {
	cacheMgr, sched, _ := setupPluginTestEnv(t)

	pluginsDir := filepath.Join(t.TempDir(), "plugins")
	configsDir := filepath.Join(t.TempDir(), "configs")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("failed to create plugins dir: %v", err)
	}
	if err := os.MkdirAll(configsDir, 0755); err != nil {
		t.Fatalf("failed to create configs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "youtube_embed_cache.ts"), []byte("export {}\n"), 0644); err != nil {
		t.Fatalf("failed to write plugin ts file: %v", err)
	}

	if _, err := NewManager(pluginsDir, configsDir, cacheMgr, sched); err == nil {
		t.Fatalf("expected error when plugin config is missing")
	}
}
