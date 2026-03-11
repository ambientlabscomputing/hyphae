package sdk

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"
)

// ── Tunnel client config ──────────────────────────────────────────────────────

// TunnelClientConfig holds all parameters needed to establish and maintain
// an mTLS reverse-tunnel connection to a Hyphae gateway.
type TunnelClientConfig struct {
	// HyphaeAddr is the host:port of the Hyphae tunnel listener, e.g.
	// "hyphae.example.com:9090".
	HyphaeAddr string

	// ServerName is the expected server name for TLS verification (SNI).
	// If empty, defaults to the hostname part of HyphaeAddr.
	ServerName string

	// CACertPath is the path to the Platform CA PEM file used to verify the
	// Hyphae server's TLS certificate. Leave empty to use the system root pool.
	CACertPath string

	// ClientCertPath and ClientKeyPath are the PEM files for the MMA node's
	// mTLS identity. Both must be set together.
	ClientCertPath string
	ClientKeyPath  string

	// TLSConfig is an optional override for the full *tls.Config. When set,
	// CACertPath / ClientCertPath / ClientKeyPath are ignored and the caller
	// is responsible for a complete TLS configuration (including client cert
	// and CA pool). Primarily used in tests.
	TLSConfig *tls.Config

	// CACertRefresher is an optional callback invoked by doConnect when a TLS
	// certificate verification error is encountered. It should return a fresh
	// *x509.CertPool built from the current platform CA certificate. When set,
	// doConnect will update its RootCAs and retry the dial once, which allows
	// the MMA to self-heal after the server_api CA is regenerated without
	// requiring a restart.
	CACertRefresher func(ctx context.Context) (*x509.CertPool, error)

	// AutoReconnect enables the supervisor goroutine that re-dials Hyphae
	// with exponential backoff whenever the session is lost.
	AutoReconnect bool

	// InitialReconnectDelay is the first wait duration before a reconnect
	// attempt. Defaults to 1 second.
	InitialReconnectDelay time.Duration

	// MaxReconnectDelay is the upper ceiling for the exponential backoff.
	// Defaults to 30 seconds.
	MaxReconnectDelay time.Duration

	// OnReconnect is called (in a new goroutine) each time a reconnection
	// succeeds. May be nil.
	OnReconnect func()

	// OnDisconnect is called (in a new goroutine) each time the tunnel
	// session is lost — before any reconnect attempt. May be nil.
	OnDisconnect func(err error)

	// EventBufferSize sets the capacity of the Events() channel.
	// Defaults to 64.
	EventBufferSize int
}

// Validate returns ErrInvalidConfig if any required field is missing.
func (c TunnelClientConfig) Validate() error {
	if c.HyphaeAddr == "" {
		return fmt.Errorf("%w: HyphaeAddr is required", ErrInvalidConfig)
	}
	if c.TLSConfig == nil {
		// When not using an explicit TLSConfig both cert and key must be set together.
		if (c.ClientCertPath == "") != (c.ClientKeyPath == "") {
			return fmt.Errorf("%w: ClientCertPath and ClientKeyPath must be set together", ErrInvalidConfig)
		}
	}
	return nil
}

// withDefaults returns a copy of c with zero-value optionals filled in.
func (c TunnelClientConfig) withDefaults() TunnelClientConfig {
	if c.InitialReconnectDelay <= 0 {
		c.InitialReconnectDelay = time.Second
	}
	if c.MaxReconnectDelay <= 0 {
		c.MaxReconnectDelay = 30 * time.Second
	}
	if c.EventBufferSize <= 0 {
		c.EventBufferSize = 64
	}
	return c
}

// ── Management client config ──────────────────────────────────────────────────

// ManagementClientConfig holds the parameters for the Hyphae REST management
// API client. The server_api uses this to issue and revoke leases.
type ManagementClientConfig struct {
	// BaseURL is the root URL of the Hyphae management API, e.g.
	// "https://hyphae.example.com:8084/api/v1". No trailing slash.
	BaseURL string

	// AuthToken is the Auth0 M2M JWT sent in every request as
	// "Authorization: Bearer <token>".
	AuthToken string

	// HTTPClient is optional. When nil a default client with a 30-second
	// timeout is used.
	HTTPClient *http.Client
}

// Validate returns ErrInvalidConfig if any required field is missing.
func (c ManagementClientConfig) Validate() error {
	if c.BaseURL == "" {
		return fmt.Errorf("%w: BaseURL is required", ErrInvalidConfig)
	}
	return nil
}

// withDefaults returns a copy of c with the HTTP client filled in if absent.
func (c ManagementClientConfig) withDefaults() ManagementClientConfig {
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return c
}
