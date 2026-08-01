#!/bin/sh
set -e

# Idempotent: creates /etc/tracer/panel.yaml, dirs, service user/group.
# Uses TRACER_PANEL_HOST for the config's host field if set.
tracer-panel install

# Idempotent: creates admin user + collector API key on first boot.
# Credentials come from TRACER_ADMIN_USERNAME / TRACER_ADMIN_PASSWORD.
# Exit code 2 means "already initialized" — that's fine on restarts.
tracer-panel -init || [ $? -eq 2 ]

# The panel requires running as its service user.
exec setpriv --reuid=tracer-panel --regid=tracer --init-groups /usr/local/bin/tracer-panel
