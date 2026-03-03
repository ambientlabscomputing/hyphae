# Hyphae Integration Plan

> **Epic:** UNDF-65 — Public Exposure via Hyphae  
> **Tasks:** UNDF-66, UNDF-67, UNDF-68  
> **Date:** March 3, 2026

---

## Overview

This plan integrates the completed Hyphae gateway into the rest of the Underleaf platform across three workstreams, designed to be implemented together as a cohesive feature. When complete, a developer will be able to run `ufctl deploy apply my-app.json --expose web:8080` (or declare `expose` in the manifest) and receive a public `https://<slug>.underleafapp.com` URL — backed by an mTLS reverse tunnel from the edge to Hyphae.

### End-to-End Flow (Target State)

```
Developer                  server_api              Hyphae :8084          Agent (UA-K)             MMA                    Hyphae :9090
    │                          │                       │                     │                     │                        │
    │  POST /deployments/:id/apply                     │                     │                     │                        │
    │  (manifest has expose)   │                       │                     │                     │                        │
    │─────────────────────────▸│                       │                     │                     │                        │
    │                          │  POST /api/v1/leases  │                     │                     │                        │
    │                          │──────────────────────▸│                     │                     │                        │
    │                          │◂──────────────────────│ Lease (pending)     │                     │                        │
    │                          │                       │                     │                     │                        │
    │                          │  create Exposure record (MongoDB)           │                     │                        │
    │                          │                       │                     │                     │                        │
    │                          │  Spine: deployments.apply.server.request    │                     │                        │
    │                          │  (payload includes exposures[])             │                     │                        │
    │                          │────────────────────────────────────────────▸│                     │                        │
    │                          │                       │                     │                     │                        │
    │                          │                       │                     │ forward exposure to  │                        │
    │                          │                       │                     │ MMA via UA event     │                        │
    │                          │                       │                     │────────────────────▸│                        │
    │                          │                       │                     │                     │                        │
    │                          │                       │                     │                     │  mTLS connect          │
    │                          │                       │                     │                     │  + HTTP Upgrade        │
    │                          │                       │                     │                     │  (X-Lease-ID)          │
    │                          │                       │                     │                     │───────────────────────▸│
    │                          │                       │                     │                     │◂───────────────────────│
    │                          │                       │                     │                     │  101 Switching Proto   │
    │                          │                       │                     │                     │                        │
    │                          │                       │                     │                     │  Spine: exposure.bound │
    │                          │                       │                     │◂────────────────────│                        │
    │                          │                       │                     │                     │                        │
    │                          │ Spine: exposure.status.update               │                     │                        │
    │                          │◂────────────────────────────────────────────│                     │                        │
    │                          │                       │                     │                     │                        │
    │                          │  update Exposure record (status: bound)     │                     │                        │
    │                          │                       │                     │                     │                        │
    │  200 OK { url: "https://my-app.underleafapp.com" }                    │                     │                        │
    │◂─────────────────────────│                       │                     │                     │                        │
```

---

## Shared Concepts

### Exposure Record

An **Exposure** is the platform-level abstraction for "make this service publicly accessible." It ties together a deployment service, a Hyphae lease, and a tunnel. The Exposure is the source of truth in `server_api`; the Lease is the source of truth in Hyphae.

```
Exposure (server_api, MongoDB)
├── exposure_id        (UUID, primary key)
├── org_id             (tenant isolation)
├── server_id          (target edge node)
├── deployment_id      (FK to AppDeployment)
├── service_name       (which service in the deployment)
├── target_port        (local port the service listens on)
├── hostname           (e.g. "my-app.underleafapp.com")
├── lease_id           (FK to Hyphae lease, set on issue)
├── status             (pending | bound | error | revoked)
├── public_url         (full https:// URL)
├── created_at / updated_at
└── error_message      (populated on failure)
```

### New Spine Topics

