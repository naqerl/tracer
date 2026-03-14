#!/usr/bin/env bash
set -euo pipefail

collector_bin=/usr/local/bin/tracer-collector
collector_config=/etc/tracer/collector.yaml
collector_group=tracer-collector
units=(
    tracer-demo-api.service
    tracer-demo-worker.service
    tracer-demo-cron.service
)

wait_for_unit_active() {
    local unit=$1
    local attempts=${2:-60}

    for ((i=0; i<attempts; i++)); do
        if systemctl is-active --quiet "$unit"; then
            return 0
        fi
        sleep 1
    done

    echo "Timed out waiting for ${unit} to become active" >&2
    return 1
}

wait_for_file() {
    local path=$1
    local attempts=${2:-60}

    for ((i=0; i<attempts; i++)); do
        if [[ -s "$path" ]]; then
            return 0
        fi
        sleep 1
    done

    echo "Timed out waiting for file ${path}" >&2
    return 1
}

main() {
    wait_for_file "$collector_config"
    wait_for_unit_active tracer-collector.service

    for unit in "${units[@]}"; do
        wait_for_unit_active "$unit"
    done

    # Let startup bursts land in journald before format guessing reads them.
    sleep 3

    for unit in "${units[@]}"; do
        "$collector_bin" -config "$collector_config" add --override "$unit"
    done

    chown root:"$collector_group" "$collector_config"
    chmod 0640 "$collector_config"
    systemctl restart tracer-collector.service
    wait_for_unit_active tracer-collector.service

    # Restart demo apps so their startup burst happens after the collector is attached.
    for unit in "${units[@]}"; do
        systemctl restart "$unit"
    done
}

main "$@"
