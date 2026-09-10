#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

DEMO_CONTAINER="${DEMO_CONTAINER:-tracer-e2e}"
DEMO_PORT="${DEMO_PORT:-18081}"
DEMO_URL="${DEMO_URL:-http://127.0.0.1:${DEMO_PORT}}"
DEMO_USER="${DEMO_USER:-demo}"
DEMO_PASSWORD="${DEMO_PASSWORD:-demo1234}"

expected_units=(
  install-on-boot.service
  tracer-panel.service
  tracer-collector.service
  nginx.service
  demo-api.service
  demo-worker.service
  demo-cron.service
  demo-conflict.service
)

app_names=(
  demo-api
  demo-worker
  demo-cron
  naqerl.com
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

# assert_no_errors only looks at entries produced during the check run so that
# stale noise from earlier manual testing does not trip the journal check.
CHECK_START="$(date '+%Y-%m-%d %H:%M:%S')"

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
    if grep -Eq '<div class="text-2xl font-bold[^"]*">[1-9][0-9]*</div>.*<div class="[^"]*text-xs text-fg-secondary">Total Logs</div>' <<<"${compact}"; then
      return 0
    fi
    sleep 1
  done
  fail "demo-api did not show ingested logs"
}

wait_for_panel_health() {
  local collector="$1"
  local attempt grid
  # Probe the apps grid fragment for a rendered uptime value.
  for attempt in $(seq 1 30); do
    grid="$(curl -fsS "${DEMO_URL}/panel/collectors/${collector}/apps" -H "Cookie: ${COOKIE}")"
    # The value span sits right after the "uptime:" label; require an actual
    # N/A or numeric value rather than just the label.
    if grep -Eq 'uptime: <span[^>]*>(N/A|[0-9]+)<' <<<"${grid}"; then
      return 0
    fi
    sleep 1
  done
  fail "panel apps grid did not render baked uptime"
}

assert_no_errors() {
  local unit="$1"
  local output
  output="$(docker exec "${DEMO_CONTAINER}" journalctl -u "${unit}" --no-pager --since "${CHECK_START}" || true)"
  # Filter out transient startup errors (connection refused during initial startup, user creation)
  # "attempt to write a readonly database" is a boot-time race: the panel
  # service starts while install-on-boot is still running `tracer-panel -init`
  # (which writes the DB); systemd restarts the panel and it comes up fine.
  filtered_output="$(printf '%s\n' "${output}" | grep -E 'SOURCE_ERROR|level=ERROR|[Ff]ailed to ' | \
    grep -v 'Failed to connect to panel' | \
    grep -v 'connection refused' | \
    grep -v 'Failed to determine user credentials' | \
    grep -v 'Failed at step USER spawning' | \
    grep -v 'attempt to write a readonly database' | \
    grep -v 'failed to reap process' | \
    # demo-conflict intentionally emits two log shapes that disagree on the
    # level key (for the add-app conflict UI); the more permissive parser
    # rejects the other shape, which is the expected panel noise.
    grep -v 'Invalid log request.*app=demo-conflict' || true)"
  if [[ -n "${filtered_output}" ]]; then
    printf '%s\n' "${filtered_output}" >&2
    fail "${unit} contains error logs"
  fi
}

require_cmd curl
require_cmd go
require_cmd docker

docker inspect "${DEMO_CONTAINER}" >/dev/null 2>&1 || fail "container ${DEMO_CONTAINER} is not running"

log "waiting for panel HTTP endpoint"
wait_for_http

log "checking expected systemd units"
for unit in "${expected_units[@]}"; do
  state="$(docker exec "${DEMO_CONTAINER}" systemctl is-active "${unit}")"
  [[ "${state}" == "active" ]] || fail "${unit} is ${state}"
done

log "checking failed units allowlist"
failed_units="$(docker exec "${DEMO_CONTAINER}" systemctl --failed --no-legend || true)"
if [[ -n "${failed_units}" ]]; then
  unexpected="$(printf '%s\n' "${failed_units}" | grep -Ev 'sys-kernel-(config|debug|tracing)\.mount|proc-sys-fs-binfmt_misc\.automount|getty@tty1\.service' || true)"
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

