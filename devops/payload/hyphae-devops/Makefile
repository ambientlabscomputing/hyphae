.PHONY: install-make install-docker install-underleaf enable-nopasswd-sudo setup

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

# install-make runs first so that subsequent targets can rely on make being present.
# build-essential is already bootstrapped by deploy-payload.sh before this runs,
# but the explicit target ensures idempotency on re-runs.
setup: install-make enable-nopasswd-sudo install-docker install-underleaf

run:
	@docker compose up -d

stop:
	@docker compose down

health:
	@docker compose exec hyphae hyphctl health

pull:
	@docker compose pull

