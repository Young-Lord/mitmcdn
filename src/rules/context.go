package rules

import (
	"regexp"
	"strings"
)

type RequestData struct {
	Method   string
	URL      string
	Host     string
	Path     string
	Scheme   string
	Query    string
	Headers  map[string]string
	Cookies  map[string]string
	ClientIP string
}

type ResponseData struct {
	Status        int
	ContentType   string
	ContentLength int64
	Headers       map[string]string
	BodyBytes     []byte
	BodyText      string
}

func baseEnvironment() map[string]any {
	return map[string]any{
		"method":           "",
		"url":              "",
		"host":             "",
		"path":             "",
		"scheme":           "",
		"query":            "",
		"headers":          map[string]string{},
		"cookies":          map[string]string{},
		"client_ip":        "",
		"status":           0,
		"content_type":     "",
		"content_length":   int64(0),
		"response_headers": map[string]string{},
		"body_bytes":       []byte{},
		"body_text":        "",
		"match": func(value, pattern string) bool {
			matched, err := regexp.MatchString(pattern, value)
			if err != nil {
				return false
			}
			return matched
		},
		"contains":  strings.Contains,
		"hasPrefix": strings.HasPrefix,
		"hasSuffix": strings.HasSuffix,
		"lower":     strings.ToLower,
		"upper":     strings.ToUpper,
	}
}

func buildEnvironment(req RequestData, resp *ResponseData) map[string]any {
	base := baseEnvironment()
	base["method"] = req.Method
	base["url"] = req.URL
	base["host"] = req.Host
	base["path"] = req.Path
	base["scheme"] = req.Scheme
	base["query"] = req.Query
	base["headers"] = req.Headers
	base["cookies"] = req.Cookies
	base["client_ip"] = req.ClientIP

	if resp != nil {
		base["status"] = resp.Status
		base["content_type"] = resp.ContentType
		base["content_length"] = resp.ContentLength
		base["response_headers"] = resp.Headers
		base["body_bytes"] = resp.BodyBytes
		base["body_text"] = resp.BodyText
	}

	return base
}
