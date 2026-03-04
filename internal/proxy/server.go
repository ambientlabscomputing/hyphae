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

	"github.com/ambientlabscomputing/hyphae/internal/repository"
	"github.com/ambientlabscomputing/hyphae/internal/utils"
)

// Config holds the minimal settings the proxy needs.
type Config struct {
	Port    string // e.g. "443"
	TLSCert string // path to wildcard fullchain PEM
	TLSKey  string // path to wildcard private key PEM
}

// Server is the public HTTPS reverse proxy.
type Server struct {
	cfg  Config
	repo repository.Repository
}

// NewProxyServer creates a public HTTPS proxy.
func NewProxyServer(cfg Config, repo repository.Repository) *Server {
	return &Server{cfg: cfg, repo: repo}
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
			MinVersion:   tls.VersionTLS12,
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
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, clientConn net.Conn) {
	defer clientConn.Close()
	logger := utils.GetLogger(ctx)

	br := bufio.NewReader(clientConn)
	req, err := http.ReadRequest(br)
	if err != nil {
		logger.Warn("Proxy: failed to read request", "error", err)
		return
	}

	host := req.Host
	if host == "" {
		writeProxyError(clientConn, http.StatusBadRequest, "missing Host header")
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

	done := make(chan struct{}, 2)
	go func() { io.Copy(stream, br); done <- struct{}{} }()
	go func() { io.Copy(clientConn, stream); done <- struct{}{} }()
	<-done
}

func writeProxyError(conn net.Conn, code int, msg string) {
	body := []byte(msg)
	resp := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s",
		code, http.StatusText(code), len(body), msg,
	)
	fmt.Fprint(conn, resp)
}