| Topic | QoS | Direction | Purpose |
|---|---|---|---|
| `exposure.bind.request` | COMMAND | server_api → agent | Tell agent to bind an exposure (agent→MMA→Hyphae tunnel) |
| `exposure.status.update` | CONTROL | agent → server_api | MMA reports bind success/failure back |

### New Entitlements

| Entitlement Key | Limit Key | Description |
|---|---|---|
| `public_exposures` | `slot_exposures` | Org-level capacity for concurrent public exposures |

---

## UNDF-66: Exposure Model & Orchestration (server_api + UA-K)

### Goal

Add exposure records and REST endpoints in `server_api`; replicate exposure state to the agent via Spine events; update statuses when the agent reports back.

### 6.1 — server_api: Exposure Data Model

**File:** `server_api/server_api/types/exposure.go` (new)

```go
type ExposureStatus string

const (
    ExposureStatusPending  ExposureStatus = "pending"
    ExposureStatusBound    ExposureStatus = "bound"
    ExposureStatusError    ExposureStatus = "error"
    ExposureStatusRevoked  ExposureStatus = "revoked"
)

type Exposure struct {
    ID            string         `json:"id" bson:"_id"`
    OrgID         string         `json:"org_id" bson:"org_id"`
    ServerID      string         `json:"server_id" bson:"server_id"`
    DeploymentID  string         `json:"deployment_id" bson:"deployment_id"`
    ServiceName   string         `json:"service_name" bson:"service_name"`
    TargetPort    int            `json:"target_port" bson:"target_port"`
    Hostname      string         `json:"hostname" bson:"hostname"`
    LeaseID       string         `json:"lease_id,omitempty" bson:"lease_id,omitempty"`
    Status        ExposureStatus `json:"status" bson:"status"`
    PublicURL     string         `json:"public_url,omitempty" bson:"public_url,omitempty"`
    ErrorMessage  string         `json:"error_message,omitempty" bson:"error_message,omitempty"`
    CreatedAt     time.Time      `json:"created_at" bson:"created_at"`
    UpdatedAt     time.Time      `json:"updated_at" bson:"updated_at"`
}

type CreateExposureRequest struct {
    DeploymentID string `json:"deployment_id" binding:"required"`
    ServiceName  string `json:"service_name" binding:"required"`
    TargetPort   int    `json:"target_port" binding:"required,min=1,max=65535"`
    Hostname     string `json:"hostname,omitempty"` // auto-generated if empty
}

type QueryExposuresRequest struct {
    DeploymentID string `form:"deployment_id"`
    ServerID     string `form:"server_id"`
    Status       string `form:"status"`
    Limit        int    `form:"limit"`
    Offset       int    `form:"offset"`
}
```

### 6.2 — server_api: Hyphae Client Configuration

**File:** `server_api/server_api/utils/settings.go` — add `Hyphae` config block

```yaml
# config.yaml addition
hyphae:
  endpoint: "http://hyphae:8084"    # management API
  hostname_suffix: ".underleafapp.com"
  default_ttl_seconds: 86400        # 24h, 0 = no expiry
  m2m:
    auth_domain: "dev-motk8gx6wyw7nmbr.us.auth0.com"
    client_id: "..."
    client_secret: "..."
    audience: "https://hyphae.underleaf.com"
```

**New dependency:** Import `hyphae/sdk` into `server_api` go.mod (use `ManagementClient` to call Hyphae REST API with M2M JWT).

### 6.3 — server_api: Repository Layer

**File:** `server_api/server_api/repository/exposure_repository.go` (new)

Standard MongoDB CRUD following existing patterns (see `server_repository.go`):
- `CreateExposure(ctx, exposure) error`
- `GetExposure(ctx, id) (*Exposure, error)`
- `GetExposuresByDeployment(ctx, orgID, deploymentID) ([]Exposure, error)`
- `GetExposuresByServer(ctx, orgID, serverID) ([]Exposure, error)`
- `QueryExposures(ctx, orgID, query) ([]Exposure, int, error)`
- `UpdateExposureStatus(ctx, id, status, leaseID, errorMsg) error`
- `DeleteExposure(ctx, id) error`
- `DeleteExposuresByDeployment(ctx, orgID, deploymentID) error`

