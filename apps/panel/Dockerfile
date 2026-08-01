# syntax=docker/dockerfile:1
#
# Multi-stage build for tracer-panel.
# Produces a minimal scratch-based image containing only the statically
# linked Go binary and CA certificates for TLS outbound connections
# (Telegram, ntfy, etc.).
#
# Build:
#   docker build -t tracer-panel -f apps/panel/Dockerfile .
#
# Run:
#   docker run -v /host/data:/var/lib/tracer tracer-panel [flags]
#

# ── Stage 1: Build ───────────────────────────────────────────────────────────
FROM docker.io/library/golang:1.26-bookworm AS build

WORKDIR /src

# Cache module downloads in a separate layer
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build via make with env var overrides (no systemd in container).
# CGO_ENABLED=0 is critical: the runtime stage is scratch (no libc),
# so the binary MUST be statically linked.
ENV CGO_ENABLED=0
RUN WITH_SYSTEMD=no make build-panel

# ── Stage 2: Runtime (scratch — minimal) ──────────────────────────────────────
# The binary is statically linked (CGO_ENABLED=0), so it needs nothing from the
# OS.  We copy CA certificates so HTTPS outbound works (Telegram, ntfy, etc.).
FROM scratch

LABEL org.opencontainers.image.title="tracer-panel"
LABEL org.opencontainers.image.description="Log aggregation & alerting panel"
LABEL org.opencontainers.image.source="https://git.naqerl.com/naqerl/tracer"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=build /src/bin/tracer-panel /tracer-panel
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# Default data directory (override at runtime with TRACER_DATA_DIR env)
ENV TRACER_DATA_DIR=/var/lib/tracer

EXPOSE 8080

ENTRYPOINT ["/tracer-panel"]
CMD ["-config", "/etc/tracer/panel.yaml"]
