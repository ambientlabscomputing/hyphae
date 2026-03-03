package sdk_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ambientlabscomputing/hyphae/sdk"
	"github.com/hashicorp/yamux"
)

// ── certificate helpers ────────────────────────────────────────────────────────

// testCerts holds file paths for the in-process test PKI.
type testCerts struct {
	CACertPath     string
	ServerCertPath string
	ServerKeyPath  string
	ClientCertPath string
	ClientKeyPath  string

	caCert     *x509.Certificate
	caKey      *ecdsa.PrivateKey
	serverCert tls.Certificate
	clientCert tls.Certificate
}

// generateTestCerts creates a self-signed CA, a server cert, and a client cert
// all signed by that CA. PEM files are written to t.TempDir().
func generateTestCerts(t *testing.T) *testCerts {
	t.Helper()
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	writePEM := func(path, pemType string, der []byte) {
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
		defer f.Close()
		pem.Encode(f, &pem.Block{Type: pemType, Bytes: der}) //nolint:errcheck
	}
	writeKeyPEM := func(path string, key *ecdsa.PrivateKey) {
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("marshal key: %v", err)
		}
		writePEM(path, "EC PRIVATE KEY", der)
	}

	caCertPath := filepath.Join(dir, "ca.pem")
	writePEM(caCertPath, "CERTIFICATE", caDER)

	// Server cert signed by CA.
	srvKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srvTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "hyphae-test"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTemplate, caCert, &srvKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}
	srvCertPath := filepath.Join(dir, "server.pem")
	srvKeyPath := filepath.Join(dir, "server-key.pem")
	writePEM(srvCertPath, "CERTIFICATE", srvDER)
	writeKeyPEM(srvKeyPath, srvKey)
	serverTLSCert, err := tls.LoadX509KeyPair(srvCertPath, srvKeyPath)
	if err != nil {
		t.Fatalf("load server cert pair: %v", err)
	}

	// Client cert signed by CA.
	cliKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cliTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test-node-1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cliDER, err := x509.CreateCertificate(rand.Reader, cliTemplate, caCert, &cliKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client cert: %v", err)
	}
	cliCertPath := filepath.Join(dir, "client.pem")
	cliKeyPath := filepath.Join(dir, "client-key.pem")
	writePEM(cliCertPath, "CERTIFICATE", cliDER)
	writeKeyPEM(cliKeyPath, cliKey)
	clientTLSCert, err := tls.LoadX509KeyPair(cliCertPath, cliKeyPath)
	if err != nil {
		t.Fatalf("load client cert pair: %v", err)
	}

	return &testCerts{
		CACertPath:     caCertPath,
		ServerCertPath: srvCertPath,
		ServerKeyPath:  srvKeyPath,
		ClientCertPath: cliCertPath,
		ClientKeyPath:  cliKeyPath,
		caCert:         caCert,
		caKey:          caKey,
		serverCert:     serverTLSCert,
		clientCert:     clientTLSCert,
	}
}

// ── mock Hyphae tunnel server ──────────────────────────────────────────────────

// mockTunnelServer simulates the Hyphae internal/tunnel/Server handler:
// it accepts mTLS connections, performs the HTTP upgrade, and starts a
// yamux.Server session. Each accepted session is sent on the Sessions channel.
type mockTunnelServer struct {
	Addr     string
	Sessions chan *yamux.Session
	ln       net.Listener
}

// startMockTunnelServer starts a mTLS listener and begins accepting
// connections in the background. Call srv.Stop() when done.
func startMockTunnelServer(t *testing.T, certs *testCerts) *mockTunnelServer {
	t.Helper()

	pool := x509.NewCertPool()
	pool.AddCert(certs.caCert)

	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{certs.serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatalf("startMockTunnelServer: listen: %v", err)
	}

	srv := &mockTunnelServer{
		Addr:     ln.Addr().String(),
		Sessions: make(chan *yamux.Session, 8),
		ln:       ln,
	}

	t.Cleanup(srv.Stop)

	go srv.serve()
	return srv
}

func (s *mockTunnelServer) Stop() {
	s.ln.Close()
}

func (s *mockTunnelServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handleConn(conn)
	}
}

func (s *mockTunnelServer) handleConn(conn net.Conn) {
	// Read the HTTP upgrade request using a single buffered reader.
	br := bufio.NewReader(conn)
	httpReq, err := readHTTPUpgradeRequest(br)
	if err != nil || httpReq == "" {
		conn.Close()
		return
	}

	// Respond with 101.
	ack := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: tunnel\r\nConnection: Upgrade\r\n\r\n"
	if _, err := fmt.Fprint(conn, ack); err != nil {
		conn.Close()
		return
	}

	yamuxCfg := yamux.DefaultConfig()
	yamuxCfg.LogOutput = io.Discard
	// Recombine the buffered reader with the raw conn as the reader.
	sess, err := yamux.Server(conn, yamuxCfg)
	if err != nil {
		conn.Close()
		return
	}

	select {
	case s.Sessions <- sess:
	default:
		sess.Close()
	}
}

