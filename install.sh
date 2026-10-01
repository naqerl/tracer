#!/usr/bin/env bash
#
# Tracer quick install script.
#
# Downloads pre-built binaries from the repository and runs their install
# subcommands to set up users, groups, directories, config files, and
# systemd service units.
#
# Usage:
#   curl -fsSL https://git.naqerl.com/naqerl/tracer/raw/branch/main/install.sh | sudo bash
#
# Environment variables:
#   TRACER_VERSION       - version tag to download (default: latest)
#   TRACER_COLLECTOR_ONLY - set to 1 to skip installing the panel
#   TRACER_DIR           - installation directory for binaries (default: /usr/local/bin)
#   TRACER_COLLECTOR_NAME - collector name to configure (skips `set name`)
#   TRACER_COLLECTOR_API_KEY - collector API key to configure
#   TRACER_COLLECTOR_PANEL_HOST - panel address (host:port or URL) to configure
#   DEBUG                - set to 1 to enable shell tracing
#
# Re-running the script is supported: an existing collector is upgraded in
# place and its configuration is preserved.
#
# Note: sudo resets the environment by default, so pass variables through sudo
# explicitly instead of exporting them first, e.g.:
#   curl -fsSL https://git.naqerl.com/naqerl/tracer/raw/main/install.sh | sudo TRACER_COLLECTOR_ONLY=1 bash
#

set -euo pipefail

# --- Debug mode ---------------------------------------------------------------
if [ "${DEBUG:-}" = "1" ]; then
	set -x
fi

# --- Defaults ----------------------------------------------------------------
TRACER_DIR="${TRACER_DIR:-/usr/local/bin}"
DOWNLOAD_URL="${DOWNLOAD_URL:-https://git.naqerl.com/naqerl/tracer/releases/download}"
API_URL="https://git.naqerl.com/api/v1/repos/naqerl/tracer"

# --- Helpers -----------------------------------------------------------------
log()  { printf "\033[1;32m%s\033[0m\n" "$*" >&2; }
warn() { printf "\033[1;33m%s\033[0m\n" "$*" >&2; }
die()  { printf "\033[1;31m%s\033[0m\n" "$*" >&2; exit 1; }

# --- Preflight checks --------------------------------------------------------
if [ "$(id -u)" -ne 0 ]; then
	die "This script must be run as root. Try: curl ... | sudo bash"
fi

for cmd in curl sha256sum uname chmod cp; do
	if ! command -v "$cmd" >/dev/null 2>&1; then
		die "Required command not found: $cmd"
	fi
done

ARCH=$(uname -m)
case "$ARCH" in
	x86_64|amd64) ARCH="amd64" ;;
	aarch64|arm64) ARCH="arm64" ;;
	*) die "Unsupported architecture: $ARCH" ;;
esac

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
if [ "$OS" != "linux" ]; then
	die "Unsupported OS: $OS (only Linux is supported)"
fi

# --- Determine version -------------------------------------------------------
TRACER_VERSION="${TRACER_VERSION:-latest}"
if [ "$TRACER_VERSION" = "latest" ]; then
	log "Detecting latest tracer version..."
	TRACER_VERSION=$(curl -fsSL "${API_URL}/releases/latest" 2>/dev/null | grep '"tag_name"' | cut -d'"' -f6 || echo "")
	if [ -z "$TRACER_VERSION" ]; then
		die "Could not determine latest version from Forgejo API"
	fi
fi

# --- Collector's previous state ------------------------------------------------
# An existing collector is upgraded: its unit is stopped before the binary is
# replaced, then started again at the end.
COLLECTOR_UNIT="tracer-collector"
COLLECTOR_WAS_INSTALLED=0
if systemctl cat "${COLLECTOR_UNIT}.service" >/dev/null 2>&1; then
	COLLECTOR_WAS_INSTALLED=1
fi

log "Installing tracer ${TRACER_VERSION} (${ARCH})..."

# --- Download binaries -------------------------------------------------------
BINARIES=("tracer-collector")
if [ "${TRACER_COLLECTOR_ONLY:-}" != "1" ]; then
	BINARIES+=("tracer-panel")
fi

TMPDIR=$(mktemp -d) || die "Failed to create temporary directory"
trap 'rm -rf "$TMPDIR"' EXIT
mkdir -p "$TMPDIR/bin"

for bin in "${BINARIES[@]}"; do
	URL="${DOWNLOAD_URL}/${TRACER_VERSION}/${bin}"
	log "Downloading ${bin}..."
	curl -fsSL "$URL" -o "$TMPDIR/bin/$bin" || die "Failed to download ${bin} from ${URL}"
done

log "Verifying checksums..."
# Release checksums.txt lists binaries with a "bin/" prefix (repo-relative
# paths), so files are downloaded into "$TMPDIR/bin". Each downloaded binary
# is verified against its own entry; this also works in collector-only mode,
# where checksums.txt still lists the panel binary we did not download.
curl -fsSL "${DOWNLOAD_URL}/${TRACER_VERSION}/checksums.txt" -o "$TMPDIR/checksums.txt" || die "Failed to download checksums"
for bin in "${BINARIES[@]}"; do
	# tail -1 takes the most recent entry if the checksums file accumulated
	# multiple hashes for the same binary; || true keeps the guard below
	# reachable under set -euo pipefail.
	expected=$(grep "bin/${bin}$" "$TMPDIR/checksums.txt" | awk '{print $1}' | tail -1 || true)
	if [ -z "$expected" ]; then
		die "No checksum entry found for ${bin} in release checksums"
	fi
	actual=$(sha256sum "$TMPDIR/bin/$bin" | awk '{print $1}')
	if [ "$actual" != "$expected" ]; then
		die "Checksum mismatch for ${bin}: expected ${expected}, got ${actual}"
	fi
	log "  ${bin} checksum OK"
