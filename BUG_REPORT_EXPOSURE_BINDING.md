# Bug Report: Hyphae Exposure Binding Fails — mTLS Tunnel Connection Error

**Date:** 2026-03-11  
**Severity:** High  
**Affects:** 3/26 E2E tests (all Hyphae exposure tests)  
**Environment:** Local OrbStack lab (2 VMs: server-01, server-02)

---

## Summary

All three Hyphae exposure E2E tests fail with a 60-second timeout waiting for
exposure status to reach `bound`. The full Spine event pipeline works correctly
— the MMA receives the `exposure.bind.request` and attempts to open the mTLS
reverse tunnel — but the tunnel dial fails with a **TLS certificate verification
error** against the **wrong Hyphae hostname**.

## Failing Tests

All in `local-underleaf-lab/tests/test_deploy/test_expose_via_hyphae.py`:

| Test | Exposure ID | Error |
|------|-------------|-------|
| `test_create_exposure_reaches_bound_status` | `81781018-35b8-4a61-b507-53be323ad70b` | TimeoutError: 60s waiting for `bound` status |
| `test_public_url_serves_traffic` | `a51f0283-3ec2-4753-9370-1087888d1c53` | TimeoutError: 60s waiting for `bound` with public URL |
| `test_revoke_clears_exposure` | `4a904680-35b0-47ca-993c-edb3753bbbec` | TimeoutError: 60s waiting for `bound` before revoking |

**23 other tests pass**, including deployments, cluster status, Raft KV, and
remote command execution.

---

## Root Cause

**Two issues, both in `server_api/config.yaml`:**

### 1. Wrong `tunnel_endpoint` — points to remote Hyphae instead of local

```yaml
# server_api/config.yaml (current — WRONG for local lab)
hyphae:
    tunnel_endpoint: hy.underleafdev.com:9090
```

The `tunnel_endpoint` is embedded in every `exposure.bind.request` event payload
(see `server_api/server_api/service/exposures.go:125`):

```go
"hyphae_tunnel_addr": s.settings.Hyphae.TunnelEndpoint,
```

The MMA on the target VM uses this address to **dial the Hyphae tunnel server**.
Since it points to `hy.underleafdev.com:9090` (the remote/production Hyphae),
the MMA tries to connect to a remote server instead of the local one running on
the `ubuntu` OrbStack VM at `ubuntu.orb.local:9090`.

**Fix:** Change to `ubuntu.orb.local:9090` for local lab.

### 2. TLS certificate verification failure (ECDSA)

Even if the hostname were correct, the mTLS handshake fails because the MMA
cannot verify the Hyphae tunnel server's TLS certificate:

```
failed to connect tunnel: hyphae sdk: dial hy.underleafdev.com:9090:
  tls: failed to verify certificate: x509: certificate signed by unknown
  authority (possibly because of "x509: ECDSA verification failure" while
  trying to verify candidate authority certificate "Underleaf Root CA")
```

This means the MMA's Hyphae SDK client either:
- Does not have the local Underleaf Root CA in its trust pool, or
- The CA cert on the MMA does not match the CA that signed Hyphae's tunnel cert
  (e.g., CA was regenerated after the agent was deployed)

**Fix:** Ensure the MMA's Hyphae SDK is configured with the correct CA cert
path, and that the CA cert matches the one used to sign Hyphae's tunnel TLS
certificate.

---

## Diagnostic Evidence

### Event Pipeline (working correctly)

The full `exposure.bind.request` pipeline works end-to-end — the failure is
only in the final tunnel dial step:

```
server_api                      UA (agent)                         MMA
    │                               │                               │
    │ PublishEvent(                  │                               │
    │  "exposure.bind.request")     │                               │
    │───── Spine gRPC ─────────────▶│ ✅ received from Spine        │
    │                               │  ├─ stored in Raft KV         │
    │                               │  └─ forwarded to MMA          │
    │                               │───── gRPC event stream ──────▶│ ✅ received
    │                               │                               │  ├─ provider.Bind()
    │                               │                               │  └─ ❌ TLS dial fails
    │                               │◀──── bind.completed ──────────│ status: "error"
    │◀──── Spine ───────────────────│ reported to server_api        │
```

### Agent Structured Log (`/tmp/underleaf-agent-structured.log` on server-01)

All 3 exposures follow the same pattern. Example for exposure `81781018`:

