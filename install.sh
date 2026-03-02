#!/bin/bash
#
# Tracer Installation Script
# Usage: curl -fsSL https://raw.githubusercontent.com/ai-shift/tracer/main/install.sh | sudo bash
#
# Options:
#   --collector-only    Install only the collector (no panel)
#   COLLECTOR_ONLY=1    Same as --collector-only (env var)
#
# This script installs:
#   - tracer-collector: runs as root (to access any log files)
#   - tracer-panel: runs as dedicated 'tracer' user (unless --collector-only)
#
set -euo pipefail

# Configuration
REPO="ai-shift/tracer"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/tracer"
DATA_DIR="/var/lib/tracer"
TRACER_USER="tracer"
TRACER_GROUP="tracer"

# Installation mode
COLLECTOR_ONLY="${COLLECTOR_ONLY:-0}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check if running as root
check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "This script must be run as root (use sudo)"
        exit 1
    fi
}

# Detect architecture
detect_arch() {
    local arch
    arch=$(uname -m)
    case $arch in
        x86_64)
            echo "amd64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        *)
            log_error "Unsupported architecture: $arch"
            exit 1
            ;;
    esac
}

# Get latest release version
get_latest_version() {
    local version
    version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
    if [[ -z "$version" ]]; then
        log_error "Failed to fetch latest version"
        exit 1
    fi
    echo "$version"
}

# Download and install binaries
install_binaries() {
    local version=$1
    local arch
    arch=$(detect_arch)
    
    log_info "Downloading tracer ${version} for linux-${arch}..."
    
    local tmp_dir
    tmp_dir=$(mktemp -d)
    trap "rm -rf ${tmp_dir}" EXIT
    
    local download_url="https://github.com/${REPO}/releases/download/${version}/tracer-${version}-linux-${arch}.tar.gz"
    
    if ! curl -fsSL "$download_url" -o "${tmp_dir}/tracer.tar.gz"; then
        log_error "Failed to download from ${download_url}"
        exit 1
    fi
    
    log_info "Extracting binaries..."
    tar -xzf "${tmp_dir}/tracer.tar.gz" -C "${tmp_dir}"
    
    log_info "Installing binaries to ${INSTALL_DIR}..."
    install -m 755 "${tmp_dir}/tracer-collector" "${INSTALL_DIR}/tracer-collector"
    if [[ "$COLLECTOR_ONLY" != "1" ]]; then
        install -m 755 "${tmp_dir}/tracer-panel" "${INSTALL_DIR}/tracer-panel"
    fi
    
    log_info "Binaries installed successfully"
}

# Create tracer user (similar to postgres) - only needed for panel
create_user() {
    if [[ "$COLLECTOR_ONLY" == "1" ]]; then
        return
    fi

    if id "${TRACER_USER}" &>/dev/null; then
        log_info "User '${TRACER_USER}' already exists"
        return
    fi
    
    log_info "Creating system user '${TRACER_USER}'..."
    
    # Create system group
    groupadd --system "${TRACER_GROUP}" 2>/dev/null || true
    
    # Create system user with home directory (like postgres)
    useradd \
        --system \
        --gid "${TRACER_GROUP}" \
        --home-dir "${DATA_DIR}" \
        --shell /usr/sbin/nologin \
        --comment "Tracer Panel Service" \
        "${TRACER_USER}"
    
    log_info "User '${TRACER_USER}' created"
}

# Create directories and set permissions
setup_directories() {
    log_info "Creating directories..."
    
    # Config directory (root-owned, readable by tracer)
    mkdir -p "${CONFIG_DIR}"
    chmod 755 "${CONFIG_DIR}"
    
    # Data directory (owned by tracer user) - only needed for panel
    if [[ "$COLLECTOR_ONLY" != "1" ]]; then
        mkdir -p "${DATA_DIR}"
        chown "${TRACER_USER}:${TRACER_GROUP}" "${DATA_DIR}"
        chmod 750 "${DATA_DIR}"
    fi
    
    log_info "Directories created"
}

