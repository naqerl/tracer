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
tracer-collector set panel-host http://localhost:8080

# The panel itself is always writable. Read-only enforcement lives in nginx,
# which reverse-proxies the public demo panel on port 8081 to the panel on
# 8080. The collector pushes config and data directly to the panel on 8080, so
# adding apps keeps working regardless of the nginx read-only gate.
#
# The panel authenticates with the demo user created during -init below.
if [ "${E2E_PANEL_READONLY:-1}" != "0" ]; then
    install -m 0644 /opt/tracer/e2e/nginx/readonly.conf /etc/nginx/conf.d/tracer.conf
    echo "[e2e] nginx read-only gate enabled"
else
    install -m 0644 /opt/tracer/e2e/nginx/writable.conf /etc/nginx/conf.d/tracer.conf
    echo "[e2e] nginx read-only gate disabled"
fi

# Initialize the panel database (admin user + collector API key) BEFORE the
# panel starts. A panel that serves with a half-initialized database rejects
# every collector WebSocket handshake until it is restarted; the install
# script orders it this way too.
api_key=$(tracer-panel -init -username demo -password demo1234 2>/dev/null | grep '^collector_api_key=' | cut -d= -f2) || true
# When the panel was already initialized (e.g. release install), -init prints
# nothing, so fall back to the installed collector config.
if [ -z "${api_key:-}" ]; then
    api_key=$(sed -n 's/.*api_key: *\([^ ]*\).*/\1/p' /etc/tracer/collector.yaml | head -n1) || true
fi

systemctl start demo-api demo-worker demo-cron demo-conflict
systemctl start tracer-collector
systemctl restart tracer-panel
# Reload nginx so the read-only gate config above takes effect.
systemctl restart nginx

# Wait for the panel to be ready before pushing config.
for i in $(seq 1 30); do
    if curl -fsS http://localhost:8080/healthz >/dev/null 2>&1; then
        break
    fi
    sleep 1
done

# Configure collector with API key (needed to authenticate the config push).
if [ -n "${api_key:-}" ]; then
    tracer-collector set api-key "$api_key"
fi
systemctl restart tracer-collector

# Push the app configuration to the panel over HTTP using the collector API
# key. The panel stores it and pushes it back to the connected collector over
# WebSocket; the collector hot-reloads and starts ingesting the demo apps.
cat > /tmp/demo-config.json <<'CONFIG'
{
  "parsers": {
    "json": {"type": "json", "params": {"level_field": "level", "message_field": "msg", "timestamp_field": "time", "timestamp_format": "2006-01-02T15:04:05.999999999Z07:00"}},
    "text": {"type": "slogtext", "params": {"level_field": "level", "message_field": "msg", "timestamp_field": "time", "timestamp_format": "2006-01-02T15:04:05.999999999Z07:00"}}
  },
  "apps": [
    {
      "name": "demo-api",
      "parser_id": "json",
      "source": {"type": "journalctl", "params": {"unit": "demo-api.service"}},
      "lifecycle": {"type": "systemd"},
      "health": {"type": "http", "params": {"url": "http://localhost:8080/healthz", "interval": "5s"}}
    },
    {
      "name": "demo-worker",
      "parser_id": "text",
      "source": {"type": "journalctl", "params": {"unit": "demo-worker.service"}},
      "lifecycle": {"type": "systemd"}
    },
    {
      "name": "demo-cron",
      "parser_id": "json",
      "source": {"type": "journalctl", "params": {"unit": "demo-cron.service"}},
      "lifecycle": {"type": "systemd"}
    },
    {
      "name": "naqerl.com",
      "health": {"type": "http", "params": {"url": "https://naqerl.com", "interval": "30s", "expected_status": "200"}}
    }
  ]
}
CONFIG

if [ -n "${api_key:-}" ]; then
    curl -fsS -X PUT \
        -H "X-API-Key: ${api_key}" \
        -H "Content-Type: application/json" \
        --data-binary @/tmp/demo-config.json \
        http://localhost:8080/ingest/config/demo
else
    echo "[e2e] WARNING: no panel API key available, skipping app config push"
fi

# Seed a "registered yet disabled" collector (demo-idle): it has its own
# systemd service, connects to the panel and reports resource stats on a 10s
# cadence for the first minute of the demo, then the service stops. Pushing a
# stored config creates its collector database so the panel keeps listing it
# as registered after it goes offline; its "last update X ago" indicator then
# ages to yellow/red.
if [ -n "${api_key:-}" ]; then
    cat > /etc/tracer/collector-idle.yaml <<CONFIG
sender:
  name: demo-idle
  panel_host: http://localhost:8080
  api_key: ${api_key}
CONFIG
    chown tracer-collector:tracer /etc/tracer/collector-idle.yaml
    chmod 0640 /etc/tracer/collector-idle.yaml
    curl -fsS -X PUT \
        -H "X-API-Key: ${api_key}" \
        -H "Content-Type: application/json" \
        --data-binary '{"parsers": {}, "stats": {"interval": "10s", "cpu": true, "mem": true, "disk": true}, "apps": []}' \
        http://localhost:8080/ingest/config/demo-idle
    systemctl start tracer-collector-idle
else
    echo "[e2e] WARNING: no panel API key available, skipping demo-idle collector seeding"
fi
