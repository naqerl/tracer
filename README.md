# Tracer

> Yes, exactly the one you thought about

Lightweight, self-hosted log aggregation and alerting for small to mid-sized projects.

OpenTelemetry is overkill for small projects. Tracer is a single binary that collects logs, stores them in per-app SQLite databases, and sends error alerts via Telegram or ntfy.sh.

Demo: [tracer.naqel.com](https://tracer.naqel.com) &middot; Landing: [naqerl.com/projects/tracer](https://naqerl.com/projects/tracer/index.html)

## Features

- **One-command install** &mdash; `curl ... | sudo bash` gets you a running panel and collector
- **Multi-user & role-based access** &mdash; admin and regular user roles with per-app access control, session-based auth with secure cookies
- **Telegram & ntfy.sh notifications** &mdash; per-user notifier configuration, enable/disable without deleting, multiple notifiers per user
- **Resource monitoring** &mdash; collects CPU, RAM, and disk usage from each collector; real-time status on the dashboard
- **API access for agents** &mdash; collector API keys for machine-to-machine auth, per-user API keys for scripted access, both with per-key revocation
- **MCP server for AI agents** &mdash; the panel serves a Model Context Protocol endpoint at `/mcp`, so Claude Desktop, Cursor and MCP Inspector can query it as read-only tools (plus one app-creation tool) using a per-user API key
- **Multiple log formats** &mdash; JSON with customizable field mappings, Go slog text, user-defined regex, journalctl
- **Per-app SQLite databases** &mdash; WAL mode, automatic retention cleanup (default: 90 days), configurable sample rate for non-error logs (default: 10%; errors always 100%)
- **Panel-driven collector config** &mdash; collectors are configured entirely from the panel UI; config is pushed over the live WebSocket and hot-reloads with no local files
- **Auto-detected log sources** &mdash; give a service name and the collector guesses the log format (JSON / slog text / regex) and field mappings automatically

## Quick Install

```bash
curl -fsSL https://git.naqerl.com/naqerl/tracer/raw/main/install.sh | sudo bash
```

**Collector only** (for remote servers that should not run the panel):

```bash
curl -fsSL https://git.naqerl.com/naqerl/tracer/raw/main/install.sh | sudo TRACER_COLLECTOR_ONLY=1 bash
```

> Pass `TRACER_COLLECTOR_ONLY` *through* `sudo` — a plain `export` in your shell
> won't reach the script, because `sudo` resets the environment by default.

The script downloads pre-built binaries, creates system users (`tracer-panel`, `tracer-collector`, group `tracer`), installs systemd services, and creates default configs at `/etc/tracer/panel.yaml` and `/etc/tracer/collector.yaml` (the collector's config holds only the sender connection: api key, panel host, and name — apps are managed from the panel).

### Configure apps from the panel

Collectors are configured entirely from the panel UI. The collector has no
local app configuration — it connects to the panel, and the panel pushes the
stored app config over the live WebSocket connection. Collectors hot-reload on
receipt, so you can add, edit, and delete apps without touching the collector
host.

- **Add an app** — the *+* next to a collector opens its app creation page (`/panel/collectors/<collector>/app`). Give a service name (systemd unit, kubectl resource, or any log source); leave it empty for an uptime-only app. For uptime-only monitoring, add an optional health check; the collector polls it and no log collection happens. Saving returns you to `/panel`. An HTTP check runs on a fresh connection and is retried up to three times (5 s per attempt, 1 s apart); a probe is down only when every attempt fails. A command check runs a shell command on the collector and is up only when its exit code is expected and its output matches the optional regexp.
- **Format guessing** — the collector runs the format guesser on the named service over the WebSocket and auto-detects the log format (JSON or slog text) and field mappings. The panel saves the result and pushes it back.
- **Delete / edit apps** — each app card has a *Delete* button (with confirmation) and a *setting* link.
- **Raw config** — the settings page exposes the JSON collector config for direct editing.

`tracer-collector -help` prints the complete CLI — every command and its
flags. Help works even when the collector is not yet configured.

Kubectl sources are read by the collector daemon, which runs as the
`tracer-collector` service user — a user with no home directory and no
kubeconfig of its own. If you need to (re)configure kubectl access separately,
run:

```bash
sudo tracer-collector kubectl-setup          # auto-detect the source config
sudo tracer-collector kubectl-setup --kubeconfig /path/to/kubeconfig
```

It verifies the config works, copies it to `/etc/tracer/kubeconfig`, verifies
the copy, and points the collector service at it by setting `KUBECONFIG` in the
systemd unit — so a kubectl source can never silently fall back to an
unreadable system default.

`tracer-collector install` does the same automatically when it finds `kubectl`
on `PATH`, so a fresh install already works. Run `kubectl-setup` yourself only
to point the collector at a different cluster.

### Point the collector at your panel

The fastest way is the collector API key page (Settings &rarr; Collector API
Keys &rarr; a key). Press **Copy** and paste the command on the collector host.
It installs the collector, or upgrades an existing one in place, and starts it:

```bash
curl -fsSL https://panel.example.com/panel/collectors/<name>/install-script \
  -H 'X-API-Key: <collector-api-key>' | sudo bash
```

The same steps by hand, if you prefer:

```bash
# API key: create it in the panel UI (Settings → Collector API Keys)
sudo tracer-collector set api-key <collector-api-key>
# Panel address: host:port (no scheme; ws:// is implied)
sudo tracer-collector set panel-host panel.example.com:8080
```

Check that the collector can reach the panel, authenticate, and register:

```bash
sudo tracer-collector verify
```

`verify` reports actionable errors: an unset key/host, a 401 rejection
(invalid API key), a handshake rejection (not a tracer panel), or a connection
failure. It exits non-zero on failure, so it can be used in setup scripts.

See [format-guessing.md](./docs/format-guessing.md) for details on how
auto-detection works.

### Start services

```bash
sudo systemctl enable --now tracer-panel
sudo systemctl enable --now tracer-collector
```

Access the panel at **http://localhost:8080**.

### Panel initialization & password reset

The installer initializes the panel automatically, but you can do it manually:

```bash
# Create the admin user and collector API key (prints credentials to stdout)
sudo tracer-panel -init -username admin -password 'your-password'

# If the panel is already initialized, overwrite the admin password instead
# (exits 0; invalidates existing sessions of the user, promotes them to admin)
sudo tracer-panel -init -force -username admin -password 'new-password'
```

- Without `-force`, `-init` on an already-initialized panel prints
  `Panel already initialized` to stderr and exits with code 2.
- If `-password` is omitted, a random password is generated and printed.
- `-init -force` never touches the existing collector API key.

### API key reveal

- On the collector API key detail page (`/settings/admin/collector-api-keys/{id}`)
  an eye icon reveals the full key. Reveal is a POST request.
- The **Copy** button next to the install command puts the whole command, key
  included, on the clipboard without revealing anything on screen. **Show**
  displays the command with the key when you want to read it.
- Keys created before this version store no plaintext and show **Not revealable**.
- The master key used to encrypt collector key plaintext at rest lives in
  `{dataDir}/keys.key` (created with 0600 permissions on first use) and can be
  overridden with the `TRACER_KEY_SECRET` env var (64 hex chars). Losing it makes
  existing keys unrevealable.

## MCP server for AI agents

The panel serves a [Model Context Protocol](https://modelcontextprotocol.io) endpoint at **`/mcp`**, so
an agent can answer questions like *"what is failing on my-server/web-api?"* from real Tracer data.
There is no separate binary and no stdio: MCP is part of the panel you already run.

Tools read the same pages a browser does, rendered as markdown by the panel's own handlers, so every
existing access rule applies unchanged. A single result is capped at 48 KiB and ends with a truncation
note when it was cut.

### Authenticate

MCP is **token-only**: every request must carry a panel **user API key** in `X-API-Key`. A browser
session cookie is deliberately not accepted, so a logged-in browser cannot drive the endpoint.

Create a key in **Settings &rarr; API Keys** and copy the plaintext (it is shown once). The key
inherits that user's app permissions: an admin key sees every collector and app, a regular user's key
sees only the apps assigned to that user.

A panel started with `-no-auth` (the public demo) has no keys to check, so MCP works there without one. `create_app` is refused in that mode, so **MCP** is read-only on the demo — the demo's web UI has its own, wider write exemptions.

### Tools

`since` selects the time window (`1h`, `6h`, `24h`, `7d`, `30d`; default `24h`) and `level` filters log
groups (`all`, `error`, `warn`, `info`; default `error`).

| Tool | What it answers |
|---|---|
| `list_collectors` | Which collectors exist, with status, resource snapshot and app counts |
| `get_collector` | One collector's overview |
| `collector_resources` | CPU, RAM and disk history for the window |
| `list_apps` | Apps on a collector with uptime bars and error counts |
| `get_app` | One app's page: its uptime, plus links to its log stats and log groups |
| `app_stats` | Log volume and the error / warning / info split |
| `app_uptime` | Uptime percentage and per-bucket bars |
| `list_log_groups` | Grouped, deduplicated log messages, newest first |
| `get_log_group` | One group's message, level and count |
| `log_group_entries` | The individual entries inside a group (50 per page, `offset` to page) |
| `get_log` | One entry in full, with trace id and context |
| `create_app` | Add an app to a collector — the only write tool |

### Write tools

`create_app` adds an app to a collector: a `service` (systemd unit, kubectl workload or file path) to
collect its logs, a health check to watch it, or both. `health_type` is `http` (the default) or
`command`; an HTTP check takes `url` and `expected_status`, while a command check takes `command`,
an optional `expect` Go regexp, and `expected_exit` (comma-separated codes, default `0`). `interval`
sets how often the check runs (default `30s`), and `alert_delay` (a Go duration such as `5m`, default
`0s`) holds an uptime alert back until the app has been down that long. It is refused when the panel
runs with `-no-auth` (demo mode), and otherwise needs a user who may modify that collector. Everything
else reads only.

A command health check is useful for a dependency that has no HTTP endpoint. For example, to watch a
Postgres replica from the collector host:

```json
{
  "collector": "office",
  "name": "pg-replica",
  "health_type": "command",
  "command": "pg_isready -h 127.0.0.1 -p 5433",
  "expect": "accepting connections",
  "expected_exit": "0",
  "interval": "30s"
}
```

The collector runs the command with `/bin/sh -c` in its own process group, gives it a timeout
(5 s by default), and kills the whole group on timeout. A failure is logged once with the tail of the
command's combined output.

### Claude Desktop

Add to `claude_desktop_config.json` (Settings &rarr; Developer &rarr; Edit Config). Remote MCP servers
are configured with a URL and headers rather than a command:

```json
{
  "mcpServers": {
    "tracer": {
      "type": "http",
      "url": "https://tracer.example.com/mcp",
      "headers": { "X-API-Key": "your-user-api-key" }
    }
  }
}
```

### Cursor

Add the same block to `.cursor/mcp.json` in the project (or `~/.cursor/mcp.json` for every project):

```json
{
  "mcpServers": {
    "tracer": {
      "type": "http",
      "url": "https://tracer.example.com/mcp",
      "headers": { "X-API-Key": "your-user-api-key" }
    }
  }
}
```

### MCP Inspector

```bash
npx @modelcontextprotocol/inspector
# Transport: Streamable HTTP   URL: https://tracer.example.com/mcp
# Header:      X-API-Key: your-user-api-key
```

The endpoint speaks the **streamable HTTP** transport: responses stream as server-sent events and each
session gets an `Mcp-Session-Id`. The older HTTP+SSE transport (separate `/sse` and `/messages`
endpoints) is not served.

## Architecture

- Collectors connect to the panel via WebSocket, register their apps, and stream log events
- Each application gets its own SQLite database
- System stats (CPU, RAM, disk) are periodically sent to the panel
- Panel stores collector API keys and user data in a separate panel database

## Development

### Build from source

```bash
# Build every binary
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
