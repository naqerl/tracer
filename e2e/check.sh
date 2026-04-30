#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

DEMO_CONTAINER="${DEMO_CONTAINER:-tracer-e2e}"
DEMO_PORT="${DEMO_PORT:-18081}"
DEMO_URL="${DEMO_URL:-http://127.0.0.1:${DEMO_PORT}}"
DEMO_USER="${DEMO_USER:-demo}"
DEMO_PASSWORD="${DEMO_PASSWORD:-demo123}"

expected_units=(
  install-on-boot.service
  tracer-panel.service
  tracer-collector.service
  demo-api.service
  demo-worker.service
  demo-cron.service
)

app_names=(
  tracer-panel
  tracer-collector
  demo-api
  demo-worker
  demo-cron
)

log() {
  printf '[check] %s\n' "$1"
}

fail() {
  printf '[fail] %s\n' "$1" >&2
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

collector_name_from_dashboard() {
  local dashboard="$1"
  local match
  match="$(grep -oE '/panel/collectors/[^"? ]+' <<<"${dashboard}" | head -n1 || true)"
  [[ -n "${match}" ]] || fail "dashboard did not expose a collector link"
  basename "${match}"
}

wait_for_http() {
  local attempt
  for attempt in $(seq 1 30); do
    if curl -fsS "${DEMO_URL}/auth/login" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "panel did not become reachable at ${DEMO_URL}/auth/login"
}

wait_for_logs() {
  local collector="$1"
  local attempt page compact
  for attempt in $(seq 1 30); do
    # Check the stats endpoint directly since main page loads async via htmx
    page="$(curl -fsS "${DEMO_URL}/panel/collectors/${collector}/apps/demo-api/stats" -H "Cookie: ${COOKIE}")"
    compact="$(tr '\n' ' ' <<<"${page}")"
    if grep -Eq '<div class="text-2xl font-bold">[1-9][0-9]*</div>.*<div class="text-xs text-fg-secondary">Total Logs</div>' <<<"${compact}"; then
      return 0
    fi
    sleep 1
  done
  fail "demo-api did not show ingested logs"
}

wait_for_panel_health() {
  local collector="$1"
  local attempt uptime
  for attempt in $(seq 1 30); do
    uptime="$(curl -fsS "${DEMO_URL}/panel/collectors/${collector}/apps/tracer-panel/uptime/percentage" -H "Cookie: ${COOKIE}")"
    if grep -Eq '[0-9]+\.[0-9]{2}%?|[0-9]+%?|N/A' <<<"${uptime}"; then
      return 0
    fi
    sleep 1
  done
  fail "panel uptime endpoint did not return a percentage"
}

assert_no_errors() {
  local unit="$1"
  local output
  output="$(podman exec "${DEMO_CONTAINER}" journalctl -u "${unit}" --no-pager || true)"
  # Filter out transient startup errors (connection refused during initial startup, user creation)
  filtered_output="$(printf '%s\n' "${output}" | grep -E 'SOURCE_ERROR|level=ERROR|[Ff]ailed to ' | \
    grep -v 'Failed to connect to panel' | \
    grep -v 'connection refused' | \
    grep -v 'Failed to determine user credentials' | \
    grep -v 'Failed at step USER spawning' || true)"
  if [[ -n "${filtered_output}" ]]; then
    printf '%s\n' "${filtered_output}" >&2
    fail "${unit} contains error logs"
  fi
}

require_cmd curl
require_cmd go
require_cmd podman

podman container exists "${DEMO_CONTAINER}" >/dev/null 2>&1 || fail "container ${DEMO_CONTAINER} is not running"

log "waiting for panel HTTP endpoint"
wait_for_http

log "checking expected systemd units"
for unit in "${expected_units[@]}"; do
  state="$(podman exec "${DEMO_CONTAINER}" systemctl is-active "${unit}")"
  [[ "${state}" == "active" ]] || fail "${unit} is ${state}"
done

log "checking failed units allowlist"
failed_units="$(podman exec "${DEMO_CONTAINER}" systemctl --failed --no-legend || true)"
if [[ -n "${failed_units}" ]]; then
  unexpected="$(printf '%s\n' "${failed_units}" | grep -Ev 'sys-kernel-(config|debug|tracing)\.mount|proc-sys-fs-binfmt_misc\.automount' || true)"
  [[ -z "${unexpected}" ]] || fail "unexpected failed units detected: ${unexpected}"
fi

log "checking panel login and session"
# Detect if panel is in no-auth mode
no_auth=false
if curl -fsS -o /dev/null -w "%{http_code}" "${DEMO_URL}/panel/" 2>/dev/null | grep -q '200'; then
  no_auth=true
  COOKIE=""
  log "no-auth mode detected, skipping login"
else
  login_headers="$(curl -fsS -D - -o /dev/null -X POST -d "username=${DEMO_USER}" -d "password=${DEMO_PASSWORD}" "${DEMO_URL}/auth/login")"
  COOKIE="$(printf '%s\n' "${login_headers}" | awk -F': ' '/^Set-Cookie: tracer_session=/{print $2}' | cut -d';' -f1)"
  [[ -n "${COOKIE}" ]] || fail "panel login did not return tracer_session cookie"
fi

log "checking dashboard"
dashboard="$(curl -fsS "${DEMO_URL}/panel/" -H "Cookie: ${COOKIE}")"
grep -Fq "resources" <<<"${dashboard}" || fail "dashboard did not render resources link"
collector_name="$(collector_name_from_dashboard "${dashboard}")"

log "checking registered apps list"
apps_page="$(curl -fsS "${DEMO_URL}/panel/collectors/${collector_name}/apps" -H "Cookie: ${COOKIE}")"
for app in "${app_names[@]}"; do
  grep -Fq "${app}" <<<"${apps_page}" || fail "apps page missing ${app}"
done

log "checking panel health and demo logs"
wait_for_panel_health "${collector_name}"
wait_for_logs "${collector_name}"

log "crawling panel UI"
(
  cd "${REPO_ROOT}"
  if $no_auth; then
    go run ./e2e/crawler/main.go -url "${DEMO_URL}" -no-auth -start /panel/
  else
    go run ./e2e/crawler/main.go -url "${DEMO_URL}" -username "${DEMO_USER}" -password "${DEMO_PASSWORD}" -start /panel/
  fi
)

log "checking panel and collector journals"
assert_no_errors tracer-panel.service
assert_no_errors tracer-collector.service

log "e2e checks passed"
