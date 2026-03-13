package proxy

import (
	"net/http"
	"net/url"

	"mitmcdn/src/rules"
)

func (p *MITMProxy) evaluateCacheRule(r *http.Request, targetURL *url.URL) (*rules.Decision, bool, error) {
	if p.rulesEngine == nil || !p.rulesEngine.HasRules() {
		return nil, false, nil
	}

	reqData := buildRequestData(r, targetURL)
	client := p.downloadSched.HTTPClient()

	var respInfo *rules.ResponseData
	var respData *rules.ResponseData
	fetcher := func(scope rules.Scope) (*rules.ResponseData, error) {
		switch scope {
		case rules.ScopeResponseInfo:
			if respData != nil {
				return respData, nil
			}
			if respInfo != nil {
				return respInfo, nil
			}
			fetched, err := probeResponseInfo(r.Context(), client, targetURL, r.Header)
			if err != nil {
				return nil, err
			}
			respInfo = fetched
			return fetched, nil
		case rules.ScopeResponseData:
			if respData != nil {
				return respData, nil
			}
			fetched, err := probeResponseData(r.Context(), client, targetURL, r.Header, responseBodyLimit)
			if err != nil {
				return nil, err
			}
			respData = fetched
			return fetched, nil
		default:
			return nil, nil
		}
	}

	return p.rulesEngine.Evaluate(reqData, fetcher)
}

func (p *HTTPReverseProxy) evaluateCacheRule(r *http.Request, targetURL *url.URL) (*rules.Decision, bool, error) {
	if p.rulesEngine == nil || !p.rulesEngine.HasRules() {
		return nil, false, nil
	}

	reqData := buildRequestData(r, targetURL)
	client := p.downloadSched.HTTPClient()

	var respInfo *rules.ResponseData
	var respData *rules.ResponseData
	fetcher := func(scope rules.Scope) (*rules.ResponseData, error) {
		switch scope {
		case rules.ScopeResponseInfo:
			if respData != nil {
				return respData, nil
			}
			if respInfo != nil {
				return respInfo, nil
			}
			fetched, err := probeResponseInfo(r.Context(), client, targetURL, r.Header)
			if err != nil {
				return nil, err
			}
			respInfo = fetched
			return fetched, nil
		case rules.ScopeResponseData:
			if respData != nil {
				return respData, nil
			}
			fetched, err := probeResponseData(r.Context(), client, targetURL, r.Header, responseBodyLimit)
			if err != nil {
				return nil, err
			}
			respData = fetched
			return fetched, nil
		default:
			return nil, nil
		}
	}

	return p.rulesEngine.Evaluate(reqData, fetcher)
}
