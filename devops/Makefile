.PHONY: help _preflight \
        run run-orb run-do \
        copy-payload deploy-payload \
        redeploy build \
        ssh status logs health \
        destroy destroy-orb

ENV ?= local

# ── Help ──────────────────────────────────────────────────────────────────────

help: ## Show this help message
	@echo "Hyphae DevOps — OrbStack VM deployment"
	@echo ""
	@echo "First-time setup:"
	@echo "  make run               Create VM, copy payload, deploy services"
	@echo ""
	@echo "Update existing VM (no VM recreate):"
	@echo "  make redeploy          Re-copy payload and redeploy services"
	@echo ""
	@echo "Daily operations:"
	@echo "  make ssh               Open a shell in the VM"
	@echo "  make status            Show running containers (docker ps)"
	@echo "  make logs              Tail hyphae container logs"
	@echo "  make health            Run hyphctl health check"
	@echo ""
	@echo "Teardown:"
	@echo "  make destroy           Stop and delete the VM"
	@echo ""
	@echo "Variables:"
	@echo "  ENV=local (default)    Target environment (only 'local' is implemented)"

# ── Preflight ─────────────────────────────────────────────────────────────────
# Validates prerequisites before creating a VM or redeploying.
# Runs before 'run' and 'redeploy' so we fail fast — not after VM creation.
_preflight:
	@if [ ! -f payload/hyphae-devops/.env ]; then \
		echo "Error: payload/hyphae-devops/.env not found." >&2; \
		echo "Run: cp payload/hyphae-devops/.env.example payload/hyphae-devops/.env" >&2; \
		echo "Then fill in AUTH0_PASSWORD and (optionally) CF_TUNNEL_TOKEN." >&2; \
		exit 1; \
	fi

# ── First-time provisioning ───────────────────────────────────────────────────

run: _preflight ## Create VM, copy payload, deploy services (first time)
	@echo "Running hyphae in ${ENV} environment..."
	@if [ "${ENV}" = "local" ]; then \
		$(MAKE) run-orb; \
	else \
		$(MAKE) run-do ENV=${ENV}; \
	fi
	@$(MAKE) copy-payload ENV=${ENV}
	@$(MAKE) deploy-payload ENV=${ENV}

run-orb:
	@echo "Running hyphae using Orb..."
	./orb_scripts/run-vm.sh

run-do: ## (stub) DigitalOcean provisioning — not yet implemented
	@echo "Not implemented: DigitalOcean provisioning for ENV=${ENV}." >&2
	@echo "To implement: add orb_scripts/do-scripts/ with equivalents of run-vm.sh." >&2
	@exit 1

# ── Payload management ────────────────────────────────────────────────────────

copy-payload: ## Stage certs and copy payload to the VM
	@echo "Copying payload to ${ENV} environment..."
	@if [ "${ENV}" = "local" ]; then \
		./orb_scripts/copy-payload.sh; \
	else \
		echo "Not implemented: copy-payload for ENV=${ENV}" >&2; \
		exit 1; \
	fi

deploy-payload: ## Bootstrap and start services on the VM
	@echo "Deploying payload on ${ENV} environment..."
	@if [ "${ENV}" = "local" ]; then \
		./orb_scripts/deploy-payload.sh; \
	else \
		echo "Not implemented: deploy-payload for ENV=${ENV}" >&2; \
		exit 1; \
	fi

redeploy: _preflight ## Re-copy payload and restart services without recreating the VM
	@echo "Redeploying to existing VM (ENV=${ENV})..."
	@$(MAKE) copy-payload ENV=${ENV}
	@$(MAKE) deploy-payload ENV=${ENV}

build: _vm-name ## Rebuild hyphae image from source in the VM (local mode only)
	@ssh -o StrictHostKeyChecking=no "$$(cat .vm-name)@orb" \
		'cd ~/hyphae-devops && make build-local'
	@echo "Image rebuilt. Run 'make redeploy' to restart the container with the new image."

# ── Operations ────────────────────────────────────────────────────────────────

_vm-name:
	@if [ ! -f .vm-name ]; then echo "Error: no VM found (.vm-name missing). Run 'make run' first." >&2; exit 1; fi

ssh: _vm-name ## Open an interactive shell in the VM
	@ssh -o StrictHostKeyChecking=no "$$(cat .vm-name)@orb"

status: _vm-name ## Show running containers on the VM
	@ssh -o StrictHostKeyChecking=no "$$(cat .vm-name)@orb" 'docker ps'

logs: _vm-name ## Tail hyphae container logs
	@ssh -o StrictHostKeyChecking=no "$$(cat .vm-name)@orb" \
		'cd ~/hyphae-devops && docker compose logs -f --tail=100 hyphae'

health: _vm-name ## Run hyphctl health check on the VM
	@ssh -o StrictHostKeyChecking=no "$$(cat .vm-name)@orb" \
		'cd ~/hyphae-devops && make health'

# ── Teardown ──────────────────────────────────────────────────────────────────

destroy: ## Stop and delete the VM
	@echo "Destroying hyphae environment: ${ENV}..."
	@if [ "${ENV}" = "local" ]; then \
		$(MAKE) destroy-orb; \
	else \
		echo "Not implemented: destroy for ENV=${ENV}" >&2; \
		exit 1; \
	fi

destroy-orb:
	@echo "Destroying OrbStack VM..."
	./orb_scripts/destroy-vm.sh

