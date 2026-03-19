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

// ── Channel types ─────────────────────────────────────────────────────────────

// ChannelStatus describes the lifecycle state of a relay channel.
type ChannelStatus string

const (
	ChannelStatusPending ChannelStatus = "pending" // issued, listener not yet connected
	ChannelStatusReady   ChannelStatus = "ready"   // listener connected, awaiting initiator
	ChannelStatusActive  ChannelStatus = "active"  // splice active, data flowing
	ChannelStatusClosed  ChannelStatus = "closed"  // completed or expired
	ChannelStatusError   ChannelStatus = "error"   // splice or auth failure
)

// Channel represents a relay channel between two agents.
type Channel struct {
	ChannelID      string        `json:"channel_id"`
	OrgID          string        `json:"org_id"`
	SourceServerID string        `json:"source_server_id"` // initiator
	DestServerID   string        `json:"dest_server_id"`   // listener
	Purpose        string        `json:"purpose"`
	Status         ChannelStatus `json:"status"`
	CreatedAt      time.Time     `json:"created_at"`
	ExpiresAt      time.Time     `json:"expires_at"`
	ActivatedAt    *time.Time    `json:"activated_at,omitempty"`
	BytesRelayed   int64         `json:"bytes_relayed"`
}

// RegisterChannelRequest is the body server_api sends to POST /api/v1/channels.
// server_api is the sole authority — hyphae only stores the record.
type RegisterChannelRequest = Channel

// RegisterChannelResponse is returned by POST /api/v1/channels.
type RegisterChannelResponse struct {
	Channel *Channel `json:"channel"`
}

// ListChannelsResponse wraps a slice of channels.
type ListChannelsResponse struct {
	Channels []*Channel `json:"channels"`
	Count    int        `json:"count"`
}

// ChannelGrant holds the verified claims extracted from a channel grant JWT.
type ChannelGrant struct {
	ChannelID      string
	OrgID          string
	SourceServerID string
	DestServerID   string
	Purpose        string
	Nonce          string
}

// ListenerRegistration represents an agent connected as a channel listener.
type ListenerRegistration struct {
	ServerID    string    `json:"server_id"`
	OrgID       string    `json:"org_id"`
	ConnectedAt time.Time `json:"connected_at"`
}

// ListListenersResponse wraps active listener registrations.
type ListListenersResponse struct {
	Listeners []*ListenerRegistration `json:"listeners"`
	Count     int                     `json:"count"`
}
