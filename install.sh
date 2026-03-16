#!/bin/bash
#
# Tracer Installation Script
# Usage: curl -fsSL https://raw.githubusercontent.com/ai-shift/tracer/main/install.sh | sudo bash
#
# Options:
#   --collector-only    Install only the collector (no panel)
#   COLLECTOR_ONLY=1    Same as --collector-only (env var)
#   TRACER_TARBALL      Install from local release tarball instead of GitHub
#   TRACER_BIN_DIR      Install from local directory containing tracer-collector/tracer-panel
#
# This script installs:
#   - tracer-collector: runs as dedicated 'tracer-collector' user
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
COLLECTOR_USER="tracer-collector"
COLLECTOR_GROUP="tracer-collector"
ADMIN_USERNAME=""
ADMIN_PASSWORD=""
API_KEY=""
TRACER_PANEL_HOST="${TRACER_PANEL_HOST:-}"
TRACER_PANEL_PORT="${TRACER_PANEL_PORT:-}"
TRACER_AUTOSTART="${TRACER_AUTOSTART:-1}"
TRACER_SELF_MONITOR="${TRACER_SELF_MONITOR:-1}"
TRACER_ADMIN_USERNAME="${TRACER_ADMIN_USERNAME:-}"
TRACER_ADMIN_PASSWORD="${TRACER_ADMIN_PASSWORD:-}"

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

can_prompt() {
    [[ -r /dev/tty && -w /dev/tty ]]
}

prompt_required_value() {
    local var_name=$1
    local prompt_label=$2
    local secret=${3:-0}
    local value=""

    if ! can_prompt; then
        log_error "${var_name} is required in non-interactive mode"
        exit 1
    fi

    while [[ -z "${value}" ]]; do
        if [[ "${secret}" == "1" ]]; then
            read -r -s -p "${prompt_label}: " value < /dev/tty
            printf '\n' > /dev/tty
        else
            read -r -p "${prompt_label}: " value < /dev/tty
        fi

        if [[ -z "${value}" ]]; then
            printf '%b\n' "${YELLOW}[WARN]${NC} ${var_name} cannot be empty" > /dev/tty
        fi
    done

    printf '%s' "${value}"
}

require_cmd() {
    local cmd=$1
    if ! command -v "$cmd" >/dev/null 2>&1; then
        log_error "Required command not found: $cmd"
        exit 1
    fi
}

# Check if running as root
check_root() {
    if [[ $EUID -ne 0 ]]; then
        log_error "This script must be run as root (use sudo)"
        exit 1
    fi
}

check_dependencies() {
    require_cmd curl
    require_cmd tar
    require_cmd install
    require_cmd runuser
    require_cmd systemctl
}

resolve_install_inputs() {
    if [[ "$COLLECTOR_ONLY" == "1" ]]; then
        return
    fi

    if [[ -z "${TRACER_PANEL_HOST}" ]]; then
        TRACER_PANEL_HOST=$(prompt_required_value "TRACER_PANEL_HOST" "Panel host URL")
    fi

    if [[ -z "${TRACER_ADMIN_USERNAME}" ]]; then
        TRACER_ADMIN_USERNAME=$(prompt_required_value "TRACER_ADMIN_USERNAME" "Admin username")
    fi

    if [[ -z "${TRACER_ADMIN_PASSWORD}" ]]; then
        TRACER_ADMIN_PASSWORD=$(prompt_required_value "TRACER_ADMIN_PASSWORD" "Admin password" 1)
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
    local tmp_dir
    tmp_dir=$(mktemp -d)
    trap "rm -rf ${tmp_dir}" EXIT
    local source_dir="${tmp_dir}"

    if [[ -n "${TRACER_BIN_DIR:-}" ]]; then
        log_info "Installing binaries from local directory ${TRACER_BIN_DIR}..."
        source_dir="${TRACER_BIN_DIR}"
    elif [[ -n "${TRACER_TARBALL:-}" ]]; then
        log_info "Installing binaries from local tarball ${TRACER_TARBALL}..."
        tar -xzf "${TRACER_TARBALL}" -C "${tmp_dir}"
    else
        local arch
        arch=$(detect_arch)
        log_info "Downloading tracer ${version} for linux-${arch}..."

        local download_url="https://github.com/${REPO}/releases/download/${version}/tracer-${version}-linux-${arch}.tar.gz"
        if ! curl -fsSL "$download_url" -o "${tmp_dir}/tracer.tar.gz"; then
            log_error "Failed to download from ${download_url}"
            exit 1
        fi

        log_info "Extracting binaries..."
        tar -xzf "${tmp_dir}/tracer.tar.gz" -C "${tmp_dir}"
    fi

    if [[ ! -x "${source_dir}/tracer-collector" ]]; then
        log_error "tracer-collector not found or not executable in ${source_dir}"
        exit 1
    fi
    if [[ "$COLLECTOR_ONLY" != "1" && ! -x "${source_dir}/tracer-panel" ]]; then
        log_error "tracer-panel not found or not executable in ${source_dir}"
        exit 1
    fi

    log_info "Installing binaries to ${INSTALL_DIR}..."
    install -m 755 "${source_dir}/tracer-collector" "${INSTALL_DIR}/tracer-collector"
    if [[ "$COLLECTOR_ONLY" != "1" ]]; then
        install -m 755 "${source_dir}/tracer-panel" "${INSTALL_DIR}/tracer-panel"
    fi

    log_info "Binaries installed successfully"
}

