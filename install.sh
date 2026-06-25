#!/usr/bin/env bash
#
# Tracer quick install script.
#
# Downloads pre-built binaries from the repository and runs their install
# subcommands to set up users, groups, directories, config files, and
# systemd service units.
#
# Usage:
#   curl -fsSL https://git.naqerl.com/eco/tracer/plain/install.sh | sudo bash
#
# Environment variables:
#   TRACER_VERSION       - version tag to download (default: latest)
#   TRACER_COLLECTOR_ONLY - set to 1 to skip installing the panel
#   TRACER_DIR           - installation directory for binaries (default: /usr/local/bin)
#

set -euo pipefail

# --- Defaults ----------------------------------------------------------------
TRACER_DIR="${TRACER_DIR:-/usr/local/bin}"
DOWNLOAD_URL="${DOWNLOAD_URL:-https://git.naqerl.com/eco/tracer/releases/download}"
REPO_URL="https://git.naqerl.com/eco/tracer"

# --- Helpers -----------------------------------------------------------------
log()  { printf "\033[1;32m%s\033[0m\n" "$*" >&2; }
warn() { printf "\033[1;33m%s\033[0m\n" "$*" >&2; }
die()  { printf "\033[1;31m%s\033[0m\n" "$*" >&2; exit 1; }

# --- Preflight checks --------------------------------------------------------
if [ "$(id -u)" -ne 0 ]; then
	die "This script must be run as root. Try: curl ... | sudo bash"
fi

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
	TRACER_VERSION=$(curl -fsSL "$REPO_URL/raw/main/VERSION" 2>/dev/null || echo "")
	if [ -z "$TRACER_VERSION" ]; then
		warn "Could not determine latest version, falling back to main branch snapshot"
		TRACER_VERSION="main"
	fi
fi

log "Installing tracer ${TRACER_VERSION} (${ARCH})..."

# --- Download binaries -------------------------------------------------------
BINARIES=("tracer-collector")
if [ -z "${TRACER_COLLECTOR_ONLY:-}" ]; then
	BINARIES+=("tracer-panel")
fi

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

for bin in "${BINARIES[@]}"; do
	URL="${DOWNLOAD_URL}/${TRACER_VERSION}/${bin}-linux-${ARCH}"
	log "Downloading ${bin}..."
	curl -fsSL "$URL" -o "$TMPDIR/$bin" || die "Failed to download ${bin} from ${URL}"
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
	OUTPUT=$(tracer-panel -init 2>/dev/null || true)
	if [ -n "$OUTPUT" ]; then
		eval "$OUTPUT"
		if [ -n "${collector_api_key:-}" ]; then
			log "Configuring collector with panel API key..."
			tracer-collector set api-key "$collector_api_key"
		fi
		echo ""
		echo "========================================"
		echo "Panel initialized!"
		echo "========================================"
		if [ -n "${admin_username:-}" ] && [ -n "${admin_password:-}" ]; then
			echo "Admin credentials:"
			echo "  Username: $admin_username"
			echo "  Password: $admin_password"
			echo ""
			echo "Save these credentials and change the password after first login."
		fi
	fi
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
echo "Panel URL: http://localhost:8080"
