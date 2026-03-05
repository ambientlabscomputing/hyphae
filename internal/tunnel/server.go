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
	"sync"
	"sync/atomic"
	"time"

	"github.com/ambientlabscomputing/hyphae/internal/service"
	"github.com/ambientlabscomputing/hyphae/internal/utils"
	"github.com/hashicorp/yamux"
)

const upgradeHeader = "tunnel"

// Server is the mTLS tunnel listener.
type Server struct {
	port                  string
	caCert                string
	tlsCert               string
	tlsKey                string
	handshakeTimeout      time.Duration
	maxConnectionsPerNode int
	svc                   service.Service

	// totalSem is a counting semaphore that caps the total number of concurrent
	// tunnel sessions. nil means no limit.
	totalSem chan struct{}

	// nodeConns tracks active connection counts per node CN (server_id).
	// Values are *atomic.Int32.
	nodeConns sync.Map
}

// Config holds the minimal settings the tunnel server needs.
type Config struct {
	Port                  string        // e.g. "9090"
	CACert                string        // path to platform CA PEM file (file path only)
	TLSCert               string        // path to tunnel server certificate PEM file
	TLSKey                string        // path to tunnel server private key PEM file
	HandshakeTimeout      time.Duration // deadline for TLS + HTTP upgrade; 0 = 10s default
	MaxConnectionsPerNode int           // per-node limit; 0 = 10 default
	MaxTotalConnections   int           // total session limit; 0 = 1000 default
}

// NewTunnelServer creates a tunnel listener.
func NewTunnelServer(cfg Config, svc service.Service) *Server {
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.MaxConnectionsPerNode == 0 {
		cfg.MaxConnectionsPerNode = 10
	}

	s := &Server{
		port:                  cfg.Port,
		caCert:                cfg.CACert,
		tlsCert:               cfg.TLSCert,
		tlsKey:                cfg.TLSKey,
		handshakeTimeout:      cfg.HandshakeTimeout,
		maxConnectionsPerNode: cfg.MaxConnectionsPerNode,
		svc:                   svc,
	}
	if cfg.MaxTotalConnections > 0 {
		s.totalSem = make(chan struct{}, cfg.MaxTotalConnections)
	}
	return s
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
		// File-only CA loading — no remote URL fetching in production.
		caPEM, err := os.ReadFile(s.caCert)
		if err != nil {
			return nil, fmt.Errorf("read CA cert %s: %w", s.caCert, err)
		}
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("failed to parse CA cert from %s", s.caCert)
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

	// Set a hard deadline covering both TLS handshake and HTTP upgrade.
	// This prevents slow peers with valid certs from holding goroutines.
	if err := tlsConn.SetDeadline(time.Now().Add(s.handshakeTimeout)); err != nil {
		logger.Warn("Tunnel: failed to set handshake deadline", "error", err)
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

	// Enforce per-node connection limit.
	counter := s.getNodeCounter(nodeServerID)
	current := counter.Add(1)
	if int(current) > s.maxConnectionsPerNode {
		counter.Add(-1)
		logger.Warn("Tunnel: per-node connection limit exceeded",
			"server_id", nodeServerID,
			"limit", s.maxConnectionsPerNode,
		)
		writeHTTPError(tlsConn, http.StatusTooManyRequests, "per-node connection limit exceeded")
		rawConn.Close()
		return
	}
	defer counter.Add(-1)

	// Enforce total connection limit.
	if s.totalSem != nil {
		select {
		case s.totalSem <- struct{}{}:
			defer func() { <-s.totalSem }()
		default:
			logger.Warn("Tunnel: total connection limit reached")
			writeHTTPError(tlsConn, http.StatusServiceUnavailable, "tunnel capacity exceeded")
			rawConn.Close()
			return
		}
	}

	logger.Info("Tunnel: node connected", "server_id", nodeServerID, "remote", rawConn.RemoteAddr())

	// Read the HTTP upgrade request (deadline already set above).
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

	// Validate that the connecting node's cert CN matches the lease's ServerID.
	// This prevents any valid-cert node from hijacking another node's lease.
	lease, err := s.svc.GetLease(ctx, leaseID)
	if err != nil {
		logger.Warn("Tunnel: lease not found", "lease_id", leaseID, "error", err)
		writeHTTPError(tlsConn, http.StatusNotFound, "lease not found")
		rawConn.Close()
		return
	}
	if lease.ServerID != "" && lease.ServerID != nodeServerID {
		logger.Warn("Tunnel: cert CN does not match lease server_id",
			"cert_cn", nodeServerID,
			"lease_server_id", lease.ServerID,
			"lease_id", leaseID,
		)
		writeHTTPError(tlsConn, http.StatusForbidden, "node identity does not match lease")
		rawConn.Close()
		return
	}

	// Clear the deadline before long-lived yamux session begins.
	if err := tlsConn.SetDeadline(time.Time{}); err != nil {
		logger.Warn("Tunnel: failed to clear deadline", "error", err)
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

// getNodeCounter returns (or initialises) the atomic counter for a given node CN.
func (s *Server) getNodeCounter(nodeServerID string) *atomic.Int32 {
	v, _ := s.nodeConns.LoadOrStore(nodeServerID, &atomic.Int32{})
	return v.(*atomic.Int32)
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
	fmt.Fprint(conn, resp) //nolint:errcheck
}
