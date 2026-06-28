# Tracer

> Yes, exactly the one you thought about

Lightweight, self-hosted log aggregation and alerting for small to mid-sized projects.

OpenTelemetry is overkill for small projects. Tracer is a single binary that collects logs, stores them in per-app SQLite databases, and sends error alerts via Telegram.

## Quick Install

```bash
curl -fsSL https://git.naqerl.com/naqerl/tracer/raw/main/install.sh | sudo bash
```

**Collector only** (for remote servers that should not run the panel):

```bash
export TRACER_COLLECTOR_ONLY=1
curl -fsSL https://git.naqerl.com/naqerl/tracer/raw/main/install.sh | sudo bash
```

The script downloads pre-built binaries, creates system users (`tracer-panel`, `tracer-collector`, group `tracer`), installs systemd services, and creates default configs at `/etc/tracer/panel.yaml` and `/etc/tracer/collector.yaml`.

### Configure collector

`tracer-collector` automatically detects log formats (JSON, slog text) and field mappings:

```bash
# Add a systemd unit as a log source
tracer-collector add myapp

# Add a log file
tracer-collector add /var/log/myapp.log
```

If logs use inconsistent field names, resolve conflicts iteratively with `--favour`:

```bash
tracer-collector add myapp --favour 'level_field:"status"'
```

See [format-guessing.md](./docs/format-guessing.md) for details.

### Start services

```bash
sudo systemctl enable --now tracer-panel
sudo systemctl enable --now tracer-collector
```

Access the panel at **http://localhost:8080**.

## Features

- Multiple log formats (JSON, regex, journalctl)
- Automatic timestamp parsing — RFC3339, ISO, Unix
- WebSocket transport with reconnection and exponential backoff
- Per-app SQLite databases (WAL mode)
- Configurable sample rate for non-error logs (default: 10%; errors always 100%)
- Automatic retention cleanup (default: 90 days)
- Trace ID grouping
- Session-based auth with admin/regular user roles and per-app access control
- Collector API keys with per-key revocation
- Telegram alerts for errors, per-user notifier configuration
- Analytics dashboard with error/warning/info counts and collector system stats (CPU, RAM, disk)
- Real-time collector status monitoring
- Startup pattern detection for application restarts

## Architecture

- Collectors connect to the panel via WebSocket, register their apps, and stream log events
- Each application gets its own SQLite database
- System stats (CPU, RAM, disk) are periodically sent to the panel
- Panel stores collector API keys and user data in a separate panel database

## Development

### Build from source

```bash
# Build both binaries
make build

# Run tests and lint
make test
make vet

# Install (requires root)
sudo make install
```

Build configuration (defaults can be overridden):

| Variable | Default | Description |
|---|---|---|
| `WITH_SYSTEMD` | `auto` | `yes`, `no`, or `auto` (detect at runtime) |
| `BINDIR` | `/usr/local/bin` | Binary installation directory |
| `PANEL_USER` | `tracer-panel` | System user for panel service |
| `COLLECTOR_USER` | `tracer-collector` | System user for collector service |
| `TRACER_GROUP` | `tracer` | System group for both services |
| `VERSION` | `unknown` | Build version (injected by CI) |

Override example:

```bash
make WITH_SYSTEMD=no BINDIR=/opt/tracer/bin build
```

### Uninstall

```bash
sudo make uninstall
```

### Container image

The panel is published as a minimal scratch-based Docker image (~15 MB):

```bash
docker pull git.naqerl.com/naqerl/panel:latest

docker run -d \
  --name tracer-panel \
  -p 8080:8080 \
  -v /path/to/data:/var/lib/tracer \
  git.naqerl.com/naqerl/panel:latest
```

Build locally:

```bash
make docker-build-panel
```

Images are automatically built and pushed on every `v*` tag by CI.

### End-to-end testing

See [e2e/README.md](./e2e/README.md) for integration test setup using a mock log generator.

## Releasing

Push a `v*` tag to trigger the CI release workflow:

```bash
git tag v1.2.3
git push origin v1.2.3
```

The workflow builds `linux/amd64` binaries, runs tests and lint, creates a Forgejo release with archives and checksums, and pushes the Docker image to `git.naqerl.com/naqerl/panel` (both `:latest` and `:vX.Y.Z`).