ensure_group() {
    local group=$1
    if getent group "$group" >/dev/null 2>&1; then
        return
    fi
    groupadd --system "$group"
}

ensure_user() {
    local user=$1
    local group=$2
    local home_dir=$3
    local comment=$4

    ensure_group "$group"

    if id "$user" >/dev/null 2>&1; then
        return
    fi

    useradd \
        --system \
        --gid "$group" \
        --home-dir "$home_dir" \
        --shell /usr/sbin/nologin \
        --comment "$comment" \
        "$user"
}

create_users() {
    log_info "Ensuring service users exist..."

    ensure_user "${COLLECTOR_USER}" "${COLLECTOR_GROUP}" "/nonexistent" "Tracer Collector Service"

    if [[ "$COLLECTOR_ONLY" != "1" ]]; then
        ensure_user "${TRACER_USER}" "${TRACER_GROUP}" "${DATA_DIR}" "Tracer Panel Service"
    fi

    log_info "Service users ready"
}

# Create directories and set permissions
setup_directories() {
    log_info "Creating directories..."

    install -d -m 0755 "${CONFIG_DIR}"

    if [[ "$COLLECTOR_ONLY" != "1" ]]; then
        install -d -o "${TRACER_USER}" -g "${TRACER_GROUP}" -m 0750 "${DATA_DIR}"
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
        chown root:"${COLLECTOR_GROUP}" "${CONFIG_DIR}/collector.yaml"
        chmod 640 "${CONFIG_DIR}/collector.yaml"
        log_info "Collector config created"
    else
        log_warn "Collector config already exists, skipping"
    fi
    chown root:"${COLLECTOR_GROUP}" "${CONFIG_DIR}/collector.yaml"
    chmod 640 "${CONFIG_DIR}/collector.yaml"

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
        chown root:"${TRACER_GROUP}" "${CONFIG_DIR}/panel.yaml"
        chmod 640 "${CONFIG_DIR}/panel.yaml"
    fi
}

configure_panel() {
    if [[ "$COLLECTOR_ONLY" == "1" ]]; then
        return
    fi

    if [[ -n "${TRACER_PANEL_PORT}" ]]; then
        if [[ ! "${TRACER_PANEL_PORT}" =~ ^[0-9]+$ ]]; then
            log_error "TRACER_PANEL_PORT must be a numeric port"
            exit 1
        fi
        log_info "Setting panel port to ${TRACER_PANEL_PORT}..."
        sed -i "s|^port:.*|port: ${TRACER_PANEL_PORT}|" "${CONFIG_DIR}/panel.yaml"
    fi

    if [[ -n "${TRACER_PANEL_HOST}" ]]; then
        log_info "Setting panel host to ${TRACER_PANEL_HOST}..."
        sed -i "s|^host:.*|host: ${TRACER_PANEL_HOST}|" "${CONFIG_DIR}/panel.yaml"
    fi
}

