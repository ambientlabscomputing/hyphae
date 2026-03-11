// Package sdk provides a client SDK for the Hyphae public gateway service.
//
// The SDK has two main components:
//
//  1. TunnelClient – establishes an mTLS reverse-tunnel from an MMA node to
//     Hyphae, then forwards inbound yamux streams to a local TCP port.
//
//  2. ManagementClient – a thin HTTP client for the Hyphae management REST
//     API (lease CRUD, connection listing, health).
//
// Basic usage for a port-forwarding tunnel:
//
//	client, err := sdk.NewTunnelClient(sdk.TunnelClientConfig{
//	    HyphaeAddr:     "hyphae.example.com:9090",
//	    CACertPath:     "/etc/hyphae/ca.pem",
//	    ClientCertPath: "/etc/hyphae/node.crt",
//	    ClientKeyPath:  "/etc/hyphae/node.key",
//	    AutoReconnect:  true,
//	})
//	if err != nil { ... }
//	defer client.Close()
//
//	ctx := context.Background()
//	if err := client.Connect(ctx, leaseID); err != nil { ... }
//
//	// Block and forward all inbound requests to localhost:8080.
//	if err := client.Forward(ctx, "localhost:8080"); err != nil { ... }
package sdk

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
)

// bufferedConn wraps a net.Conn so that bytes already buffered inside a
// bufio.Reader are replayed before reads fall through to the raw connection.
// This is necessary after parsing the HTTP upgrade response: if the bufio
// reader consumed bytes beyond the \r\n\r\n header terminator, those bytes
// belong to the yamux stream and must not be silently discarded.
type bufferedConn struct {
	net.Conn
	r io.Reader // bufio.Reader draining into conn, or just conn
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// newBufferedConn returns a net.Conn whose Read path drains any bytes already
// buffered in br before reading from the underlying connection.
func newBufferedConn(conn net.Conn, br *bufio.Reader) net.Conn {
	if br.Buffered() == 0 {
		return conn
	}
	return &bufferedConn{Conn: conn, r: io.MultiReader(br, conn)}
}

// ── TunnelClient ─────────────────────────────────────────────────────────────

// TunnelClient manages a persistent mTLS reverse-tunnel to a Hyphae gateway.
// It mirrors the handshake that internal/tunnel/server.go expects:
//
//  1. Dial HyphaeAddr with mTLS (client cert + platform CA).
//  2. Send HTTP/1.1 Upgrade request with "Upgrade: tunnel" and
//     "X-Lease-ID: <leaseID>" headers.
//  3. Await HTTP 101 Switching Protocols.
//  4. Start yamux.Client — Hyphae opens streams; we accept and forward them.
type TunnelClient struct {
	cfg     TunnelClientConfig
	tlsCfg  *tls.Config
	leaseID string // set on first Connect; reused across reconnects

	mu        sync.Mutex
	session   *yamux.Session
	rawConn   net.Conn // underlying TLS connection kept for close
	connected atomic.Bool

	// lifecycle
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup // tracks supervisor + active Forward goroutines
	closeOnce sync.Once      // ensures Close() is idempotent

	events chan TunnelEvent
	errs   chan error
}

// NewTunnelClient validates cfg and pre-builds the TLS configuration.
// The returned client is not connected; call Connect next.
func NewTunnelClient(cfg TunnelClientConfig) (*TunnelClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("hyphae sdk: build TLS config: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &TunnelClient{
		cfg:    cfg,
		tlsCfg: tlsCfg,
		ctx:    ctx,
		cancel: cancel,
		events: make(chan TunnelEvent, cfg.EventBufferSize),
		errs:   make(chan error, 10),
	}, nil
}

// Events returns a read-only channel of tunnel lifecycle events.
// Sends are non-blocking; a full channel drops the oldest unsent event.
func (c *TunnelClient) Events() <-chan TunnelEvent {
	return c.events
}

// Errors returns a read-only channel of asynchronous errors (e.g. stream
// forwarding failures). Sends are non-blocking.
func (c *TunnelClient) Errors() <-chan error {
	return c.errs
}

// IsConnected reports whether a live yamux session is currently active.
func (c *TunnelClient) IsConnected() bool {
	return c.connected.Load()
}

// Session returns the underlying yamux session, or nil if not connected.
// Advanced callers may use this for custom stream handling.
func (c *TunnelClient) Session() *yamux.Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// Connect dials the Hyphae tunnel server, performs the HTTP upgrade handshake
// for leaseID, and establishes the yamux client session. It blocks until the
// session is ready or an error occurs.
//
// Connect may only be called once per TunnelClient. Calling it again after a
// successful connect returns an error — create a new TunnelClient instead.
//
// If AutoReconnect is enabled, a supervisor goroutine is started that
// re-establishes the tunnel automatically whenever the session is lost.
func (c *TunnelClient) Connect(ctx context.Context, leaseID string) error {
	if leaseID == "" {
		return fmt.Errorf("%w: leaseID is required", ErrInvalidConfig)
	}

	// Guard against double-connect: if a session already exists this client
	// is already connected (or in the middle of reconnecting). Each client
	// must be used for exactly one lease — create a new one for a new lease.
	c.mu.Lock()
	if c.session != nil {
		c.mu.Unlock()
		return fmt.Errorf("hyphae sdk: already connected; call Close() before reconnecting")
	}
	c.leaseID = leaseID // write under mutex — read by supervisor/reconnect goroutines
	c.mu.Unlock()

	if err := c.doConnect(ctx); err != nil {
		return err
	}

	if c.cfg.AutoReconnect {
		c.wg.Add(1)
		go c.supervisor()
	}
	return nil
}

// Forward accepts yamux streams opened by Hyphae's proxy (one per inbound
// HTTP request) and pipes each stream to localAddr (e.g. "localhost:8080").
// It blocks until ctx is cancelled or Close() is called.
//
// When AutoReconnect is enabled and the session dies, Forward waits for the
// supervisor to re-establish the connection and then resumes accepting streams
// on the new session — so it never exits on a transient session failure.
func (c *TunnelClient) Forward(ctx context.Context, localAddr string) error {
	for {
		stream, err := c.AcceptStream(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-c.ctx.Done():
				return nil
			default:
				// Session died. If AutoReconnect is on, wait for the supervisor
				// to establish a new session then continue the accept loop.
				if c.cfg.AutoReconnect {
					if reconnErr := c.waitForReconnect(ctx); reconnErr != nil {
						return nil // client is shutting down
					}
					continue
				}
				return err
			}
		}

		c.mu.Lock()
		leaseID := c.leaseID
		c.mu.Unlock()

		c.emit(TunnelEvent{
			Type:      EventStreamOpened,
			LeaseID:   leaseID,
			Timestamp: time.Now(),
			Detail:    localAddr,
		})

		c.wg.Add(1)
		go c.forwardStream(stream, localAddr)
	}
}

