package service

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/ambientlabscomputing/hyphae/internal/repository"
	"github.com/ambientlabscomputing/hyphae/sdk"
	"github.com/google/uuid"
	"github.com/hashicorp/yamux"
)

// hostnamePattern accepts valid DNS labels: letters, digits, dots, hyphens.
// Must not start or end with a hyphen or dot.
var hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.\-]*[a-zA-Z0-9])?$`)

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

// ServiceConfig holds policy limits for the service layer.
type ServiceConfig struct {
	MaxLeasesPerOrg int   // 0 = 100 default
	MaxTTLSeconds   int64 // 0 = 86400 default
}

// AppService implements Service.
type AppService struct {
	repo repository.Repository
	cfg  ServiceConfig
}

func NewService(ctx context.Context, repo repository.Repository, cfg ServiceConfig) (Service, error) {
	if cfg.MaxLeasesPerOrg == 0 {
		cfg.MaxLeasesPerOrg = 100
	}
	if cfg.MaxTTLSeconds == 0 {
		cfg.MaxTTLSeconds = 86400
	}
	return &AppService{repo: repo, cfg: cfg}, nil
}

func (s *AppService) IssueLease(ctx context.Context, req sdk.IssueLeaseRequest) (*sdk.Lease, error) {
	if req.Hostname == "" {
		return nil, fmt.Errorf("hostname is required")
	}
	if req.OrgID == "" {
		return nil, fmt.Errorf("org_id is required")
	}

	// Validate hostname format.
	if len(req.Hostname) > 253 {
		return nil, fmt.Errorf("hostname exceeds 253 characters")
	}
	if !hostnamePattern.MatchString(req.Hostname) {
		return nil, fmt.Errorf("hostname %q contains invalid characters (only letters, digits, dots, hyphens allowed)", req.Hostname)
	}

	// Enforce maximum TTL.
	if int64(req.TTLSeconds) > s.cfg.MaxTTLSeconds {
		return nil, fmt.Errorf("ttl_seconds %d exceeds maximum allowed value of %d", req.TTLSeconds, s.cfg.MaxTTLSeconds)
	}

	// Enforce per-org lease limit.
	allLeases, err := s.repo.ListLeases(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check org lease count: %w", err)
	}
	orgCount := 0
	for _, l := range allLeases {
		if l.OrgID == req.OrgID {
			orgCount++
		}
	}
	if orgCount >= s.cfg.MaxLeasesPerOrg {
		return nil, fmt.Errorf("org %q has reached the maximum of %d active leases", req.OrgID, s.cfg.MaxLeasesPerOrg)
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
