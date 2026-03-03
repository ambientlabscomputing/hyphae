package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ambientlabscomputing/hyphae/sdk"
)

// ────────────────────────────────────────────────────────────────────────────
// helpers
// ────────────────────────────────────────────────────────────────────────────

func mockLease(id string) *sdk.Lease {
	return &sdk.Lease{
		LeaseID:  id,
		OrgID:    "org-1",
		Hostname: "host.example",
		Status:   sdk.LeaseStatusPending,
	}
}

// startMockManagementServer returns a test HTTP server that simulates the
// Hyphae management API.  All routes require the bearer token to match
// `token` except GET /health which is open.
func startMockManagementServer(t *testing.T, token string) *httptest.Server {
	t.Helper()

	lease := mockLease("lease-abc")
	conn := sdk.TunnelConnection{
		LeaseID:    lease.LeaseID,
		RemoteAddr: "10.0.0.1:9090",
	}

	mux := http.NewServeMux()

	checkAuth := func(w http.ResponseWriter, r *http.Request) bool {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
		return true
	}

	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Fatalf("writeJSON: %v", err)
		}
	}

	// POST /api/v1/leases
	mux.HandleFunc("POST /api/v1/leases", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		var req sdk.IssueLeaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := sdk.IssueLeaseResponse{Lease: lease}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, resp)
	})

	// GET /api/v1/leases
	mux.HandleFunc("GET /api/v1/leases", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		writeJSON(w, sdk.ListLeasesResponse{Leases: []*sdk.Lease{lease}})
	})

	// GET /api/v1/leases/{id}
	mux.HandleFunc("GET /api/v1/leases/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		id := r.PathValue("id")
		if id != lease.LeaseID {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, lease)
	})

	// DELETE /api/v1/leases/{id}
	mux.HandleFunc("DELETE /api/v1/leases/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		id := r.PathValue("id")
		if id != lease.LeaseID {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /api/v1/connections
	mux.HandleFunc("GET /api/v1/connections", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(w, r) {
			return
		}
		writeJSON(w, sdk.ListConnectionsResponse{Connections: []*sdk.TunnelConnection{&conn}})
	})

	// GET /health
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, sdk.HealthResponse{Status: "ok"})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestManagementClient(t *testing.T, srv *httptest.Server, token string) *sdk.ManagementClient {
	t.Helper()
	cfg := sdk.ManagementClientConfig{
		BaseURL:    srv.URL + "/api/v1",
		AuthToken:  token,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
	c, err := sdk.NewManagementClient(cfg)
	if err != nil {
		t.Fatalf("NewManagementClient: %v", err)
	}
	return c
}

// ────────────────────────────────────────────────────────────────────────────
// tests
// ────────────────────────────────────────────────────────────────────────────

func TestManagementClient_InvalidConfig(t *testing.T) {
	_, err := sdk.NewManagementClient(sdk.ManagementClientConfig{})
	if !errors.Is(err, sdk.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestManagementClient_IssueLease(t *testing.T) {
	srv := startMockManagementServer(t, "tok-1")
	c := newTestManagementClient(t, srv, "tok-1")

	lease, err := c.IssueLease(context.Background(), sdk.IssueLeaseRequest{
		OrgID:    "org-1",
		Hostname: "host.example",
	})
	if err != nil {
		t.Fatalf("IssueLease: %v", err)
	}
	if lease == nil {
		t.Fatal("expected lease in response")
	}
	if lease.LeaseID != "lease-abc" {
		t.Errorf("unexpected lease ID %q", lease.LeaseID)
	}
}

func TestManagementClient_GetLease(t *testing.T) {
	srv := startMockManagementServer(t, "tok-1")
	c := newTestManagementClient(t, srv, "tok-1")

	lease, err := c.GetLease(context.Background(), "lease-abc")
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if lease.LeaseID != "lease-abc" {
		t.Errorf("unexpected lease ID %q", lease.LeaseID)
	}
}

func TestManagementClient_GetLease_NotFound(t *testing.T) {
	srv := startMockManagementServer(t, "tok-1")
	c := newTestManagementClient(t, srv, "tok-1")

	_, err := c.GetLease(context.Background(), "does-not-exist")
	if !errors.Is(err, sdk.ErrLeaseNotFound) {
		t.Fatalf("expected ErrLeaseNotFound, got %v", err)
	}
}

func TestManagementClient_ListLeases(t *testing.T) {
	srv := startMockManagementServer(t, "tok-1")
	c := newTestManagementClient(t, srv, "tok-1")

	leases, err := c.ListLeases(context.Background())
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(leases) != 1 {
		t.Fatalf("expected 1 lease, got %d", len(leases))
	}
	if leases[0].LeaseID != "lease-abc" {
		t.Errorf("unexpected lease ID %q", leases[0].LeaseID)
	}
}

func TestManagementClient_RevokeLease(t *testing.T) {
	srv := startMockManagementServer(t, "tok-1")
	c := newTestManagementClient(t, srv, "tok-1")

	if err := c.RevokeLease(context.Background(), "lease-abc"); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}
}

func TestManagementClient_RevokeLease_NotFound(t *testing.T) {
	srv := startMockManagementServer(t, "tok-1")
	c := newTestManagementClient(t, srv, "tok-1")

	err := c.RevokeLease(context.Background(), "ghost-lease")
	if !errors.Is(err, sdk.ErrLeaseNotFound) {
		t.Fatalf("expected ErrLeaseNotFound, got %v", err)
	}
}

func TestManagementClient_ListConnections(t *testing.T) {
	srv := startMockManagementServer(t, "tok-1")
	c := newTestManagementClient(t, srv, "tok-1")

	conns, err := c.ListConnections(context.Background())
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(conns))
	}
	if conns[0].LeaseID != "lease-abc" {
		t.Errorf("unexpected lease ID in connection %q", conns[0].LeaseID)
	}
}

func TestManagementClient_Health(t *testing.T) {
	srv := startMockManagementServer(t, "tok-1")
	c := newTestManagementClient(t, srv, "tok-1")

	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("unexpected status %q", h.Status)
	}
}

func TestManagementClient_Unauthorized(t *testing.T) {
	srv := startMockManagementServer(t, "correct-token")
	// Create client with wrong token.
	cfg := sdk.ManagementClientConfig{
		BaseURL:    srv.URL + "/api/v1",
		AuthToken:  "wrong-token",
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
	c, err := sdk.NewManagementClient(cfg)
	if err != nil {
		t.Fatalf("NewManagementClient: %v", err)
	}

	_, err = c.ListLeases(context.Background())
	if !errors.Is(err, sdk.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestManagementClient_WithTraceID(t *testing.T) {
	var capturedTraceID string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/leases", func(w http.ResponseWriter, r *http.Request) {
		capturedTraceID = r.Header.Get("X-Trace-ID")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(sdk.ListLeasesResponse{}); err != nil {
			t.Fatalf("encode: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := sdk.ManagementClientConfig{
		BaseURL:    srv.URL + "/api/v1",
		AuthToken:  "any",
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
	c, err := sdk.NewManagementClient(cfg)
	if err != nil {
		t.Fatalf("NewManagementClient: %v", err)
	}

	const wantTraceID = "trace-xyz-123"
	ctx := sdk.WithTraceID(context.Background(), wantTraceID)
	if _, err := c.ListLeases(ctx); err != nil {
		t.Fatalf("ListLeases: %v", err)
	}

	if !strings.Contains(capturedTraceID, wantTraceID) {
		t.Errorf("X-Trace-ID not propagated: got %q, want %q", capturedTraceID, wantTraceID)
	}
}