// readHTTPUpgradeRequest uses http.ReadRequest so header names are parsed
// with canonical casing (e.g. "X-Lease-Id"), matching what Go's http client sends.
func readHTTPUpgradeRequest(br *bufio.Reader) (leaseID string, err error) {
	req, err := http.ReadRequest(br)
	if err != nil {
		return "", err
	}
	// Canonical key for "X-Lease-ID" in Go's http is "X-Lease-Id".
	leaseID = req.Header.Get("X-Lease-Id")
	if leaseID == "" {
		leaseID = req.Header.Get("X-Lease-ID")
	}
	return leaseID, nil
}

// ── test-local echo server ────────────────────────────────────────────────────

// startEchoServer listens on a random local TCP port and echoes any bytes
// received back to the sender. Returns the listener address.
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("startEchoServer: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go io.Copy(conn, conn) //nolint:errcheck
		}
	}()
	return ln.Addr().String()
}

// ── TunnelClient helper ───────────────────────────────────────────────────────

func newTestTunnelClient(t *testing.T, certs *testCerts, srv *mockTunnelServer) *sdk.TunnelClient {
	t.Helper()

	pool := x509.NewCertPool()
	pool.AddCert(certs.caCert)

	tlsCfg := &tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{certs.clientCert},
		MinVersion:   tls.VersionTLS12,
		ServerName:   "localhost",
	}

	client, err := sdk.NewTunnelClient(sdk.TunnelClientConfig{
		HyphaeAddr: srv.Addr,
		TLSConfig:  tlsCfg,
	})
	if err != nil {
		t.Fatalf("NewTunnelClient: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// isErrInvalidConfig returns true when err wraps sdk.ErrInvalidConfig.
func isErrInvalidConfig(err error) bool {
	return errors.Is(err, sdk.ErrInvalidConfig)
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestTunnelClient_InvalidConfig_EmptyAddr(t *testing.T) {
	_, err := sdk.NewTunnelClient(sdk.TunnelClientConfig{})
	if err == nil {
		t.Fatal("expected error for empty HyphaeAddr")
	}
	if !isErrInvalidConfig(err) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestTunnelClient_InvalidConfig_MismatchedCerts(t *testing.T) {
	_, err := sdk.NewTunnelClient(sdk.TunnelClientConfig{
		HyphaeAddr:     "localhost:9090",
		ClientCertPath: "/some/cert.pem",
		// ClientKeyPath intentionally omitted
	})
	if err == nil {
		t.Fatal("expected error for mismatched cert/key")
	}
	if !isErrInvalidConfig(err) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestTunnelClient_Connect_ServerDown(t *testing.T) {
	certs := generateTestCerts(t)
	pool := x509.NewCertPool()
	pool.AddCert(certs.caCert)

	client, err := sdk.NewTunnelClient(sdk.TunnelClientConfig{
		HyphaeAddr: "127.0.0.1:19999", // nothing listening here
		TLSConfig: &tls.Config{
			RootCAs:      pool,
			Certificates: []tls.Certificate{certs.clientCert},
			ServerName:   "localhost",
		},
	})
	if err != nil {
		t.Fatalf("NewTunnelClient: %v", err)
	}
	defer client.Close()

	err = client.Connect(context.Background(), "lease-1")
	if err == nil {
		t.Fatal("expected error dialing a closed port")
	}
}

func TestTunnelClient_Connect_HappyPath(t *testing.T) {
	certs := generateTestCerts(t)
	srv := startMockTunnelServer(t, certs)
	client := newTestTunnelClient(t, certs, srv)

	if err := client.Connect(context.Background(), "lease-1"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if !client.IsConnected() {
		t.Error("expected IsConnected() == true after Connect()")
	}

	// Expect EventConnected on the events channel.
	select {
	case ev := <-client.Events():
		if ev.Type != sdk.EventConnected {
			t.Errorf("expected EventConnected, got %v", ev.Type)
		}
		if ev.LeaseID != "lease-1" {
			t.Errorf("expected LeaseID == lease-1, got %q", ev.LeaseID)
		}
	case <-time.After(2 * time.Second):
		t.Error("timed out waiting for EventConnected")
	}
}

func TestTunnelClient_Connect_EmptyLeaseID(t *testing.T) {
	certs := generateTestCerts(t)
	srv := startMockTunnelServer(t, certs)
	client := newTestTunnelClient(t, certs, srv)

	err := client.Connect(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty leaseID")
	}
	if !isErrInvalidConfig(err) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestTunnelClient_AcceptStream(t *testing.T) {
	certs := generateTestCerts(t)
	srv := startMockTunnelServer(t, certs)
	client := newTestTunnelClient(t, certs, srv)

	if err := client.Connect(context.Background(), "lease-2"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Wait for the mock server to get the session.
	var serverSess *yamux.Session
	select {
	case serverSess = <-srv.Sessions:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server session")
	}

	// Open a stream from the server side (simulating the proxy).
	serverStream, err := serverSess.Open()
	if err != nil {
		t.Fatalf("server Open stream: %v", err)
	}

	// Accept the stream from the client SDK side.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	clientStream, err := client.AcceptStream(ctx)
	if err != nil {
		t.Fatalf("AcceptStream: %v", err)
	}
	defer clientStream.Close()

	// Verify bidirectional communication.
	const msg = "hello from proxy"
	if _, err := serverStream.Write([]byte(msg)); err != nil {
		t.Fatalf("server write: %v", err)
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(clientStream, buf); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(buf) != msg {
		t.Errorf("message mismatch: got %q, want %q", buf, msg)
	}
}

func TestTunnelClient_Forward(t *testing.T) {
	certs := generateTestCerts(t)
	srv := startMockTunnelServer(t, certs)
	client := newTestTunnelClient(t, certs, srv)

	echoAddr := startEchoServer(t)

	if err := client.Connect(context.Background(), "lease-3"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Start forwarding to the local echo server.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	forwardErr := make(chan error, 1)
	go func() {
		forwardErr <- client.Forward(ctx, echoAddr)
	}()

	// Wait for the mock server's yamux session.
	var serverSess *yamux.Session
	select {
	case serverSess = <-srv.Sessions:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server session")
	}

	// Open a stream (simulating Hyphae's proxy forwarding an HTTP request).
	stream, err := serverSess.Open()
	if err != nil {
		t.Fatalf("server Open stream: %v", err)
	}
	defer stream.Close()

	const payload = "GET / HTTP/1.1\r\nHost: hello.example.com\r\n\r\n"
	if _, err := stream.Write([]byte(payload)); err != nil {
		t.Fatalf("write to stream: %v", err)
	}

	// Echo server reflects bytes; read them back from the stream.
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read from stream: %v", err)
	}
	if string(buf) != payload {
		t.Errorf("round-trip mismatch: got %q, want %q", buf, payload)
	}

	// Check that EventStreamOpened was emitted.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-client.Events():
			if !ok {
				goto done
			}
			if ev.Type == sdk.EventStreamOpened {
				goto done
			}
		case <-deadline:
			t.Error("timed out waiting for EventStreamOpened")
			goto done
		}
	}
done:
	cancel() // stop Forward
}

func TestTunnelClient_Close_Idempotent(t *testing.T) {
	certs := generateTestCerts(t)
	srv := startMockTunnelServer(t, certs)
	client := newTestTunnelClient(t, certs, srv)

	if err := client.Connect(context.Background(), "lease-4"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Drain events so Close doesn't block on the channel.
	go func() {
		for range client.Events() {
		}
	}()

	// Calling Close twice should not panic.
	client.Close() //nolint:errcheck
	client.Close() //nolint:errcheck
}

func TestTunnelClient_AcceptStream_WhenNotConnected(t *testing.T) {
	certs := generateTestCerts(t)
	// Don't start a server; just create the client without connecting.
	pool := x509.NewCertPool()
	pool.AddCert(certs.caCert)

	client, err := sdk.NewTunnelClient(sdk.TunnelClientConfig{
		HyphaeAddr: "127.0.0.1:19998",
		TLSConfig: &tls.Config{
			RootCAs:      pool,
			Certificates: []tls.Certificate{certs.clientCert},
			ServerName:   "localhost",
		},
	})
	if err != nil {
		t.Fatalf("NewTunnelClient: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = client.AcceptStream(ctx)
	if err == nil {
		t.Fatal("expected error AcceptStream on unconnected client")
	}
	if err != sdk.ErrNotConnected {
		t.Fatalf("expected ErrNotConnected, got %v", err)
	}
}

func TestTunnelClient_Events_Disconnect(t *testing.T) {
	certs := generateTestCerts(t)
	srv := startMockTunnelServer(t, certs)
	client := newTestTunnelClient(t, certs, srv)

	if err := client.Connect(context.Background(), "lease-5"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Drain EventConnected.
	select {
	case <-client.Events():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EventConnected")
	}

	// Drain remaining events and Close in background.
	go func() {
		for range client.Events() {
		}
	}()

	// Close the client and check IsConnected transitions to false.
	client.Close() //nolint:errcheck
	if client.IsConnected() {
		t.Error("expected IsConnected() == false after Close()")
	}
}

func TestTunnelClient_CertFileLoading(t *testing.T) {
	certs := generateTestCerts(t)
	srv := startMockTunnelServer(t, certs)

	// Use cert file paths (not a raw *tls.Config) to exercise buildTLSConfig.
	client, err := sdk.NewTunnelClient(sdk.TunnelClientConfig{
		HyphaeAddr:     srv.Addr,
		CACertPath:     certs.CACertPath,
		ClientCertPath: certs.ClientCertPath,
		ClientKeyPath:  certs.ClientKeyPath,
	})
	if err != nil {
		t.Fatalf("NewTunnelClient with cert files: %v", err)
	}
	defer client.Close()

	// The server name in the cert is "hyphae-test" / SAN localhost.
	// tls.DialWithDialer will use the host portion of HyphaeAddr as the server
	// name, which is "127.0.0.1". Our server cert has IPAddress SAN for 127.0.0.1.
	if err := client.Connect(context.Background(), "lease-6"); err != nil {
		t.Fatalf("Connect with cert files: %v", err)
	}
	if !client.IsConnected() {
		t.Error("expected IsConnected() after cert-file connect")
	}
}
