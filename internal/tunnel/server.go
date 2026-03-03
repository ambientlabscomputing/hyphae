// Package tunnel implements the mTLS reverse-tunnel listener.
//
// MMA nodes open outbound connections to the TunnelServer. Each connection
// sends an HTTP/1.1 Upgrade request carrying the lease ID in the
// X-Lease-ID header. Hyphae validates the node's mTLS client certificate
// (signed by the platform CA), upgrades the connection, and registers the
// raw net.Conn in the lease table so the proxy layer can forward traffic.
package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/ambientlabscomputing/hyphae/internal/service"
	"github.com/ambientlabscomputing/hyphae/internal/utils"
)

const upgradeHeader = "tunnel"

// Server is the mTLS tunnel listener.
type Server struct {
	settings interface {
		TunnelPort() string
		TunnelCACert() string
	}
	port   string
	caCert string
	svc    service.Service
}

// Config holds the minimal settings the tunnel server needs.
type Config struct {
	Port   string // e.g. "9090"
	CACert string // path to platform CA PEM file
}

// NewTunnelServer creates a tunnel listener.
func NewTunnelServer(cfg Config, svc service.Service) *Server {
	return &Server{port: cfg.Port, caCert: cfg.CACert, svc: svc}
}

// Listen starts accepting mTLS connections on the configured port.
// It blocks until ctx is cancelled.
func (s *Server) Listen(ctx context.Context) error {
	logger := utils.GetLogger(ctx)

	tlsCfg, err := s.buildTLSConfig()
	if err != nil {
		return fmt.Errorf("tunnel: build TLS config: %w", err)
	}

	ln, err := tls.Listen("tcp", ":"+s.port, tlsCfg)
	if err != nil {
		return fmt.Errorf("tunnel: listen on :%s: %w", s.port, err)
	}
	defer ln.Close()

	logger.Info("Tunnel listener started", "port", s.port)

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
				logger.Warn("Tunnel accept error", "error", err)
				continue
			}
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) buildTLSConfig() (*tls.Config, error) {
	pool := x509.NewCertPool()
	if s.caCert != "" {
		caPEM, err := os.ReadFile(s.caCert)
		if err != nil {
			return nil, fmt.Errorf("read CA cert %s: %w", s.caCert, err)
		}
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("failed to parse CA cert from %s", s.caCert)
		}
	}

	return &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  pool,
		MinVersion: tls.VersionTLS13,
	}, nil
}

func (s *Server) handleConn(ctx context.Context, rawConn net.Conn) {
	logger := utils.GetLogger(ctx)

	tlsConn, ok := rawConn.(*tls.Conn)
	if !ok {
		logger.Warn("Tunnel: received non-TLS connection")
		rawConn.Close()
		return
	}

	// Complete the TLS handshake so we can inspect the client certificate.
	if err := tlsConn.Handshake(); err != nil {
		logger.Warn("Tunnel TLS handshake failed", "error", err)
		rawConn.Close()
		return
	}

	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		logger.Warn("Tunnel: no client certificate presented")
		rawConn.Close()
		return
	}

	nodeServerID := extractServerID(state.PeerCertificates[0])
	logger.Info("Tunnel: node connected", "server_id", nodeServerID, "remote", rawConn.RemoteAddr())

	// Read the HTTP upgrade request.
	br := bufio.NewReader(tlsConn)
	req, err := http.ReadRequest(br)
	if err != nil {
		logger.Warn("Tunnel: failed to read HTTP request", "error", err)
		rawConn.Close()
		return
	}

	leaseID := req.Header.Get("X-Lease-ID")
	if leaseID == "" {
		writeHTTPError(tlsConn, http.StatusBadRequest, "missing X-Lease-ID header")
		rawConn.Close()
		return
	}

	if req.Header.Get("Upgrade") != upgradeHeader {
		writeHTTPError(tlsConn, http.StatusUpgradeRequired, "must upgrade to tunnel")
		rawConn.Close()
		return
	}

	// Bind the raw connection to the lease.
	if err := s.svc.BindTunnel(ctx, leaseID, tlsConn); err != nil {
		logger.Error("Tunnel: BindTunnel failed", "lease_id", leaseID, "error", err)
		writeHTTPError(tlsConn, http.StatusBadRequest, err.Error())
		rawConn.Close()
		return
	}

	// Acknowledge the upgrade.
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: tunnel\r\nConnection: Upgrade\r\n\r\n"
	if _, err := fmt.Fprint(tlsConn, resp); err != nil {
		logger.Warn("Tunnel: failed to send 101", "error", err)
		return
	}

	logger.Info("Tunnel: lease bound", "lease_id", leaseID, "server_id", nodeServerID)

	// Hold the connection open — net.Conn lifetime is managed by the repository.
	// When revoked or the peer closes, Read will return an error.
	buf := make([]byte, 1)
	for {
		if _, err := tlsConn.Read(buf); err != nil {
			logger.Info("Tunnel: connection closed", "lease_id", leaseID, "error", err)
			return
		}
	}
}

// extractServerID reads the CN from the client certificate as the server ID.
func extractServerID(cert *x509.Certificate) string {
	if cert == nil {
		return "unknown"
	}
	return cert.Subject.CommonName
}

func writeHTTPError(conn net.Conn, code int, msg string) {
	body := []byte(msg)
	resp := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s",
		code, http.StatusText(code), len(body), msg,
	)
	fmt.Fprint(conn, resp)
}
