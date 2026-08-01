# syntax=docker/dockerfile:1
#
# Multi-stage build for tracer-panel.
# Produces a self-contained scratch-based image with the binary,
# config, and initialized database baked in.
#
# Build:
#   docker build -t tracer-panel -f apps/panel/Dockerfile .
#
# Run:
#   docker run -v /host/data:/var/lib/tracer -p 8080:8080 tracer-panel
#

# ── Stage 1: Build ───────────────────────────────────────────────────────────
FROM docker.io/library/golang:1.26-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build via make with env var overrides (no systemd in container).
# CGO_ENABLED=0 is critical: the runtime stage is scratch (no libc),
# so the binary MUST be statically linked.
ENV CGO_ENABLED=0
RUN WITH_SYSTEMD=no make build-panel

# ── Stage 2: Init (has shell for install/init subcommands) ──────────────────
FROM debian:bookworm-slim AS init

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /src/bin/tracer-panel /usr/local/bin/tracer-panel

# install: creates /etc/tracer, /var/lib/tracer, generates panel.yaml
# -init: creates admin user + collector API key in the SQLite DB
ENV TRACER_PANEL_HOST=https://tracer.naqerl.com
RUN tracer-panel install && \
    tracer-panel -init

# ── Stage 3: Runtime (scratch — minimal) ─────────────────────────────────────
FROM scratch

LABEL org.opencontainers.image.title="tracer-panel"
LABEL org.opencontainers.image.description="Log aggregation & alerting panel"
LABEL org.opencontainers.image.source="https://git.naqerl.com/naqerl/tracer"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=init /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=init /etc/passwd /etc/passwd
COPY --from=init /etc/group /etc/group
COPY --from=build /src/bin/tracer-panel /usr/local/bin/tracer-panel
COPY --from=init /etc/tracer /etc/tracer
COPY --from=init --chown=tracer-panel:tracer /var/lib/tracer /var/lib/tracer

EXPOSE 8080

ENTRYPOINT ["tracer-panel"]
