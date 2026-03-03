# Implementation Review: Hyphae Integration (UNDF-65)

## Overall Verdict

The implementation covers approximately **70% of the plan's surface area**. The data model layer, type definitions, configuration structures, Spine topics, CLI commands, and provider scaffolding are all in place. However, the **end-to-end flow is broken** because the critical integration seams — Hyphae SDK calls, completion event callbacks, and deployment-to-exposure wiring — are stubbed or missing. An exposure created today would persist in `pending` status forever: no Hyphae lease would be issued, no tunnel bound, and no status ever reported back.

## What's Done Well

| Area | Assessment |
|---|---|
| **Exposure data model** (types, repository, indexes) | Complete and well-structured. MongoDB indexes match the plan. |
| **server_api REST API** (routes, handlers, middleware) | 6 endpoints implemented with dual-auth and org-context middleware. |
| **Spine topics & QoS** | Both topics registered with correct QoS levels. |
| **Agent exposure handler** | Spine → Raft KV → UA event stream flow implemented for bind and unbind. |
| **MMA provider interface + Hyphae implementation** | Clean abstraction. `HyphaeProvider` correctly uses `sdk.TunnelClient`. |
| **MMA event consumer** | Handlers registered for bind/unbind requested events. |
| **MMA config & go.mod** | `HyphaeConfig` with mTLS fields; `hyphae/sdk` imported. |
| **CLI `expose` subcommand** (create/list/get/revoke) | Fully implemented with table formatting and JSON output. |
| **CLI `--expose` flag** on `deploy apply` | Parses `service:port`, attaches `ExposeConfig` to the spec. |
| **Hyphae SDK** | `TunnelClient` and `ManagementClient` are complete and tested. |

## What's Broken or Missing

| # | Gap | Severity | Impact |
|---|---|---|---|
| 1 | **3 compile errors** in `server_api/server_api/router/exposures.go` `UpdateExposureResultHandler` — return arity mismatch, type cast, unused var | **Blocking** | server_api won't compile |
| 2 | **No Hyphae SDK client calls** in server_api service layer — `IssueLease` and `RevokeLease` are TODO stubs in `server_api/server_api/service/exposures.go` | **Critical** | No real lease is ever issued; `LeaseID` is faked |
| 3 | **No `server_api` → Hyphae client instantiation** — `hyphae/sdk` not in `server_api/server_api/go.mod`; no `ManagementClient` created | **Critical** | Prerequisite for #2 |
| 4 | **MMA completion events not emitted** — `umcs/mycelium_mesh_agent/internal/exposure/handlers.go` has Phase 2i TODOs for `exposure.bind.completed` | **Critical** | Agent never learns tunnel is bound → no status update flows back |
| 5 | **Agent → server_api result reporting missing** — no `PostExposureResult` method; no handler for MMA completion events | **Critical** | server_api DB stays `pending` forever |
| 6 | **`ApplyAppDeployment` doesn't auto-create exposures** — `server_api/server_api/service/app_deployments.go` has zero exposure references | **High** | `expose` in manifest has no effect on the server-side deploy path |
| 7 | **Deployment engine `Deploy()` never calls `EmitServiceExposure()`** — the method exists in `umcs/deployment_engine/internal/deployment/handler.go` but is dead code | **High** | The sequencing signal (containers ready → start tunnel) never fires |
| 8 | **`DeleteAppDeployment` doesn't cascade** to exposure deletion — no call to `DeleteExposuresByDeployment` | **High** | Orphaned exposures and leaked Hyphae leases on deployment deletion |
| 9 | **`DeleteExposuresByDeployment` missing from interfaces** — exists on `AppService` concrete type but not in `Service` or `Repository` interfaces | **Medium** | Can't mock in tests; can't call polymorphically |
| 10 | **`config.yaml.example` missing `hyphae:` section** | **Medium** | Operators have no documentation for required config |
| 11 | **No `public_exposures` feature entitlement** — exposures piggyback on `Deployments` entitlement | **Low** | Can't independently gate exposure feature per org |
| 12 | **Zero unit/integration tests** for exposure service, repository, router, MMA provider, agent handler | **High** | No regression safety |
| 13 | **Deployment engine `ServiceSpec` lacks `Expose` field** — `umcs/deployment_engine/internal/deployment/handler.go` doesn't include it | **Medium** | Even if `EmitServiceExposure` were called, `Deploy()` can't detect which services need exposure |
| 14 | **Exposure `serverID` is empty** — `server_api/server_api/service/exposures.go` TODO to look up deployment's target server | **High** | Spine can't route `exposure.bind.request` to the correct agent |

