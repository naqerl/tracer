#!/bin/sh
set -e

# Idempotent: creates /etc/tracer/panel.yaml, dirs, service user/group.
# Uses TRACER_PANEL_HOST for the config's host field if set.
tracer-panel install

# Creates the admin user on first boot; on subsequent boots, -force
# resets the admin password to match TRACER_ADMIN_USERNAME /
# TRACER_ADMIN_PASSWORD from the environment (single source of truth).
tracer-panel -init -force

# The panel requires running as its service user.
exec setpriv --reuid=tracer-panel --regid=tracer --init-groups /usr/local/bin/tracer-panel