panel_bind_port() {
    if [[ "$COLLECTOR_ONLY" == "1" ]]; then
        return
    fi

    if [[ -n "${TRACER_PANEL_PORT}" ]]; then
        printf '%s\n' "${TRACER_PANEL_PORT}"
        return
    fi

    awk -F': *' '/^port:/ {gsub(/"/, "", $2); print $2; exit}' "${CONFIG_DIR}/panel.yaml"
}

# Initialize panel: create admin user and API key
init_panel() {
    if [[ "$COLLECTOR_ONLY" == "1" ]]; then
        return
    fi

    log_info "Initializing panel..."

    # Initialize panel with required credentials.
    local init_output
    local init_args=("-init")
    init_args+=("-username" "${TRACER_ADMIN_USERNAME}")
    init_args+=("-password" "${TRACER_ADMIN_PASSWORD}")

    if ! init_output=$(runuser -u "${TRACER_USER}" -- "${INSTALL_DIR}/tracer-panel" "${init_args[@]}" 2>&1); then
        if grep -Fq "panel already initialized" <<<"$init_output"; then
            log_warn "Panel already initialized, skipping initialization"
            return
        fi
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

    log_info "Panel initialized successfully"
}

# Build supplementary groups for collector service.
collector_supplementary_groups() {
    local groups=()

    if getent group systemd-journal >/dev/null 2>&1; then
        groups+=("systemd-journal")
    fi
    if getent group adm >/dev/null 2>&1; then
        groups+=("adm")
    fi

    if [[ ${#groups[@]} -gt 0 ]]; then
        printf '%s\n' "SupplementaryGroups=${groups[*]}"
    fi
}

# Create systemd service files
create_systemd_services() {
    log_info "Creating systemd services..."

    local collector_deps=""
    if [[ "$COLLECTOR_ONLY" != "1" ]]; then
        collector_deps=$(cat << EOF
After=network.target tracer-panel.service
Wants=tracer-panel.service
EOF
)
    else
        collector_deps="After=network.target"
    fi

    local collector_groups
    collector_groups=$(collector_supplementary_groups)

    cat > /etc/systemd/system/tracer-collector.service << EOF
[Unit]
Description=Tracer Log Collector
Documentation=https://github.com/ai-shift/tracer
${collector_deps}

[Service]
Type=simple
User=${COLLECTOR_USER}
Group=${COLLECTOR_GROUP}
${collector_groups}
ExecStart=${INSTALL_DIR}/tracer-collector -config ${CONFIG_DIR}/collector.yaml
Restart=always
RestartSec=5

# Config and filesystem setup
ConfigurationDirectory=tracer
ConfigurationDirectoryMode=0755

# Security hardening
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

    if [[ "$COLLECTOR_ONLY" != "1" ]]; then
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

# Config and data setup
ConfigurationDirectory=tracer
ConfigurationDirectoryMode=0755
StateDirectory=tracer
StateDirectoryMode=0750

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes

[Install]
WantedBy=multi-user.target
EOF
    fi

    systemctl daemon-reload
    log_info "Systemd services created"
}

wait_for_unit_active() {
    local unit=$1
    local attempts=${2:-20}
    local delay=${3:-1}
    local i

    for ((i=0; i<attempts; i++)); do
        if systemctl is-active --quiet "$unit"; then
            return 0
        fi
        sleep "$delay"
    done

    return 1
}

configure_self_monitoring() {
    local changed=0
    local panel_port=""
    local panel_health_url=""

    if [[ "$TRACER_SELF_MONITOR" != "1" ]]; then
        return
    fi

    if ! systemctl is-active --quiet tracer-collector.service; then
        log_warn "Skipping self-monitoring bootstrap because tracer-collector is not active"
        return
    fi

    if [[ "$COLLECTOR_ONLY" != "1" ]]; then
        panel_port=$(panel_bind_port)
        if [[ -n "${panel_port}" ]]; then
            panel_health_url="http://127.0.0.1:${panel_port}/auth/login"
        fi
        if grep -Fq "unit: tracer-panel.service" "${CONFIG_DIR}/collector.yaml"; then
            log_info "Self-monitoring for tracer-panel.service already configured"
        else
            log_info "Adding self-monitoring for tracer-panel.service..."
            if [[ -n "${panel_health_url}" ]]; then
                "${INSTALL_DIR}/tracer-collector" -config "${CONFIG_DIR}/collector.yaml" add --override --ignore-format-change --health "${panel_health_url}" tracer-panel.service
            else
                "${INSTALL_DIR}/tracer-collector" -config "${CONFIG_DIR}/collector.yaml" add --override --ignore-format-change tracer-panel.service
            fi
            changed=1
        fi
    fi

    if grep -Fq "unit: tracer-collector.service" "${CONFIG_DIR}/collector.yaml"; then
        log_info "Self-monitoring for tracer-collector.service already configured"
    else
        log_info "Adding self-monitoring for tracer-collector.service..."
        "${INSTALL_DIR}/tracer-collector" -config "${CONFIG_DIR}/collector.yaml" add --override --ignore-format-change tracer-collector.service
        changed=1
    fi

    if [[ "$changed" == "1" ]]; then
        chown root:"${COLLECTOR_GROUP}" "${CONFIG_DIR}/collector.yaml"
        chmod 640 "${CONFIG_DIR}/collector.yaml"
        log_info "Restarting tracer-collector to apply self-monitoring changes..."
        systemctl restart tracer-collector.service
    fi
}

start_services() {
    if [[ "$TRACER_AUTOSTART" != "1" ]]; then
        return
    fi

    log_info "Enabling and starting services..."

    if [[ "$COLLECTOR_ONLY" != "1" ]]; then
        if ! grep -qE '^host:[[:space:]]*[^[:space:]]+' "${CONFIG_DIR}/panel.yaml"; then
            log_warn "Skipping service startup because panel host is not configured"
            return
        fi
        systemctl enable --now tracer-panel.service
        if ! wait_for_unit_active tracer-panel.service; then
            log_warn "tracer-panel did not become active in time"
        fi
    fi

    systemctl enable --now tracer-collector.service
    if ! wait_for_unit_active tracer-collector.service; then
        log_warn "tracer-collector did not become active in time"
        return
    fi

    sleep 2
    configure_self_monitoring
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
        echo "Service user: ${COLLECTOR_USER}"
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
        echo "Permissions:"
        echo "   - Journal access is granted through systemd supplementary groups when available"
        echo "   - For custom log paths, grant ${COLLECTOR_USER} read access via group membership or ACLs"
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

        if [[ -n "${ADMIN_USERNAME}" && -n "${ADMIN_PASSWORD}" ]]; then
            echo "Admin credentials:"
            echo "  Username: ${ADMIN_USERNAME}"
            echo "  Password: ${ADMIN_PASSWORD}"
            echo ""
        fi

        echo "Next steps:"
        echo ""
        echo "1. Configure the collector to watch your log files:"
        echo "   sudo nano ${CONFIG_DIR}/collector.yaml"
        echo ""
        echo "2. Start the services:"
        echo "   sudo systemctl enable --now tracer-panel"
        echo "   sudo systemctl enable --now tracer-collector"
        echo ""
        echo "3. Access the panel at:"
        echo "   http://localhost:8080"
        echo ""
        echo "Permissions:"
        echo "   - Collector runs as ${COLLECTOR_USER}"
        echo "   - Journal access is granted through systemd supplementary groups when available"
        echo "   - For custom log paths, grant ${COLLECTOR_USER} read access via group membership or ACLs"
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
                echo "  TRACER_TARBALL      Use local release tarball instead of downloading"
                echo "  TRACER_BIN_DIR      Use local directory with built binaries"
                echo "  TRACER_PANEL_HOST   Set panel host before first start"
                echo "  TRACER_PANEL_PORT   Set panel listen port before first start"
                echo "  TRACER_AUTOSTART=1  Enable and start services during install"
                echo "  TRACER_SELF_MONITOR=1  Add tracer-panel/tracer-collector journal sources"
                echo "  TRACER_ADMIN_USERNAME  Admin username to use during panel initialization"
                echo "  TRACER_ADMIN_PASSWORD  Admin password to use during panel initialization"
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
    check_dependencies
    resolve_install_inputs

    local version
    if [[ -n "${TRACER_VERSION:-}" ]]; then
        version="${TRACER_VERSION}"
        log_info "Using specified version: ${version}"
    else
        version=$(get_latest_version)
        log_info "Latest version: ${version}"
    fi

    install_binaries "$version"
    create_users
    setup_directories
    generate_configs
    configure_panel
    init_panel
    create_systemd_services
    start_services
    print_instructions
}

main "$@"
