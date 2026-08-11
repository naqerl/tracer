#!/usr/bin/env bash
set -euo pipefail

# E2E bootstrap script - installs tracer and sets up demo services.
# In release mode, downloads pre-built binaries via the install script.
# In build mode, uses binaries copied during image build.

export TRACER_PANEL_HOST=http://localhost:8080

if [ "${E2E_INSTALL_MODE:-build}" = "release" ]; then
    echo "[e2e] Installing tracer from release..."
    export TRACER_ADMIN_USERNAME=demo
    export TRACER_ADMIN_PASSWORD=demo1234
    curl -fsSL https://git.naqerl.com/naqerl/tracer/raw/branch/main/install.sh | bash
else
    echo "[e2e] Installing from pre-built binaries..."
    tracer-collector install
    tracer-panel install
fi

tracer-collector set name demo

# Add no-auth and optional readonly flags for demo iframes. The panel stays
# read-only by default; set E2E_PANEL_READONLY=0 (via `make -C e2e up`) to
# allow state-changing requests such as notifier management.
mkdir -p /etc/systemd/system/tracer-panel.service.d
panel_flags="-no-auth"
if [ "${E2E_PANEL_READONLY:-1}" != "0" ]; then
    panel_flags="$panel_flags -readonly"
fi
cat > /etc/systemd/system/tracer-panel.service.d/demo-flags.conf <<EOF
[Service]
ExecStart=
ExecStart=/usr/local/bin/tracer-panel -config /etc/tracer/panel.yaml $panel_flags
EOF
systemctl daemon-reload

# Initialize the panel database (admin user + collector API key) BEFORE the
# panel starts. A panel that serves with a half-initialized database rejects
# every collector WebSocket handshake until it is restarted; the install
# script orders it this way too.
if [ "${E2E_INSTALL_MODE:-build}" = "release" ]; then
    # In release mode, install script already ran -init and set the api key.
    # Re-init if needed (will exit 2 = already initialized, which is fine).
    tracer-panel -init -username demo -password demo1234 2>/dev/null || true
else
    api_key=$(tracer-panel -init -username demo -password demo1234 2>/dev/null | grep '^collector_api_key=' | cut -d= -f2) || true
    if [ -n "$api_key" ]; then
        tracer-collector set api-key "$api_key"
    fi
fi

systemctl start demo-api demo-worker demo-cron
systemctl start tracer-collector
# Restart (not just start) so the -no-auth drop-in above is applied even if
# the panel unit was already pulled up at boot; otherwise the panel runs in
# auth mode and rejects the collector's WebSocket handshake.
systemctl restart tracer-panel

tracer-collector add demo-api.service
tracer-collector add demo-worker.service
tracer-collector add demo-cron.service
tracer-collector add -health "${TRACER_PANEL_HOST}/auth/login" tracer-panel.service
tracer-collector add tracer-collector.service

systemctl restart tracer-collector
