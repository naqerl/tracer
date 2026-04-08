#!/bin/bash
#
# Tracer Installation Script
# Usage: curl -fsSL https://git.naqerl.com/eco/tracer/plain/install.sh | sudo bash
#
# This script installs Go if needed, downloads the latest stable source from main,
# configures the build, and installs tracer. Supports multiple init systems via
# ./configure options.
#
# The main branch always contains the latest stable version.
#
# Options:
#   --without-panel    Install collector only (no panel)
#   --without-systemd  Disable systemd support
#
set -euo pipefail

REPO_URL="https://git.naqerl.com/eco/tracer"
GO_MIN_VERSION="1.23.0"
GO_DOWNLOAD_VERSION="1.23.6"

# Stable version source - main branch always contains latest stable
tracer_ref="main"

# Parse command line arguments
CONFIGURE_FLAGS=""
WITHOUT_PANEL=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --without-panel)
            WITHOUT_PANEL=true
            CONFIGURE_FLAGS="$CONFIGURE_FLAGS --without-panel"
            shift
            ;;
        --without-systemd)
            CONFIGURE_FLAGS="$CONFIGURE_FLAGS --without-systemd"
            shift
            ;;
        --prefix=*|--bindir=*|--sysconfdir=*|--localstatedir=*)
            CONFIGURE_FLAGS="$CONFIGURE_FLAGS $1"
            shift
            ;;
        --help|-h)
            echo "Usage: install.sh [options]"
            echo ""
            echo "Installation options:"
            echo "  --without-panel       Install collector only (no panel)"
            echo "  --without-systemd     Disable systemd support"
            echo "  --prefix=PATH         Installation prefix (default: /usr/local)"
            echo "  --bindir=PATH         Binary directory (default: PREFIX/bin)"
            echo "  --sysconfdir=PATH     Config directory (default: /etc)"
            echo "  --localstatedir=PATH  State directory (default: /var)"
            echo "  --help                Show this help"
            echo ""
            echo "Environment variables:"
            echo "  TRACER_ADMIN_USERNAME Admin username (default: admin)"
            echo "  TRACER_ADMIN_PASSWORD Admin password (auto-generated if not set)"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

# Trim leading space
CONFIGURE_FLAGS="${CONFIGURE_FLAGS# }"

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

version_ge() {
    printf '%s\n%s\n' "$2" "$1" | sort -V -C
}

check_go() {
    if command -v go &> /dev/null; then
        local current_version
        current_version=$(go version | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1)
        if [[ -n "$current_version" ]]; then
            if version_ge "$current_version" "$GO_MIN_VERSION"; then
                log_info "Found Go $current_version"
                return 0
            else
                log_warn "Go $current_version found, but $GO_MIN_VERSION+ required"
            fi
        fi
    fi
    return 1
}

install_go_temp() {
    log_info "Go ${GO_MIN_VERSION}+ is required but not found in PATH"
    echo ""
    echo "Official installation instructions: https://go.dev/doc/install"
    echo ""
    
    local arch
    arch=$(detect_arch)
    local go_tarball="go${GO_DOWNLOAD_VERSION}.linux-${arch}.tar.gz"
    local go_url="https://go.dev/dl/${go_tarball}"
    
    # Create temp directory for Go installation
    GO_TEMP_DIR=$(mktemp -d)
    log_info "Downloading Go ${GO_DOWNLOAD_VERSION} to temporary directory: $GO_TEMP_DIR"
    log_info "Go will be deleted after successful installation"
    
    if ! curl -fsSL "$go_url" -o "${GO_TEMP_DIR}/${go_tarball}"; then
        rm -rf "$GO_TEMP_DIR"
        log_error "Failed to download Go. Please install Go ${GO_MIN_VERSION}+ manually."
        echo ""
        echo "Installation instructions: https://go.dev/doc/install"
        exit 1
    fi
    
    log_info "Extracting Go..."
    tar -C "$GO_TEMP_DIR" -xzf "${GO_TEMP_DIR}/${go_tarball}"
    rm -f "${GO_TEMP_DIR}/${go_tarball}"
    
    # Export PATH to use temp Go
    export PATH="${GO_TEMP_DIR}/go/bin:$PATH"
    
    if ! command -v go &> /dev/null; then
        rm -rf "$GO_TEMP_DIR"
        log_error "Go installation verification failed"
        exit 1
    fi
    
    log_info "Go $(go version) ready for build (temporary)"
    echo ""
}