---

## Plan A: Fill Implementation Gaps

Wire the end-to-end flow by fixing compile errors, integrating the Hyphae SDK into server_api, implementing the MMA→agent→server_api callback path, and connecting exposure creation to the deployment lifecycle. 14 steps across server_api, agent, MMA, and deployment engine.

### Steps

1. **Fix compile errors** in `server_api/server_api/router/exposures.go` `UpdateExposureResultHandler`: cast `req.Status` to `types.ExposureStatus`, change `exposure, err :=` to `err :=`, remove unused `orgID` or use it, return `204 No Content` on success.

2. **Add `hyphae/sdk` to server_api** — update `server_api/server_api/go.mod` to `require github.com/ambientlabscomputing/hyphae`. Create a `hyphae_client.go` in `server_api/server_api/service/` that initializes `sdk.ManagementClient` with M2M credentials from `settings.Hyphae.M2M`, including a token refresh mechanism (Auth0 `password` grant **NOT** `client_credentials` with retry on 401, user server_api's TokenManager as an example).

3. **Wire `IssueLease` in `CreateExposure`** — replace the TODO stub in `server_api/server_api/service/exposures.go` with an actual `hyphaeClient.IssueLease()` call using the `sdk.IssueLeaseRequest` type. Populate `LeaseID` from the response.

4. **Wire `RevokeLease` in `RevokeExposure`** — replace the TODO in `server_api/server_api/service/exposures.go` with `hyphaeClient.RevokeLease(ctx, exposure.LeaseID)`. Handle "lease not found" gracefully (already-expired).

5. **Resolve `serverID` in `CreateExposure`** — replace the TODO in `server_api/server_api/service/exposures.go`: fetch the deployment from the repository, extract target server IDs from `deployment.Targeting`, and create one exposure per target server (or error if no servers targeted).

6. **Wire `ApplyAppDeployment` → auto-create exposures** — in `server_api/server_api/service/app_deployments.go`, after publishing `deployments.apply.server.request`, iterate over `deployment.Services` and for each service with `Expose != nil`, call `s.CreateExposure()`. Include `exposure_id`, `lease_id`, `hostname`, and `target_port` in the Spine payload so the agent has the exposure data.

7. **Wire `DeleteAppDeployment` → cascade exposure deletion** — in `server_api/server_api/service/app_deployments.go` `DeleteAppDeployment`, call `s.DeleteExposuresByDeployment(ctx, id)` before deleting the deployment.

8. **Add `DeleteExposuresByDeployment` to interfaces** — add the method to both the `Repository` interface in `server_api/server_api/repository/repository.go` and the `Service` interface in `server_api/server_api/service/service.go`.

9. **MMA: Emit completion events** — in `umcs/mycelium_mesh_agent/internal/exposure/handlers.go`, after `provider.Bind()` returns, emit `EventExposureBindCompleted` with result status via kernel `EventService.EmitEvent`. Similarly for `Unbind`. Replace the 4 Phase 2i TODOs.

10. **Agent: Handle MMA completion events** — create a handler in `underleaf_client/internal/agent/exposure_handler.go` for `exposure.bind.completed` UA events (received via `EventService.SubscribeLocal` or a new kernel event subscription). Update Raft KV status. POST result to server_api.

11. **Agent: Add `PostExposureResult` to control plane client** — add a method to `underleaf_client/internal/controlplane/api.go` that POSTs to `/exposures/:id/results` following the pattern of `ReportDeploymentResult` in `underleaf_client/internal/controlplane/deployment_client.go`.

12. **Deployment engine: Wire `EmitServiceExposure` into `Deploy()`** — in `umcs/deployment_engine/internal/deployment/handler.go`, add `Expose *ExposeConfig` to the local `ServiceSpec` struct, and after container creation succeeds in `Deploy()`, iterate services and call `EmitServiceExposure()` for those with expose configs.

13. **Add `hyphae:` section to `config.yaml.example`** — in `server_api/config.yaml.example`, add the documented config block with `endpoint`, `hostname_suffix`, `default_ttl_seconds`, and `m2m` sub-fields.

14. **Add `EntitlementExposures` feature flag** — in `server_api/server_api/types/entitlement_requirements.go`, add a `public_exposures` feature flag so the exposure feature can be independently gated per organization, and use it in the exposure route middleware.

### Verification

- `go build ./...` in `server_api/server_api/`, `underleaf_client/`, and `umcs/mycelium_mesh_agent/` should all pass with zero errors.
- Manual smoke: call `POST /exposures` → verify a real Hyphae lease is issued (check via `hyphctl leases list`).
- End-to-end: `ufctl deploy apply my-app.json --expose web:8080` → verify exposure reaches `bound` status in server_api DB.

### Decisions

- Use HTTP callback (Option A from plan §6.6) for agent → server_api status reporting, consistent with existing `POST /deployments/results` and `POST /commands/results` patterns.
- server_api creates one exposure per (service × target server) pair during `ApplyAppDeployment`, not one global exposure.

---

## Plan B: Testing & Validation

Build test coverage across all four services (server_api, agent, MMA, deployment engine) at unit, integration, and E2E levels. Prioritize the critical path: exposure creation → lease issuance → tunnel bind → status callback.

### Steps

1. **server_api: Exposure service unit tests** — create `server_api/server_api/service/exposures_test.go`. Mock `ExposureRepository` and `ManagementClient`. Test:
   - `CreateExposure` — hostname generation, lease issuance call, error handling (Hyphae unavailable, duplicate hostname)
   - `RevokeExposure` — lease revocation, status transitions, idempotency
   - `HandleExposureStatusUpdate` — `pending→bound`, `pending→error`, invalid transitions
   - `DeleteExposuresByDeployment` — cascading revoke-then-delete
   - `generateHostname` — slug format, uniqueness suffix

2. **server_api: Exposure repository unit tests** — create `server_api/server_api/repository/exposure_repository_test.go`. Use a test MongoDB instance (existing pattern in the codebase). Test:
   - CRUD operations
   - Unique hostname index enforcement (duplicate insert should fail)
   - `QueryExposures` pagination and filtering
   - `DeleteExposuresByDeployment` removes all matching records

3. **server_api: Exposure router handler tests** — create `server_api/server_api/router/exposures_test.go`. Use `httptest` + mock service. Test:
   - `POST /exposures` — valid request, missing fields, entitlement check failure
   - `GET /exposures` — query parameter parsing, pagination
   - `DELETE /exposures/:id` — success, not-found
   - `POST /exposures/:id/results` — valid status update, invalid status
   - Auth middleware enforcement (no token → 401)

4. **server_api: Integration test for `ApplyAppDeployment` with exposures** — in `server_api/server_api/service/app_deployments_test.go` (or a new file). Test that applying a deployment with `Expose` config on a service results in `CreateExposure` being called, and that the Spine payload includes exposure data.

5. **MMA: Provider unit tests** — create `umcs/mycelium_mesh_agent/internal/exposure/hyphae_provider_test.go`. Mock `sdk.TunnelClient`. Test:
   - `Bind` — successful connect+forward, connect failure, context cancellation
   - `Unbind` — active tunnel cleanup, unbind of non-existent exposure
   - `Status` — returns correct status for active/unknown exposures
   - `Close` — all tunnels torn down

6. **MMA: Handler unit tests** — create `umcs/mycelium_mesh_agent/internal/exposure/handlers_test.go`. Mock `Provider` and `EventConsumer`. Test:
   - `handleExposureBindRequested` — valid payload → `Bind` called, malformed payload → error logged
   - `handleExposureUnbindRequested` — valid payload → `Unbind` called
   - Completion event emission (once implemented in Plan A step 9)

7. **Agent: Exposure handler unit tests** — create `underleaf_client/internal/agent/exposure_handler_test.go`. Mock Raft KV and `EventStreamServer`. Test:
   - `HandleExposureBindRequested` — stores in Raft KV, publishes UA event, handles Raft unavailability
   - `HandleExposureUnbindRequested` — publishes unbind event
   - Completion handler (once implemented in Plan A step 10) — updates Raft KV, POSTs to server_api

8. **CLI: Expand exposure command tests** — enhance `underleaf_client/internal/commands/expose/expose_test.go` beyond the current stubs. Use `httptest` server to mock the server_api. Test:
   - `ufctl expose create` — all required flags, optional hostname, API error handling
   - `ufctl expose list` — table and JSON output formatting, empty list
   - `ufctl expose get` — found, not found
   - `ufctl expose revoke` — confirmation prompt, `--force` bypass
   - `ufctl deploy apply --expose web:8080` — flag parsing, `ExposeConfig` attachment

9. **Integration test: server_api → Hyphae SDK** — create an integration test using `httptest` to simulate Hyphae's management API. Test the full `CreateExposure` → `IssueLease` → store-exposure flow and `RevokeExposure` → `RevokeLease` flow, including error scenarios (Hyphae returns 500, lease conflict, token expiry).

10. **E2E test: Full exposure lifecycle** — add to the existing `mycelium_spine/e2e/` infrastructure (or create a new `e2e/exposure_test.go`). Requires Docker Compose with all services running. Test:
    - `POST /deployments` with expose config → `POST /deployments/:id/apply` → verify exposure created with `pending` status
    - Agent receives `exposure.bind.request` → MMA binds tunnel → status updates to `bound`
    - `GET /exposures/:id` returns `bound` status with `public_url`
    - HTTP request to `public_url` (through Hyphae :443) reaches the deployed service
    - `DELETE /exposures/:id` → tunnel torn down → lease revoked
    - Delete deployment → all associated exposures cleaned up

11. **Negative/edge-case tests** — across all levels:
    - Duplicate hostname creation (should fail with conflict error)
    - Exposure creation when Hyphae is unreachable (should fail gracefully, not leave partial state)
    - MMA tunnel reconnection after disconnect (if `AutoReconnect` is enabled)
    - Concurrent bind requests for the same exposure (idempotency)
    - Exposure creation exceeding `max_exposures` entitlement limit
    - Agent offline during exposure creation (Spine durable mailbox delivery on reconnect)

### Verification

- `go test ./...` passes in all four services with >80% coverage on exposure code paths.
- E2E test passes in CI (make cloud-run → test suite → teardown).
- Negative tests confirm graceful error handling — no leaked goroutines, no orphaned DB records.

### Decisions

- Use `httptest` to mock Hyphae in server_api integration tests (avoids needing real Hyphae in CI for unit/integration level).
- E2E tests do NOT involve themselves in app deployment--they only test the fully deployed app we point them at. The developer is responsible for running the app how they see fit which is almost **NEVER** docker compose. Usualy it is done by running `make run-cloud` from the underleaf directory.
- Prioritize testing the critical path (create → bind → status callback) before edge cases.
