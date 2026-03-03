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
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/ambientlabscomputing/hyphae/internal/service"
	"github.com/ambientlabscomputing/hyphae/internal/utils"
	"github.com/hashicorp/yamux"
)

const upgradeHeader = "tunnel"

// Server is the mTLS tunnel listener.
type Server struct {
	port    string
	caCert  string
	tlsCert string
	tlsKey  string
	svc     service.Service
}

// Config holds the minimal settings the tunnel server needs.
type Config struct {
	Port    string // e.g. "9090"
	CACert  string // path to platform CA PEM file
	TLSCert string // path to tunnel server certificate PEM file
	TLSKey  string // path to tunnel server private key PEM file
}

// NewTunnelServer creates a tunnel listener.
func NewTunnelServer(cfg Config, svc service.Service) *Server {
	return &Server{port: cfg.Port, caCert: cfg.CACert, tlsCert: cfg.TLSCert, tlsKey: cfg.TLSKey, svc: svc}
}

// Listen starts accepting mTLS connections on the configured port.
// It blocks until ctx is cancelled.
func (s *Server) Listen(ctx context.Context) error {
	logger := utils.GetLogger(ctx)

	if s.caCert == "" {
		logger.Warn("TunnelServer: ca_cert not configured — tunnel listener disabled")
		<-ctx.Done()
		return nil
	}

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
		// Try to load from file first
		caPEM, err := os.ReadFile(s.caCert)
		if err == nil {
			if !pool.AppendCertsFromPEM(caPEM) {
				return nil, fmt.Errorf("failed to parse CA cert from %s", s.caCert)
			}
		} else if isURLPath(s.caCert) {
			// If the path looks like a URL, try to fetch it
			caPEM, err := fetchCA(s.caCert)
			if err != nil {
				return nil, fmt.Errorf("failed to fetch CA cert from %s: %w", s.caCert, err)
			}
			if !pool.AppendCertsFromPEM(caPEM) {
				return nil, fmt.Errorf("failed to parse fetched CA cert")
			}
		} else {
			// Not a URL and file doesn't exist
			return nil, fmt.Errorf("read CA cert %s: %w", s.caCert, err)
		}
	}

	// Load server certificate and key
	var serverCerts []tls.Certificate
	if s.tlsCert != "" && s.tlsKey != "" {
		cert, err := tls.LoadX509KeyPair(s.tlsCert, s.tlsKey)
		if err != nil {
			return nil, fmt.Errorf("load server cert/key: %w", err)
		}
		serverCerts = []tls.Certificate{cert}
	} else if s.tlsCert != "" || s.tlsKey != "" {
		// One is set but not the other
		return nil, fmt.Errorf("both tls_cert and tls_key must be set or both must be empty")
	}

	return &tls.Config{
		Certificates: serverCerts,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
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

	// Acknowledge the upgrade — MMA expects 101 before starting yamux.
	ack := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: tunnel\r\nConnection: Upgrade\r\n\r\n"
	if _, err := fmt.Fprint(tlsConn, ack); err != nil {
		logger.Warn("Tunnel: failed to send 101", "error", err)
		rawConn.Close()
		return
	}

	// Wrap the upgraded connection in a yamux server session.
	// MMA's side calls yamux.Client after receiving the 101.
	yamuxCfg := yamux.DefaultConfig()
	yamuxCfg.KeepAliveInterval = 30 * time.Second
	session, err := yamux.Server(tlsConn, yamuxCfg)
	if err != nil {
		logger.Error("Tunnel: yamux.Server failed", "lease_id", leaseID, "error", err)
		writeHTTPError(tlsConn, http.StatusInternalServerError, "mux init failed")
		return
	}

	connID, err := s.svc.BindTunnel(ctx, leaseID, session)
	if err != nil {
		logger.Error("Tunnel: BindTunnel failed", "lease_id", leaseID, "error", err)
		session.Close()
		return
	}
	defer s.svc.UnbindTunnel(ctx, connID)

	logger.Info("Tunnel: lease bound", "lease_id", leaseID, "server_id", nodeServerID)

	// Hold the session open by draining inbound streams.
	// AcceptStream returns an error when the session closes (either side).
	// This goroutine must NOT read from tlsConn directly — the proxy
	// opens yamux streams through the session and owns those byte flows.
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			logger.Info("Tunnel: session closed", "lease_id", leaseID, "conn_id", connID, "error", err)
			return
		}
		// We don't expect the node to open streams toward Hyphae.
		go stream.Close()
	}
}

// extractServerID reads the CN from the client certificate as the server ID.
func extractServerID(cert *x509.Certificate) string {
	if cert == nil {
		return "unknown"
	}
	return cert.Subject.CommonName
}

// isURLPath checks if a string looks like an HTTP(S) URL
func isURLPath(path string) bool {
	return len(path) > 0 && (path[0:7] == "http://" || (len(path) > 8 && path[0:8] == "https://"))
}

// fetchCA fetches a CA certificate from an HTTP(S) URL
func fetchCA(url string) ([]byte, error) {
	respClient := &http.Client{
		Timeout: 10 * time.Second,
	}
	resp, err := respClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch CA cert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d fetching CA cert", resp.StatusCode)
	}

	caPEM, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert response: %w", err)
	}

	return caPEM, nil
}

func writeHTTPError(conn net.Conn, code int, msg string) {
	body := []byte(msg)
	resp := fmt.Sprintf(
		"HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s",
		code, http.StatusText(code), len(body), msg,
	)
	fmt.Fprint(conn, resp)
}
