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
#   DEBUG                - set to 1 to enable shell tracing
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

for cmd in curl sha256sum uname chmod cp tar; do
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
	TRACER_VERSION=$(curl -fsSL "${API_URL}/releases/latest" 2>/dev/null | grep '"tag_name"' | cut -d'"' -f4 || echo "")
	if [ -z "$TRACER_VERSION" ]; then
		die "Could not determine latest version from Forgejo API"
	fi
fi

log "Installing tracer ${TRACER_VERSION} (${ARCH})..."

# --- Download binaries -------------------------------------------------------
BINARIES=("tracer-collector")
if [ -z "${TRACER_COLLECTOR_ONLY:-}" ]; then
	BINARIES+=("tracer-panel")
fi

TMPDIR=$(mktemp -d) || die "Failed to create temporary directory"
trap 'rm -rf "$TMPDIR"' EXIT

for bin in "${BINARIES[@]}"; do
	ARCHIVE="${bin}-linux-${ARCH}.tar.gz"
	URL="${DOWNLOAD_URL}/${TRACER_VERSION}/${ARCHIVE}"
	log "Downloading ${bin}..."
	curl -fsSL "$URL" -o "$TMPDIR/$ARCHIVE" || die "Failed to download ${bin} from ${URL}"
done

log "Verifying checksums..."
curl -fsSL "${DOWNLOAD_URL}/${TRACER_VERSION}/checksums.txt" -o "$TMPDIR/checksums.txt" || die "Failed to download checksums"
sed "s|^.*/|${TMPDIR}/|" "$TMPDIR/checksums.txt" > "$TMPDIR/checksums.fixed.txt"
(cd "$TMPDIR" && sha256sum -c "checksums.fixed.txt") || die "Checksum verification failed"

log "Extracting binaries..."
for bin in "${BINARIES[@]}"; do
	ARCHIVE="${bin}-linux-${ARCH}.tar.gz"
	tar -xzf "$TMPDIR/$ARCHIVE" -C "$TMPDIR"
	chmod 0755 "$TMPDIR/$bin"
done

# --- Install binaries --------------------------------------------------------
log "Installing binaries to ${TRACER_DIR}..."
mkdir -p "$TRACER_DIR"
for bin in "${BINARIES[@]}"; do
	cp "$TMPDIR/$bin" "${TRACER_DIR}/${bin}"
	log "  Installed ${TRACER_DIR}/${bin}"
done

# Ensure binaries are in PATH for the install subcommand
export PATH="$TRACER_DIR:$PATH"

# --- Run tracer-collector install --------------------------------------------
log "Running tracer-collector install..."
tracer-collector install

# --- Run tracer-panel install (if applicable) --------------------------------
if [ -z "${TRACER_COLLECTOR_ONLY:-}" ]; then
	log "Running tracer-panel install..."
	tracer-panel install

	log "Initializing panel..."
	set +e
	OUTPUT=$(tracer-panel -init 2>&1)
	EXIT_CODE=$?
	set -e

	if [ "$EXIT_CODE" -eq 2 ]; then
		log "Panel already initialized, skipping initialization"
	elif [ "$EXIT_CODE" -ne 0 ]; then
		die "Failed to initialize panel (exit code ${EXIT_CODE})"
	else
		ADMIN_USERNAME=$(echo "$OUTPUT" | grep '^admin_username=' | cut -d= -f2)
		ADMIN_PASSWORD=$(echo "$OUTPUT" | grep '^admin_password=' | cut -d= -f2)
		COLLECTOR_API_KEY=$(echo "$OUTPUT" | grep '^collector_api_key=' | cut -d= -f2)

		if [ -z "$COLLECTOR_API_KEY" ]; then
			die "Panel init succeeded but no collector_api_key in output"
		fi

		log "Configuring collector with panel API key..."
		tracer-collector set api-key "$COLLECTOR_API_KEY"

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

echo ""
echo "========================================"
echo "Installation complete!"
echo "========================================"
echo ""
echo "Start services with:"
echo "  sudo systemctl start tracer-collector"
if [ -z "${TRACER_COLLECTOR_ONLY:-}" ]; then
	echo "  sudo systemctl start tracer-panel"
fi
echo ""
if [ -n "${PANEL_PORT:-}" ]; then
	echo "Panel URL: http://localhost:${PANEL_PORT}"
fi