# Generate configuration files using CLI tools
generate_configs() {
    log_info "Generating configuration files..."

    # Collector config (only if doesn't exist)
    if [[ ! -f "${CONFIG_DIR}/collector.yaml" ]]; then
        log_info "Creating collector configuration..."
        "${INSTALL_DIR}/tracer-collector" -print-defaults > "${CONFIG_DIR}/collector.yaml"
        chmod 644 "${CONFIG_DIR}/collector.yaml"
        log_info "Collector config created"
    else
        log_warn "Collector config already exists, skipping"
    fi
    
    # Panel config (only if doesn't exist and not collector-only mode)
    if [[ "$COLLECTOR_ONLY" != "1" ]]; then
        if [[ ! -f "${CONFIG_DIR}/panel.yaml" ]]; then
            log_info "Creating panel configuration..."
            "${INSTALL_DIR}/tracer-panel" -print-defaults > "${CONFIG_DIR}/panel.yaml"
            chmod 640 "${CONFIG_DIR}/panel.yaml"
            chown root:${TRACER_GROUP} "${CONFIG_DIR}/panel.yaml"
            log_info "Panel config created"
        else
            log_warn "Panel config already exists, skipping"
        fi
    fi
}

# Initialize panel: create admin user and API key
init_panel() {
    if [[ "$COLLECTOR_ONLY" == "1" ]]; then
        return
    fi
    
    log_info "Initializing panel..."
    
    # Check if panel is already initialized (users exist)
    local has_users
    has_users=$("${INSTALL_DIR}/tracer-panel" -get-api-key 2>/dev/null && echo "yes" || echo "no")
    
    if [[ "$has_users" == "yes" ]]; then
        log_warn "Panel already initialized, skipping initialization"
        return
    fi
    
    # Initialize panel with auto-generated password
    local init_output
    if ! init_output=$("${INSTALL_DIR}/tracer-panel" -init 2>&1); then
        log_error "Failed to initialize panel: $init_output"
        exit 1
    fi
    
    # Parse initialization output
    ADMIN_USERNAME=$(echo "$init_output" | grep '^admin_username=' | cut -d= -f2)
    ADMIN_PASSWORD=$(echo "$init_output" | grep '^admin_password=' | cut -d= -f2)
    API_KEY=$(echo "$init_output" | grep '^collector_api_key=' | cut -d= -f2)
    
    # Update collector config with API key
    if [[ -n "$API_KEY" ]]; then
        log_info "Configuring collector with API key..."
        sed -i "s/api_key:.*/api_key: ${API_KEY}/" "${CONFIG_DIR}/collector.yaml"
    fi
    
    # Store credentials for display
    echo "$ADMIN_USERNAME" > "${DATA_DIR}/.admin_username"
    echo "$ADMIN_PASSWORD" > "${DATA_DIR}/.admin_password"
    chmod 600 "${DATA_DIR}/.admin_username" "${DATA_DIR}/.admin_password"
    chown "${TRACER_USER}:${TRACER_GROUP}" "${DATA_DIR}/.admin_username" "${DATA_DIR}/.admin_password"
    
    log_info "Panel initialized successfully"
}

# Create systemd service files
create_systemd_services() {
    log_info "Creating systemd services..."
    
    # Collector service (runs as root to access all logs)
    if [[ "$COLLECTOR_ONLY" == "1" ]]; then
        # Standalone collector - no dependency on panel
        cat > /etc/systemd/system/tracer-collector.service << EOF
[Unit]
Description=Tracer Log Collector
Documentation=https://github.com/ai-shift/tracer
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/tracer-collector -config ${CONFIG_DIR}/collector.yaml
Restart=always
RestartSec=5

# Security hardening (while maintaining root for log access)
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes

[Install]
WantedBy=multi-user.target
EOF
    else
        # Collector with local panel dependency
        cat > /etc/systemd/system/tracer-collector.service << EOF
[Unit]
Description=Tracer Log Collector
Documentation=https://github.com/ai-shift/tracer
After=network.target tracer-panel.service
Wants=tracer-panel.service

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/tracer-collector -config ${CONFIG_DIR}/collector.yaml
Restart=always
RestartSec=5

# Security hardening (while maintaining root for log access)
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes

[Install]
WantedBy=multi-user.target
EOF

        # Panel service (runs as tracer user)
        cat > /etc/systemd/system/tracer-panel.service << EOF
[Unit]
Description=Tracer Panel
Documentation=https://github.com/ai-shift/tracer
After=network.target

[Service]
Type=simple
User=${TRACER_USER}
Group=${TRACER_GROUP}
ExecStart=${INSTALL_DIR}/tracer-panel -config ${CONFIG_DIR}/panel.yaml
Restart=always
RestartSec=5

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
ReadWritePaths=${DATA_DIR}

[Install]
WantedBy=multi-user.target
EOF
    fi

    systemctl daemon-reload
    log_info "Systemd services created"
}

