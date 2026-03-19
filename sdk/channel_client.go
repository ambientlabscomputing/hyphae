package sdk

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// ── ChannelInitiator ─────────────────────────────────────────────────────────
//
// ChannelInitiator dials the Hyphae tunnel endpoint to establish a relay
// channel as the initiating side.  The single Connect call performs the full
// handshake and returns a transparent net.Conn whose other end is the
// ChannelListener registered on the destination server.
//
// Usage:
//
//	init := sdk.NewChannelInitiator(cfg)
//	conn, err := init.Connect(ctx, channelID, grantJWT)
//	if err != nil { ... }
//	defer conn.Close()
//	// conn is a raw net.Conn — use it like any TCP connection.

// ChannelInitiator opens one-shot relay channel connections to Hyphae.
type ChannelInitiator struct {
	cfg    TunnelClientConfig
	tlsCfg *tls.Config
}

// NewChannelInitiator validates cfg and pre-builds the TLS configuration.
func NewChannelInitiator(cfg TunnelClientConfig) (*ChannelInitiator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("hyphae sdk: ChannelInitiator: build TLS config: %w", err)
	}
	return &ChannelInitiator{cfg: cfg, tlsCfg: tlsCfg}, nil
}

// Connect dials the Hyphae tunnel server and establishes a channel relay
// identified by channelID.  grant is the signed JWT returned by IssueChannel
// on the management client.
//
// On success the returned net.Conn is the raw, transparent TCP connection to
// the ChannelListener on the destination agent.  The caller owns the
// connection and must call Close() when done.
func (ci *ChannelInitiator) Connect(ctx context.Context, channelID, grant string) (net.Conn, error) {
	if channelID == "" {
		return nil, fmt.Errorf("hyphae sdk: ChannelInitiator.Connect: channelID is required")
	}
	if grant == "" {
		return nil, fmt.Errorf("hyphae sdk: ChannelInitiator.Connect: grant is required")
	}

	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", ci.cfg.HyphaeAddr, ci.tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("hyphae sdk: ChannelInitiator: dial %s: %w", ci.cfg.HyphaeAddr, err)
	}

	// Build and send the HTTP upgrade request.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ci.cfg.HyphaeAddr+"/", nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("hyphae sdk: ChannelInitiator: create request: %w", err)
	}
	req.Header.Set("Upgrade", "tunnel")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("X-Channel-ID", channelID)
	req.Header.Set("X-Channel-Grant", grant)

	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("hyphae sdk: ChannelInitiator: send request: %w", err)
	}

	// Read the 101 Switching Protocols response.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("hyphae sdk: ChannelInitiator: read response: %w", err)
	}
	resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusSwitchingProtocols:
		// success — fall through
	case http.StatusUnauthorized, http.StatusForbidden:
		conn.Close()
		return nil, fmt.Errorf("%w: channel auth rejected (%d)", ErrUnauthorized, resp.StatusCode)
	default:
		conn.Close()
		return nil, fmt.Errorf("%w: server returned %d %s", ErrUpgradeFailed, resp.StatusCode, resp.Status)
	}

	// Return a conn that replays any bytes the bufio.Reader pre-buffered beyond
	// the HTTP response headers.
	return newBufferedConn(conn, br), nil
}

// ── ChannelListener ──────────────────────────────────────────────────────────
//
// ChannelListener registers as the destination end of relay channels for a
// given org.  It dials Hyphae once and maintains a persistent yamux.Client
// session.  Hyphae opens yamux streams on that session whenever an initiator
// connects via a matching channel grant.
//
// Usage:
//
//	listener, err := sdk.NewChannelListener(cfg)
//	if err != nil { ... }
//	if err := listener.Listen(ctx, orgID); err != nil { ... }
//	for {
//	    conn, err := listener.AcceptChannel(ctx)
//	    if err != nil { break }
//	    go handleConn(conn)
//	}

// ChannelListener maintains a yamux.Client session to Hyphae and exposes
// incoming relay channel connections as net.Conn via AcceptChannel.
type ChannelListener struct {
	cfg     TunnelClientConfig
	tlsCfg  *tls.Config
	orgID   string
	rawConn net.Conn

	mu      sync.Mutex
	session *yamux.Session
}

