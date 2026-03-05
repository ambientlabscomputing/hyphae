.PHONY: install-docker setup

install-docker:
	@./scripts/install-docker.sh

VERSION := dev
install-underleaf:
	@echo "Installing underleaf version $(VERSION)..."
	@./scripts/install-underleaf.sh $(VERSION)

enable-nopasswd-sudo:
	@sudo sh scripts/enable-nopasswd-sudo.sh

setup: enable-nopasswd-sudo install-docker install-underleaf

run:
	@docker compose up -d

stop:
	@docker compose down

health:
	@docker compose exec hyphae hyphctl health

pull:
	@docker compose pull