wait_for_apps() {
  local collector="$1"
  local attempt page
  for attempt in $(seq 1 30); do
    page="$(curl -fsS "${DEMO_URL}/panel/collectors/${collector}/apps" -H "Cookie: ${COOKIE}")"
    local all_present=true
    for app in "${app_names[@]}"; do
      if ! grep -Fq "${app}" <<<"${page}"; then
        all_present=false
        break
      fi
    done
    if [ "$all_present" = "true" ]; then
      return 0
    fi
    sleep 1
  done
  fail "apps page missing apps: ${app_names[*]} after 30s"
}

log "checking collector received config from panel"
attempt=0
until [ "$(docker exec "${DEMO_CONTAINER}" journalctl -u tracer-collector --no-pager -o cat 2>&1 | grep -c 'applied panel-pushed config.*apps=')" -gt 0 ]; do
  attempt=$((attempt+1))
  [ "${attempt}" -ge 30 ] && fail "collector never received panel-pushed config after 30s"
  sleep 1
done

log "checking registered apps list"
wait_for_apps "${collector_name}"
apps_page="$(curl -fsS "${DEMO_URL}/panel/collectors/${collector_name}/apps" -H "Cookie: ${COOKIE}")"
for app in "${app_names[@]}"; do
  grep -Fq "${app}" <<<"${apps_page}" || fail "apps page missing ${app}"
done

log "checking panel health and demo logs"
wait_for_panel_health "${collector_name}"
wait_for_logs "${collector_name}"

log "checking app edit form"
# The edit form must show the app name in a disabled input (names are immutable)
# and a working log-source input that can be updated via PUT.
edit_page="$(curl -fsS "${DEMO_URL}/panel/collectors/${collector_name}/apps/demo-api/settings" -H "Cookie: ${COOKIE}")"

# Name input must be disabled and pre-filled with the app name.
if ! grep -Eq 'name="name".*value="demo-api".*disabled' <<<"${edit_page}"; then
  fail "edit form name input must be disabled and pre-filled with demo-api"
fi

# Log source input must be present and editable (not disabled).
if ! grep -Eq 'name="service"[^>]*id="app-service-input"' <<<"${edit_page}"; then
  fail "edit form log source input (app-service-input) must be present"
fi

# Verify the form submits via PUT.
if ! grep -Eq 'hx-put="[^"]+/apps/demo-api"' <<<"${edit_page}"; then
  fail "edit form must use hx-put for update"
fi

# Update the log source via PUT and verify the save response. A health URL
# must not drop the journalctl source (the form always submits both blocks).
put_response="$(curl -fsS -X PUT \
  -d 'name=demo-api' \
  -d 'service=demo-api.service' \
  -d 'source_type=journalctl' \
  -d 'source_params=unit: demo-api.service' \
  -d 'parser_type=json' \
  -d 'parser_param_level_field=level' \
  -d 'parser_param_message_field=msg' \
  -d 'parser_param_timestamp_field=time' \
  -d 'parser_param_timestamp_format=2006-01-02T15:04:05.999999999Z07:00' \
  -d 'url=http://localhost:8080/healthz' \
  -d 'interval=5s' \
  -d 'expected_status=200' \
  -H "Cookie: ${COOKIE}" \
  "${DEMO_URL}/panel/collectors/${collector_name}/apps/demo-api")"
if ! grep -Fq 'App "demo-api" saved' <<<"${put_response}"; then
  fail "PUT update for demo-api did not return success"
fi
if ! grep -Eq 'hx-get="[^"]+/apps/demo-api"' <<<"${put_response}"; then
  fail "saved response for edit must hx-get the app page (not apps list)"
fi

demo_api_settings="$(curl -fsS "${DEMO_URL}/panel/collectors/${collector_name}/apps/demo-api/settings" -H "Cookie: ${COOKIE}")"
grep -Fq 'value="journalctl"' <<<"${demo_api_settings}" || fail "demo-api edit form lost journalctl source after save with health URL"
grep -Fq 'value="http://localhost:8080/healthz"' <<<"${demo_api_settings}" || fail "demo-api edit form lost health URL after save"

