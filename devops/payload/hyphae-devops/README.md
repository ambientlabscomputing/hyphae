# hyphae-devops payload

This directory is the VM-side payload for the Hyphae local dev deployment. It is
copied verbatim to `~/hyphae-devops/` inside the OrbStack VM by `copy-payload.sh`
and then executed via SSH by `deploy-payload.sh`.

**You do not run these scripts directly on your Mac.** Use the host-side Makefile
in `hyphae/devops/` instead.

## Secrets Setup (required before first deploy)

```bash
# Run this on your Mac, in hyphae/devops/payload/hyphae-devops/
cp .env.example .env
$EDITOR .env   # fill in AUTH0_PASSWORD, HYPHAE_IMAGE_MODE, etc.
```

Key `.env` variables:

| Variable | Description |
|----------|-------------|
| `HYPHAE_IMAGE_MODE` | `develop` (default), `v1.2.3`, or `local` |
| `HYPHAE_IMAGE_TAG` | Hub image tag — used when mode is `develop` or `v*` |
| `HYPHAE_SOURCE_PATH` | Mac source path via `/mnt/mac` — used when mode is `local` |
| `AUTH0_USERNAME` / `AUTH0_PASSWORD` | Shared dev Auth0 credentials |
| `CF_TUNNEL_TOKEN` | Cloudflare tunnel token (only for `run-with-tunnel`) |

The `.env` file is gitignored and copied to the VM at deploy time. Docker Compose
reads it automatically for variable substitution. `make render-config` uses it to
render `hyphae/config.yaml` from `hyphae/config.yaml.template`.

## VM-Side Makefile Targets

| Target | Description |
|--------|-------------|
| `make setup` | Full first-time setup: make, sudo, docker, ufctl, render-config |
| `make run` | Start hyphae from Docker Hub image (develop or v* mode) |
| `make run-local` | Build from `/mnt/mac` source then start hyphae (local mode) |
| `make run-with-tunnel` | Start hyphae + Cloudflare tunnel (requires `CF_TUNNEL_TOKEN` in `.env`) |
| `make build-local` | Build image from source without starting — tag written to `.local-image-tag` |
| `make rebuild` | Mode-aware rebuild: source build (local) or `docker pull` + recreate (remote) |
| `make stop` | Stop all services (including tunnel if running) |
| `make health` | Run `hyphctl health` via the admin socket |
| `make pull` | Pull the latest hyphae image from Docker Hub |
| `make render-config` | Re-render `hyphae/config.yaml` from template + `.env` |

## Config Rendering

The runtime config is never stored in git with real secrets. Instead:

1. `hyphae/config.yaml.template` — committed, contains `${VAR}` placeholders
2. `.env` — gitignored, contains real values
3. `make render-config` runs `envsubst < config.yaml.template > config.yaml`
4. `hyphae/config.yaml` — gitignored, the rendered output read by the container

`make setup` calls `render-config` automatically as its last step.

## Directory Structure

```
.env.example              Secret placeholders — never commit .env
.local-image-tag          Build artifact: tag written by build-local.sh (gitignored)
docker-compose.yaml       Services: hyphae (default) + cf_tunnel (profile: tunnel)
Makefile                  VM-side targets (see table above)
hyphae/
  config.yaml.template    Config with ${VAR} placeholders (committed)
  config.yaml.example     Human-readable reference (committed)
  config.yaml             Generated at deploy time by render-config (gitignored)
  certs/                  Populated at deploy time by copy-payload.sh (gitignored)
scripts/
  install-make.sh         Installs make + gettext-base (envsubst)
  install-docker.sh       Installs Docker CE for Ubuntu
  install-underleaf.sh    Downloads ufctl + underleaf_agent (detects arm64/amd64)
  enable-nopasswd-sudo.sh Adds a validated sudoers drop-in
  build-local.sh          Builds hyphae Docker image from /mnt/mac source
```
