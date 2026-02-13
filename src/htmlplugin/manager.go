package htmlplugin

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mitmcdn/src/cache"
	"mitmcdn/src/download"
)

type HTMLPlugin interface {
	Name() string
	Process(pageURL, html string) (string, bool, error)
}

type Manager struct {
	plugins []HTMLPlugin
}

func NewManager(pluginsDir, configsDir string, cacheMgr *cache.Manager, sched *download.Scheduler) (*Manager, error) {
	manager := &Manager{plugins: make([]HTMLPlugin, 0)}

	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return manager, nil
		}
		return nil, fmt.Errorf("failed to read plugins directory: %w", err)
	}

	pluginNames := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".ts") {
			pluginNames = append(pluginNames, strings.TrimSuffix(entry.Name(), ".ts"))
		}
	}
	sort.Strings(pluginNames)

	for _, pluginName := range pluginNames {
		switch pluginName {
		case "youtube_embed_cache":
			cfgPath := filepath.Join(configsDir, pluginName, "config.toml")
			plugin, err := NewYouTubeEmbedCachePlugin(cfgPath, cacheMgr, sched)
			if err != nil {
				return nil, fmt.Errorf("failed to load plugin %s: %w", pluginName, err)
			}
			manager.plugins = append(manager.plugins, plugin)
		}
	}

	return manager, nil
}

func (m *Manager) ModifyResponse(resp *http.Response) error {
	if m == nil || len(m.plugins) == 0 {
		return nil
	}
	if resp == nil || resp.Request == nil {
		return nil
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "text/html") {
		return nil
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}
	resp.Body.Close()

	originalHTML := string(bodyBytes)
	modifiedHTML, changed, err := m.Apply(resp.Request.URL.String(), originalHTML)
	if err != nil {
		return err
	}
	if !changed {
		resp.Body = io.NopCloser(strings.NewReader(originalHTML))
		resp.ContentLength = int64(len(originalHTML))
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(originalHTML)))
		return nil
	}

	resp.Body = io.NopCloser(strings.NewReader(modifiedHTML))
	resp.ContentLength = int64(len(modifiedHTML))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(modifiedHTML)))
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Transfer-Encoding")

	return nil
}

func (m *Manager) Apply(pageURL, html string) (string, bool, error) {
	if m == nil || len(m.plugins) == 0 {
		return html, false, nil
	}

	current := html
	changed := false

	for _, plugin := range m.plugins {
		next, pluginChanged, err := plugin.Process(pageURL, current)
		if err != nil {
			return html, false, fmt.Errorf("plugin %s failed: %w", plugin.Name(), err)
		}
		if pluginChanged {
			changed = true
			current = next
		}
	}

	return current, changed, nil
}
