# Hyphae DevOps

OrbStack-based local dev deployment for the Hyphae service. Provisions an Ubuntu VM, copies a self-contained payload (config, certs, docker-compose, install scripts), and starts hyphae inside Docker.

## Architecture

```
[Mac host]                          [OrbStack Ubuntu VM]
  hyphae/devops/
    orb_scripts/
      run-vm.sh       ──orb create──►  <vm-name>.orb.local
      copy-payload.sh ──tar+ssh──────► ~/hyphae-devops/
      deploy-payload.sh ──ssh make──►  make setup && make run
                                         ├ install-make.sh
                                         ├ enable-nopasswd-sudo.sh
                                         ├ install-docker.sh
                                         ├ install-underleaf.sh
                                         └ render-config (envsubst)
                                       docker compose up -d hyphae
```

The payload is a standalone directory (`payload/hyphae-devops/`) that contains everything the VM needs. The host only needs OrbStack and SSH access; Docker, make, and ufctl are all installed inside the VM by the payload scripts.

## Prerequisites

- **OrbStack** installed and running (`brew install orbstack` or https://orbstack.dev)
- **Tunnel certs** in `hyphae/certs/` (wildcard cert + CA cert + key). Contact the team if you need these.
- **Secrets `.env` file**: `cp payload/hyphae-devops/.env.example payload/hyphae-devops/.env` then fill in:
  - `AUTH0_USERNAME` / `AUTH0_PASSWORD` — shared dev Auth0 M2M client credentials
  - `CF_TUNNEL_TOKEN` — Cloudflare Zero Trust tunnel token (only needed for `run-with-tunnel`)
  - `HYPHAE_IMAGE_MODE` — image source mode (see below; default: `develop`)

## Image Modes

Set `HYPHAE_IMAGE_MODE` in `payload/hyphae-devops/.env` before running `make run`.

| Mode | How it works | When to use |
|------|--------------|-------------|
| `develop` | Pulls `ambientlabsjose/hyphae:develop` from Docker Hub | Default — tracks the latest merged code |
| `v1.2.3` | Pulls a specific tag from Docker Hub | Testing a specific release |
| `local` | Builds from Mac source via OrbStack's `/mnt/mac` mount | Iterating on hyphae source code |

### Local build mode

When `HYPHAE_IMAGE_MODE=local`, also set `HYPHAE_SOURCE_PATH` in `.env`:
```
HYPHAE_SOURCE_PATH=/mnt/mac/Users/your-username/ambient_labs/underleaf/hyphae
```
The VM builds the Docker image directly from your Mac's source tree (OrbStack auto-mounts it at `/mnt/mac`). The resulting image is tagged `local-<git-sha>`. No pushing or exporting needed.

Iteration cycle for local mode:
```bash
# First time: full provision
make run

# After changing Go code on Mac:
make build           # rebuild image in VM from current /mnt/mac source
# then inside the VM via 'make ssh':
cd ~/hyphae-devops && make rebuild  # force-recreate container with new image
```

### Publishing a tagged release

To publish a `v*` tag to Docker Hub (run from `hyphae/`):
```bash
cd ../../   # from devops/ → hyphae/
make docker-publish-tag TAG=v1.2.3
```

## Quickstart

```bash
# 1. Fill in secrets
cp payload/hyphae-devops/.env.example payload/hyphae-devops/.env
$EDITOR payload/hyphae-devops/.env

# 2. Provision VM, copy payload, deploy
make run

# 3. Verify
make status
make health
```

After `make run`, hyphae is reachable at:
- `http://<vm-name>.orb.local:8084` — management API
- `<vm-name>.orb.local:9090` — mTLS tunnel listener
- `<vm-name>.orb.local:8080` — public gateway (plain HTTP for local dev)

## Daily Operations

| Command | Description |
|---------|-------------|
| `make ssh` | Open a shell in the VM |
| `make status` | `docker ps` on the VM |
| `make logs` | Tail hyphae container logs |
| `make health` | `hyphctl health` on the VM |
| `make build` | Rebuild image from Mac source without redeploying (local mode) |
| `make redeploy` | Re-copy payload + restart (no VM recreate) |
| `make destroy` | Stop and delete the VM |

## Starting the Cloudflare Tunnel

The Cloudflare tunnel is optional — the default `make run` starts hyphae only.
To also start the tunnel:

```bash
make ssh
# inside the VM:
cd ~/hyphae-devops && make run-with-tunnel
```

Or during initial deploy, run `make run-with-tunnel` instead of `make run` inside
the payload Makefile after SSH-ing in.

## Updating an Existing VM

To push config/script changes without recreating the VM:

```bash
make redeploy         # re-copy payload + re-run setup + restart services
```

## Troubleshooting

**`make run` fails with "A VM is already registered"**
Run `make destroy` first, or use `make redeploy` if you just want to update.

**Local build fails: `/mnt/mac` not accessible**
Verify OrbStack is running and the VM was created via OrbStack. Inside the VM run `ls /mnt/mac` — should list your Mac home dirs. If empty, restart OrbStack.

**Local build fails: `Dockerfile not found` at HYPHAE_SOURCE_PATH**
Check that `HYPHAE_SOURCE_PATH` in `.env` points to the `hyphae/` service directory (contains `Dockerfile`, `go.mod`, `cmd/`). Update the path to match your Mac username and checkout location.

**SSH not ready after VM creation**
`copy-payload.sh` retries for up to 60 seconds. If the VM is slow to boot, run `make copy-payload && make deploy-payload` manually after waiting.

**`render-config` fails with "envsubst not found"**
`install-make.sh` installs `gettext-base` (which provides `envsubst`) during `make setup`. If you bypassed setup, run: `sudo apt-get install -y gettext-base` on the VM.

**Container won't start — check logs**
```bash
make logs
# or
make ssh
docker compose -f ~/hyphae-devops/docker-compose.yaml logs
```

**Cert issues — tunnel listener fails**
Verify certs were staged: `ls hyphae/certs/` should show `ca.crt`, `tunnel.crt`, `tunnel.key`.
Check the warning output of `make copy-payload` for cert staging errors.

## Structure

```
orb_scripts/          Host-side scripts (run on Mac, talk to OrbStack)
  run-vm.sh           Create and start the OrbStack Ubuntu VM
  copy-payload.sh     Stage certs + tar-over-ssh copy of payload to VM
  deploy-payload.sh   Bootstrap make on VM, run 'make setup' + 'make run'
  destroy-vm.sh       Stop and delete the VM

payload/hyphae-devops/   VM-side payload (copied as-is to ~/hyphae-devops)
  .env.example            Secret placeholders — copy to .env and fill in
  docker-compose.yaml     Hyphae + optional Cloudflare tunnel
  Makefile                VM-side targets: setup, run, run-with-tunnel, health…
  hyphae/
    config.yaml.template  Config template with ${VAR} placeholders
    config.yaml.example   Human-readable config reference
    certs/                Populated at deploy time by copy-payload.sh
  scripts/
    install-make.sh       Installs make + envsubst (gettext-base)
    install-docker.sh     Installs Docker CE (Ubuntu)
    install-underleaf.sh  Downloads ufctl + underleaf_agent (arch-aware)
    enable-nopasswd-sudo.sh  Adds sudoers drop-in for passwordless sudo
```