**MongoDB collection:** `exposures` (index on `org_id + deployment_id`, unique index on `hostname`).

### 6.4 — server_api: Service Layer

**File:** `server_api/server_api/service/exposures.go` (new)

Methods added to the `Service` interface:

| Method | Logic |
|---|---|
| `CreateExposure(ctx, req)` | Generate hostname if empty (`<slug>-<short_uuid>.underleafapp.com`), call `hyphaeClient.IssueLease()`, store exposure with `lease_id` and `status: pending`, return exposure with `public_url` |
| `GetExposures(ctx, query)` | Query repo with org scoping |
| `GetExposure(ctx, id)` | Single fetch |
| `RevokeExposure(ctx, id)` | Call `hyphaeClient.RevokeLease()`, update status to `revoked`, publish `exposure.bind.revoke` via Spine to tell agent/MMA to tear down tunnel |
| `HandleExposureStatusUpdate(ctx, payload)` | Called when Spine delivers `exposure.status.update` from the agent — update exposure status (`bound` or `error`) in DB |
| `DeleteExposuresByDeployment(ctx, deploymentID)` | Iterate, revoke each lease, delete records |

**Integration with `ApplyAppDeployment`:** After publishing `deployments.apply.server.request`, if the deployment has services with `expose` fields, auto-create exposures for each exposed service×server pair and publish `exposure.bind.request` events.

### 6.5 — server_api: REST Endpoints

**Routes added to `router.go`:**

```go
// Exposure routes (nested under deployments for discoverability)
exposures := api.Group("/exposures")
exposures.Use(DualAuthMiddleware(..., []string{"read:deployments", "write:deployments"}))
exposures.Use(OrgContextMiddleware(...))

exposureCreateRequirements := types.EntitlementRequirements{
    RequiredLimits: []types.LimitRequirement{
        {Key: types.SlotExposures, Count: 1},
    },
}
exposures.POST("", EntitlementsMiddleware(..., exposureCreateRequirements), router.CreateExposureHandler)
exposures.GET("", router.QueryExposuresHandler)
exposures.GET("/:id", router.GetExposureHandler)
exposures.DELETE("/:id", router.RevokeExposureHandler)
```

Also add convenience sub-routes under deployments:
```go
deployments.GET("/:id/exposures", router.GetDeploymentExposuresHandler)
```

### 6.6 — server_api: Spine Topics & Event Handling

**Files to modify:**
- `event_client/topics.go` — add `ExposureBindRequestTopic`, `ExposureStatusUpdateTopic`
- `spine_client/topic_mapping.go` — add QoS mappings (`COMMAND` for bind request, `CONTROL` for status update)

**New subscriber:** `server_api` needs to listen for `exposure.status.update` from agents. Currently `server_api` only publishes; it doesn't subscribe. Two options:

- **Option A (recommended):** Agent reports back via HTTP (`POST /exposures/results`) — same pattern as `HandleDeploymentResult` and `HandleCommandResult`. This avoids adding a subscriber to `server_api`.
- **Option B:** Add a Spine subscriber goroutine in `server_api` for exposure updates.

**Recommendation:** Use Option A — add `POST /exposures/results` endpoint, called by the agent (via HTTP, authenticated with mTLS/JWT) after MMA reports tunnel status. This is consistent with the existing `POST /deployments/results` and `POST /commands/results` patterns.

### 6.7 — UA-K Agent: Exposure Event Handler

**Files to modify in `underleaf_client/`:**

- `internal/agent/wiring.go` — register handler for `exposure.bind.request` Spine event
- `internal/agent/exposure_handler.go` (new) — receives exposure bind request, stores exposure data in Raft KV (`exposures/<exposure_id>`), emits UA event `exposure.bind.requested` to MMA via event stream

