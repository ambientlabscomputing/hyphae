package repository_test

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/ambientlabscomputing/hyphae/internal/repository"
	"github.com/ambientlabscomputing/hyphae/sdk"
	"github.com/hashicorp/yamux"
)

func newRepo(t *testing.T) repository.Repository {
	t.Helper()
	return repository.NewRepository(context.Background())
}

// makeSession returns a yamux client session backed by a net.Pipe for testing.
// The returned cleanup func closes both sides.
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

func sampleLease(id, hostname string) *sdk.Lease {
	return &sdk.Lease{LeaseID: id, OrgID: "org-1", ServerID: "srv-1", Hostname: hostname, Status: sdk.LeaseStatusPending}
}

func TestHealth_ReturnsOK(t *testing.T) {
	repo := newRepo(t)
	h, err := repo.Health(context.Background())
	if err != nil {
		t.Fatalf("Health error: %v", err)
	}
	if (*h)["status"] != "healthy" {
		t.Errorf("want healthy, got %v", (*h)["status"])
	}
}

func TestHealth_ReflectsLeaseCount(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("l1", "a.example.com"))
	_ = repo.IssueLease(ctx, sampleLease("l2", "b.example.com"))
	h, _ := repo.Health(ctx)
	if (*h)["leases"] != 2 {
		t.Errorf("want 2, got %v", (*h)["leases"])
	}
}

func TestIssueLease_Success(t *testing.T) {
	repo := newRepo(t)
	if err := repo.IssueLease(context.Background(), sampleLease("l1", "foo.example.com")); err != nil {
		t.Fatalf("IssueLease: %v", err)
	}
}

func TestIssueLease_DuplicateHostname(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("l1", "dup.example.com"))
	if err := repo.IssueLease(ctx, sampleLease("l2", "dup.example.com")); err == nil {
		t.Fatal("expected error on duplicate hostname")
	}
}

func TestGetLease_Found(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("lid", "get.example.com"))
	got, err := repo.GetLease(ctx, "lid")
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if got.LeaseID != "lid" {
		t.Errorf("want lid, got %s", got.LeaseID)
	}
}

func TestGetLease_NotFound(t *testing.T) {
	repo := newRepo(t)
	if _, err := repo.GetLease(context.Background(), "nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestListLeases_Empty(t *testing.T) {
	repo := newRepo(t)
	leases, err := repo.ListLeases(context.Background())
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(leases) != 0 {
		t.Errorf("want 0, got %d", len(leases))
	}
}

func TestListLeases_Multiple(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("l1", "a.example.com"))
	_ = repo.IssueLease(ctx, sampleLease("l2", "b.example.com"))
	_ = repo.IssueLease(ctx, sampleLease("l3", "c.example.com"))
	leases, err := repo.ListLeases(ctx)
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(leases) != 3 {
		t.Errorf("want 3, got %d", len(leases))
	}
}

func TestRevokeLease_Success(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("lrev", "rev.example.com"))
	if err := repo.RevokeLease(ctx, "lrev"); err != nil {
		t.Fatalf("RevokeLease: %v", err)
	}
	if _, err := repo.GetLease(ctx, "lrev"); err == nil {
		t.Fatal("lease should be gone")
	}
}

func TestRevokeLease_NotFound(t *testing.T) {
	repo := newRepo(t)
	if err := repo.RevokeLease(context.Background(), "ghost"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRevokeLease_ClosesConnection(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("lbound", "bound.example.com"))
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = false
	cfg.LogOutput = io.Discard
	c, sRaw := net.Pipe()
	session, _ := yamux.Client(c, cfg)
	_, _ = repo.BindLease(ctx, "lbound", session)
	_ = repo.RevokeLease(ctx, "lbound")
	buf := make([]byte, 1)
	if _, err := sRaw.Read(buf); err == nil {
		t.Fatal("expected closed pipe after revocation")
	}
}

func TestBindLease_SetsStatusBound(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("lbind", "bind.example.com"))
	session, cleanup := makeSession(t)
	defer cleanup()
	if _, err := repo.BindLease(ctx, "lbind", session); err != nil {
		t.Fatalf("BindLease: %v", err)
	}
	got, _ := repo.GetLease(ctx, "lbind")
	if got.Status != sdk.LeaseStatusBound {
		t.Errorf("want bound, got %s", got.Status)
	}
	if got.BoundAt == nil {
		t.Error("BoundAt should be set")
	}
}

