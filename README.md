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
app_name = "my-app"
panel_endpoint = "ws://localhost:8080/ws/ingest"

# JSON application logs
[[log_files]]
path = "/var/log/my-app/app.log"
format = "json"

# Systemd service via journalctl
[[log_files]]
format = "journalctl"
unit = "my-service"
```

### Start Services

```bash
sudo systemctl enable --now tracer-panel
sudo systemctl enable --now tracer-collector
```

Access the panel at http://localhost:8080

## Architecture

### Data isolation

- Flat list of applications (no organization/project hierarchy)
- Each application has its own SQLite database
- SQLite databases use WAL mode for concurrent read/write performance

### Data retention

- Configurable retention period (default: 3 months)
- Automatic cleanup of old logs

### Transport

- Collector communicates with Panel via WebSocket for low-latency log streaming

## TO-DO

- [ ] Collector
    - [ ] Reads given file, parses logs and sends them to the panel endpoint via WebSocket
    - [ ] Configured via systemd service
    - [ ] Formats (with datetime, level, trace id and message groups set up via `.toml` at /etc/tracer/config.toml)
        - [ ] JSON
        - [ ] User-defined Regex patterns
- [ ] Analytics panel
    - [ ] Persists errors (100%)
    - [ ] Persists random other log records (configurable sample rate)
    - [ ] Configured via `.toml` file
    - [ ] Sends alerts to the given Telegram chat
    - [ ] Real-time percentile calculations
    - [ ] Saving all the context gathered with traceID
    - [ ] Per-app database isolation
    - [ ] Configurable log retention (default 3 months)
