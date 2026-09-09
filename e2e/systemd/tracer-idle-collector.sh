#!/usr/bin/env bash
set -euo pipefail

# Runs the demo-idle collector: a second tracer collector that connects to the
# panel, registers, and reports resource stats for one minute, then stops. The
# panel keeps listing it as registered, so its "last update X ago" indicator
# ages and turns yellow then red — demonstrating a registered-but-disabled
# collector on the dashboard.

collector_pid=""

stop_collector() {
  if [ -n "${collector_pid}" ]; then
    kill "${collector_pid}" 2>/dev/null || true
    wait "${collector_pid}" 2>/dev/null || true
    collector_pid=""
  fi
}

trap stop_collector TERM INT EXIT

tracer-collector -config /etc/tracer/collector-idle.yaml &
collector_pid=$!

sleep 60 || true
stop_collector
exit 0