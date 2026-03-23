package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/ambientlabscomputing/hyphae/internal/utils"
	"github.com/hashicorp/yamux"
)

// handleListenerConn processes a destination agent that has connected to hyphae
// to accept incoming relay channels targeting its serverID.
//
// Flow:
//  1. Send 101 Switching Protocols to acknowledge the upgrade
//  2. Start a yamux.Server session over the TLS connection
//  3. Register the session in the channel repository by (serverID, channelID)
//  4. Drain the AcceptStream loop to keep the session alive (yamux flow control)
//  5. On connection close, unregister the listener
//
// The listener agent creates a yamux.Client on its side and calls AcceptStream()
// to receive channel connections opened by hyphae on behalf of initiators.
func (s *Server) handleListenerConn(
	ctx context.Context,
	tlsConn *tls.Conn,
	nodeServerID string,
	orgID string,
	channelID string,
	br *bufio.Reader,
) {
	logger := utils.GetLogger(ctx).With("listener", nodeServerID, "org_id", orgID, "channel_id", channelID)

	if orgID == "" {
		logger.Warn("Listener: missing X-Org-ID header")
		writeHTTPError(tlsConn, http.StatusBadRequest, "missing X-Org-ID header")
		tlsConn.Close()
		return
	}

	// ── 1. Send 101 Switching Protocols ──────────────────────────────────────
	ack := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: tunnel\r\nConnection: Upgrade\r\n\r\n"
	if _, err := fmt.Fprint(tlsConn, ack); err != nil {
		logger.Warn("Listener: failed to send 101", "error", err)
		tlsConn.Close()
		return
	}

	// Enable TCP keepalive on the underlying connection to detect dead peers.
	if tc, ok := tlsConn.NetConn().(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}

	// ── 2. Start yamux.Server ─────────────────────────────────────────────────
	// If br has buffered bytes (from the HTTP upgrade read), wrap the connection
	// so those bytes are replayed before reading from the raw TLS conn.
	var yamuxIO io.ReadWriteCloser = tlsConn
	if br.Buffered() > 0 {
		yamuxIO = &bufferedRWC{
			Reader: io.MultiReader(br, tlsConn),
			conn:   tlsConn,
		}
	}

	yamuxCfg := yamux.DefaultConfig()
	yamuxCfg.KeepAliveInterval = 5 * time.Second
	yamuxCfg.ConnectionWriteTimeout = 30 * time.Second
	session, err := yamux.Server(yamuxIO, yamuxCfg)
	if err != nil {
		logger.Error("Listener: failed to create yamux server session", "error", err)
		tlsConn.Close()
		return
	}

	// ── 3. Register session ───────────────────────────────────────────────────
	if err := s.svc.RegisterListener(ctx, nodeServerID, orgID, channelID, session); err != nil {
		logger.Error("Listener: failed to register session", "error", err)
		session.Close()
		return
	}

	logger.Info("Listener: registered")

	// ── 4. Drain AcceptStream ─────────────────────────────────────────────────
	// Hyphae does NOT accept inbound streams from the listener agent; it OPENS
	// streams TO it in handleChannelConn. The drain loop here exists solely to
	// keep the yamux session alive and detect when the underlying connection is
	// closed by reading yamux keepalive frames and connection-level control
	// messages. If the listener agent opens an unexpected stream, we close it.
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			// Session closed — listener disconnected.
			break
		}
		// Unexpected inbound stream from the listener agent; close it.
		stream.Close()
	}

	// ── 5. Unregister ─────────────────────────────────────────────────────────
	if err := s.svc.UnregisterListener(ctx, nodeServerID, channelID, session); err != nil {
		logger.Warn("Listener: failed to unregister session", "error", err)
	}

	logger.Info("Listener: disconnected")
}
