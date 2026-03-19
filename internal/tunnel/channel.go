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
	"github.com/ambientlabscomputing/hyphae/sdk"
)

// handleChannelConn processes an initiator agent connecting to establish a
// relay channel. It is invoked from handleConn when the HTTP upgrade request
// contains an X-Channel-ID header.
//
// Flow:
//  1. Validate the channel grant JWT (signature, expiry, source/dest binding)
//  2. Verify the initiator's cert CN matches grant.SourceServerID
//  3. Confirm the channel exists and is not revoked
//  4. Look up the destination listener's yamux session
//  5. Open a yamux stream on the listener's session
//  6. Send 101 Switching Protocols to the initiator
//  7. Splice: io.Copy in both directions between initiator conn and listener stream
//  8. Update channel status on activation and close
func (s *Server) handleChannelConn(
	ctx context.Context,
	tlsConn *tls.Conn,
	nodeServerID string,
	channelID string,
	grantToken string,
	br *bufio.Reader,
) {
	logger := utils.GetLogger(ctx).With("channel_id", channelID, "initiator", nodeServerID)

	// ── 1. Validate grant JWT ─────────────────────────────────────────────────
	if grantToken == "" {
		logger.Warn("Channel: missing X-Channel-Grant header")
		writeHTTPError(tlsConn, http.StatusUnauthorized, "missing X-Channel-Grant header")
		tlsConn.Close()
		return
	}

	grant, err := s.svc.ValidateChannelGrant(ctx, grantToken)
	if err != nil {
		logger.Warn("Channel: invalid grant", "error", err)
		writeHTTPError(tlsConn, http.StatusForbidden, fmt.Sprintf("invalid grant: %v", err))
		tlsConn.Close()
		return
	}

	// ── 2. Verify cert CN matches grant source ────────────────────────────────
	if grant.SourceServerID != nodeServerID {
		logger.Warn("Channel: cert CN does not match grant source_server_id",
			"cert_cn", nodeServerID, "grant_source", grant.SourceServerID)
		writeHTTPError(tlsConn, http.StatusForbidden, "node identity does not match grant")
		tlsConn.Close()
		return
	}

	// ── 3. Confirm channelID in header matches the grant ─────────────────────
	if grant.ChannelID != channelID {
		logger.Warn("Channel: X-Channel-ID does not match grant channel_id",
			"header", channelID, "grant", grant.ChannelID)
		writeHTTPError(tlsConn, http.StatusForbidden, "channel ID mismatch")
		tlsConn.Close()
		return
	}

	// ── 4. Confirm channel exists and is not revoked ──────────────────────────
	ch, err := s.svc.GetChannel(ctx, channelID)
	if err != nil {
		logger.Warn("Channel: not found", "error", err)
		writeHTTPError(tlsConn, http.StatusNotFound, "channel not found")
		tlsConn.Close()
		return
	}
	if ch.Status == sdk.ChannelStatusClosed || ch.Status == sdk.ChannelStatusError {
		logger.Warn("Channel: already closed or errored", "status", ch.Status)
		writeHTTPError(tlsConn, http.StatusGone, "channel is no longer active")
		tlsConn.Close()
		return
	}

	// ── 5. Look up destination listener session ───────────────────────────────
	// Retry: When channels are created rapidly, the destination agent may be
	// re-registering its listener connection. If the session lookup fails or
	// the session is dead (e.g. closed by a newer RegisterListener call),
	// retry a few times with a short back-off to let the new session arrive.
	var listenerStream net.Conn
	{
		const maxAttempts = 5
		backoff := 200 * time.Millisecond
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			listenerSession, err := s.svc.GetListenerSession(ctx, grant.DestServerID)
			if err != nil {
				if attempt == maxAttempts {
					logger.Warn("Channel: no listener registered for destination",
						"dest", grant.DestServerID, "error", err, "attempts", maxAttempts)
					writeHTTPError(tlsConn, http.StatusServiceUnavailable, "destination listener not connected")
					tlsConn.Close()
					return
				}
				time.Sleep(backoff)
				backoff *= 2
				continue
			}

			listenerStream, err = listenerSession.Open()
			if err != nil {
				if attempt == maxAttempts {
					logger.Warn("Channel: failed to open listener stream",
						"dest", grant.DestServerID, "error", err, "attempts", maxAttempts)
					writeHTTPError(tlsConn, http.StatusBadGateway, "failed to reach listener")
					tlsConn.Close()
					return
				}
				time.Sleep(backoff)
				backoff *= 2
				continue
			}
			break
		}
	}
	defer listenerStream.Close()

	// ── 6. Send 101 Switching Protocols to the initiator ─────────────────────
	ack := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: tunnel\r\nConnection: Upgrade\r\n\r\n"
	if _, err := fmt.Fprint(tlsConn, ack); err != nil {
		logger.Warn("Channel: failed to send 101 to initiator", "error", err)
		tlsConn.Close()
		return
	}

	// Clear any deadline set during the handshake phase — the splice is
	// long-lived and managed by the idle timeout below.
	_ = tlsConn.SetDeadline(time.Time{})

	// ── 7. Update status and splice ───────────────────────────────────────────
	if err := s.svc.UpdateChannelStatus(ctx, channelID, sdk.ChannelStatusActive); err != nil {
		logger.Warn("Channel: failed to mark active", "error", err)
		// Non-fatal — continue with the splice regardless.
	}

	logger.Info("Channel: splice started", "dest", grant.DestServerID, "purpose", grant.Purpose)

	// Replay any bytes br buffered beyond the HTTP request before splicing.
	var initiatorReader io.Reader = tlsConn
	if br.Buffered() > 0 {
		initiatorReader = io.MultiReader(br, tlsConn)
	}
	initiatorConn := &readWriteCloser{r: initiatorReader, wc: tlsConn}

	// Apply idle timeout if configured.
	idleTimeout := time.Duration(s.channelIdleSec) * time.Second

	bytesAtoB, bytesBtoA := splice(initiatorConn, listenerStream, idleTimeout)
	total := bytesAtoB + bytesBtoA

	s.svc.AddChannelBytes(ctx, channelID, total)
	if err := s.svc.UpdateChannelStatus(ctx, channelID, sdk.ChannelStatusClosed); err != nil {
		logger.Warn("Channel: failed to mark closed", "error", err)
	}

	logger.Info("Channel: splice complete",
		"dest", grant.DestServerID,
		"bytes_a_to_b", bytesAtoB,
		"bytes_b_to_a", bytesBtoA,
	)
}