done

log "Setting permissions..."
for bin in "${BINARIES[@]}"; do
	chmod 0755 "$TMPDIR/bin/$bin"
done

# --- Install binaries --------------------------------------------------------
log "Installing binaries to ${TRACER_DIR}..."
mkdir -p "$TRACER_DIR"
if [ "${COLLECTOR_WAS_INSTALLED}" -eq 1 ]; then
	# A running executable cannot be overwritten (ETXTBSY).
	log "Stopping ${COLLECTOR_UNIT}..."
	systemctl stop "${COLLECTOR_UNIT}.service" || warn "Could not stop ${COLLECTOR_UNIT}"
fi
for bin in "${BINARIES[@]}"; do
	install -m 0755 "$TMPDIR/bin/$bin" "${TRACER_DIR}/${bin}"
	log "  Installed ${TRACER_DIR}/${bin}"
done

# Ensure binaries are in PATH for the install subcommand
export PATH="$TRACER_DIR:$PATH"

# --- Run tracer-collector install --------------------------------------------
log "Running tracer-collector install..."
tracer-collector install

# --- Configure the collector ---------------------------------------------------
# Values passed through the environment win, so a panel-generated install
# command configures the collector in a single run.
configure_collector() {
	local key="$1" envvar="$2"
	local value="${!envvar:-}"
	if [ -n "$value" ]; then
		log "Configuring collector ${key}..."
		tracer-collector set "$key" "$value"
	fi
}
configure_collector name TRACER_COLLECTOR_NAME
configure_collector api-key TRACER_COLLECTOR_API_KEY
configure_collector panel-host TRACER_COLLECTOR_PANEL_HOST

# --- Run tracer-panel install (if applicable) --------------------------------
if [ "${TRACER_COLLECTOR_ONLY:-}" != "1" ]; then
	log "Running tracer-panel install..."
	tracer-panel install

	log "Initializing panel..."
	set +e
	OUTPUT=$(tracer-panel -init 2>&1)
	EXIT_CODE=$?
	set -e

	if [ "$EXIT_CODE" -eq 2 ] || echo "$OUTPUT" | grep -q "already initialized"; then
		log "Panel already initialized, skipping initialization"
	elif [ "$EXIT_CODE" -ne 0 ]; then
		die "Failed to initialize panel (exit code ${EXIT_CODE}):\n${OUTPUT}"
	else
		ADMIN_USERNAME=$(echo "$OUTPUT" | grep '^admin_username=' | cut -d= -f2)
		ADMIN_PASSWORD=$(echo "$OUTPUT" | grep '^admin_password=' | cut -d= -f2)
		INIT_API_KEY=$(echo "$OUTPUT" | grep '^collector_api_key=' | cut -d= -f2)

		if [ -z "$INIT_API_KEY" ]; then
			die "Panel init succeeded but no collector_api_key in output"
		fi

		log "Configuring collector with panel API key..."
		tracer-collector set api-key "$INIT_API_KEY"

		echo ""
		echo "========================================"
		echo "Panel initialized!"
		echo "========================================"
		if [ -n "$ADMIN_USERNAME" ] && [ -n "$ADMIN_PASSWORD" ]; then
			echo "Admin credentials:"
			echo "  Username: $ADMIN_USERNAME"
			echo "  Password: $ADMIN_PASSWORD"
			echo ""
			echo "Save these credentials and change the password after first login."
		fi
	fi

	PANEL_PORT=$(tracer-panel config port) || die "Failed to get panel port from config"
fi

start_services() {
	local unit="$1"
	systemctl enable "${unit}" >/dev/null 2>&1 || warn "Could not enable ${unit} for boot"
	systemctl restart "${unit}" || die "${unit} failed to start. Check: journalctl -u ${unit} -n 50"
	log "${unit} is running"
}

echo ""
echo "========================================"
echo "Installation complete!"
echo "========================================"
echo ""

if [ "${TRACER_COLLECTOR_ONLY:-}" != "1" ] && [ -n "${PANEL_PORT:-}" ]; then
	start_services tracer-panel.service
	echo ""
	echo "Panel URL: http://localhost:${PANEL_PORT}"
	echo ""
fi

log "Starting tracer-collector..."
start_services "${COLLECTOR_UNIT}.service"

# Only worth checking when the collector actually has somewhere to connect.
if [ -n "${TRACER_COLLECTOR_API_KEY:-}${INIT_API_KEY:-}" ]; then
	echo ""
	log "Verifying the collector can reach the panel..."
	tracer-collector verify || warn "The collector could not reach the panel yet. Check the panel address and API key, then run: sudo tracer-collector verify"
else
	echo ""
	warn "The collector has no panel address configured yet. Set it with:"
	echo "  sudo tracer-collector set api-key <collector-api-key>"
	echo "  sudo tracer-collector set panel-host <panel-host>:8080"
fi

echo ""
echo "Follow the collector logs with: journalctl -u ${COLLECTOR_UNIT} -f"
