# syntax=docker/dockerfile:1
#
# tracer-panel image.
# Builds a static binary, then wraps it in a minimal runtime with an
# entrypoint that runs `install` + `init` on first start.
#
# Build:
#   docker build -t tracer-panel -f apps/panel/Dockerfile .
#
# Run:
#   docker run \
#     -e TRACER_ADMIN_USERNAME=admin \
#     -e TRACER_ADMIN_PASSWORD=secret \
#     -e TRACER_PANEL_HOST=https://tracer.example.com \
#     -v /host/config:/etc/tracer \
#     -v /host/data:/var/lib/tracer \
#     -p 8080:8080 \
#     tracer-panel
#
#   The entrypoint runs `tracer-panel install`, which creates the
#   service user/group `tracer-panel:tracer` and chowns /etc/tracer
#   (root:tracer) and the data dir (tracer-panel:tracer). Host-mounted
#   volumes get the same ownership applied, so no manual chown is
#   needed on first start.

# ── Stage 1: Build ───────────────────────────────────────────────────────────
FROM docker.io/library/golang:1.26-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=0
ARG VERSION=unknown
RUN WITH_SYSTEMD=no VERSION=${VERSION} make build-panel

# ── Stage 2: Runtime ─────────────────────────────────────────────────────────
FROM debian:bookworm-slim

LABEL org.opencontainers.image.title="tracer-panel"
LABEL org.opencontainers.image.description="Log aggregation & alerting panel"
LABEL org.opencontainers.image.source="https://git.naqerl.com/naqerl/tracer"
LABEL org.opencontainers.image.licenses="MIT"

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /src/bin/tracer-panel /usr/local/bin/tracer-panel
COPY apps/panel/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080

ENTRYPOINT ["/entrypoint.sh"]
