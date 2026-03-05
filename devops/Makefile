.PHONY: run run-orb run-do copy-payload deploy-payload deploy-payload-orb copy-payload-do deploy-payload-do

ENV ?= local
run:
	@echo "Running hyphae in ${ENV} environment..."
	@if [ "${ENV}" = "local" ]; then \
		$(MAKE) run-orb; \
	else \
		$(MAKE) run-do ENV=${ENV}; \
	fi
	@make copy-payload ENV=${ENV}
	@make deploy-payload ENV=${ENV}

run-orb:
	@echo "Running hyphae using Orb..."
	./orb_scripts/run-vm.sh

run-do:
	@echo "Not implemented: Running hyphae on DigitalOcean for ${ENV} environment"
	@exit 1

copy-payload:
	@echo "Copying payload to ${ENV} environment..."
	@if [ "${ENV}" = "local" ]; then \
		./orb_scripts/copy-payload.sh; \
	else \
		echo "Not implemented: Copying payload to DigitalOcean for ${ENV} environment"; \
		exit 1; \
	fi

deploy-payload:
	@echo "Deploying payload on ${ENV} environment..."
	@if [ "${ENV}" = "local" ]; then \
		./orb_scripts/deploy-payload.sh; \
	else \
		echo "Not implemented: Deploying payload on DigitalOcean for ${ENV} environment"; \
		exit 1; \
	fi