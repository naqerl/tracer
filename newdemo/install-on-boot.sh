#!/usr/bin/env bash
set -euo pipefail

export TRACER_BIN_DIR=/opt/tracer-bin
export TRACER_VERSION=local
export TRACER_PANEL_HOST="${TRACER_PANEL_HOST:-http://localhost:8080}"
export TRACER_AUTOSTART=1
export TRACER_SELF_MONITOR=1
export TRACER_ADMIN_USERNAME="${TRACER_ADMIN_USERNAME:-demo}"
export TRACER_ADMIN_PASSWORD="${TRACER_ADMIN_PASSWORD:-demo123}"

/opt/tracer/install.sh