**Agent exposure handler flow:**
1. Receive `exposure.bind.request` from Spine with payload `{exposure_id, lease_id, hostname, target_port, service_name}`
2. Store in Raft KV for persistence: `exposures/<exposure_id>` → `{lease_id, hostname, target_port, status: "pending"}`
3. Emit UA event to MMA: `exposure.bind.requested` with full exposure details
4. When MMA reports back (via kernel `EventService.EmitEvent`): update Raft KV status, POST result to `server_api /exposures/results`

**New UA event types for MMA (add to mesh.go types):**
- `exposure.bind.requested`
- `exposure.bind.completed`
- `exposure.unbind.requested`

### 6.8 — server_api: Deployment Type Extensions

**Files to modify:**
- `server_api/server_api/types/app_deployment.go` — add `Expose` field to `Service` struct
- `underleaf_client/internal/types/deployment.go` — mirror the field

```go
type Service struct {
    Name        string            `json:"name" bson:"name"`
    Image       string            `json:"image" bson:"image"`
    Ports       []string          `json:"ports,omitempty" bson:"ports,omitempty"`
    Environment map[string]string `json:"environment,omitempty" bson:"environment,omitempty"`
    Networks    []string          `json:"networks,omitempty" bson:"networks,omitempty"`
    Volumes     []string          `json:"volumes,omitempty" bson:"volumes,omitempty"`
    // NEW
    Expose      *ExposeConfig     `json:"expose,omitempty" bson:"expose,omitempty"`
}

type ExposeConfig struct {
    Port     int    `json:"port" bson:"port"`                             // required: local port to expose
    Hostname string `json:"hostname,omitempty" bson:"hostname,omitempty"` // optional: auto-generated if empty
}
```

---

## UNDF-67: Mesh Provider & Hyphae Integration (MMA)

### Goal

Add a provider interface to MMA; implement a Hyphae provider that requests leases, establishes/maintains tunnels, and reports status via Spine.

### 7.1 — MMA: Exposure Provider Interface

**File:** `umcs/mycelium_mesh_agent/internal/exposure/provider.go` (new)

```go
// Provider represents a service exposure backend (e.g., Hyphae, Cloudflare Tunnel).
type Provider interface {
    // Name returns the provider identifier (e.g., "hyphae").
    Name() string
    // Bind establishes a tunnel for the given exposure. Blocks until connected or ctx cancelled.
    Bind(ctx context.Context, req BindRequest) (*BindResult, error)
    // Unbind tears down the tunnel for the given exposure.
    Unbind(ctx context.Context, exposureID string) error
    // Status returns the current status of an active exposure.
    Status(exposureID string) (ExposureStatus, error)
    // Close shuts down the provider and all active tunnels.
    Close() error
}

type BindRequest struct {
    ExposureID string
    LeaseID    string
    Hostname   string
    TargetPort int
    LocalAddr  string // e.g. "localhost:8080"
}

type BindResult struct {
    ExposureID string
    PublicURL  string
    Status     string // "bound" or "error"
    Error      string
}
```

### 7.2 — MMA: Hyphae Provider Implementation

**File:** `umcs/mycelium_mesh_agent/internal/exposure/hyphae_provider.go` (new)

Uses `hyphae/sdk.TunnelClient` to establish the mTLS tunnel:

```go
type HyphaeProvider struct {
    tunnelClient *sdk.TunnelClient
    syscall      *syscall.Client   // kernel syscall for events
    activeMu     sync.RWMutex
    active       map[string]*activeTunnel // exposureID -> tunnel
    logger       *slog.Logger
}

type activeTunnel struct {
    exposureID string
    leaseID    string
    client     *sdk.TunnelClient
    cancel     context.CancelFunc
}
```

**Bind flow:**
1. Create `TunnelClient` with mTLS credentials (from kernel `IdentityService.IssueLocalCertificate` or config)
2. Call `client.Connect(ctx, leaseID)` — performs Hyphae mTLS handshake + HTTP Upgrade
3. Start `client.Forward(ctx, localAddr)` in a goroutine — pipes yamux streams to the local service port
4. Listen to `client.Events()` for `EventConnected` → report `bound`; `EventDisconnected` → report `error`
5. Emit `exposure.bind.completed` via kernel `EventService.EmitEvent` with status

