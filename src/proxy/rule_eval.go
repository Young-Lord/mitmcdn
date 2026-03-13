package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"mitmcdn/src/rules"
)

const (
	responseProbeTimeout = 15 * time.Second
	responseBodyLimit    = 64 * 1024
)

func buildRequestData(r *http.Request, targetURL *url.URL) rules.RequestData {
	headers := map[string]string{}
	for key, values := range r.Header {
		if len(values) == 0 {
			continue
		}
		headers[key] = values[0]
		if len(values) > 1 {
			for i := 1; i < len(values); i++ {
				headers[key] += "," + values[i]
			}
		}
	}

	cookies := map[string]string{}
	for _, cookie := range r.Cookies() {
		cookies[cookie.Name] = cookie.Value
	}

	clientIP := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		clientIP = host
	}

	return rules.RequestData{
		Method:   r.Method,
		URL:      targetURL.String(),
		Host:     targetURL.Host,
		Path:     targetURL.Path,
		Scheme:   targetURL.Scheme,
		Query:    targetURL.RawQuery,
		Headers:  headers,
		Cookies:  cookies,
		ClientIP: clientIP,
	}
}

func probeResponseInfo(ctx context.Context, client *http.Client, targetURL *url.URL, headers http.Header) (*rules.ResponseData, error) {
	probeCtx, cancel := context.WithTimeout(ctx, responseProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, targetURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	req.Host = targetURL.Host

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusMethodNotAllowed {
		return probeResponseData(ctx, client, targetURL, headers, 1)
	}

	return buildResponseData(resp, nil), nil
}

func probeResponseData(ctx context.Context, client *http.Client, targetURL *url.URL, headers http.Header, limit int64) (*rules.ResponseData, error) {
	probeCtx, cancel := context.WithTimeout(ctx, responseProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, targetURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	req.Host = targetURL.Host
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", limit-1))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, err
	}

	return buildResponseData(resp, body), nil
}

func buildResponseData(resp *http.Response, body []byte) *rules.ResponseData {
	responseHeaders := map[string]string{}
	for key, values := range resp.Header {
		if len(values) == 0 {
			continue
		}
		responseHeaders[key] = values[0]
		if len(values) > 1 {
			for i := 1; i < len(values); i++ {
				responseHeaders[key] += "," + values[i]
			}
		}
	}

	bodyText := ""
	if len(body) > 0 && utf8.Valid(body) {
		bodyText = string(body)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return &rules.ResponseData{
		Status:        resp.StatusCode,
		ContentType:   contentType,
		ContentLength: resp.ContentLength,
		Headers:       responseHeaders,
		BodyBytes:     body,
		BodyText:      bodyText,
	}
}
