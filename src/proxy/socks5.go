package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"mitmcdn/src/cache"
	"mitmcdn/src/config"
	"mitmcdn/src/download"

	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

type SOCKS5Proxy struct {
	config        *config.Config
	cacheManager  *cache.Manager
	downloadSched *download.Scheduler
	mitmProxy     *MITMProxy
	server        *socks5.Server
}

func NewSOCKS5Proxy(cfg *config.Config, cacheMgr *cache.Manager, sched *download.Scheduler, mitm *MITMProxy) (*SOCKS5Proxy, error) {
	proxy := &SOCKS5Proxy{
		config:        cfg,
		cacheManager:  cacheMgr,
		downloadSched: sched,
		mitmProxy:     mitm,
	}

	// Create custom resolver that checks CDN rules
	resolver := &CDNResolver{
		config: cfg,
	}

	server := socks5.NewServer(
		socks5.WithResolver(resolver),
		socks5.WithConnectHandle(proxy.handleConnect),
	)

	proxy.server = server
	return proxy, nil
}

func (p *SOCKS5Proxy) ListenAndServe(addr string) error {
	return p.server.ListenAndServe("tcp", addr)
}

// Serve serves connections from the given listener
func (p *SOCKS5Proxy) Serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
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
	if r.config == nil {
		return ctx, nil, fmt.Errorf("resolver config is nil")
	}

	ips, err := net.DefaultResolver.LookupIPAddr(ctx, name)
	if err != nil {
		return ctx, nil, err
	}
	if len(ips) == 0 {
		return ctx, nil, fmt.Errorf("no IPs found for %s", name)
	}

	return ctx, ips[0].IP, nil
}

// socksBufferedConn ensures we consume already-buffered data in request.Reader
// before reading directly from the underlying TCP connection.
type socksBufferedConn struct {
	net.Conn
	reader io.Reader
}

func (c *socksBufferedConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

func (p *SOCKS5Proxy) handleConnect(ctx context.Context, writer io.Writer, request *socks5.Request) error {
	host, port, err := extractSOCKS5Target(request)
	if err != nil {
		if sendErr := socks5.SendReply(writer, statute.RepAddrTypeNotSupported, nil); sendErr != nil {
			return fmt.Errorf("failed to send reply: %w", sendErr)
		}
		return err
	}

	if p.mitmProxy != nil && p.mitmProxy.shouldIntercept(host) {
		return p.handleInterceptedConnection(writer, request, host, port)
	}

	return p.forwardConnect(ctx, writer, request)
}

func (p *SOCKS5Proxy) handleInterceptedConnection(writer io.Writer, request *socks5.Request, host string, port int) error {
	clientConn, ok := writer.(net.Conn)
	if !ok {
		return fmt.Errorf("writer does not implement net.Conn")
	}

	if err := socks5.SendReply(writer, statute.RepSuccess, clientConn.LocalAddr()); err != nil {
		return fmt.Errorf("failed to send SOCKS5 success reply: %w", err)
	}

	bufferedConn := &socksBufferedConn{
		Conn:   clientConn,
		reader: request.Reader,
	}

	peekedConn := &peekConn{Conn: bufferedConn, peeked: false}
	protocol, err := detectProtocol(peekedConn)
	if err != nil {
		return err
	}

	scheme := "http"
	streamConn := net.Conn(peekedConn)
	if protocol == "https" {
		scheme = "https"

		cert, err := p.mitmProxy.getCertificate(host)
		if err != nil {
			return fmt.Errorf("failed to generate certificate for %s: %w", host, err)
		}

		tlsConn := tls.Server(peekedConn, &tls.Config{
			Certificates: []tls.Certificate{*cert},
		})
		if err := tlsConn.Handshake(); err != nil {
			return err
		}
		streamConn = tlsConn
	}

	return p.handleMITMConnection(streamConn, scheme, host, port)
}

func (p *SOCKS5Proxy) handleMITMConnection(conn net.Conn, scheme, host string, port int) error {
	br := bufio.NewReader(conn)

	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			if isIgnorableProxyError(err) {
				return nil
			}
			return err
		}

		normalizeSOCKS5Request(req, scheme, host, port)

		w := &tlsResponseWriter{
			conn:   conn,
			header: make(http.Header),
			writer: bufio.NewWriter(conn),
		}

		p.mitmProxy.processRequestWithWriter(req, w)

		if req.Body != nil {
			req.Body.Close()
		}

		if err := w.Close(); err != nil {
			return err
		}
		w.Flush()

		if req.Close || strings.EqualFold(req.Header.Get("Connection"), "close") || req.ProtoMajor < 1 || (req.ProtoMajor == 1 && req.ProtoMinor == 0) {
			return nil
		}
	}
}

