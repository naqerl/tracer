#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEMO_CONTAINER="${DEMO_CONTAINER:-tracer-e2e}"

WATCH_PATHS=(
  "${ROOT_DIR}/go.mod"
  "${ROOT_DIR}/go.sum"
  "${ROOT_DIR}/Makefile"
  "${ROOT_DIR}/apps/panel"
  "${ROOT_DIR}/libs"
)

log() {
  printf '[live-reload] %s\n' "$*"
}

require_cmd() {
  local cmd="$1"
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    printf 'Missing required command: %s\n' "${cmd}" >&2
    exit 1
  fi
}

ensure_container_running() {
  if ! docker inspect "${DEMO_CONTAINER}" >/dev/null 2>&1; then
    printf 'Container %s does not exist. Run `make -C e2e up` first.\n' "${DEMO_CONTAINER}" >&2
    exit 1
  fi

  local state
  state="$(docker inspect -f '{{.State.Status}}' "${DEMO_CONTAINER}")"
  if [[ "${state}" != "running" ]]; then
    printf 'Container %s is not running (state: %s).\n' "${DEMO_CONTAINER}" "${state}" >&2
    exit 1
  fi
}

print_watch_files() {
  local path
  for path in "${WATCH_PATHS[@]}"; do
    if [[ -d "${path}" ]]; then
      find "${path}" -type f \
        \( -name '*.go' -o -name '*.html' -o -name '*.css' -o -name '*.js' -o -name '*.sql' -o -name '*.yaml' -o -name '*.yml' \) \
        -print
    elif [[ -f "${path}" ]]; then
      printf '%s\n' "${path}"
    fi
  done | sort -u
}

reload_panel() {
  log "Building panel binary"
  make -C "${ROOT_DIR}" build-panel

  log "Copying binary into ${DEMO_CONTAINER}"
  docker cp "${ROOT_DIR}/bin/tracer-panel" "${DEMO_CONTAINER}:/tmp/tracer-panel.new"

  log "Replacing binary and restarting tracer-panel.service"
  if ! docker exec "${DEMO_CONTAINER}" bash -lc \
    'install -m 0755 /tmp/tracer-panel.new /usr/local/bin/tracer-panel && rm -f /tmp/tracer-panel.new && systemctl restart tracer-panel.service && systemctl is-active --quiet tracer-panel.service'; then
    printf 'Failed to restart tracer-panel.service. Inspect with `make -C e2e logs` or `docker exec %s systemctl status tracer-panel.service`.\n' "${DEMO_CONTAINER}" >&2
    exit 1
  fi

  log "Panel reload complete"
}

main() {
  require_cmd make
  require_cmd docker
  require_cmd entr
  require_cmd find

  ensure_container_running

  log "Watching panel sources for changes with entr"
  log "Container: ${DEMO_CONTAINER}"

  reload_panel

  print_watch_files | entr -dnrs 'bash -lc "set -euo pipefail; cd \"'"${ROOT_DIR}"'\"; ./e2e/live-reload.sh --reload"'
}

if [[ "${1:-}" == "--reload" ]]; then
  require_cmd make
  require_cmd docker
  ensure_container_running
  reload_panel
  exit 0
fi

main "$@"
