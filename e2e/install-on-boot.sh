#!/usr/bin/env bash
set -euo pipefail

# E2E bootstrap script - runs self-install for pre-built binaries.
# Binaries are built with correct ldflags during container image build.

export TRACER_PANEL_HOST=http://localhost:8080

tracer-collector install
tracer-collector set name demo
tracer-panel install

# Move panel to port 9090 so Caddy can serve on 8080
sed -i 's/^port: .*/port: "9090"/' /etc/tracer/panel.yaml

# Add no-auth and readonly flags for demo iframes
mkdir -p /etc/systemd/system/tracer-panel.service.d
cat > /etc/systemd/system/tracer-panel.service.d/demo-flags.conf <<'EOF'
[Service]
ExecStart=
ExecStart=/usr/local/bin/tracer-panel -config /etc/tracer/panel.yaml -no-auth -readonly
EOF
systemctl daemon-reload

systemctl start caddy
systemctl start demo-api demo-worker demo-cron
systemctl start tracer-collector tracer-panel

sleep 3

api_key=$(tracer-panel -init -username demo -password demodemo123 2>/dev/null | grep '^collector_api_key=' | cut -d= -f2) || true
if [ -n "$api_key" ]; then
    tracer-collector set api-key "$api_key"
fi

tracer-collector add demo-api.service || true
tracer-collector add demo-worker.service || true
tracer-collector add demo-cron.service || true
tracer-collector add -health "http://localhost:8080/auth/login" tracer-panel.service || true
tracer-collector add tracer-collector.service || true

systemctl restart tracer-collector
