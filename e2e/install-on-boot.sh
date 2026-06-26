#!/usr/bin/env bash
set -euo pipefail

# E2E bootstrap script - installs tracer and sets up demo services.
# In release mode, downloads pre-built binaries via the install script.
# In build mode, uses binaries copied during image build.

export TRACER_PANEL_HOST=http://localhost:8080

if [ "${E2E_INSTALL_MODE:-build}" = "release" ]; then
    echo "[e2e] Installing tracer from release..."
    export TRACER_ADMIN_USERNAME=demo
    export TRACER_ADMIN_PASSWORD=demodemo123
    curl -fsSL https://git.naqerl.com/naqerl/tracer/raw/branch/main/install.sh | bash
else
    echo "[e2e] Installing from pre-built binaries..."
    tracer-collector install
    tracer-panel install
fi

tracer-collector set name demo

# Add no-auth and readonly flags for demo iframes
mkdir -p /etc/systemd/system/tracer-panel.service.d
cat > /etc/systemd/system/tracer-panel.service.d/demo-flags.conf <<'EOF'
[Service]
ExecStart=
ExecStart=/usr/local/bin/tracer-panel -config /etc/tracer/panel.yaml -no-auth -readonly
EOF
systemctl daemon-reload

systemctl start demo-api demo-worker demo-cron
systemctl start tracer-collector tracer-panel

sleep 3

if [ "${E2E_INSTALL_MODE:-build}" = "release" ]; then
    # In release mode, install script already ran -init and set the api key.
    # Re-init if needed (will exit 2 = already initialized, which is fine).
    tracer-panel -init -username demo -password demodemo123 2>/dev/null || true
else
    api_key=$(tracer-panel -init -username demo -password demodemo123 2>/dev/null | grep '^collector_api_key=' | cut -d= -f2) || true
    if [ -n "$api_key" ]; then
        tracer-collector set api-key "$api_key"
    fi
fi

tracer-collector add demo-api.service || true
tracer-collector add demo-worker.service || true
tracer-collector add demo-cron.service || true
tracer-collector add -health "http://localhost:8080/auth/login" tracer-panel.service || true
tracer-collector add tracer-collector.service || true

systemctl restart tracer-collector