log "checking create/update save logs and health together"
combo_app="combo-app"
create_response="$(curl -fsS -X POST \
  -d "name=${combo_app}" \
  -d 'service=demo-api.service' \
  -d 'source_type=journalctl' \
  -d 'source_params=unit: demo-api.service' \
  -d 'parser_type=json' \
  -d 'parser_param_level_field=level' \
  -d 'parser_param_message_field=msg' \
  -d 'parser_param_timestamp_field=time' \
  -d 'parser_param_timestamp_format=2006-01-02T15:04:05.999999999Z07:00' \
  -d 'url=http://localhost:8080/healthz' \
  -d 'interval=5s' \
  -d 'expected_status=200' \
  -H "Cookie: ${COOKIE}" \
  "${DEMO_URL}/panel/collectors/${collector_name}/apps")"
if ! grep -Fq "App \"${combo_app}\" saved" <<<"${create_response}"; then
  fail "POST create for ${combo_app} (logs+health) did not return success: ${create_response}"
fi

wait_for_app_settings() {
  local app="$1"
  local attempt page
  for attempt in $(seq 1 30); do
    page="$(curl -sS "${DEMO_URL}/panel/collectors/${collector_name}/apps/${app}/settings" -H "Cookie: ${COOKIE}" || true)"
    if grep -Fq 'value="journalctl"' <<<"${page}" && grep -Fq 'value="http://localhost:8080/healthz"' <<<"${page}"; then
      return 0
    fi
    sleep 1
  done
  fail "${app} settings did not show both journalctl source and health URL"
}
wait_for_app_settings "${combo_app}"

wait_for_combo_logs() {
  local attempt page compact
  for attempt in $(seq 1 30); do
    page="$(curl -sS "${DEMO_URL}/panel/collectors/${collector_name}/apps/${combo_app}/stats" -H "Cookie: ${COOKIE}" || true)"
    compact="$(tr '\n' ' ' <<<"${page}")"
    if grep -Eq '<div class="text-2xl font-bold[^"]*">[1-9][0-9]*</div>.*<div class="[^"]*text-xs text-fg-secondary">Total Logs</div>' <<<"${compact}"; then
      return 0
    fi
    sleep 1
  done
  fail "${combo_app} did not ingest logs after create with health URL"
}
wait_for_combo_logs

uptime_only="uptime-only-app"
uptime_response="$(curl -fsS -X POST \
  -d "name=${uptime_only}" \
  -d 'url=http://localhost:8080/healthz' \
  -d 'interval=5s' \
  -d 'expected_status=200' \
  -H "Cookie: ${COOKIE}" \
  "${DEMO_URL}/panel/collectors/${collector_name}/apps")"
if ! grep -Fq "App \"${uptime_only}\" saved" <<<"${uptime_response}"; then
  fail "POST create for ${uptime_only} did not return success: ${uptime_response}"
fi

wait_for_uptime_settings() {
  local attempt page
  for attempt in $(seq 1 30); do
    page="$(curl -sS "${DEMO_URL}/panel/collectors/${collector_name}/apps/${uptime_only}/settings" -H "Cookie: ${COOKIE}" || true)"
    if grep -Fq 'value="http://localhost:8080/healthz"' <<<"${page}"; then
      if grep -Eq 'option[^>]*value="journalctl"[^>]*selected' <<<"${page}"; then
        fail "${uptime_only} should not have a journalctl source"
      fi
      return 0
    fi
    sleep 1
  done
  fail "${uptime_only} settings did not show health URL"
}
wait_for_uptime_settings

# Add a health URL to a logs-only app (demo-cron) without dropping its source.
cron_put="$(curl -fsS -X PUT \
  -d 'name=demo-cron' \
  -d 'service=demo-cron.service' \
  -d 'source_type=journalctl' \
  -d 'source_params=unit: demo-cron.service' \
  -d 'parser_type=json' \
  -d 'parser_param_level_field=level' \
  -d 'parser_param_message_field=msg' \
  -d 'parser_param_timestamp_field=time' \
  -d 'parser_param_timestamp_format=2006-01-02T15:04:05.999999999Z07:00' \
  -d 'url=http://localhost:8080/healthz' \
  -d 'interval=5s' \
  -d 'expected_status=200' \
  -H "Cookie: ${COOKIE}" \
  "${DEMO_URL}/panel/collectors/${collector_name}/apps/demo-cron")"
