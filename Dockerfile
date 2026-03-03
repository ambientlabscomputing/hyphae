# Build stage
FROM golang:1.25-alpine AS builder

# Install build dependencies
RUN apk add --no-cache --no-scripts git make

WORKDIR /build

# Copy go mod files first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the server binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o hyphae ./cmd/serve

# Build the admin CLI
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o hyphctl ./cmd/hyphctl

# Runtime stage
FROM alpine:latest

# Install ca-certificates for HTTPS (--no-scripts avoids trigger issues in ARM64 QEMU)
RUN apk --no-cache add --no-scripts ca-certificates tzdata wget

WORKDIR /app

# Copy the binaries from builder
COPY --from=builder /build/hyphae .
COPY --from=builder /build/hyphctl /usr/local/bin/hyphctl

# Create a non-root user
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser && \
    chown -R appuser:appuser /app

USER appuser

# Management API port
EXPOSE 8084
# Tunnel listener port (mTLS, inbound from MMA nodes)
EXPOSE 9090
# Public HTTPS gateway port
EXPOSE 443

# Liveness probe against the management API health endpoint
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --spider -q http://localhost:8084/health || exit 1

# Run the server
CMD ["./hyphae"]
