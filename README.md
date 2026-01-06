# Tracer

> Yes, exactly the one you thought about

## What problem does it solve

*OpenTelemetry* setup for the small to mid project is insanely expensive overkill

## How tracer helps

Simplicity.

- Run logs collector as a single binary 
- Self host logs and analytics panel

### Philosophy

In production logs I care about two main things:

1. Get error alert from infrastructure and not the client
2. Get all the context about the error so tests and fixes could be written right away

Also I hate to setup infrastructure for each project to get this two simple things done.

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
sudo nano /etc/tracer/collector.toml
```

Example configuration:

```toml
name = "my-server"  # optional, defaults to outbound IP
panel_endpoint = "ws://localhost:8080/api/ws/ingest?api_key=YOUR_API_KEY"

# JSON application logs
[[apps]]
name = "my-app"
path = "/var/log/my-app/app.log"
format = "json"
timestamp_field = "time"
level_field = "level"
message_field = "msg"
trace_id_field = "trace_id"

# Systemd service via journalctl
[[apps]]
name = "my-service"
format = "journalctl"
unit = "my-service"

# Custom regex pattern
[[apps]]
name = "nginx"
path = "/var/log/nginx/error.log"
format = "regex"
pattern = '(?P<timestamp>\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}) \[(?P<level>\w+)\] .+?: (?P<message>.+)'
```

### Panel Configuration

```bash
sudo nano /etc/tracer/panel.toml
```

```toml
port = "8080"
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
  - Journalctl (systemd services)
- **Automatic timestamp parsing** - RFC3339, ISO, Unix timestamps
- **Field extraction** - timestamp, level, trace_id, message, context
- **WebSocket transport** - low-latency streaming to panel
- **Reconnection** - automatic reconnect with exponential backoff

### Panel

- **Authentication**
  - Session-based auth with secure cookies
  - Admin and regular user roles
  - Per-app access control for non-admin users
  - API key authentication for collectors

- **Log management**
  - Per-app SQLite databases (WAL mode)
  - Configurable sample rate for non-error logs
  - Automatic retention cleanup
  - Trace ID grouping

- **Analytics**
  - Error/warning/info counts (24h)
  - Hourly distribution breakdown
  - Real-time collector status

- **Notifications**
  - Telegram alerts for errors
  - Per-user notifier configuration
  - App-specific or global (admin) notifications

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
```

Syslog priority levels (0-7) are converted to standard levels (EMERG/ALERT/CRIT/ERROR/WARN/NOTICE/INFO/DEBUG).
