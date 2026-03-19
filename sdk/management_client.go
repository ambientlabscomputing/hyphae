package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ManagementClient is a thin HTTP client for the Hyphae management REST API.
// It is used by the server_api (and, indirectly, by an MMA node that wants to
// inspect or revoke its own leases) to perform lease lifecycle operations.
//
// All methods respect context cancellation. Token refresh is the caller's
// responsibility — create a new client or call SetAuthToken when the M2M JWT
// expires.
type ManagementClient struct {
	cfg ManagementClientConfig
}

// NewManagementClient validates cfg and returns a ready-to-use client.
func NewManagementClient(cfg ManagementClientConfig) (*ManagementClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	return &ManagementClient{cfg: cfg}, nil
}

// SetAuthToken replaces the bearer token used for subsequent requests.
func (m *ManagementClient) SetAuthToken(token string) {
	m.cfg.AuthToken = token
}

// ── Lease operations ──────────────────────────────────────────────────────────

// IssueLease calls POST /leases and returns the created Lease on success.
func (m *ManagementClient) IssueLease(ctx context.Context, req IssueLeaseRequest) (*Lease, error) {
	var out IssueLeaseResponse
	if err := m.doJSON(ctx, http.MethodPost, "/leases", req, &out); err != nil {
		return nil, fmt.Errorf("IssueLease: %w", err)
	}
	return out.Lease, nil
}

// GetLease calls GET /leases/:id and returns the Lease. Returns
// ErrLeaseNotFound if the server responds with 404.
func (m *ManagementClient) GetLease(ctx context.Context, leaseID string) (*Lease, error) {
	var out Lease
	if err := m.doJSON(ctx, http.MethodGet, "/leases/"+leaseID, nil, &out); err != nil {
		return nil, fmt.Errorf("GetLease: %w", err)
	}
	return &out, nil
}

// ListLeases calls GET /leases and returns all active leases.
func (m *ManagementClient) ListLeases(ctx context.Context) ([]*Lease, error) {
	var out ListLeasesResponse
	if err := m.doJSON(ctx, http.MethodGet, "/leases", nil, &out); err != nil {
		return nil, fmt.Errorf("ListLeases: %w", err)
	}
	return out.Leases, nil
}

// RevokeLease calls DELETE /leases/:id. Returns ErrLeaseNotFound if the
// server responds with 404.
func (m *ManagementClient) RevokeLease(ctx context.Context, leaseID string) error {
	if err := m.doJSON(ctx, http.MethodDelete, "/leases/"+leaseID, nil, nil); err != nil {
		return fmt.Errorf("RevokeLease: %w", err)
	}
	return nil
}

// ── Channel operations ────────────────────────────────────────────────────────

// RegisterChannel calls POST /channels to store a pre-built channel on the
// hyphae gateway. The channel is created and signed by server_api before being
// forwarded to hyphae for relay-lookup.
func (m *ManagementClient) RegisterChannel(ctx context.Context, ch *Channel) (*Channel, error) {
	var out RegisterChannelResponse
	if err := m.doJSON(ctx, http.MethodPost, "/channels", ch, &out); err != nil {
		return nil, fmt.Errorf("RegisterChannel: %w", err)
	}
	return out.Channel, nil
}

// GetChannel calls GET /channels/:id and returns the Channel. Returns
// ErrLeaseNotFound if the server responds with 404.
func (m *ManagementClient) GetChannel(ctx context.Context, channelID string) (*Channel, error) {
	var out Channel
	if err := m.doJSON(ctx, http.MethodGet, "/channels/"+channelID, nil, &out); err != nil {
		return nil, fmt.Errorf("GetChannel: %w", err)
	}
	return &out, nil
}

// ListChannels calls GET /channels and returns all known channels.
func (m *ManagementClient) ListChannels(ctx context.Context) ([]*Channel, error) {
	var out ListChannelsResponse
	if err := m.doJSON(ctx, http.MethodGet, "/channels", nil, &out); err != nil {
		return nil, fmt.Errorf("ListChannels: %w", err)
	}
	return out.Channels, nil
}

