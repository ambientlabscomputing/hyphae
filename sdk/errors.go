package sdk

import "errors"

// Sentinel errors returned by the Hyphae SDK. Callers may use errors.Is to
// check for any of these conditions.
var (
	// ErrNotConnected is returned when an operation is attempted on a
	// TunnelClient that has not been successfully connected yet.
	ErrNotConnected = errors.New("hyphae: client not connected")

	// ErrSessionClosed is returned when the underlying yamux session is
	// closed while an operation is in progress (e.g. Forward or AcceptStream).
	ErrSessionClosed = errors.New("hyphae: tunnel session closed")

	// ErrInvalidConfig is returned when a required configuration field is
	// missing or has an invalid value.
	ErrInvalidConfig = errors.New("hyphae: invalid configuration")

	// ErrLeaseNotFound is returned by ManagementClient when the server
	// responds with 404 for a lease lookup or revocation.
	ErrLeaseNotFound = errors.New("hyphae: lease not found")

	// ErrUpgradeFailed is returned when the Hyphae tunnel server rejects the
	// HTTP/1.1 upgrade request (non-101 status code).
	ErrUpgradeFailed = errors.New("hyphae: tunnel upgrade failed")

	// ErrUnauthorized is returned when the server rejects a request due to
	// authentication failure (401) — either a bad JWT or an mTLS cert issue.
	ErrUnauthorized = errors.New("hyphae: unauthorized")
)
