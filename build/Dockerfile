# syntax=docker/dockerfile:1

# ============================================================================
# DockWarden - Multi-stage, Multi-arch Dockerfile
# ============================================================================

# Build stage
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /build

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build arguments for cross-compilation
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

# Build the binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-w -s \
        -X github.com/emon5122/dockwarden/internal/meta.Version=${VERSION} \
        -X github.com/emon5122/dockwarden/internal/meta.Commit=${COMMIT} \
        -X github.com/emon5122/dockwarden/internal/meta.BuildDate=${BUILD_DATE}" \
    -o dockwarden \
    ./cmd/dockwarden

# ============================================================================
# Runtime stage - minimal scratch image
# ============================================================================
FROM scratch

# Labels following OCI Image Format Specification
LABEL org.opencontainers.image.title="DockWarden" \
      org.opencontainers.image.description="Modern Docker Container Manager - Auto-Update & Health Monitor" \
      org.opencontainers.image.authors="emon5122" \
      org.opencontainers.image.url="https://github.com/emon5122/dockwarden" \
      org.opencontainers.image.source="https://github.com/emon5122/dockwarden" \
      org.opencontainers.image.documentation="https://github.com/emon5122/dockwarden#readme" \
      org.opencontainers.image.vendor="emon5122" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.base.name="scratch"

# Copy CA certificates for HTTPS registry communication
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy timezone data for TZ support
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the binary
COPY --from=builder /build/dockwarden /dockwarden

# Default timezone
ENV TZ=Asia/Dhaka

# Expose API port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD ["/dockwarden", "--health-check"]

# Entry point - runs as root to access Docker socket
ENTRYPOINT ["/dockwarden"]