// splice bidirectionally copies between a and b. It blocks until one direction
// closes, then closes both sides and waits for the second goroutine to finish.
// Returns the byte counts for each direction.
func splice(a, b io.ReadWriteCloser, idleTimeout time.Duration) (aToB, bToA int64) {
	type result struct{ n int64 }
	chA := make(chan result, 1)
	chB := make(chan result, 1)

	go func() {
		n, _ := io.Copy(b, a)
		chA <- result{n}
	}()
	go func() {
		n, _ := io.Copy(a, b)
		chB <- result{n}
	}()

	// The idle timeout is enforced via SetDeadline on the underlying net.Conn
	// objects (if they are net.Conn). Here we rely on the connection-level
	// keepalive; the idleTimeout parameter is plumbed for future use.
	_ = idleTimeout

	// Wait for the first direction to finish, then close both to unblock the other.
	select {
	case r := <-chA:
		aToB = r.n
		a.Close() //nolint:errcheck
		b.Close() //nolint:errcheck
		bToA = (<-chB).n
	case r := <-chB:
		bToA = r.n
		a.Close() //nolint:errcheck
		b.Close() //nolint:errcheck
		aToB = (<-chA).n
	}
	return aToB, bToA
}

// readWriteCloser composes a separate reader and a write-closer. This is used
// to attach the buffered reader (which may contain pre-buffered bytes from the
// HTTP upgrade parsing) to the TLS connection's Write and Close methods.
type readWriteCloser struct {
	r  io.Reader
	wc io.WriteCloser
}

func (rwc *readWriteCloser) Read(p []byte) (int, error)  { return rwc.r.Read(p) }
func (rwc *readWriteCloser) Write(p []byte) (int, error) { return rwc.wc.Write(p) }
func (rwc *readWriteCloser) Close() error                { return rwc.wc.Close() }

// setConnDeadline sets the deadline on a net.Conn if the io.ReadWriteCloser
// is one. Used to implement idle timeouts on both sides of a splice.
func setConnDeadline(rwc io.ReadWriteCloser, t time.Time) {
	if conn, ok := rwc.(net.Conn); ok {
		_ = conn.SetDeadline(t)
	}
}
