package sdk

import "time"

// TunnelEventType is a string discriminator for tunnel lifecycle events
// emitted on the TunnelClient.Events() channel.
type TunnelEventType string

const (
	// EventConnected is emitted when the initial (or a reconnected) tunnel
	// session has been successfully established.
	EventConnected TunnelEventType = "connected"

	// EventDisconnected is emitted when the tunnel session is lost, either
	// because the remote side closed it, the network failed, or Close() was
	// called explicitly.
	EventDisconnected TunnelEventType = "disconnected"

	// EventReconnecting is emitted each time the supervisor goroutine begins
	// a reconnection attempt (AutoReconnect must be enabled).
	EventReconnecting TunnelEventType = "reconnecting"

	// EventStreamOpened is emitted when Hyphae opens a new yamux stream for
	// an inbound HTTP request (Forward mode).
	EventStreamOpened TunnelEventType = "stream_opened"

	// EventStreamClosed is emitted when a forwarded yamux stream finishes
	// (both directions complete or an error closes the stream early).
	EventStreamClosed TunnelEventType = "stream_closed"
)

// TunnelEvent is a timestamped lifecycle notification sent over the channel
// returned by TunnelClient.Events(). Receivers should not block on this
// channel; the client uses non-blocking sends so a slow consumer will miss
// events rather than cause back-pressure on the tunnel.
type TunnelEvent struct {
	// Type identifies the kind of event.
	Type TunnelEventType

	// LeaseID is the lease this event is associated with.
	LeaseID string

	// Timestamp is the wall-clock time the event was generated.
	Timestamp time.Time

	// Error is non-nil for EventDisconnected and EventStreamClosed events
	// where the cause was an error rather than a clean shutdown.
	Error error

	// Detail is an optional human-readable description (e.g. local forward
	// address, remote peer address).
	Detail string
}