**Unbind flow:**
1. Call `client.Close()` on the `activeTunnel`
2. Remove from `active` map
3. Emit `exposure.unbind.completed` event

### 7.3 — MMA: UA Event Consumer Integration

**File:** `umcs/mycelium_mesh_agent/internal/discovery/ua_event_consumer.go` — register handlers for new exposure events

Register handlers in the `EventConsumer`:
```go
consumer.RegisterHandler("exposure.bind.requested", mma.handleExposureBind)
consumer.RegisterHandler("exposure.unbind.requested", mma.handleExposureUnbind)
```

**`handleExposureBind`:**
1. Parse `ExposureBindRequestPayload` from event
2. Call `hyphaeProvider.Bind(ctx, BindRequest{...})`
3. On success: emit `exposure.bind.completed` with status=bound
4. On failure: emit `exposure.bind.completed` with status=error + error message

### 7.4 — MMA: Types & Events

**File:** `umcs/mycelium_mesh_agent/internal/types/mesh.go` — add exposure types/constants

```go
// New event type constants
const (
    EventExposureBindRequested  = "exposure.bind.requested"
    EventExposureBindCompleted  = "exposure.bind.completed"
    EventExposureUnbindRequested = "exposure.unbind.requested"
    EventExposureUnbindCompleted = "exposure.unbind.completed"
)

// Payloads
type ExposureBindRequestPayload struct {
    ExposureID string `json:"exposure_id"`
    LeaseID    string `json:"lease_id"`
    Hostname   string `json:"hostname"`
    TargetPort int    `json:"target_port"`
    LocalAddr  string `json:"local_addr"` // resolved by agent from deployment service ports
}

type ExposureBindResultPayload struct {
    ExposureID string `json:"exposure_id"`
    LeaseID    string `json:"lease_id"`
    Status     string `json:"status"` // "bound" or "error"
    PublicURL  string `json:"public_url,omitempty"`
    Error      string `json:"error,omitempty"`
}
```

### 7.5 — MMA: Configuration

**File:** `umcs/mycelium_mesh_agent/internal/config/config.go` — add Hyphae section

```go
type HyphaeConfig struct {
    Enabled        bool   `yaml:"enabled"`
    TunnelAddr     string `yaml:"tunnel_addr"`     // e.g. "hyphae.underleaf.com:9090"
    CACertPath     string `yaml:"ca_cert_path"`    // platform CA for mTLS
    ClientCertPath string `yaml:"client_cert_path"`
    ClientKeyPath  string `yaml:"client_key_path"`
    AutoReconnect  bool   `yaml:"auto_reconnect"`
}
```

### 7.6 — MMA: go.mod Dependency

Add `hyphae/sdk` as a dependency to MMA's `go.mod`. The SDK is already standalone with `TunnelClient` + `ManagementClient` — MMA only needs `TunnelClient`.

---

## UNDF-68: CLI & Manifest Integration (ufctl + deployment engine)

### Goal

Update `ufctl deploy` commands and manifest format to support `--expose` / `expose` in manifests; request exposures from server_api; display public URLs.

### 8.1 — ufctl: Manifest `expose` Field

**File:** `underleaf_client/internal/types/deployment.go` — add `Expose` to `ServiceSpec`

```go
type ServiceSpec struct {
    Name        string            `json:"name"`
    Image       string            `json:"image"`
    Environment map[string]string `json:"environment,omitempty"`
    Volumes     []string          `json:"volumes,omitempty"`
    Networks    []string          `json:"networks,omitempty"`
    Ports       []string          `json:"ports,omitempty"`
    // NEW
    Expose      *ExposeConfig     `json:"expose,omitempty"`
}

type ExposeConfig struct {
    Port     int    `json:"port"`               // local port to expose publicly
    Hostname string `json:"hostname,omitempty"` // custom hostname (auto-generated if empty)
}
```