// NewChannelListener validates cfg and pre-builds the TLS configuration.
func NewChannelListener(cfg TunnelClientConfig) (*ChannelListener, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("hyphae sdk: ChannelListener: build TLS config: %w", err)
	}
	return &ChannelListener{cfg: cfg, tlsCfg: tlsCfg}, nil
}

// Listen dials Hyphae and registers as a channel listener for orgID.
// It blocks until the upgrade handshake is complete and the yamux.Client
// session is established. AcceptChannel may then be called to receive incoming
// channels. Listen may only be called once per ChannelListener instance.
func (cl *ChannelListener) Listen(ctx context.Context, orgID string) error {
	if orgID == "" {
		return fmt.Errorf("hyphae sdk: ChannelListener.Listen: orgID is required")
	}

	cl.mu.Lock()
	if cl.session != nil {
		cl.mu.Unlock()
		return fmt.Errorf("hyphae sdk: ChannelListener.Listen: already listening; create a new ChannelListener to re-listen")
	}
	cl.mu.Unlock()

	cl.orgID = orgID

	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", cl.cfg.HyphaeAddr, cl.tlsCfg)
	if err != nil {
		return fmt.Errorf("hyphae sdk: ChannelListener: dial %s: %w", cl.cfg.HyphaeAddr, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+cl.cfg.HyphaeAddr+"/", nil)
	if err != nil {
		conn.Close()
		return fmt.Errorf("hyphae sdk: ChannelListener: create request: %w", err)
	}
	req.Header.Set("Upgrade", "tunnel")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("X-Listener-Register", "true")
	req.Header.Set("X-Org-ID", orgID)

	if err := req.Write(conn); err != nil {
		conn.Close()
		return fmt.Errorf("hyphae sdk: ChannelListener: send request: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return fmt.Errorf("hyphae sdk: ChannelListener: read response: %w", err)
	}
	resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusSwitchingProtocols:
		// success — fall through
	case http.StatusUnauthorized, http.StatusForbidden:
		conn.Close()
		return fmt.Errorf("%w: listener registration rejected (%d)", ErrUnauthorized, resp.StatusCode)
	default:
		conn.Close()
		return fmt.Errorf("%w: server returned %d %s for listener registration", ErrUpgradeFailed, resp.StatusCode, resp.Status)
	}

	// Hyphae creates yamux.Server on its side; we create yamux.Client here.
	// Hyphae will Open() streams toward us; we AcceptStream() them.
	yamuxCfg := yamux.DefaultConfig()
	yamuxCfg.KeepAliveInterval = 30 * time.Second
	sess, err := yamux.Client(newBufferedConn(conn, br), yamuxCfg)
	if err != nil {
		conn.Close()
		return fmt.Errorf("hyphae sdk: ChannelListener: yamux.Client: %w", err)
	}

	cl.mu.Lock()
	cl.rawConn = conn
	cl.session = sess
	cl.mu.Unlock()

	return nil
}

// AcceptChannel blocks until Hyphae opens a stream for an incoming relay
// channel. The returned net.Conn is the connected initiator's byte stream.
// Returns an error when the session is closed.
func (cl *ChannelListener) AcceptChannel(ctx context.Context) (net.Conn, error) {
	cl.mu.Lock()
	sess := cl.session
	cl.mu.Unlock()

	if sess == nil {
		return nil, fmt.Errorf("hyphae sdk: ChannelListener.AcceptChannel: session not established — call Listen first")
	}

	// AcceptStream blocks until Hyphae opens a new stream or the session closes.
	stream, err := sess.AcceptStream()
	if err != nil {
		return nil, fmt.Errorf("hyphae sdk: ChannelListener.AcceptChannel: %w", err)
	}
	return stream, nil
}

// Close shuts down the listener session and the underlying connection.
func (cl *ChannelListener) Close() error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	var errs []error
	if cl.session != nil {
		if err := cl.session.Close(); err != nil {
			errs = append(errs, err)
		}
		cl.session = nil
	}
	if cl.rawConn != nil {
		if err := cl.rawConn.Close(); err != nil {
			errs = append(errs, err)
		}
		cl.rawConn = nil
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}
