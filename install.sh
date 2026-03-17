#!/bin/bash
#
# Tracer Installation Script
# Usage: curl -fsSL https://raw.githubusercontent.com/ai-shift/tracer/main/install.sh | sudo bash
#
# This script downloads binaries to /tmp and lets them handle their own installation.
#
set -euo pipefail

REPO="ai-shift/tracer"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "This script must be run as root (use sudo)"
        exit 1
    fi
}

detect_arch() {
    case $(uname -m) in
        x86_64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) log_error "Unsupported architecture: $(uname -m)"; exit 1 ;;
    esac
}

get_latest_version() {
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/'
}

main() {
    check_root

    echo ""
    echo "=========================================="
    echo "       Tracer Installation Script        "
    echo "=========================================="
    echo ""

    local version=${TRACER_VERSION:-$(get_latest_version)}
    log_info "Installing tracer ${version}..."

    local tmp_dir=$(mktemp -d)
    trap "rm -rf ${tmp_dir}" EXIT

    # Download binaries to /tmp
    if [[ -n "${TRACER_BIN_DIR:-}" ]]; then
        log_info "Using local binaries from ${TRACER_BIN_DIR}"
        cp "${TRACER_BIN_DIR}/tracer-collector" "${tmp_dir}/"
        cp "${TRACER_BIN_DIR}/tracer-panel" "${tmp_dir}/" 2>/dev/null || true
    else
        local arch=$(detect_arch)
        log_info "Downloading tracer ${version} for linux-${arch}..."
        curl -fsSL "https://github.com/${REPO}/releases/download/${version}/tracer-${version}-linux-${arch}.tar.gz" \
            | tar -xz -C "${tmp_dir}"
    fi

    # Run panel self-installation first (unless collector-only) to initialize and get API key
    local api_key=""
    if [[ -z "${TRACER_COLLECTOR_ONLY:-}" ]]; then
        log_info "Setting up tracer-panel..."
        # Panel logs to stderr, only API key goes to stdout
        api_key=$("${tmp_dir}/tracer-panel" install setup)
        if [[ -n "$api_key" ]]; then
            log_info "Panel initialized, API key generated"
        fi
    fi

    # Run collector self-installation from /tmp
    log_info "Setting up tracer-collector..."
    if [[ -n "$api_key" ]]; then
        # Pass API key to collector via flag
        "${tmp_dir}/tracer-collector" install setup -api-key "$api_key"
    else
        "${tmp_dir}/tracer-collector" install setup
    fi

    echo ""
    echo "=============================================="
    log_info "Installation complete!"
    echo "=============================================="
    echo ""

    # Start services
    log_info "Starting services..."
    systemctl enable --now tracer-collector

    if [[ -z "${TRACER_COLLECTOR_ONLY:-}" ]]; then
        systemctl enable --now tracer-panel
    fi

    # Add self-monitoring
    if [[ -z "${TRACER_COLLECTOR_ONLY:-}" ]]; then
        log_info "Configuring self-monitoring..."

        # Wait for panel to be ready by watching journalctl for 5 log entries
        journalctl -u tracer-panel -f | timeout 10 head -n 5 || true
        tracer-collector add \
                         --health "$(tracer-panel config host)/auth/login" \
                         tracer-panel.service || true
    fi

    # Wait for collector logs
    journalctl -u tracer-collector -f | timeout 10 head -n 5 || true
    tracer-collector add tracer-collector.service || true

    echo ""
    echo "Services status:"
    systemctl status tracer-collector --no-pager -l | head -5
    if [[ -z "${TRACER_COLLECTOR_ONLY:-}" ]]; then
        systemctl status tracer-panel --no-pager -l | head -5
    fi
}

main "$@"