```json
// 1. Spine delivers bind request to agent ✅
{"msg":"handling exposure bind request from Spine","event_type":"exposure.bind.request",
 "exposure_id":"81781018-35b8-4a61-b507-53be323ad70b"}

// 2. Agent stores in Raft KV ✅
{"msg":"exposure metadata stored in Raft KV","key":"/exposures/81781018-..."}

// 3. Agent forwards to MMA via event stream ✅
{"msg":"publishing event to subscribers","event_type":"exposure.bind.requested","subscriber_count":1}
{"msg":"sent event to subscriber","subscriber_id":"sub-1773262920883772394"}
{"msg":"exposure bind request event published to MMA"}

// 4. MMA reports back FAILURE ❌
{"msg":"handling exposure bind completed event from Spine",
 "exposure_id":"81781018-35b8-4a61-b507-53be323ad70b","status":"error"}

// 5. Agent reports error to server_api
{"msg":"APIClient POST","url":".../exposures/81781018-.../results",
 "payload":{"status":"error","error":"failed to connect tunnel: hyphae sdk: dial hy.underleafdev.com:9090: tls: failed to verify certificate: x509: certificate signed by unknown authority (possibly because of \"x509: ECDSA verification failure\" while trying to verify candidate authority certificate \"Underleaf Root CA\")"}}
```

### Hyphae Health (on `ubuntu` VM)

```json
{"connections":0,"leases":0,"status":"healthy"}
```

Zero tunnel connections ever established. Leases are created by server_api (201)
and deleted ~60s later when the exposure times out (204), but no tunnel client
ever connects to consume them.

### Hyphae Logs (container on `ubuntu` VM)

Only lease CRUD and health checks — never a tunnel connection attempt:

```
POST   /api/v1/leases  → 201  (lease created by server_api)
DELETE /api/v1/leases/… → 204  (lease cleaned up after timeout)
GET    /health          → 200  (health checks every 30s)
```

### MMA Logs (`~/.underleaf/providers/logs/underleaf.mma-dev.log` on server-01)

Only 15 lines total — GIN router startup output. **Zero HTTP requests logged.**
The MMA processes the bind request via the gRPC event stream (not HTTP), and the
failure happens in the Hyphae SDK `Dial()` call before any HTTP activity.

### Agent Config on VMs (`~/.underleaf/config.yaml`)

```yaml
hyphae:
  host: ubuntu.orb.local
  port: "8084"
  tunnel_addr: ubuntu.orb.local:9090
  gateway_port: "8080"
```

The agent-side config correctly points to local Hyphae. However, the MMA's
Hyphae SDK uses the `hyphae_tunnel_addr` from the **event payload** (set by
server_api), not the agent's local config. This is why the wrong address is used.

---

## Proposed Fix

### 1. Fix `server_api/config.yaml` tunnel endpoint

```yaml
# BEFORE (wrong — remote Hyphae)
hyphae:
    tunnel_endpoint: hy.underleafdev.com:9090

# AFTER (correct — local OrbStack Hyphae)
hyphae:
    tunnel_endpoint: ubuntu.orb.local:9090
```

### 2. Fix MMA CA trust for Hyphae tunnel TLS

The MMA's Hyphae SDK needs the correct CA certificate to verify the Hyphae
tunnel server's TLS cert. Investigate:

- Where does the MMA/Hyphae SDK load its CA cert from?
- Is the CA cert on the VM stale (from before the recent CA regeneration)?
- Does the Hyphae SDK respect a `ca_cert_path` config or environment variable?

Likely fix: ensure the MMA copies/uses the same CA cert that signed Hyphae's
tunnel server cert (the Underleaf Root CA from server_api).

### 3. Rebuild and redeploy

After fixing the config:

```bash
# Rebuild server_api (picks up new config)
cd server_api && make build

# Restart server_api container (or process)
# Re-run E2E tests
cd local-underleaf-lab && make test ENV=local
```

---

## Environment Details

| Component | Location | Port |
|-----------|----------|------|
| server_api | Mac (localhost) | 8082 (behind LB on 8080) |
| Mycelium Spine | ubuntu VM (Docker) | 9443 (nginx → 9097) |
| Hyphae mgmt API | ubuntu VM (Docker) | 8084 |
| Hyphae tunnel | ubuntu VM (Docker) | 9090 |
| Hyphae gateway | ubuntu VM (Docker) | 8080 |
| Agent (UA) | server-01/02 VMs | 2240 |
| MMA | server-01/02 VMs | (child process of agent) |

**Lab config:** `cluster.local.json`  
**All containers:** local builds (`IMAGE_MODE=local`)
