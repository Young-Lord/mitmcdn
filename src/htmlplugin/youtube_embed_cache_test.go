package htmlplugin

import (
	"regexp"
	"testing"
)

func TestYouTubePluginURLMatch(t *testing.T) {
	plugin := &YouTubeEmbedCachePlugin{
		urlPatterns: []*regexp.Regexp{
			regexp.MustCompile(`^https://example\.com/.*$`),
		},
	}

	if !plugin.matchesURL("https://example.com/page") {
		t.Fatal("expected URL to match")
	}

	if plugin.matchesURL("https://other.com/page") {
		t.Fatal("expected URL not to match")
	}
}