func TestBindLease_UnknownLease(t *testing.T) {
	repo := newRepo(t)
	session, cleanup := makeSession(t)
	defer cleanup()
	if _, err := repo.BindLease(context.Background(), "missing", session); err == nil {
		t.Fatal("expected error binding unknown lease")
	}
}

func TestBindLease_RebindClosesOldConn(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("lreb", "rebind.example.com"))
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = false
	cfg.LogOutput = io.Discard
	c1, s1 := net.Pipe()
	session1, _ := yamux.Client(c1, cfg)
	defer s1.Close()
	_, _ = repo.BindLease(ctx, "lreb", session1)
	session2, cleanup2 := makeSession(t)
	defer cleanup2()
	_, _ = repo.BindLease(ctx, "lreb", session2)
	buf := make([]byte, 1)
	if _, err := s1.Read(buf); err == nil {
		t.Fatal("old conn should be closed on rebind")
	}
}

func TestGetConnByHostname_Success(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("lhost", "myhost.example.com"))
	session, cleanup := makeSession(t)
	defer cleanup()
	_, _ = repo.BindLease(ctx, "lhost", session)
	got, err := repo.GetSessionByHostname(ctx, "myhost.example.com")
	if err != nil {
		t.Fatalf("GetSessionByHostname: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil session")
	}
}

func TestGetConnByHostname_NoLease(t *testing.T) {
	repo := newRepo(t)
	if _, err := repo.GetSessionByHostname(context.Background(), "unknown.example.com"); err == nil {
		t.Fatal("expected error for unknown hostname")
	}
}

func TestGetConnByHostname_NotBound(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("lunb", "unbound.example.com"))
	if _, err := repo.GetSessionByHostname(ctx, "unbound.example.com"); err == nil {
		t.Fatal("expected error for unbound lease")
	}
}

func TestListConnections_Empty(t *testing.T) {
	repo := newRepo(t)
	conns, err := repo.ListConnections(context.Background())
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	if len(conns) != 0 {
		t.Errorf("want 0, got %d", len(conns))
	}
}

func TestListConnections_AfterBind(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("l1", "a.example.com"))
	_ = repo.IssueLease(ctx, sampleLease("l2", "b.example.com"))
	s1, cleanup1 := makeSession(t)
	defer cleanup1()
	s2, cleanup2 := makeSession(t)
	defer cleanup2()
	_, _ = repo.BindLease(ctx, "l1", s1)
	_, _ = repo.BindLease(ctx, "l2", s2)
	conns, err := repo.ListConnections(ctx)
	if err != nil {
		t.Fatalf("ListConnections: %v", err)
	}
	if len(conns) != 2 {
		t.Errorf("want 2, got %d", len(conns))
	}
}

func TestGetConnection_Found(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("lgc", "gc.example.com"))
	session, cleanup := makeSession(t)
	defer cleanup()
	_, _ = repo.BindLease(ctx, "lgc", session)
	conns, _ := repo.ListConnections(ctx)
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection")
	}
	tc, err := repo.GetConnection(ctx, conns[0].ConnectionID)
	if err != nil {
		t.Fatalf("GetConnection: %v", err)
	}
	if tc.LeaseID != "lgc" {
		t.Errorf("want lgc, got %s", tc.LeaseID)
	}
}

func TestGetConnection_NotFound(t *testing.T) {
	repo := newRepo(t)
	if _, err := repo.GetConnection(context.Background(), "nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRemoveConnection_ResetsLease(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	_ = repo.IssueLease(ctx, sampleLease("lrm", "rm.example.com"))
	session, cleanup := makeSession(t)
	defer cleanup()
	_, _ = repo.BindLease(ctx, "lrm", session)
	conns, _ := repo.ListConnections(ctx)
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection")
	}
	if err := repo.RemoveConnection(ctx, conns[0].ConnectionID); err != nil {
		t.Fatalf("RemoveConnection: %v", err)
	}
	l, _ := repo.GetLease(ctx, "lrm")
	if l.Status != sdk.LeaseStatusPending {
		t.Errorf("want pending, got %s", l.Status)
	}
	if l.BoundAt != nil {
		t.Error("BoundAt should be nil after removal")
	}
	conns, _ = repo.ListConnections(ctx)
	if len(conns) != 0 {
		t.Errorf("want 0 conns, got %d", len(conns))
	}
}

func TestRemoveConnection_Idempotent(t *testing.T) {
	repo := newRepo(t)
	if err := repo.RemoveConnection(context.Background(), "ghost"); err != nil {
		t.Fatalf("RemoveConnection on non-existent should not error: %v", err)
	}
}
