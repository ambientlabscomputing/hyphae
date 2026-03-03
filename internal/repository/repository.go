package repository

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/ambientlabscomputing/hyphae/sdk"
)

// Repository defines the data-access contract for Hyphae's in-memory store.
type Repository interface {
	Health(ctx context.Context) (*map[string]interface{}, error)

	// Lease operations
	IssueLease(ctx context.Context, lease *sdk.Lease) error
	GetLease(ctx context.Context, leaseID string) (*sdk.Lease, error)
	ListLeases(ctx context.Context) ([]*sdk.Lease, error)
	RevokeLease(ctx context.Context, leaseID string) error
	BindLease(ctx context.Context, leaseID string, conn net.Conn) error
	GetConnByHostname(ctx context.Context, hostname string) (net.Conn, error)

	// Connection operations
	ListConnections(ctx context.Context) ([]*sdk.TunnelConnection, error)
	GetConnection(ctx context.Context, connectionID string) (*sdk.TunnelConnection, error)
	RemoveConnection(ctx context.Context, connectionID string) error
}

// tunnelSession holds both the metadata and the live connection for a bound lease.
type tunnelSession struct {
	meta *sdk.TunnelConnection
	conn net.Conn
}

// MemoryRepository is a thread-safe in-memory implementation of Repository.
type MemoryRepository struct {
	mu           sync.RWMutex
	leases       map[string]*sdk.Lease     // keyed by LeaseID
	byHost       map[string]string         // hostname → LeaseID
	sessions     map[string]*tunnelSession // keyed by ConnectionID
	leaseSession map[string]string         // LeaseID → ConnectionID
}

func NewRepository(ctx context.Context) Repository {
	return &MemoryRepository{
		leases:       make(map[string]*sdk.Lease),
		byHost:       make(map[string]string),
		sessions:     make(map[string]*tunnelSession),
		leaseSession: make(map[string]string),
	}
}

func (r *MemoryRepository) Health(ctx context.Context) (*map[string]interface{}, error) {
	r.mu.RLock()
	leaseCount := len(r.leases)
	sessionCount := len(r.sessions)
	r.mu.RUnlock()
	m := map[string]interface{}{
		"status":      "healthy",
		"leases":      leaseCount,
		"connections": sessionCount,
	}
	return &m, nil
}

func (r *MemoryRepository) IssueLease(ctx context.Context, lease *sdk.Lease) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byHost[lease.Hostname]; exists {
		return fmt.Errorf("hostname %q already has an active lease", lease.Hostname)
	}
	r.leases[lease.LeaseID] = lease
	r.byHost[lease.Hostname] = lease.LeaseID
	return nil
}

func (r *MemoryRepository) GetLease(ctx context.Context, leaseID string) (*sdk.Lease, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	l, ok := r.leases[leaseID]
	if !ok {
		return nil, fmt.Errorf("lease %q not found", leaseID)
	}
	return l, nil
}

func (r *MemoryRepository) ListLeases(ctx context.Context) ([]*sdk.Lease, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*sdk.Lease, 0, len(r.leases))
	for _, l := range r.leases {
		out = append(out, l)
	}
	return out, nil
}

func (r *MemoryRepository) RevokeLease(ctx context.Context, leaseID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.leases[leaseID]
	if !ok {
		return fmt.Errorf("lease %q not found", leaseID)
	}
	if connID, ok := r.leaseSession[leaseID]; ok {
		if s, ok := r.sessions[connID]; ok {
			s.conn.Close()
			delete(r.sessions, connID)
		}
		delete(r.leaseSession, leaseID)
	}
	delete(r.byHost, l.Hostname)
	delete(r.leases, leaseID)
	return nil
}

func (r *MemoryRepository) BindLease(ctx context.Context, leaseID string, conn net.Conn) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.leases[leaseID]
	if !ok {
		return fmt.Errorf("lease %q not found", leaseID)
	}
	if oldConnID, ok := r.leaseSession[leaseID]; ok {
		if s, ok := r.sessions[oldConnID]; ok {
			s.conn.Close()
			delete(r.sessions, oldConnID)
		}
	}
	now := time.Now()
	l.Status = sdk.LeaseStatusBound
	l.BoundAt = &now
	connID := fmt.Sprintf("%s-%d", leaseID, now.UnixNano())
	tc := &sdk.TunnelConnection{
		ConnectionID: connID,
		LeaseID:      leaseID,
		ServerID:     l.ServerID,
		OrgID:        l.OrgID,
		RemoteAddr:   conn.RemoteAddr().String(),
		ConnectedAt:  now,
	}
	r.sessions[connID] = &tunnelSession{meta: tc, conn: conn}
	r.leaseSession[leaseID] = connID
	return nil
}

func (r *MemoryRepository) GetConnByHostname(ctx context.Context, hostname string) (net.Conn, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	leaseID, ok := r.byHost[hostname]
	if !ok {
		return nil, fmt.Errorf("no lease for hostname %q", hostname)
	}
	connID, ok := r.leaseSession[leaseID]
	if !ok {
		return nil, fmt.Errorf("lease %q is not bound", leaseID)
	}
	s, ok := r.sessions[connID]
	if !ok {
		return nil, fmt.Errorf("session %q not found", connID)
	}
	return s.conn, nil
}

func (r *MemoryRepository) ListConnections(ctx context.Context) ([]*sdk.TunnelConnection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*sdk.TunnelConnection, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s.meta)
	}
	return out, nil
}

func (r *MemoryRepository) GetConnection(ctx context.Context, connectionID string) (*sdk.TunnelConnection, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[connectionID]
	if !ok {
		return nil, fmt.Errorf("connection %q not found", connectionID)
	}
	return s.meta, nil
}

func (r *MemoryRepository) RemoveConnection(ctx context.Context, connectionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[connectionID]
	if !ok {
		return nil
	}
	s.conn.Close()
	leaseID := s.meta.LeaseID
	delete(r.sessions, connectionID)
	if r.leaseSession[leaseID] == connectionID {
		delete(r.leaseSession, leaseID)
		if l, ok := r.leases[leaseID]; ok {
			l.Status = sdk.LeaseStatusPending
			l.BoundAt = nil
		}
	}
	return nil
}
