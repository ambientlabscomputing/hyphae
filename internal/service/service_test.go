package service_test

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/ambientlabscomputing/hyphae/internal/repository"
	"github.com/ambientlabscomputing/hyphae/internal/service"
	"github.com/ambientlabscomputing/hyphae/sdk"
	"github.com/hashicorp/yamux"
)

func newSvc(t *testing.T) service.Service {
	t.Helper()
	ctx := context.Background()
	repo := repository.NewRepository(ctx)
	svc, err := service.NewService(ctx, repo, service.ServiceConfig{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func makeSession(t *testing.T) (*yamux.Session, func()) {
	t.Helper()
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = false
	cfg.LogOutput = io.Discard
	c, s := net.Pipe()
	client, err := yamux.Client(c, cfg)
	if err != nil {
		c.Close()
		s.Close()
		t.Fatalf("yamux.Client: %v", err)
	}
	return client, func() { client.Close(); s.Close() }
}

func validReq(hostname string) sdk.IssueLeaseRequest {
	return sdk.IssueLeaseRequest{OrgID: "org-1", ServerID: "srv-1", Hostname: hostname}
}

func TestService_Health(t *testing.T) {
	svc := newSvc(t)
	h, err := svc.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if (*h)["status"] != "healthy" {
		t.Errorf("want healthy, got %v", (*h)["status"])
	}
}

func TestService_IssueLease_ReturnsLease(t *testing.T) {
	svc := newSvc(t)
	l, err := svc.IssueLease(context.Background(), validReq("foo.example.com"))
	if err != nil {
		t.Fatalf("IssueLease: %v", err)
	}
	if l == nil {
		t.Fatal("expected non-nil lease")
	}
	if l.LeaseID == "" {
		t.Error("LeaseID should be set")
	}
	if l.OrgID != "org-1" {
		t.Errorf("OrgID want org-1, got %s", l.OrgID)
	}
	if l.Status != sdk.LeaseStatusPending {
		t.Errorf("want pending, got %s", l.Status)
	}
}

func TestService_IssueLease_MissingHostname(t *testing.T) {
	svc := newSvc(t)
	if _, err := svc.IssueLease(context.Background(), validReq("")); err == nil {
		t.Fatal("expected error for empty hostname")
	}
}

func TestService_IssueLease_MissingOrgID(t *testing.T) {
	svc := newSvc(t)
	req := sdk.IssueLeaseRequest{ServerID: "s", Hostname: "h.example.com"}
	if _, err := svc.IssueLease(context.Background(), req); err == nil {
		t.Fatal("expected error for empty org_id")
	}
}

func TestService_IssueLease_DuplicateHostname(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	if _, err := svc.IssueLease(ctx, validReq("dup.example.com")); err != nil {
		t.Fatalf("first IssueLease: %v", err)
	}
	if _, err := svc.IssueLease(ctx, validReq("dup.example.com")); err == nil {
		t.Fatal("expected error on duplicate hostname")
	}
}

func TestService_GetLease_Found(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	l, _ := svc.IssueLease(ctx, validReq("get.example.com"))
	got, err := svc.GetLease(ctx, l.LeaseID)
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if got.LeaseID != l.LeaseID {
		t.Errorf("want %s, got %s", l.LeaseID, got.LeaseID)
	}
}

func TestService_GetLease_NotFound(t *testing.T) {
	svc := newSvc(t)
	if _, err := svc.GetLease(context.Background(), "ghost"); err == nil {
		t.Fatal("expected error")
	}
}

func TestService_ListLeases_Empty(t *testing.T) {
	svc := newSvc(t)
	leases, err := svc.ListLeases(context.Background())
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(leases) != 0 {
		t.Errorf("want 0, got %d", len(leases))
	}
}

func TestService_ListLeases_AfterIssue(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	_, _ = svc.IssueLease(ctx, validReq("a.example.com"))
	_, _ = svc.IssueLease(ctx, validReq("b.example.com"))
	leases, err := svc.ListLeases(ctx)
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(leases) != 2 {
		t.Errorf("want 2, got %d", len(leases))
	}
}

func TestService_RevokeLease_Success(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	l, _ := svc.IssueLease(ctx, validReq("rev.example.com"))
	if err := svc.RevokeLease(ctx, l.LeaseID); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}
	if _, err := svc.GetLease(ctx, l.LeaseID); err == nil {
		t.Fatal("lease should be gone")
	}
}

func TestService_RevokeLease_NotFound(t *testing.T) {
	svc := newSvc(t)
	if err := svc.RevokeLease(context.Background(), "ghost"); err == nil {
		t.Fatal("expected error")
	}
}

func TestService_BindTunnel_UpdatesStatus(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	l, _ := svc.IssueLease(ctx, validReq("bind.example.com"))
	session, cleanup := makeSession(t)
	defer cleanup()
	if _, err := svc.BindTunnel(ctx, l.LeaseID, session); err != nil {
		t.Fatalf("BindTunnel: %v", err)
	}
	got, _ := svc.GetLease(ctx, l.LeaseID)
	if got.Status != sdk.LeaseStatusBound {
		t.Errorf("want bound, got %s", got.Status)
	}
}

func TestService_BindTunnel_UnknownLease(t *testing.T) {
	svc := newSvc(t)
	session, cleanup := makeSession(t)
	defer cleanup()
	if _, err := svc.BindTunnel(context.Background(), "ghost", session); err == nil {
		t.Fatal("expected error")
	}
}

func TestService_ListConnections_Empty(t *testing.T) {
	svc := newSvc(t)
	conns, err := svc.ListConnections(context.Background())
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	if len(conns) != 0 {
		t.Errorf("want 0, got %d", len(conns))
	}
}

func TestService_ListConnections_AfterBind(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	l, _ := svc.IssueLease(ctx, validReq("conn.example.com"))
	session, cleanup := makeSession(t)
	defer cleanup()
	_, _ = svc.BindTunnel(ctx, l.LeaseID, session)
	conns, err := svc.ListConnections(ctx)
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	if len(conns) != 1 {
		t.Errorf("want 1, got %d", len(conns))
	}
	if conns[0].LeaseID != l.LeaseID {
		t.Errorf("LeaseID want %s, got %s", l.LeaseID, conns[0].LeaseID)
	}
}

func TestService_ListConnections_ConnectionHasFields(t *testing.T) {
	svc := newSvc(t)
	ctx := context.Background()
	l, _ := svc.IssueLease(ctx, validReq("fields.example.com"))
	session, cleanup := makeSession(t)
	defer cleanup()
	_, _ = svc.BindTunnel(ctx, l.LeaseID, session)
	conns, _ := svc.ListConnections(ctx)
	if len(conns) == 0 {
		t.Fatal("expected connections")
	}
	tc := conns[0]
	if tc.ConnectionID == "" {
		t.Error("ConnectionID should be non-empty")
	}
	if tc.OrgID != "org-1" {
		t.Errorf("OrgID want org-1, got %s", tc.OrgID)
	}
	if tc.ConnectedAt.IsZero() {
		t.Error("ConnectedAt should be set")
	}
}