// waitForReconnect blocks until the client is reconnected (connected.Load()
// becomes true) or the caller's context / client context is cancelled.
func (c *TunnelClient) waitForReconnect(ctx context.Context) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.ctx.Done():
			return ErrSessionClosed
		case <-ticker.C:
			if c.connected.Load() {
				return nil
			}
		}
	}
}

// AcceptStream waits for the next yamux stream opened by Hyphae. Returns
// ErrNotConnected if there is no active session, or ErrSessionClosed if the
// session terminates while waiting.
func (c *TunnelClient) AcceptStream(ctx context.Context) (net.Conn, error) {
	c.mu.Lock()
	sess := c.session
	c.mu.Unlock()

	if sess == nil {
		return nil, ErrNotConnected
	}

	type result struct {
		stream net.Conn
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := sess.AcceptStream()
		if err != nil {
			ch <- result{nil, ErrSessionClosed}
			return
		}
		ch <- result{s, nil}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, ErrSessionClosed
	case r := <-ch:
		return r.stream, r.err
	}
}

// Close shuts down the TunnelClient, stopping the supervisor and all active
// forwarding goroutines, and closing the tunnel connection. Safe to call
// multiple times — subsequent calls are no-ops.
func (c *TunnelClient) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		c.cancel() // signal all goroutines to stop

		c.mu.Lock()
		sess := c.session
		conn := c.rawConn
		c.session = nil
		c.rawConn = nil
		c.mu.Unlock()

		c.connected.Store(false)

		if sess != nil {
			if err := sess.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
		if conn != nil {
			if err := conn.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}

		c.wg.Wait() // wait for supervisor and all forwardStream goroutines
		close(c.events)
		close(c.errs)
	})
	return closeErr
}