# Print post-installation instructions
print_instructions() {
    echo ""
    echo "=============================================="
    echo -e "${GREEN}Tracer installed successfully!${NC}"
    echo "=============================================="
    echo ""
    
    if [[ "$COLLECTOR_ONLY" == "1" ]]; then
        echo "Mode: Collector only (no panel)"
        echo ""
        echo "Configuration file:"
        echo "  - Collector: ${CONFIG_DIR}/collector.yaml"
        echo ""
        echo "Next steps:"
        echo ""
        echo "1. Configure the collector with your panel endpoint and API key:"
        echo "   sudo nano ${CONFIG_DIR}/collector.yaml"
        echo ""
        echo "   Set transport.panel_host and transport.api_key, e.g.:"
        echo "   panel_host: panel.example.com:8080"
        echo "   api_key: YOUR_KEY"
        echo ""
        echo "2. Add your applications to monitor (see examples in config)"
        echo ""
        echo "3. Start the collector:"
        echo "   sudo systemctl enable --now tracer-collector"
        echo ""
        echo "Useful commands:"
        echo "   sudo systemctl status tracer-collector"
        echo "   sudo journalctl -u tracer-collector -f"
    else
        echo "Mode: Full installation (panel + collector)"
        echo ""
        echo "Configuration files:"
        echo "  - Collector: ${CONFIG_DIR}/collector.yaml"
        echo "  - Panel:     ${CONFIG_DIR}/panel.yaml"
        echo ""
        echo "Data directory: ${DATA_DIR}"
        echo ""
        
        # Read credentials if they exist
        if [[ -f "${DATA_DIR}/.admin_username" ]]; then
            ADMIN_USERNAME=$(cat "${DATA_DIR}/.admin_username")
            ADMIN_PASSWORD=$(cat "${DATA_DIR}/.admin_password")
            echo "Admin credentials (auto-generated):"
            echo "  Username: ${ADMIN_USERNAME}"
            echo "  Password: ${ADMIN_PASSWORD}"
            echo ""
            # Clean up credential files after display
            rm -f "${DATA_DIR}/.admin_username" "${DATA_DIR}/.admin_password"
        fi
        
        echo "Next steps:"
        echo ""
        echo "1. Configure the panel host (required):"
        echo "   sudo nano ${CONFIG_DIR}/panel.yaml"
        echo ""
        echo "   Set the host URL for your panel, e.g.:"
        echo "   host: https://tracer.example.com"
        echo ""
        echo "2. Configure the collector to watch your log files:"
        echo "   sudo nano ${CONFIG_DIR}/collector.yaml"
        echo ""
        echo "3. Start the services:"
        echo "   sudo systemctl enable --now tracer-panel"
        echo "   sudo systemctl enable --now tracer-collector"
        echo ""
        echo "4. Access the panel at:"
        echo "   http://localhost:8080"
        echo ""
        echo "Useful commands:"
        echo "   sudo systemctl status tracer-panel"
        echo "   sudo systemctl status tracer-collector"
        echo "   sudo journalctl -u tracer-panel -f"
        echo "   sudo journalctl -u tracer-collector -f"
    fi
    echo ""
}

# Parse command line arguments
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --collector-only)
                COLLECTOR_ONLY="1"
                shift
                ;;
            --help|-h)
                echo "Tracer Installation Script"
                echo ""
                echo "Usage: install.sh [OPTIONS]"
                echo ""
                echo "Options:"
                echo "  --collector-only    Install only the collector (for remote servers)"
                echo "  --help, -h          Show this help message"
                echo ""
                echo "Environment variables:"
                echo "  COLLECTOR_ONLY=1    Same as --collector-only"
                echo "  TRACER_VERSION      Install specific version (e.g. v1.0.0)"
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                echo "Use --help for usage information"
                exit 1
                ;;
        esac
    done
}

# Main installation flow
main() {
    parse_args "$@"

    echo ""
    echo "=========================================="
    echo "       Tracer Installation Script        "
    echo "=========================================="
    echo ""
    
    if [[ "$COLLECTOR_ONLY" == "1" ]]; then
        log_info "Installing collector only (no panel)"
    fi
    
    check_root
    
    local version
    if [[ -n "${TRACER_VERSION:-}" ]]; then
        version="${TRACER_VERSION}"
        log_info "Using specified version: ${version}"
    else
        version=$(get_latest_version)
        log_info "Latest version: ${version}"
    fi
    
    install_binaries "$version"
    create_user
    setup_directories
    generate_configs
    init_panel
    create_systemd_services
    print_instructions
}

main "$@"
