// Package proxy implements the public HTTPS gateway.
//
// The ProxyServer listens on the public HTTPS port with a wildcard TLS cert.
// For each inbound connection it reads the HTTP Host header, looks up the
// corresponding live tunnel connection in the repository, and pipes traffic
// bidirectionally between the client and the tunnel net.Conn.
package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/ambientlabscomputing/hyphae/internal/repository"
	"github.com/ambientlabscomputing/hyphae/internal/utils"
)

// hostPattern matches valid DNS hostnames (letters, digits, dots, hyphens).
var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.\-]*[a-zA-Z0-9])?$`)

// Config holds the minimal settings the proxy needs.
type Config struct {
	Port           string        // e.g. "443"
	TLSCert        string        // path to wildcard fullchain PEM
	TLSKey         string        // path to wildcard private key PEM
	MaxConnections int           // max concurrent connections; 0 = no limit
	ReadTimeout    time.Duration // deadline for reading the initial HTTP request
	WriteTimeout   time.Duration // deadline for writing the initial response/error
	IdleTimeout    time.Duration // max idle time during bidirectional copy
}

// Server is the public HTTPS reverse proxy.
type Server struct {
	cfg  Config
	repo repository.Repository
	sem  chan struct{} // counting semaphore; nil when MaxConnections == 0
}

// NewProxyServer creates a public HTTPS proxy.
func NewProxyServer(cfg Config, repo repository.Repository) *Server {
	s := &Server{cfg: cfg, repo: repo}
	if cfg.MaxConnections > 0 {
		s.sem = make(chan struct{}, cfg.MaxConnections)
	}
	return s
}

// Listen starts accepting HTTPS connections. Blocks until ctx is cancelled.
func (s *Server) Listen(ctx context.Context) error {
	logger := utils.GetLogger(ctx)

	var ln net.Listener

	if s.cfg.TLSCert == "" || s.cfg.TLSKey == "" {
		// Dev mode: plain HTTP fallback when no TLS cert/key configured
		logger.Warn("ProxyServer: TLS cert/key not configured — falling back to plain HTTP (dev mode)")
		var listenErr error
		ln, listenErr = net.Listen("tcp", ":"+s.cfg.Port)
		if listenErr != nil {
			return fmt.Errorf("proxy: listen on :%s: %w", s.cfg.Port, listenErr)
		}
	} else {
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey)
		if err != nil {
			return fmt.Errorf("proxy: load TLS key pair: %w", err)
		}

		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
			// Explicit cipher suite preference — only PFS ciphers.
			// Go 1.17+ ignores CipherSuites for TLS 1.3 (they are fixed),
			// but keeping the list here documents intent and guards TLS 1.2 fallback
			// if MinVersion is ever lowered.
			CipherSuites: []uint16{
				tls.TLS_AES_128_GCM_SHA256,
				tls.TLS_AES_256_GCM_SHA384,
				tls.TLS_CHACHA20_POLY1305_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			},
		}

		var listenErr error
		ln, listenErr = tls.Listen("tcp", ":"+s.cfg.Port, tlsCfg)
		if listenErr != nil {
			return fmt.Errorf("proxy: listen on :%s: %w", s.cfg.Port, listenErr)
		}
	}
	defer ln.Close()

	logger.Info("Public gateway started", "port", s.cfg.Port, "tls", s.cfg.TLSCert != "")

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				logger.Warn("Proxy accept error", "error", err)
				continue
			}
		}

		// Enforce max concurrent connection limit.
		if s.sem != nil {
			select {
			case s.sem <- struct{}{}:
				// acquired
			default:
				// Semaphore full — reject immediately.
				writeProxyError(conn, http.StatusServiceUnavailable, "server busy")
				conn.Close()
				logger.Warn("Proxy: connection limit reached, rejecting connection", "remote", conn.RemoteAddr())
				continue
			}
		}

		go func() {
			if s.sem != nil {
				defer func() { <-s.sem }()
			}
			s.handleConn(ctx, conn)
		}()
	}
}

func (s *Server) handleConn(ctx context.Context, clientConn net.Conn) {
	defer clientConn.Close()
	logger := utils.GetLogger(ctx)

	// Apply initial read deadline to prevent Slowloris-style attacks.
	if s.cfg.ReadTimeout > 0 {
		if err := clientConn.SetReadDeadline(time.Now().Add(s.cfg.ReadTimeout)); err != nil {
			logger.Warn("Proxy: failed to set read deadline", "error", err)
			return
		}
	}

	br := bufio.NewReader(clientConn)
	req, err := http.ReadRequest(br)
	if err != nil {
		// Don't log every EOF — this is normal for port scanners / probes.
		if err != io.EOF {
			logger.Warn("Proxy: failed to read request", "error", err)
		}
		return
	}

	// Clear the read deadline — the yamux session handles keepalive from here.
	if s.cfg.ReadTimeout > 0 {
		_ = clientConn.SetReadDeadline(time.Time{})
	}

	host := req.Host
	if host == "" {
		writeProxyError(clientConn, http.StatusBadRequest, "missing Host header")
		return
	}

	// Strip port if present (e.g., "example.com:443" → "example.com")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	// Validate host header to reject path-traversal attempts and malformed values.
	if len(host) > 253 || !hostPattern.MatchString(host) {
		writeProxyError(clientConn, http.StatusBadRequest, "invalid Host header")
		return
	}

	session, err := s.repo.GetSessionByHostname(ctx, host)
	if err != nil {
		logger.Warn("Proxy: no tunnel for host", "host", host, "error", err)
		writeProxyError(clientConn, http.StatusServiceUnavailable, "no active tunnel for host")
		return
	}

	// Open a dedicated yamux stream for this request.
	// Each request gets its own isolated byte stream, so concurrent
	// requests to the same hostname cannot interleave each other's bytes.
	stream, err := session.Open()
	if err != nil {
		logger.Warn("Proxy: failed to open yamux stream", "host", host, "error", err)
		writeProxyError(clientConn, http.StatusBadGateway, "tunnel stream unavailable")
		return
	}
	defer stream.Close()

	if err := req.Write(stream); err != nil {
		logger.Warn("Proxy: failed to forward request", "host", host, "error", err)
		return
	}

	// Apply idle timeout: if neither direction transfers data within IdleTimeout,
	// close the connection to free resources.
	idleReset := func() {
		if s.cfg.IdleTimeout > 0 {
			_ = clientConn.SetDeadline(time.Now().Add(s.cfg.IdleTimeout))
			_ = stream.SetDeadline(time.Now().Add(s.cfg.IdleTimeout))
		}
	}
	idleReset()

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(stream, br) //nolint:errcheck
		done <- struct{}{}
	}()
	go func() {
		io.Copy(clientConn, stream) //nolint:errcheck
		done <- struct{}{}
	}()
	<-done
}

// securityHeaders writes common security headers to HTTP error responses.
const securityHeaders = "Strict-Transport-Security: max-age=63072000; includeSubDomains\r\n" +
	"X-Content-Type-Options: nosniff\r\n" +
	"X-Frame-Options: DENY\r\n" +
	"Cache-Control: no-store\r\n"

func writeProxyError(conn net.Conn, code int, msg string) {
	body := []byte(msg)
	resp := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n%s\r\n%s",
		code, http.StatusText(code), len(body), securityHeaders, msg,
	)
	fmt.Fprint(conn, resp) //nolint:errcheck
}
