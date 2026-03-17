# Tracer

> Yes, exactly the one you thought about

Lightweight, self-hosted log aggregation and alerting for small to mid-sized projects.

## What problem does it solve

*OpenTelemetry* setup for small to mid projects is insanely expensive overkill.

## How tracer helps

Simplicity.

- Run logs collector as a single binary
- Self host logs and analytics panel
- Get error alerts via Telegram
- View logs grouped by trace ID

### Philosophy

In production logs I care about two main things:

1. Get error alert from infrastructure and not the client
2. Get all the context about the error so tests and fixes could be written right away

Also I hate to setup infrastructure for each project to get these two simple things done.

Any other logs may show some performance degradation or health status but in both cases do not require persistence. `tracer` uses single SQLite database to store all info about errors and only selected percent of other logs (chosen randomly)

## Installation

### Install

**Full install (panel + collector):**

```bash
curl -fsSL https://raw.githubusercontent.com/ai-shift/tracer/main/install.sh | sudo bash
```

**Collector only (for remote servers):**

```bash
curl -fsSL https://raw.githubusercontent.com/ai-shift/tracer/main/install.sh | sudo bash -s -- --collector-only
```

This installs both components:
- **tracer-collector** - runs as dedicated `tracer-collector` user
- **tracer-panel** - runs as dedicated `tracer` user (unless `--collector-only`)

### Configure collector

`tracer-collector` automatically detects log formats (JSON, slog text) and field mappings. See [format-guessing.md](./docs/format-guessing.md) for detailed documentation.

**Quick start:**
```bash
# Add a systemd service
tracer-collector add myapp

# Add a log file
tracer-collector add /var/log/myapp.log
```

The collector will:
1. Read sample logs from the source
2. Detect the format and field mappings
3. Show a preview of the detected configuration
4. Ask for confirmation before saving

**Resolving format conflicts:**
If your logs use inconsistent field names (e.g., some lines use `level`, others use `status`), the collector will report a conflict. Use `--favour` to specify which field to use:

```bash
# First attempt shows the conflict
tracer-collector add myapp
# Error: inconsistent level_field: found both 'level' and 'status'

# Resolve by favouring the 'status' field
tracer-collector add myapp --favour 'level_field:"status"'
```

Fix conflicts one at a time - each error message suggests the exact `--favour` value to use.

### Start Services

```bash
sudo systemctl enable --now tracer-panel
sudo systemctl enable --now tracer-collector
```

Access the panel at http://localhost:8080

## Features

### Collector

- **Multiple log formats**
  - JSON with customizable field mappings
  - User-defined regex patterns
  - Journalctl (systemd services) with optional MESSAGE field parsing
- **Automatic timestamp parsing** - RFC3339, ISO, Unix timestamps
- **Field extraction** - timestamp, level, trace_id, message, context
- **WebSocket transport** - low-latency streaming to panel with ping/pong keepalive
- **Reconnection** - automatic reconnect with exponential backoff
- **Named collectors** - identify collectors by name or auto-detect via outbound IP
- **System stats collection** - CPU, RAM, disk usage sent to panel at configurable intervals
- **Startup detection** - optional pattern matching to mark application restarts

### Panel

- **Authentication**
  - Session-based auth with secure cookies
  - Configurable session duration
  - Admin and regular user roles
  - Per-app access control for non-admin users
  - Multiple collector API keys with per-key revocation and one-time plaintext display

- **User management** (admin only)
  - Create/delete users
  - Assign admin privileges
  - Per-user app access control

- **Log management**
  - Per-app SQLite databases (WAL mode)
  - Configurable sample rate for non-error logs (errors always persisted at 100%)
  - Automatic retention cleanup (configurable days)
  - Trace ID grouping for request tracking
  - Log detail view with raw message and context
  - Filter logs by level

- **Analytics dashboard**
  - Error/warning/info counts (24h)
  - Real-time collector status monitoring with system stats (CPU, RAM, disk)
  - Per-app analytics views
  - Configurable auto-refresh interval (default: 10s)

- **Notifications**
  - Telegram alerts for errors
  - Per-user notifier configuration
  - App-specific or global (admin) notifications
  - Enable/disable notifiers without deleting
  - Multiple notifiers per user

## Architecture

### Data isolation

- Flat list of applications (no organization/project hierarchy)
- Each application has its own SQLite database
- SQLite databases use WAL mode for concurrent read/write performance

### Data retention

- Configurable retention period (default: 3 months)
- Automatic daily cleanup of old logs
- Errors always persisted, other logs sampled

### Transport

- Collector communicates with Panel via WebSocket
- Collectors register their apps on connect
- Keep-alive ping/pong for connection health

## Development

### Deployment

See `deploy/` directory for deployment scripts and systemd service files.

```bash
cd deploy
make deploy           # Deploy both panel and collector
make deploy-panel     # Deploy only panel
make deploy-collector # Deploy only collector
make restart          # Restart services
```

## Roadmap

- [x] Multiple log formats (JSON, regex, journalctl)
- [x] WebSocket transport with reconnection
- [x] Per-app SQLite databases
- [x] Authentication and user management
- [x] Per-user app access control
- [x] Telegram notifications
- [x] Analytics dashboard
- [x] Collector status monitoring with system stats
- [x] Trace ID grouping
- [x] Log retention cleanup
- [x] API key authentication
- [x] Journalctl MESSAGE field parsing
- [x] Configurable refresh intervals
- [x] Startup pattern detection
- [ ] Log search with full-text
- [x] Metrics collection (beyond logs)
