#!/usr/bin/env bash
set -euo pipefail

export TRACER_BIN_DIR=/opt/tracer-bin
export TRACER_VERSION=local
export TRACER_PANEL_HOST="${TRACER_PANEL_HOST:-http://localhost:8080}"
export TRACER_ADMIN_USERNAME="${TRACER_ADMIN_USERNAME:-demo}"
export TRACER_ADMIN_PASSWORD="${TRACER_ADMIN_PASSWORD:-demo123}"

/opt/tracer/install.sh

# Register demo apps in collector
collector_bin="/usr/local/bin/tracer-collector"

units=(
    tracer-demo-api.service
    tracer-demo-worker.service
    tracer-demo-cron.service
)

for unit in "${units[@]}"; do
    journalctl -u "$unit" -f | timeout 10 head -n 5 || true
    "$collector_bin" add "$unit" || true
done

systemctl restart tracer-collector.service
