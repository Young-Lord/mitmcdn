package proxy

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/armon/go-socks5"
	"mitmcdn/src/cache"
	"mitmcdn/src/config"
	"mitmcdn/src/download"
)

type SOCKS5Proxy struct {
	config        *config.Config
	cacheManager  *cache.Manager
	downloadSched *download.Scheduler
	mitmProxy     *MITMProxy
	server        *socks5.Server
}

func NewSOCKS5Proxy(cfg *config.Config, cacheMgr *cache.Manager, sched *download.Scheduler, mitm *MITMProxy) (*SOCKS5Proxy, error) {
	// Create custom resolver that checks CDN rules
	resolver := &CDNResolver{
		config: cfg,
	}

	// Create custom dialer that intercepts CDN connections
	dialer := &CDNDialer{
		config:        cfg,
		cacheManager:  cacheMgr,
		downloadSched: sched,
		mitmProxy:     mitm,
	}

	conf := &socks5.Config{
		Resolver: resolver,
		Dial:     dialer.Dial,
	}

	server, err := socks5.New(conf)
	if err != nil {
		return nil, err
	}

	return &SOCKS5Proxy{
		config:        cfg,
		cacheManager:  cacheMgr,
		downloadSched: sched,
		mitmProxy:     mitm,
		server:        server,
	}, nil
}

func (p *SOCKS5Proxy) ListenAndServe(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}

		go func() {
			defer conn.Close()
			p.server.ServeConn(conn)
		}()
	}
}

// CDNResolver resolves addresses, checking if they match CDN rules
type CDNResolver struct {
	config *config.Config
}

func (r *CDNResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	// Check if this is a CDN domain we should intercept
	for _, rule := range r.config.CDNRules {
		if strings.Contains(name, rule.Domain) {
			// Return a special marker IP or resolve normally
			// For now, resolve normally but mark for interception
		}
	}

	// Default: resolve using system resolver
	ips, err := net.LookupIP(name)
	if err != nil {
		return ctx, nil, err
	}
	if len(ips) == 0 {
		return ctx, nil, fmt.Errorf("no IPs found for %s", name)
	}

	return ctx, ips[0], nil
}

// CDNDialer dials connections, intercepting CDN traffic
type CDNDialer struct {
	config        *config.Config
	cacheManager  *cache.Manager
	downloadSched *download.Scheduler
	mitmProxy     *MITMProxy
}

func (d *CDNDialer) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	// Extract host from address
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	// Check if this is a CDN we should intercept
	shouldIntercept := false
	for _, rule := range d.config.CDNRules {
		if strings.Contains(host, rule.Domain) {
			shouldIntercept = true
			break
		}
	}

	if shouldIntercept {
		// Create a connection that will be handled by MITM proxy
		// For SOCKS5, we need to handle this differently
		// This is a simplified version - in production, you'd need to
		// establish the connection and then hand it off to the MITM handler
	}

	// Default: dial normally (or through upstream proxy)
	if d.config.UpstreamProxy != "" {
		// Parse and use upstream proxy
		// TODO: Implement upstream proxy dialing
	}

	return net.Dial(network, addr)
}
