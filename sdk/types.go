package sdk

import "time"

// Exposure is the payload sent by server_api when requesting a new lease.
type Exposure struct {
	ExposureID   string
	OrgID        string
	ServerID     string
	DeploymentID string
	ServiceName  string
	TargetPort   int
	Hostname     string
}

// LeaseStatus describes the lifecycle state of a lease.
type LeaseStatus string

const (
	LeaseStatusPending LeaseStatus = "pending" // issued, not yet tunnel-bound
	LeaseStatusBound   LeaseStatus = "bound"   // tunnel connected, traffic forwarding
	LeaseStatusError   LeaseStatus = "error"   // bind failed
)

// Lease tracks a single hostname → tunnel mapping.
type Lease struct {
	LeaseID      string      `json:"lease_id"`
	ExposureID   string      `json:"exposure_id"`
	OrgID        string      `json:"org_id"`
	ServerID     string      `json:"server_id"`
	DeploymentID string      `json:"deployment_id"`
	ServiceName  string      `json:"service_name"`
	Hostname     string      `json:"hostname"`
	TargetPort   int         `json:"target_port"`
	Status       LeaseStatus `json:"status"`
	CreatedAt    time.Time   `json:"created_at"`
	BoundAt      *time.Time  `json:"bound_at,omitempty"`
	ExpiresAt    *time.Time  `json:"expires_at,omitempty"`
}

// TunnelConnection represents a live tunnel session from an MMA node.
type TunnelConnection struct {
	ConnectionID string    `json:"connection_id"`
	LeaseID      string    `json:"lease_id"`
	ServerID     string    `json:"server_id"`
	OrgID        string    `json:"org_id"`
	RemoteAddr   string    `json:"remote_addr"`
	ConnectedAt  time.Time `json:"connected_at"`
}

// IssueLeaseRequest is the body server_api sends to POST /api/v1/leases.
type IssueLeaseRequest struct {
	ExposureID   string `json:"exposure_id"`
	OrgID        string `json:"org_id"`
	ServerID     string `json:"server_id"`
	DeploymentID string `json:"deployment_id"`
	ServiceName  string `json:"service_name"`
	Hostname     string `json:"hostname"`
	TargetPort   int    `json:"target_port"`
	TTLSeconds   int    `json:"ttl_seconds,omitempty"`
}

// IssueLeaseResponse is returned by POST /api/v1/leases.
type IssueLeaseResponse struct {
	Lease *Lease `json:"lease"`
}

// ListLeasesResponse wraps a slice of leases.
type ListLeasesResponse struct {
	Leases []*Lease `json:"leases"`
	Count  int      `json:"count"`
}

// ListConnectionsResponse wraps active tunnel connections.
type ListConnectionsResponse struct {
	Connections []*TunnelConnection `json:"connections"`
	Count       int                 `json:"count"`
}

// HealthResponse is returned by GET /health.
type HealthResponse struct {
	Status      string `json:"status"`
	Leases      int    `json:"leases"`
	Connections int    `json:"connections"`
}