**Example manifest (my-app.json):**
```json
{
    "name": "My Web App",
    "slug": "my-web-app",
    "version": 1,
    "services": [
        {
            "name": "web",
            "image": "nginx:latest",
            "ports": ["8080:80"],
            "expose": {
                "port": 8080
            }
        },
        {
            "name": "api",
            "image": "myapi:latest",
            "ports": ["3000:3000"]
        }
    ],
    "targeting": {
        "mode": "server_ids",
        "server_ids": ["srv-abc123"]
    }
}
```

### 8.2 — ufctl: `deploy apply --expose` Flag

**File:** `underleaf_client/internal/commands/deploy/apply.go` — add `--expose` flag

```go
var applyExpose string // e.g. "web:8080"

func init() {
    ApplyCmd.Flags().StringVar(&applyExpose, "expose", "", 
        "Expose a service publicly (format: service_name:port)")
}
```

**Flag semantics:** `--expose web:8080` is a CLI shortcut equivalent to setting `expose.port: 8080` on the `web` service in the manifest. The flag overrides/supplements the manifest.

### 8.3 — ufctl: Remote Apply Flow (via server_api)

Currently `ufctl deploy apply` runs the deployment **locally** against Docker. For remote deployments (server_api path), the existing flow is:

1. User creates deployment via `POST /deployments`
2. User triggers via `POST /deployments/:id/apply`
3. server_api fans out via Spine to targeted servers

The expose integration hooks into step 2:

**When apply detects exposed services:**
1. For each service with `expose`, call `POST /exposures` on server_api
2. server_api issues Hyphae lease + stores exposure
3. server_api publishes deployment (which now includes exposure data) to agents
4. Display the returned `public_url` to the user

**File:** `underleaf_client/internal/commands/deploy/apply.go` — after successful apply, if any service has `expose`, print the public URL table:

```
🌐 Public Exposures:
  SERVICE   PORT   URL                                    STATUS
  web       8080   https://my-web-app-a1b2.underleafapp.com   pending
```

### 8.4 — ufctl: `expose` Subcommand (New)

**File:** `underleaf_client/internal/commands/expose/` (new package)

Standalone exposure management commands:

```
ufctl expose create --deployment <id> --service <name> --port <port> [--hostname <hostname>]
ufctl expose list [--deployment <id>] [--server <id>]
ufctl expose get <exposure-id>
ufctl expose revoke <exposure-id>
```

These map directly to the server_api REST endpoints from §6.5.

### 8.5 — ufctl: API Client Extensions

**File:** `underleaf_client/internal/api/client.go` (or similar HTTP client wrapper)

Add methods:
```go
func (c *Client) CreateExposure(ctx, req) (*Exposure, error)
func (c *Client) ListExposures(ctx, query) ([]Exposure, error)
func (c *Client) GetExposure(ctx, id) (*Exposure, error)
func (c *Client) RevokeExposure(ctx, id) error
```

### 8.6 — Deployment Engine: Exposure Awareness

**File:** `umcs/deployment_engine/internal/deployment/handler.go`

When the deployment engine receives a deployment spec with `expose` fields, it should:

1. **After container creation:** Verify the exposed port is actually listening (health check)
2. **Emit event:** `deployment.service.exposed` with `{deployment_id, service_name, port, container_id}` — this confirms the local side is ready
3. This event is consumed by the agent, which uses it as a signal to start the MMA tunnel bind

This creates a clean sequencing:
```
server_api issues lease (pending) → agent receives deployment → deployment engine starts containers
→ containers healthy → deployment engine emits service.exposed → agent triggers MMA bind → MMA connects tunnel
→ agent reports bound → server_api updates exposure status
```

---

## Implementation Order

The three tasks have natural dependencies. Here's the recommended implementation sequence:

```
Phase 1: Data Model & API  (UNDF-66 — server_api)
├── 1a. Exposure types (types/exposure.go)
├── 1b. Settings: Hyphae config block
├── 1c. Service type extensions (ExposeConfig on Service)
├── 1d. Repository layer (exposure_repository.go)
├── 1e. Hyphae M2M client integration (import hyphae/sdk)
├── 1f. Service layer (exposures.go)
├── 1g. REST endpoints + handlers
├── 1h. Spine topic additions (topics.go, topic_mapping.go)
└── 1i. Tests: unit tests for service + repository + handlers

Phase 2: Agent + MMA  (UNDF-66 agent-side + UNDF-67)
├── 2a. MMA types: exposure events & payloads (mesh.go)
├── 2b. MMA exposure provider interface (exposure/provider.go)
├── 2c. MMA Hyphae provider implementation (exposure/hyphae_provider.go)
├── 2d. MMA config: Hyphae section
├── 2e. MMA event consumer: register exposure handlers
├── 2f. MMA go.mod: add hyphae/sdk dependency
├── 2g. Agent: exposure_handler.go (Spine → Raft KV → MMA event)
├── 2h. Agent: wiring.go — register exposure.bind.request handler
├── 2i. Agent: report back to server_api POST /exposures/results
└── 2j. Tests: unit tests for provider, handler, event flow

Phase 3: CLI & Deployment Engine  (UNDF-68)
├── 3a. Client types: add ExposeConfig to ServiceSpec
├── 3b. ufctl API client: exposure methods
├── 3c. ufctl deploy apply: --expose flag + expose handling
├── 3d. ufctl expose subcommand (create/list/get/revoke)
├── 3e. Deployment engine: emit service.exposed event
├── 3f. ufctl deploy apply: display public URL table
└── 3g. Tests: CLI unit tests, integration tests

Phase 4: Integration Testing & Docs
├── 4a. E2E test: full flow from CLI → server_api → Hyphae → agent → MMA → tunnel
├── 4b. Update ARCHITECTURE.md with exposure flow
├── 4c. Update server_api README, API docs
└── 4d. Update ufctl CONTRIBUTING.md, CHANGELOG.md
```

### Dependencies Between Phases

```
Phase 1 ──▸ Phase 2 (agent needs server_api endpoints + Spine topics)
Phase 1 ──▸ Phase 3 (CLI needs server_api endpoints)
Phase 2 ──▸ Phase 3e (deployment engine emits event consumed by agent)
Phase 1+2+3 ──▸ Phase 4 (E2E needs all pieces)
```

Phase 1 is the critical path. Phases 2 and 3 can be parallelized once Phase 1 is complete, except for 3e which depends on 2g.

---

## Files to Create (New)

| # | File | Service | Purpose |
|---|---|---|---|
| 1 | `server_api/server_api/types/exposure.go` | server_api | Exposure data model + request/response types |
| 2 | `server_api/server_api/repository/exposure_repository.go` | server_api | MongoDB CRUD for exposures |
| 3 | `server_api/server_api/service/exposures.go` | server_api | Exposure business logic + Hyphae orchestration |
| 4 | `server_api/server_api/router/exposures.go` | server_api | REST handlers for exposure endpoints |
| 5 | `underleaf_client/internal/agent/exposure_handler.go` | agent (UA-K) | Spine event handler for exposure.bind.request |
| 6 | `umcs/mycelium_mesh_agent/internal/exposure/provider.go` | MMA | Exposure provider interface |
| 7 | `umcs/mycelium_mesh_agent/internal/exposure/hyphae_provider.go` | MMA | Hyphae TunnelClient-based provider |
| 8 | `underleaf_client/internal/commands/expose/expose.go` | ufctl CLI | Expose subcommand (create/list/get/revoke) |
| 9 | `underleaf_client/internal/commands/expose/create.go` | ufctl CLI | `ufctl expose create` |
| 10 | `underleaf_client/internal/commands/expose/list.go` | ufctl CLI | `ufctl expose list` |
| 11 | `underleaf_client/internal/commands/expose/get.go` | ufctl CLI | `ufctl expose get` |
| 12 | `underleaf_client/internal/commands/expose/revoke.go` | ufctl CLI | `ufctl expose revoke` |

