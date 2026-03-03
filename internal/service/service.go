package service

import (
	"context"
	"fmt"
	"time"

	"github.com/ambientlabscomputing/hyphae/internal/repository"
	"github.com/ambientlabscomputing/hyphae/sdk"
	"github.com/google/uuid"
	"github.com/hashicorp/yamux"
)

// Service is the business logic interface for Hyphae.
type Service interface {
	Health(ctx context.Context) (*map[string]interface{}, error)

	// Lease management
	IssueLease(ctx context.Context, req sdk.IssueLeaseRequest) (*sdk.Lease, error)
	GetLease(ctx context.Context, leaseID string) (*sdk.Lease, error)
	ListLeases(ctx context.Context) ([]*sdk.Lease, error)
	RevokeLease(ctx context.Context, leaseID string) error

	// Tunnel operations
	BindTunnel(ctx context.Context, leaseID string, session *yamux.Session) (string, error)
	UnbindTunnel(ctx context.Context, connectionID string) error

	// Connection queries
	ListConnections(ctx context.Context) ([]*sdk.TunnelConnection, error)
}

// AppService implements Service.
type AppService struct {
	repo repository.Repository
}

func NewService(ctx context.Context, repo repository.Repository) (Service, error) {
	return &AppService{repo: repo}, nil
}

func (s *AppService) IssueLease(ctx context.Context, req sdk.IssueLeaseRequest) (*sdk.Lease, error) {
	if req.Hostname == "" {
		return nil, fmt.Errorf("hostname is required")
	}
	if req.OrgID == "" {
		return nil, fmt.Errorf("org_id is required")
	}
	lease := &sdk.Lease{
		LeaseID:      uuid.NewString(),
		ExposureID:   req.ExposureID,
		OrgID:        req.OrgID,
		ServerID:     req.ServerID,
		DeploymentID: req.DeploymentID,
		ServiceName:  req.ServiceName,
		Hostname:     req.Hostname,
		TargetPort:   req.TargetPort,
		Status:       sdk.LeaseStatusPending,
		CreatedAt:    time.Now(),
	}
	if req.TTLSeconds > 0 {
		t := time.Now().Add(time.Duration(req.TTLSeconds) * time.Second)
		lease.ExpiresAt = &t
	}
	if err := s.repo.IssueLease(ctx, lease); err != nil {
		return nil, err
	}
	return lease, nil
}

func (s *AppService) GetLease(ctx context.Context, leaseID string) (*sdk.Lease, error) {
	return s.repo.GetLease(ctx, leaseID)
}

func (s *AppService) ListLeases(ctx context.Context) ([]*sdk.Lease, error) {
	return s.repo.ListLeases(ctx)
}

func (s *AppService) RevokeLease(ctx context.Context, leaseID string) error {
	return s.repo.RevokeLease(ctx, leaseID)
}

func (s *AppService) BindTunnel(ctx context.Context, leaseID string, session *yamux.Session) (string, error) {
	return s.repo.BindLease(ctx, leaseID, session)
}

func (s *AppService) UnbindTunnel(ctx context.Context, connectionID string) error {
	return s.repo.RemoveConnection(ctx, connectionID)
}

func (s *AppService) ListConnections(ctx context.Context) ([]*sdk.TunnelConnection, error) {
	return s.repo.ListConnections(ctx)
}