// ── internal ──────────────────────────────────────────────────────────────────

func (c *TunnelClient) doConnect(ctx context.Context) error {
	conn, err := tls.DialWithDialer(&net.Dialer{
		Timeout: 15 * time.Second,
	}, "tcp", c.cfg.HyphaeAddr, c.tlsCfg)
	if err != nil {
		return fmt.Errorf("hyphae sdk: dial %s: %w", c.cfg.HyphaeAddr, err)
	}

	// Send the HTTP/1.1 Upgrade request.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.cfg.HyphaeAddr+"/", nil)
	if err != nil {
		conn.Close()
		return fmt.Errorf("hyphae sdk: create upgrade request: %w", err)
	}
	req.Header.Set("Upgrade", "tunnel")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("X-Lease-ID", c.leaseID)

	if err := req.Write(conn); err != nil {
		conn.Close()
		return fmt.Errorf("hyphae sdk: send upgrade request: %w", err)
	}

	// Read the server's response. We keep a handle on br so that any bytes
	// buffered beyond the response headers can be replayed into the yamux
	// session via newBufferedConn.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return fmt.Errorf("hyphae sdk: read upgrade response: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		conn.Close()
		return fmt.Errorf("%w: server returned 401", ErrUnauthorized)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return fmt.Errorf("%w: server returned %d %s", ErrUpgradeFailed, resp.StatusCode, resp.Status)
	}

	// Upgrade succeeded — wrap in a yamux client session.
	// Use newBufferedConn to replay any bytes the bufio.Reader may have
	// consumed beyond the HTTP response headers.
	yamuxCfg := yamux.DefaultConfig()
	yamuxCfg.KeepAliveInterval = 30 * time.Second
	sess, err := yamux.Client(newBufferedConn(conn, br), yamuxCfg)
	if err != nil {
		conn.Close()
		return fmt.Errorf("hyphae sdk: yamux.Client: %w", err)
	}

	c.mu.Lock()
	// Close any stale session/connection from a previous connect cycle
	// (supervisor-triggered reconnect).  On the first connect c.session is nil.
	if c.session != nil {
		c.session.Close() //nolint:errcheck
	}
	if c.rawConn != nil {
		c.rawConn.Close() //nolint:errcheck
	}
	c.session = sess
	c.rawConn = conn
	c.mu.Unlock()

	c.connected.Store(true)
	c.mu.Lock()
	leaseID := c.leaseID
	c.mu.Unlock()
	c.emit(TunnelEvent{
		Type:      EventConnected,
		LeaseID:   leaseID,
		Timestamp: time.Now(),
		Detail:    c.cfg.HyphaeAddr,
	})
	return nil
}

