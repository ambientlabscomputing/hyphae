.PHONY: proto
proto:
	@echo "Generating gRPC code from proto definitions..."
	@mkdir -p internal/proto
	protoc \
		--proto_path=proto \
		--go_out=internal/proto \
		--go_opt=paths=source_relative \
		--go-grpc_out=internal/proto \
		--go-grpc_opt=paths=source_relative \
		proto/admin/common.proto \
		proto/admin/admin_health.proto \
		proto/admin/admin_leases.proto \
		proto/admin/admin_connections.proto
	@echo "Proto code generation complete"


.PHONY: run
run:
	export CONFIG_PATH=${PWD}/config.yaml && \
	go run ./cmd/serve

.PHONY: build
build:
	go build -o bin/hyphae ./cmd/serve
	@echo "hyphae built → bin/hyphae"

VERSION ?= dev

.PHONY: build-hyphctl
build-hyphctl:
	go build -ldflags "-X main.version=$(VERSION)" -o bin/hyphctl ./cmd/hyphctl
	@echo "hyphctl built → bin/hyphctl"

.PHONY: install-hyphctl
install-hyphctl:
	go install -ldflags "-X main.version=$(VERSION)" ./cmd/hyphctl
	@echo "hyphctl installed to GOPATH/bin"

.PHONY: test
test:
	go test ./... -v

.PHONY: test-unit
test-unit:
	go test ./... -v

.PHONY: docs
docs:
	swag init -g cmd/serve/main.go --output docs

.PHONY: clean
clean:
	rm -f bin/hyphae bin/hyphctl
	rm -rf docs

.PHONY: docker-publish-dev
docker-publish-dev:
	docker build -t ambientlabsjose/hyphae:develop . && \
	docker push ambientlabsjose/hyphae:develop

TAG ?=
.PHONY: docker-publish-tag
docker-publish-tag:
	@if [ -z "$(TAG)" ]; then \
		echo "Error: TAG is required. Usage: make docker-publish-tag TAG=v1.2.3" >&2; \
		exit 1; \
	fi
	docker build -t ambientlabsjose/hyphae:$(TAG) . && \
	docker push ambientlabsjose/hyphae:$(TAG)
	@echo "Published ambientlabsjose/hyphae:$(TAG)"

.PHONY: tidy
tidy:
	go mod tidy