func normalizeSOCKS5Request(req *http.Request, scheme, host string, port int) {
	authority := formatTargetAuthority(host, port, scheme)

	if req.URL == nil {
		req.URL = &url.URL{}
	}
	req.URL.Scheme = scheme
	req.URL.Host = authority
	if req.Host == "" {
		req.Host = authority
	}
	if req.URL.Path == "" {
		req.URL.Path = "/"
	}
}

func formatTargetAuthority(host string, port int, scheme string) string {
	normalizedHost := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	defaultPort := 0
	switch scheme {
	case "http":
		defaultPort = 80
	case "https":
		defaultPort = 443
	}

	if port != 0 && port != defaultPort {
		return net.JoinHostPort(normalizedHost, strconv.Itoa(port))
	}

	if ip := net.ParseIP(normalizedHost); ip != nil && ip.To4() == nil {
		return "[" + normalizedHost + "]"
	}

	return normalizedHost
}

func extractSOCKS5Target(request *socks5.Request) (string, int, error) {
	var host string
	port := 0

	if request.RawDestAddr != nil {
		host = request.RawDestAddr.FQDN
		if host == "" && request.RawDestAddr.IP != nil {
			host = request.RawDestAddr.IP.String()
		}
		port = request.RawDestAddr.Port
	}

	if request.DestAddr != nil {
		if host == "" {
			host = request.DestAddr.FQDN
			if host == "" && request.DestAddr.IP != nil {
				host = request.DestAddr.IP.String()
			}
		}
		if port == 0 {
			port = request.DestAddr.Port
		}
	}

	if host == "" || port == 0 {
		return "", 0, fmt.Errorf("invalid SOCKS5 target address")
	}

	return host, port, nil
}

func (p *SOCKS5Proxy) forwardConnect(ctx context.Context, writer io.Writer, request *socks5.Request) error {
	targetConn, err := (&net.Dialer{}).DialContext(ctx, "tcp", request.DestAddr.String())
	if err != nil {
		reply := mapDialErrorToReply(err)
		if sendErr := socks5.SendReply(writer, reply, nil); sendErr != nil {
			return fmt.Errorf("failed to send reply: %w", sendErr)
		}
		return fmt.Errorf("connect to %v failed: %w", request.RawDestAddr, err)
	}
	defer targetConn.Close()

	if err := socks5.SendReply(writer, statute.RepSuccess, targetConn.LocalAddr()); err != nil {
		return fmt.Errorf("failed to send success reply: %w", err)
	}

	errCh := make(chan error, 2)
	go func() { errCh <- proxyConn(targetConn, request.Reader) }()
	go func() { errCh <- proxyConn(writer, targetConn) }()

	for i := 0; i < 2; i++ {
		err := <-errCh
		if err != nil && !isIgnorableProxyError(err) {
			return err
		}
	}

	return nil
}

func mapDialErrorToReply(err error) uint8 {
	msg := err.Error()
	if strings.Contains(msg, "refused") {
		return statute.RepConnectionRefused
	}
	if strings.Contains(msg, "network is unreachable") {
		return statute.RepNetworkUnreachable
	}
	return statute.RepHostUnreachable
}

func proxyConn(dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)
	if tcpConn, ok := dst.(interface{ CloseWrite() error }); ok {
		tcpConn.CloseWrite()
	}
	return err
}

func isIgnorableProxyError(err error) bool {
	if err == nil {
		return true
	}

	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	return strings.Contains(err.Error(), "use of closed network connection")
}
