package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"mitmcdn/src/cache"
	"mitmcdn/src/config"
	"mitmcdn/src/download"
	"mitmcdn/src/htmlplugin"
)

type HTTPReverseProxy struct {
	config        *config.Config
	cacheManager  *cache.Manager
	downloadSched *download.Scheduler
	mitmProxy     *MITMProxy
	htmlPlugins   *htmlplugin.Manager
}

func NewHTTPReverseProxy(cfg *config.Config, cacheMgr *cache.Manager, sched *download.Scheduler, mitm *MITMProxy, htmlPlugins *htmlplugin.Manager) *HTTPReverseProxy {
	return &HTTPReverseProxy{
		config:        cfg,
		cacheManager:  cacheMgr,
		downloadSched: sched,
		mitmProxy:     mitm,
		htmlPlugins:   htmlPlugins,
	}
}

// ServeHTTP handles requests in URL path format: /https://origin.cdn.com/file.exe
func (p *HTTPReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract target URL from path
	// Path format: /https://origin.cdn.com/file.exe or /http://...
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		http.Error(w, "No target URL provided in path", http.StatusBadRequest)
		return
	}

	// Parse the target URL (may contain query parameters like ?a=33)
	// Note: When path is "https://httpbin.org/get?a=33", url.Parse will correctly extract the query
	targetURL, err := url.Parse(path)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid target URL: %v", err), http.StatusBadRequest)
		return
	}

	// Merge query parameters: target URL params take precedence, append original request params if not conflicting
	if r.URL.RawQuery != "" {
		originalParams, _ := url.ParseQuery(r.URL.RawQuery)
		targetParams, _ := url.ParseQuery(targetURL.RawQuery)
		// Append original params that don't exist in target params
		for key, values := range originalParams {
			if _, exists := targetParams[key]; !exists {
				targetParams[key] = values
			}
		}
		if len(targetParams) > 0 {
			targetURL.RawQuery = targetParams.Encode()
		}
	}

	// Check if URL is invalid (e.g., https:///favicon.ico)
	if targetURL.Host == "" || targetURL.Scheme == "" || strings.HasPrefix(targetURL.String(), "https:///") || strings.HasPrefix(targetURL.String(), "http:///") {
		// Try to serve from assets directory
		if p.serveFromAssets(w, r) {
			return
		}
		// If not found in assets, return 404
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// Check if this matches any CDN rule
	rule := p.findMatchingRule(targetURL.String(), targetURL.Host)
	if rule == nil {
		// Not a CDN file, forward to upstream
		p.forwardRequest(w, r, targetURL)
		return
	}

	// Extract cookie from request
	cookie := r.Header.Get("Cookie")

	// Extract filename
	filename := p.extractFilename(targetURL.Path)

	// Get or create file entry
	file, err := p.cacheManager.GetOrCreateFile(
		targetURL.String(),
		cookie,
		filename,
		rule.DedupStrategy,
	)
	if err != nil {
		logErrorWithStack(err, "Failed to get or create file: %s", targetURL.String())
		http.Error(w, fmt.Sprintf("Failed to get file: %v", err), http.StatusInternalServerError)
		return
	}

	// Check if file is complete
	if file.DownloadStatus == "complete" {
		// Serve from cache
		http.ServeFile(w, r, file.SavedPath)
		return
	}

	// Stream file (will trigger download if needed)
	if err := p.downloadSched.StreamFile(file, w, r); err != nil {
		logErrorWithStack(err, "Failed to stream file: %s", targetURL.String())
		// Error already sent to client by StreamFile
	}
}

// findMatchingRule finds matching CDN rule
func (p *HTTPReverseProxy) findMatchingRule(urlStr, host string) *config.CDNRule {
	for _, rule := range p.config.CDNRules {
		if cache.MatchCDNRule(urlStr, rule.Domain, rule.MatchPattern) {
			return &rule
		}
	}
	return nil
}

// extractFilename extracts filename from URL path
func (p *HTTPReverseProxy) extractFilename(path string) string {
	parts := strings.Split(path, "/")
	filename := parts[len(parts)-1]
	if idx := strings.Index(filename, "?"); idx != -1 {
		filename = filename[:idx]
	}
	return filename
}

// serveFromAssets tries to serve file from assets directory
func (p *HTTPReverseProxy) serveFromAssets(w http.ResponseWriter, r *http.Request) bool {
	if p.config.AssetsDir == "" {
		return false
	}

	// Extract filename from path
	filename := p.extractFilename(r.URL.Path)
	if filename == "" || filename == "/" {
		filename = "index.html"
	}

	// Build file path
	filePath := filepath.Join(p.config.AssetsDir, filename)

	// Security: ensure file is within assets directory
	absAssetsDir, err := filepath.Abs(p.config.AssetsDir)
	if err != nil {
		return false
	}
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return false
	}
	if !strings.HasPrefix(absFilePath, absAssetsDir) {
		return false
	}

	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return false
	}

	// Serve file
	http.ServeFile(w, r, filePath)
	return true
}

// forwardRequest forwards request to target URL
func (p *HTTPReverseProxy) forwardRequest(w http.ResponseWriter, r *http.Request, targetURL *url.URL) {
	// Check if URL is invalid before forwarding
	if targetURL.Host == "" || targetURL.Scheme == "" {
		// Try to serve from assets directory
		if p.serveFromAssets(w, r) {
			return
		}
		http.Error(w, "Invalid target URL: no host or scheme", http.StatusBadRequest)
		return
	}

	// Create reverse proxy
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Copy all properties from targetURL to preserve query parameters
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.URL.Path = targetURL.Path
			req.URL.RawPath = targetURL.RawPath
			req.URL.RawQuery = targetURL.RawQuery // Preserve query parameters from target URL
			req.URL.Fragment = targetURL.Fragment
			req.Host = targetURL.Host
			req.Header.Del("Accept-Encoding")
		},
		ModifyResponse: func(resp *http.Response) error {
			if p.htmlPlugins == nil {
				return nil
			}
			return p.htmlPlugins.ModifyResponse(resp)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logErrorWithStack(err, "HTTP reverse proxy error: %s %s", r.Method, r.URL.String())
			// Try to serve from assets as fallback
			if p.serveFromAssets(w, r) {
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
		},
	}

	// Configure upstream proxy if needed
	if p.config.UpstreamProxy != "" {
		proxyURL, err := url.Parse(p.config.UpstreamProxy)
		if err == nil {
			proxy.Transport = &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			}
		}
	}

	proxy.ServeHTTP(w, r)
}