// RevokeChannel calls DELETE /channels/:id to remove a channel. Returns
// ErrLeaseNotFound if the server responds with 404.
func (m *ManagementClient) RevokeChannel(ctx context.Context, channelID string) error {
	if err := m.doJSON(ctx, http.MethodDelete, "/channels/"+channelID, nil, nil); err != nil {
		return fmt.Errorf("RevokeChannel: %w", err)
	}
	return nil
}

// ListListeners calls GET /listeners and returns all connected listener registrations.
func (m *ManagementClient) ListListeners(ctx context.Context) ([]*ListenerRegistration, error) {
	var out ListListenersResponse
	if err := m.doJSON(ctx, http.MethodGet, "/listeners", nil, &out); err != nil {
		return nil, fmt.Errorf("ListListeners: %w", err)
	}
	return out.Listeners, nil
}

// ── Connection operations ─────────────────────────────────────────────────────

// ListConnections calls GET /connections and returns all live tunnel sessions.
func (m *ManagementClient) ListConnections(ctx context.Context) ([]*TunnelConnection, error) {
	var out ListConnectionsResponse
	if err := m.doJSON(ctx, http.MethodGet, "/connections", nil, &out); err != nil {
		return nil, fmt.Errorf("ListConnections: %w", err)
	}
	return out.Connections, nil
}

// ── Health ────────────────────────────────────────────────────────────────────

// Health calls GET /health (no auth required) and returns the current status.
func (m *ManagementClient) Health(ctx context.Context) (*HealthResponse, error) {
	// Health is served from the root, not under BasePath — derive the health
	// URL by stripping the path suffix from BaseURL if needed.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.healthURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("Health: build request: %w", err)
	}

	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Health: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Health: unexpected status %d: %s", resp.StatusCode, body)
	}

	var out HealthResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("Health: decode response: %w", err)
	}
	return &out, nil
}

// healthURL derives the /health path from BaseURL by stripping any API path
// suffix (e.g. "/api/v1" → "<scheme>://<host>/health").
func (m *ManagementClient) healthURL() string {
	// Best-effort: strip common base paths. If BaseURL is already a host root
	// just append /health.
	base := m.cfg.BaseURL
	// Walk backwards to find the scheme+host boundary.
	// e.g. "https://host:8084/api/v1" → "https://host:8084/health"
	for _, suffix := range []string{"/api/v1", "/api/v2", "/api"} {
		if len(base) > len(suffix) && base[len(base)-len(suffix):] == suffix {
			return base[:len(base)-len(suffix)] + "/health"
		}
	}
	return base + "/health"
}

// ── HTTP helper ───────────────────────────────────────────────────────────────

// doJSON performs an HTTP request, encoding bodyIn as JSON (when non-nil) and
// decoding the response body into out (when non-nil). It maps common HTTP
// error codes to SDK sentinel errors.
func (m *ManagementClient) doJSON(ctx context.Context, method, path string, bodyIn, out any) error {
	var body io.Reader
	if bodyIn != nil {
		raw, err := json.Marshal(bodyIn)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, m.cfg.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if bodyIn != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if m.cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+m.cfg.AuthToken)
	}

	// Propagate trace ID if present in context.
	if traceID, ok := ctx.Value(contextKeyTraceID{}).(string); ok && traceID != "" {
		req.Header.Set("X-Trace-ID", traceID)
	}

	resp, err := m.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		// success
	case http.StatusNotFound:
		return ErrLeaseNotFound
	case http.StatusUnauthorized:
		return ErrUnauthorized
	default:
		return fmt.Errorf("server returned %d %s: %s", resp.StatusCode, resp.Status, respBody)
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// contextKeyTraceID is the same key used by the internal utils package so that
// a trace ID injected by the caller's framework flows through to Hyphae.
type contextKeyTraceID struct{}

// WithTraceID returns a copy of ctx carrying a trace ID that ManagementClient
// will forward in the X-Trace-ID request header.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, contextKeyTraceID{}, traceID)
}
