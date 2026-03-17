#!/usr/bin/env bash
set -euo pipefail

collector_bin="/usr/local/bin/tracer-collector"

units=(
    tracer-demo-api.service
    tracer-demo-worker.service
    tracer-demo-cron.service
)

main() {
    for unit in "${units[@]}"; do
        journalctl -u "$unit" -f | timeout 10 head -n 5 || true
        "$collector_bin" add "$unit" || true
    done

    systemctl restart tracer-collector.service
}

main "$@"