if ! grep -Fq 'App "demo-cron" saved' <<<"${cron_put}"; then
  fail "PUT update adding health to demo-cron did not return success"
fi
wait_for_app_settings "demo-cron"

log "checking collector self-registration"
# The collector must have connected and registered its apps. Verify the apps
# page shows the demo apps with uptime values.
collector_card="$(curl -fsS "${DEMO_URL}/panel/collectors/${collector_name}/apps" -H "Cookie: ${COOKIE}")"
if ! grep -Eq 'uptime: <span[^>]*>([0-9]+|N/A)<' <<<"${collector_card}"; then
  fail "apps page did not render uptime values"
fi

log "crawling panel UI"
(
  cd "${REPO_ROOT}"
  if $no_auth; then
    go run ./e2e/crawler/main.go -url "${DEMO_URL}" -no-auth -start /panel/
  else
    go run ./e2e/crawler/main.go -url "${DEMO_URL}" -username "${DEMO_USER}" -password "${DEMO_PASSWORD}" -start /panel/
  fi
)

log "checking registered-but-disabled collector indicator"
idle_collector="demo-idle"
idle_unit="tracer-collector-idle.service"
last_update_url="${DEMO_URL}/panel/collectors/${idle_collector}/last-update"

# The panel must keep listing demo-idle as a registered collector even though
# its service only reports stats during its first minute.
attempt=0
until curl -fsS "${DEMO_URL}/panel/" -H "Cookie: ${COOKIE}" | grep -Fq "${idle_collector}"; do
  attempt=$((attempt+1))
  [ "${attempt}" -ge 30 ] && fail "dashboard did not list registered collector ${idle_collector}"
  sleep 1
done

wait_for_last_update() {
  local phase="$1" regex="$2"
  local attempt page
  for attempt in $(seq 1 60); do
    page="$(curl -fsS "${last_update_url}" -H "Cookie: ${COOKIE}")"
    if grep -Eq "${regex}" <<<"${page}"; then
      return 0
    fi
    sleep 1
  done
  fail "last-update indicator for ${idle_collector} did not ${phase}"
}

wait_for_idle_stats() {
  local attempt page
  for attempt in $(seq 1 60); do
    page="$(curl -fsS "${DEMO_URL}/panel/collectors/${idle_collector}/stats" -H "Cookie: ${COOKIE}")"
    if ! grep -Fq 'No stats available' <<<"${page}"; then
      return 0
    fi
    sleep 1
  done
  fail "${idle_collector} did not report resource stats"
}

# Seed at least one stats sample so "stale after stop" has a timestamp to age,
# then confirm the indicator turns stale once events stop and fresh again
# once the collector resumes reporting.
docker exec "${DEMO_CONTAINER}" systemctl start "${idle_unit}"
wait_for_idle_stats
docker exec "${DEMO_CONTAINER}" systemctl stop "${idle_unit}" >/dev/null 2>&1 || true
wait_for_last_update "turn stale after its service stopped" 'text-(yellow-400|red-400)[^>]*>[^<]*last update'

docker exec "${DEMO_CONTAINER}" systemctl start "${idle_unit}"
wait_for_last_update "hide once fresh after its service restarted" '^[[:space:]]*$'

# Leave the demo in the registered-but-disabled state.
docker exec "${DEMO_CONTAINER}" systemctl stop "${idle_unit}" >/dev/null 2>&1 || true

log "checking panel and collector journals"
assert_no_errors tracer-panel.service
assert_no_errors tracer-collector.service

# The hot reload test mutates the collector config via the ingest API. Read-only
# mode is enforced by the nginx proxy in front of the panel, which rejects
# non-GET requests. Detect it by probing a state-changing request through the
# public URL, and verify the rejection when read-only is on.
api_key="$(docker exec "${DEMO_CONTAINER}" sed -n 's/.*api_key: *\([^ ]*\).*/\1/p' /etc/tracer/collector.yaml | head -n1)"
if [ -z "${api_key}" ]; then
  fail "could not read collector api key from /etc/tracer/collector.yaml"
