.PHONY: install-make install-docker install-underleaf enable-nopasswd-sudo \
        render-config setup \
        build-local run run-local run-with-tunnel stop \
        health pull rebuild

# ── Installation targets ──────────────────────────────────────────────────────

install-make:
	@./scripts/install-make.sh

install-docker:
	@./scripts/install-docker.sh

VERSION := dev
install-underleaf:
	@echo "Installing underleaf version $(VERSION)..."
	@./scripts/install-underleaf.sh $(VERSION)

enable-nopasswd-sudo:
	@sudo sh scripts/enable-nopasswd-sudo.sh

# ── Config rendering ──────────────────────────────────────────────────────────
# Renders hyphae/config.yaml from hyphae/config.yaml.template using envsubst.
# Requires a .env file in this directory (copy .env.example → .env and fill in values).
render-config:
	@if [ ! -f .env ]; then \
		echo "Error: .env not found. Copy .env.example → .env and fill in secrets." >&2; \
		exit 1; \
	fi
	@echo "Rendering hyphae/config.yaml from template..."
	@set -a; . ./.env; set +a; envsubst < hyphae/config.yaml.template > hyphae/config.yaml
	@echo "hyphae/config.yaml generated."

# ── Full setup ────────────────────────────────────────────────────────────────
# install-make runs first so subsequent targets can rely on make being present.
# render-config runs last so the config is ready before services start.
setup: install-make enable-nopasswd-sudo install-docker install-underleaf render-config

# ── Local image build (HYPHAE_IMAGE_MODE=local) ───────────────────────────────
# Builds the hyphae Docker image from Mac source via OrbStack's /mnt/mac mount.
# Writes the resulting tag to .local-image-tag for use by run-local / rebuild.
build-local:
	@./scripts/build-local.sh

# ── Service lifecycle ─────────────────────────────────────────────────────────

## Start hyphae from Docker Hub image (HYPHAE_IMAGE_MODE=develop or v*).
run:
	@./scripts/refresh-tunnel-certs.sh
	@docker compose up -d --force-recreate hyphae

## Build from source then start hyphae (HYPHAE_IMAGE_MODE=local).
run-local: build-local
	@./scripts/refresh-tunnel-certs.sh
	@IMAGE_TAG="$$(cat .local-image-tag)"; \
	 echo "Starting hyphae with locally-built image: ambientlabsjose/hyphae:$$IMAGE_TAG"; \
	 HYPHAE_IMAGE_TAG="$$IMAGE_TAG" docker compose up -d --force-recreate hyphae

## Start hyphae + Cloudflare tunnel (requires CF_TUNNEL_TOKEN in .env).
run-with-tunnel:
	@docker compose --profile tunnel up -d

stop:
	@docker compose --profile tunnel down

## Rebuild: mode-aware. local → rebuild from source; remote → pull from Hub.
rebuild:
	@set -a; . ./.env; set +a; \
	./scripts/refresh-tunnel-certs.sh; \
	if [ "$${HYPHAE_IMAGE_MODE:-develop}" = "local" ]; then \
		./scripts/build-local.sh; \
		IMAGE_TAG="$$(cat .local-image-tag)"; \
		HYPHAE_IMAGE_TAG="$$IMAGE_TAG" docker compose up -d --force-recreate hyphae; \
	else \
		docker compose pull hyphae && docker compose up -d --force-recreate hyphae; \
	fi

# ── Observability ─────────────────────────────────────────────────────────────

health:
	@docker compose exec hyphae hyphctl health

pull:
	@docker compose pull hyphae