cleanup_go_temp() {
    if [[ -n "${GO_TEMP_DIR:-}" && -d "$GO_TEMP_DIR" ]]; then
        log_info "Cleaning up temporary Go installation..."
        rm -rf "$GO_TEMP_DIR"
    fi
}

clone_repo() {
    local dest_dir="$1"

    log_info "Downloading tracer source (stable: ${tracer_ref})..."

    local snapshot_url="${REPO_URL}/snapshot/tracer-${tracer_ref}.tar.gz"
    if curl -fsSL "$snapshot_url" 2>/dev/null | tar -xz -C "$dest_dir" --strip-components=1; then
        return 0
    fi

    log_error "Failed to download tracer source"
    return 1
}

install_tracer() {
    local repo_dir="$1"
    local go_was_temp="${2:-false}"
    
    cd "$repo_dir"
    
    # Run configure with all flags
    log_info "Configuring build..."
    if [[ -n "$CONFIGURE_FLAGS" ]]; then
        log_info "Configure flags: $CONFIGURE_FLAGS"
        # shellcheck disable=SC2086
        ./configure $CONFIGURE_FLAGS
    else
        ./configure
    fi
    
    # Build
    log_info "Building..."
    make build
    
    # Install
    log_info "Installing..."
    make install
    
    # Cleanup temp Go on successful installation
    if [[ "$go_was_temp" == "true" ]]; then
        cleanup_go_temp
    fi
}

start_services() {
    if [[ -f /etc/systemd/system/tracer-collector.service ]]; then
        log_info "Starting tracer-collector service..."
        systemctl daemon-reload 2>/dev/null || true
        systemctl enable --now tracer-collector 2>/dev/null || log_warn "Failed to start tracer-collector"
    fi
    
    if [[ "$WITHOUT_PANEL" == "false" ]] && [[ -f /etc/systemd/system/tracer-panel.service ]]; then
        log_info "Starting tracer-panel service..."
        systemctl enable --now tracer-panel 2>/dev/null || log_warn "Failed to start tracer-panel"
        
        # Add self-monitoring
        sleep 2
        tracer-collector add --health "http://localhost:8080/auth/login" tracer-panel.service 2>/dev/null || true
    fi
    
    tracer-collector add tracer-collector.service 2>/dev/null || true
}

main() {
    check_root

    echo ""
    echo "=========================================="
    echo "       Tracer Installation Script        "
    echo "=========================================="
    echo ""

    # Check Go early and show warning upfront if we'll need temporary install
    if ! check_go &>/dev/null; then
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        log_warn "Go is not installed or version is below ${GO_MIN_VERSION}"
        log_warn "This script will temporarily download Go to build tracer."
        log_warn "The self-update feature will NOT work unless Go is permanently installed."
        echo ""
        echo "To enable updates, please install Go:"
        echo "  https://go.dev/doc/install"
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        echo ""
    fi

    local go_was_temp=false
    local GO_TEMP_DIR=""
    
    if ! check_go; then
        install_go_temp
        go_was_temp=true
    fi
    
    local src_dir
    src_dir=$(mktemp -d)
    
    if ! clone_repo "$src_dir"; then
        rm -rf "$src_dir"
        exit 1
    fi
    
    if ! install_tracer "$src_dir" "$go_was_temp"; then
        log_error "Installation failed"
        rm -rf "$src_dir"
        exit 1
    fi
    
    log_info "Cleaning up build directory..."
    rm -rf "$src_dir"
    
    echo ""
    echo "=============================================="
    log_info "Installation complete!"
    echo "=============================================="
    echo ""
    
    if [[ "$WITHOUT_PANEL" == "false" ]]; then
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        log_info "Panel initialization:"
        echo "  Run: tracer-panel -init -username <user> -password <pass>"
        echo "  Or visit http://<host>:8080/setup to create the admin account"
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        echo ""
    fi
    
    if [[ "$go_was_temp" == "true" ]]; then
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        log_warn "IMPORTANT: Go was installed temporarily for this build only."
        log_warn "The self-update feature requires Go to be permanently installed."
        echo ""
        echo "To enable updates, please install Go:"
        echo "  https://go.dev/doc/install"
        echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
        echo ""
    fi
    
    start_services

    echo ""
    echo "Services status:"
    systemctl status tracer-collector --no-pager -l 2>/dev/null | head -5 || \
        echo "  tracer-collector: check 'systemctl status tracer-collector'"
    
    if [[ "$WITHOUT_PANEL" == "false" ]]; then
        systemctl status tracer-panel --no-pager -l 2>/dev/null | head -5 || \
            echo "  tracer-panel: check 'systemctl status tracer-panel'"
    fi
}

main "$@"