## Files to Modify (Existing)

| # | File | Change |
|---|---|---|
| 1 | `server_api/server_api/utils/settings.go` | Add `Hyphae` config block to `Settings` struct |
| 2 | `server_api/server_api/types/app_deployment.go` | Add `Expose *ExposeConfig` to `Service` struct |
| 3 | `server_api/server_api/service/service.go` | Add exposure methods to `Service` interface |
| 4 | `server_api/server_api/service/app_deployments.go` | In `ApplyAppDeployment`, auto-create exposures for services with `expose` |
| 5 | `server_api/server_api/repository/repository.go` | Add exposure methods to `Repository` interface |
| 6 | `server_api/server_api/router/router.go` | Register exposure routes |
| 7 | `server_api/server_api/event_client/topics.go` | Add `ExposureBindRequestTopic`, `ExposureStatusUpdateTopic` |
| 8 | `server_api/server_api/spine_client/topic_mapping.go` | Add QoS mappings for exposure topics |
| 9 | `server_api/config.yaml.example` | Add `hyphae:` section |
| 10 | `underleaf_client/internal/types/deployment.go` | Add `Expose *ExposeConfig` to `ServiceSpec` |
| 11 | `underleaf_client/internal/agent/wiring.go` | Register `exposure.bind.request` handler |
| 12 | `underleaf_client/internal/commands/deploy/deploy.go` | Register expose subcommand, add `--expose` flag to apply |
| 13 | `underleaf_client/internal/commands/deploy/apply.go` | Handle expose flag, print public URL table |
| 14 | `umcs/mycelium_mesh_agent/internal/types/mesh.go` | Add exposure event types + payloads |
| 15 | `umcs/mycelium_mesh_agent/internal/config/config.go` | Add `HyphaeConfig` |
| 16 | `umcs/mycelium_mesh_agent/internal/discovery/ua_event_consumer.go` | Register exposure event handlers |
| 17 | `umcs/deployment_engine/internal/deployment/handler.go` | Emit `deployment.service.exposed` after container start |
| 18 | `server_api/go.mod` (or `server_api/server_api/go.mod`) | Add `hyphae/sdk` dependency |
| 19 | `umcs/mycelium_mesh_agent/go.mod` | Add `hyphae/sdk` dependency |

---

## Risk Mitigations

| Risk | Mitigation |
|---|---|
| Hyphae lease expires before MMA connects | Use generous TTL (24h default); MMA auto-reconnect handles transient failures; server_api monitors lease status |
| MMA crashes mid-tunnel | `TunnelClient.AutoReconnect` re-establishes the session; Hyphae keeps the lease alive as long as TTL holds |
| Hostname collisions | Unique index on `hostname` in MongoDB; auto-generated hostnames include random suffix |
| M2M token expiry (server_api → Hyphae) | Token refresh on 401 retry; standard `client_credentials` grant |
| Agent offline when exposure is created | Spine durable mailbox holds `exposure.bind.request` until agent reconnects (QoS_COMMAND guarantees delivery) |
| Deployment deleted but lease lingers | `DeleteAppDeployment` → `DeleteExposuresByDeployment` → revoke all leases |
| Rate limit on Hyphae management API | Batch lease operations where possible; expose only on explicit request (not automatic) |

---

## Testing Strategy

| Level | Scope | Tool |
|---|---|---|
| Unit | Exposure service logic, hostname generation, status transitions | Go `testing` + mocks |
| Unit | MMA provider Bind/Unbind, event payload parsing | Go `testing` + mock TunnelClient |
| Unit | CLI flag parsing, expose command output formatting | Go `testing` |
| Integration | server_api → Hyphae SDK (mock Hyphae server) | Go `httptest` + real MongoDB |
| Integration | Agent → MMA event flow (mock Spine + mock MMA) | Go integration test |
| E2E | Full flow: CLI → server_api → Hyphae → agent → MMA | Docker Compose (existing `e2e/` infra) |
