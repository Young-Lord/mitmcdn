package htmlplugin

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"mitmcdn/src/cache"
	"mitmcdn/src/download"

	"github.com/pelletier/go-toml/v2"
)

type youTubeEmbedCacheConfig struct {
	URLPatterns      []string `toml:"url_patterns"`
	YTDLPCommand     []string `toml:"yt_dlp_command"`
	DownloadPriority int      `toml:"download_priority"`
}

type YouTubeEmbedCachePlugin struct {
	urlPatterns        []*regexp.Regexp
	youtubeIframeRegex *regexp.Regexp
	cacheManager       *cache.Manager
	downloadSched      *download.Scheduler
	downloadPriority   int
}

func NewYouTubeEmbedCachePlugin(configPath string, cacheMgr *cache.Manager, sched *download.Scheduler) (*YouTubeEmbedCachePlugin, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg youTubeEmbedCacheConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if len(cfg.URLPatterns) == 0 {
		return nil, fmt.Errorf("url_patterns cannot be empty")
	}

	compiledPatterns := make([]*regexp.Regexp, 0, len(cfg.URLPatterns))
	for _, pattern := range cfg.URLPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid url pattern %q: %w", pattern, err)
		}
		compiledPatterns = append(compiledPatterns, re)
	}

	if len(cfg.YTDLPCommand) == 0 {
		return nil, fmt.Errorf("yt_dlp_command cannot be empty")
	}

	sched.ConfigureYTDLPCommand(cfg.YTDLPCommand)

	priority := cfg.DownloadPriority
	if priority == 0 {
		priority = 90
	}

	return &YouTubeEmbedCachePlugin{
		urlPatterns:        compiledPatterns,
		youtubeIframeRegex: regexp.MustCompile(`(?is)<iframe[^>]*\bsrc=["'](?:(?:https?:)?//)?(?:www\.)?youtube\.com/embed/([A-Za-z0-9_-]{6,})[^"']*["'][^>]*>\s*</iframe>`),
		cacheManager:       cacheMgr,
		downloadSched:      sched,
		downloadPriority:   priority,
	}, nil
}

func (p *YouTubeEmbedCachePlugin) Name() string {
	return "youtube_embed_cache"
}

func (p *YouTubeEmbedCachePlugin) Process(pageURL, html string) (string, bool, error) {
	if !p.matchesURL(pageURL) {
		return html, false, nil
	}

	videoIDs := make(map[string]struct{})
	modified := p.youtubeIframeRegex.ReplaceAllStringFunc(html, func(match string) string {
		parts := p.youtubeIframeRegex.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}

		videoID := parts[1]
		videoIDs[videoID] = struct{}{}

		// Check if the video is already cached and complete.
		if p.isVideoCached(videoID) {
			return fmt.Sprintf(`<iframe frameborder="0" allowfullscreen style="border: none;position: absolute;top: 0;left: 0;width: 100%%;height: 100%%;" src="/cache/yt/%s/player"></iframe>`, videoID)
		}

		return fmt.Sprintf(`<iframe frameborder="0" allowfullscreen style="border: none;position: absolute;top: 0;left: 0;width: 100%%;height: 100%%;" src="//www.youtube.com/embed/%s"></iframe>`, videoID)
	})

	if modified == html {
		return html, false, nil
	}

	for videoID := range videoIDs {
		if err := p.ensureVideoCached(videoID); err != nil {
			return html, false, err
		}
	}

	return modified, true, nil
}

func (p *YouTubeEmbedCachePlugin) matchesURL(pageURL string) bool {
	for _, pattern := range p.urlPatterns {
		if pattern.MatchString(pageURL) {
			return true
		}
	}
	return false
}

// isVideoCached returns true if the video has been fully downloaded.
func (p *YouTubeEmbedCachePlugin) isVideoCached(videoID string) bool {
	cacheURL := fmt.Sprintf("yt-dlp://%s", videoID)
	file, err := p.cacheManager.GetOrCreateFile(cacheURL, "", videoID+".mp4", "full_url")
	if err != nil {
		return false
	}
	return file.DownloadStatus == "complete"
}

func (p *YouTubeEmbedCachePlugin) ensureVideoCached(videoID string) error {
	if strings.TrimSpace(videoID) == "" {
		return fmt.Errorf("invalid youtube video id")
	}

	cacheURL := fmt.Sprintf("yt-dlp://%s", videoID)
	file, err := p.cacheManager.GetOrCreateFile(cacheURL, "", videoID+".mp4", "full_url")
	if err != nil {
		return fmt.Errorf("failed to get cache entry for %s: %w", videoID, err)
	}

	if file.DownloadStatus == "complete" || file.DownloadStatus == "downloading" {
		return nil
	}

	if err := p.downloadSched.StartDownload(file, file.OriginalURL, "", p.downloadPriority); err != nil {
		return fmt.Errorf("failed to start yt-dlp download for %s: %w", videoID, err)
	}

	return nil
}