fi

if docker exec "${DEMO_CONTAINER}" grep -q 'return 405' /etc/nginx/conf.d/tracer.conf 2>/dev/null; then
  log "panel is read-only; verifying write rejection"
  probe_code="$(curl -sS -o /dev/null -w '%{http_code}' -X PUT \
    -H "X-API-Key: ${api_key}" \
    -H "Content-Type: application/json" \
    --data-binary '{"parsers": {}, "apps": []}' \
    "${DEMO_URL}/ingest/config/${collector_name}")"
  if [ "${probe_code}" != "405" ] && [ "${probe_code}" != "403" ]; then
    fail "read-only proxy accepted a state-changing request (code ${probe_code})"
  fi
  log "read-only proxy correctly rejected state-changing request"
else
  log "panel is writable; testing collector config hot reload via panel API"
  # The collector is now driven entirely from the panel over WebSocket. Push a
  # config that adds an app, verify it appears in the dashboard without any
  # collector restart (hot reload), then push a config that removes it and
  # verify it disappears.

  put_apps_config() {
    local names_json="$1"
    curl -fsS -X PUT \
      -H "X-API-Key: ${api_key}" \
      -H "Content-Type: application/json" \
      --data-binary "$(cat <<EOF
{
  "parsers": { "json": {"type": "json", "params": {"level_field": "level", "message_field": "msg", "timestamp_field": "time", "timestamp_format": "2006-01-02T15:04:05.999999999Z07:00"}} },
  "apps": ${names_json}
}
EOF
  )" "${DEMO_URL}/ingest/config/${collector_name}" >/dev/null
  }

  hot_app="hot-reload-demo"
  base_apps="[\
    {\"name\": \"demo-api\", \"parser_id\": \"json\", \"source\": {\"type\": \"journalctl\", \"params\": {\"unit\": \"demo-api.service\"}}, \"lifecycle\": {\"type\": \"systemd\"}},\
    {\"name\": \"demo-worker\", \"parser_id\": \"json\", \"source\": {\"type\": \"journalctl\", \"params\": {\"unit\": \"demo-worker.service\"}}, \"lifecycle\": {\"type\": \"systemd\"}},\
    {\"name\": \"demo-cron\", \"parser_id\": \"json\", \"source\": {\"type\": \"journalctl\", \"params\": {\"unit\": \"demo-cron.service\"}}, \"lifecycle\": {\"type\": \"systemd\"}},\
    {\"name\": \"naqerl.com\", \"health\": {\"type\": \"http\", \"params\": {\"url\": \"https://naqerl.com\", \"interval\": \"30s\", \"expected_status\": \"200\"}}}\
  ]"
  full_apps="${base_apps%]*}, {\"name\": \"${hot_app}\", \"parser_id\": \"json\", \"source\": {\"type\": \"journalctl\", \"params\": {\"unit\": \"${hot_app}.service\"}}, \"lifecycle\": {\"type\": \"systemd\"}}]"

  put_apps_config "${full_apps}"
  attempt=0
  until curl -fsS "${DEMO_URL}/panel/collectors/${collector_name}/apps" -H "Cookie: ${COOKIE}" | grep -Fq "${hot_app}"; do
    attempt=$((attempt+1))
    [ "${attempt}" -ge 30 ] && fail "hot-loaded app ${hot_app} did not appear in the dashboard"
    sleep 1
  done
  log "hot reload: app ${hot_app} appeared without a collector restart"

  # Revert to the original apps so the hot-reload app is removed.
  put_apps_config "${base_apps}"

  # Verify the collector applied the reduced config via its journal logs.
  # The panel retains app databases for log history, so the app won't disappear
  # from the apps page — check the collector journal instead.
  attempt=0
  until [ "$(docker exec "${DEMO_CONTAINER}" journalctl -u tracer-collector --no-pager -o cat 2>&1 | grep -c 'applied panel-pushed config.*apps=4')" -gt 0 ]; do
    attempt=$((attempt+1))
    [ "${attempt}" -ge 30 ] && fail "collector did not apply reverted config after 30s"
    sleep 1
  done
  log "hot reload: collector applied reverted config (4 apps)"
fi

log "e2e checks passed"
