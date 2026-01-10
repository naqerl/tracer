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

### Quick Install

```bash
curl -fsSL https://raw.githubusercontent.com/ai-shift/tracer/main/install.sh | sudo bash
```

This installs both components:
- **tracer-collector** - runs as root (to access any log files)
- **tracer-panel** - runs as dedicated `tracer` user

### Configuration

After installation, configure your log sources:

```bash
sudo vim /etc/tracer/collector.toml
```

Example configuration:

```toml
name = "my-server"            # optional, defaults to outbound IP
panel_endpoint = "ws://localhost:8080/api/ws/ingest?api_key=YOUR_API_KEY"
stats_interval = "30s"        # optional, system stats collection interval (default: 30s)
data_dir = "/var/lib/tracer"  # optional, data directory for offsets (default: /var/lib/tracer)

# JSON application logs
[[apps]]
name = "my-app"
path = "/var/log/my-app/app.log"
format = "json"
timestamp_field = "time"
level_field = "level"
message_field = "msg"
trace_id_field = "trace_id"
startup_pattern = "Server started"  # optional, marks app restart in logs

# Systemd service via journalctl
[[apps]]
name = "my-service"
format = "journalctl"
unit = "my-service"
# optional: parse MESSAGE field content with regex
pattern = '^(?P<level>\w+):\s+[\d.:]+\s+-\s+(?P<message>.+)$'

# Custom regex pattern
[[apps]]
name = "nginx"
path = "/var/log/nginx/error.log"
format = "regex"
pattern = '(?P<timestamp>\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}) \[(?P<level>\w+)\] .+?: (?P<message>.+)'
```

### Panel Configuration

```bash
sudo vim /etc/tracer/panel.toml
```

```toml
port = "8080"
panel_url = "https://tracer.example.com"  # optional, used in email/notification links
data_dir = "/var/lib/tracer"
sample_rate = 0.1        # persist 10% of non-error logs
retention_days = 90      # keep logs for 3 months

[auth]
session_duration = "24h"
```

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
  - API key authentication for collectors (regeneratable)

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

## Supported Log Formats

### JSON

```toml
[[apps]]
name = "my-app"
path = "/var/log/app.log"
format = "json"
timestamp_field = "time"      # or "timestamp", "ts", "@timestamp"
level_field = "level"         # or "severity", "lvl"
message_field = "msg"         # or "message"
trace_id_field = "trace_id"   # optional
```

### Regex

Common patterns supported out of the box:
- nginx access/error logs
- PostgreSQL logs
- Syslog
- Go stdlib log
- Go slog
- Python logging

```toml
[[apps]]
name = "nginx"
path = "/var/log/nginx/error.log"
format = "regex"
pattern = '(?P<timestamp>...)\s+\[(?P<level>\w+)\]\s+(?P<message>.*)'
```

### Journalctl

```toml
[[apps]]
name = "my-service"
format = "journalctl"
unit = "my-service.service"
# optional: parse MESSAGE field content with regex
pattern = '^(?P<level>\w+):\s+(?P<message>.+)$'
```

Syslog priority levels (0-7) are converted to standard levels (EMERG/ALERT/CRIT/ERROR/WARN/NOTICE/INFO/DEBUG).

**MESSAGE field parsing**: The optional `pattern` field allows extracting structured data from the MESSAGE content using named groups:
- `(?P<level>...)` - override syslog priority with application-specific level
- `(?P<message>...)` - extract clean message without metadata
- `(?P<timestamp>...)` - override journal timestamp with application timestamp
- `(?P<trace_id>...)` - extract trace ID for request correlation

This is useful for applications that log structured content to journalctl (e.g., uvicorn, gunicorn).

## Development

### Local Development

```bash
# Run full demo (panel + collector + mock logger)
make dev

# Stop all dev components
make dev-stop

# View panel logs
make dev-logs
```

### Building

```bash
# Build all binaries
make build

# Build release binaries (static, stripped)
make build-release

# Run tests
make test

# Run linter
make lint
```

### Deployment

See `deploy/` directory for deployment scripts and systemd service files.

```bash
cd deploy
make deploy           # Deploy both panel and collector
make deploy-panel     # Deploy only panel
make deploy-collector # Deploy only collector
make restart          # Restart services
```

## Collector-Only Installation

You can install only the collector on servers where you don't need the panel:

```bash
curl -fsSL https://raw.githubusercontent.com/ai-shift/tracer/main/install.sh | sudo bash -s -- --collector-only
```

This is useful for:
- Sending logs to a centralized tracer panel on another server
- Monitoring remote servers
- Distributed deployments

Configuration remains the same, just point `panel_endpoint` to your centralized panel instance.

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
- [ ] Slack/Discord notifications
- [ ] Log search with full-text
- [x] Metrics collection (beyond logs)
