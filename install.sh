#!/bin/bash
#
# Tracer Installation Script
# Usage: curl -fsSL https://raw.githubusercontent.com/ai-shift/tracer/main/install.sh | sudo bash
#
# This script installs:
#   - tracer-collector: runs as root (to access any log files)
#   - tracer-panel: runs as dedicated 'tracer' user
#
set -euo pipefail

# Configuration
REPO="ai-shift/tracer"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/tracer"
DATA_DIR="/var/lib/tracer"
TRACER_USER="tracer"
TRACER_GROUP="tracer"

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
    install -m 755 "${tmp_dir}/tracer-panel" "${INSTALL_DIR}/tracer-panel"
    
    log_info "Binaries installed successfully"
}

# Create tracer user (similar to postgres)
create_user() {
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
    
    # Data directory (owned by tracer user)
    mkdir -p "${DATA_DIR}"
    chown "${TRACER_USER}:${TRACER_GROUP}" "${DATA_DIR}"
    chmod 750 "${DATA_DIR}"
    
    log_info "Directories created"
}

# Create example configuration files
create_configs() {
    # Collector config (only if doesn't exist)
    if [[ ! -f "${CONFIG_DIR}/collector.toml" ]]; then
        log_info "Creating collector configuration..."
        cat > "${CONFIG_DIR}/collector.toml" << 'EOF'
# Tracer Collector Configuration
# Documentation: https://github.com/ai-shift/tracer

panel_endpoint = "ws://localhost:8080/api/ws/ingest"

# Each app entry creates a separate application in the panel
# The 'name' field is the app identifier that will appear in the dashboard

# Example: JSON application logs
# [[apps]]
# name = "my-app"
# path = "/var/log/my-app/app.log"
# format = "json"

# Example: systemd service via journalctl
# [[apps]]
# name = "my-service"
# format = "journalctl"
# unit = "my-service"

# Example: Nginx access logs
# [[apps]]
# name = "nginx"
# path = "/var/log/nginx/access.log"
# format = "regex"
# pattern = '^(?P<ip>\S+) \S+ \S+ \[(?P<timestamp>[^\]]+)\] "(?P<method>\S+) (?P<path>\S+) \S+" (?P<status>\d+) (?P<bytes>\d+) "(?P<referer>[^"]*)" "(?P<user_agent>[^"]*)"'
# timestamp_field = "timestamp"
# level_field = "status"
# message_field = "path"
EOF
        chmod 644 "${CONFIG_DIR}/collector.toml"
    else
        log_warn "Collector config already exists, skipping"
    fi
    
    # Panel config (only if doesn't exist)
    if [[ ! -f "${CONFIG_DIR}/panel.toml" ]]; then
        log_info "Creating panel configuration..."
        cat > "${CONFIG_DIR}/panel.toml" << EOF
# Tracer Panel Configuration
# Documentation: https://github.com/ai-shift/tracer

port = "8080"
data_dir = "${DATA_DIR}"

# Percentage of non-error logs to persist (0.0 - 1.0)
# Errors are always persisted at 100%
sample_rate = 0.1

# Days to keep logs before automatic cleanup
retention_days = 90

# Telegram alerts (optional)
[telegram]
token = ""
chat_id = 0
EOF
        chmod 640 "${CONFIG_DIR}/panel.toml"
        chown root:${TRACER_GROUP} "${CONFIG_DIR}/panel.toml"
    else
        log_warn "Panel config already exists, skipping"
    fi
}

# Create systemd service files
create_systemd_services() {
    log_info "Creating systemd services..."
    
    # Collector service (runs as root to access all logs)
    cat > /etc/systemd/system/tracer-collector.service << EOF
[Unit]
Description=Tracer Log Collector
Documentation=https://github.com/ai-shift/tracer
After=network.target tracer-panel.service
Wants=tracer-panel.service

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/tracer-collector -config ${CONFIG_DIR}/collector.toml
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
ExecStart=${INSTALL_DIR}/tracer-panel -config ${CONFIG_DIR}/panel.toml
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
    echo "Configuration files:"
    echo "  - Collector: ${CONFIG_DIR}/collector.toml"
    echo "  - Panel:     ${CONFIG_DIR}/panel.toml"
    echo ""
    echo "Data directory: ${DATA_DIR}"
    echo ""
    echo "Next steps:"
    echo ""
    echo "1. Configure the collector to watch your log files:"
    echo "   sudo nano ${CONFIG_DIR}/collector.toml"
    echo ""
    echo "2. (Optional) Configure Telegram alerts in panel config:"
    echo "   sudo nano ${CONFIG_DIR}/panel.toml"
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
    echo ""
}

# Main installation flow
main() {
    echo ""
    echo "=========================================="
    echo "       Tracer Installation Script        "
    echo "=========================================="
    echo ""
    
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
    create_configs
    create_systemd_services
    print_instructions
}

main "$@"