// supervisor is the auto-reconnect goroutine. It waits for the session to
// become unhealthy, then re-dials with exponential backoff.
func (c *TunnelClient) supervisor() {
	defer c.wg.Done()

	for {
		// Poll the session liveness. yamux exposes IsClosed on the session;
		// we approximate it with a KeepAlive ping at low cost.
		c.mu.Lock()
		sess := c.session
		c.mu.Unlock()

		if sess != nil {
			if _, err := sess.Ping(); err != nil {
				c.connected.Store(false)
				c.emit(TunnelEvent{
					Type:      EventDisconnected,
					LeaseID:   c.leaseID,
					Timestamp: time.Now(),
					Error:     err,
				})
				if c.cfg.OnDisconnect != nil {
					go c.cfg.OnDisconnect(err)
				}
				c.reconnectLoop()
			}
		}

		select {
		case <-c.ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// reconnectLoop blocks until a successful reconnect or c.ctx is cancelled.
func (c *TunnelClient) reconnectLoop() {
	delay := c.cfg.InitialReconnectDelay
	for {
		c.emit(TunnelEvent{
			Type:      EventReconnecting,
			LeaseID:   c.leaseID,
			Timestamp: time.Now(),
			Detail:    fmt.Sprintf("retrying in %s", delay),
		})

		select {
		case <-c.ctx.Done():
			return
		case <-time.After(delay):
		}

		if err := c.doConnect(c.ctx); err != nil {
			c.sendErr(fmt.Errorf("hyphae sdk: reconnect failed: %w", err))
			// Exponential backoff, capped at MaxReconnectDelay.
			delay *= 2
			if delay > c.cfg.MaxReconnectDelay {
				delay = c.cfg.MaxReconnectDelay
			}
			continue
		}

		// Success.
		if c.cfg.OnReconnect != nil {
			go c.cfg.OnReconnect()
		}
		return
	}
}

// forwardStream dials localAddr, then pipes bytes between stream and the
// local connection until both directions complete.
func (c *TunnelClient) forwardStream(stream net.Conn, localAddr string) {
	defer c.wg.Done()
	defer stream.Close()

	local, err := net.DialTimeout("tcp", localAddr, 10*time.Second)
	if err != nil {
		c.sendErr(fmt.Errorf("hyphae sdk: dial local %s: %w", localAddr, err))
		c.emit(TunnelEvent{
			Type:      EventStreamClosed,
			LeaseID:   c.leaseID,
			Timestamp: time.Now(),
			Error:     err,
			Detail:    localAddr,
		})
		return
	}
	defer local.Close()

	done := make(chan struct{}, 2)
	go func() { io.Copy(local, stream); done <- struct{}{} }() //nolint:errcheck
	go func() { io.Copy(stream, local); done <- struct{}{} }() //nolint:errcheck
	<-done

	c.emit(TunnelEvent{
		Type:      EventStreamClosed,
		LeaseID:   c.leaseID,
		Timestamp: time.Now(),
		Detail:    localAddr,
	})
}

// emit sends a TunnelEvent non-blocking; drops the event if the buffer is full.
func (c *TunnelClient) emit(e TunnelEvent) {
	select {
	case c.events <- e:
	default:
	}
}

// sendErr sends an error to the errors channel non-blocking; drops if full.
func (c *TunnelClient) sendErr(err error) {
	select {
	case c.errs <- err:
	default:
	}
}

// ── TLS helpers ───────────────────────────────────────────────────────────────

// buildTLSConfig constructs a *tls.Config from the SDK config fields, or
// returns cfg.TLSConfig directly if one was provided.
func buildTLSConfig(cfg TunnelClientConfig) (*tls.Config, error) {
	if cfg.TLSConfig != nil {
		return cfg.TLSConfig, nil
	}

	// DEPRECATED: file-based TLS config is the fallback path. Clients should pass a bootstrapped
	// *tls.Config via TunnelClientConfig.TLSConfig instead. This path remains as an escape hatch
	// for disaster recovery but should not be relied upon in normal operation.
	if cfg.CACertPath != "" || cfg.ClientCertPath != "" {
		slog.Default().Warn("[DEPRECATED] hyphae sdk: using file-based TLS config; prefer bootstrapped TLSConfig via IssueLocalCertificate",
			"ca_cert_path", cfg.CACertPath, "client_cert_path", cfg.ClientCertPath)
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Set ServerName for SNI verification.
	if cfg.ServerName != "" {
		tlsCfg.ServerName = cfg.ServerName
	} else {
		// Extract hostname from HyphaeAddr (e.g., "hyphae.example.com:9090" -> "hyphae.example.com")
		host, _, err := net.SplitHostPort(cfg.HyphaeAddr)
		if err == nil {
			tlsCfg.ServerName = host
		}
	}

	// Load the CA pool for server verification.
	if cfg.CACertPath != "" {
		pool := x509.NewCertPool()
		caPEM, err := os.ReadFile(cfg.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("read CA cert %s: %w", cfg.CACertPath, err)
		}
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("failed to parse CA cert from %s", cfg.CACertPath)
		}
		tlsCfg.RootCAs = pool
	}

	// Load the client certificate (mTLS identity).
	if cfg.ClientCertPath != "" {
		cert, err := tls.LoadX509KeyPair(cfg.ClientCertPath, cfg.ClientKeyPath)
		if err != nil {
			return nil, fmt.Errorf("load client cert %s/%s: %w", cfg.ClientCertPath, cfg.ClientKeyPath, err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tlsCfg, nil
}
