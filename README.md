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

- **Add an app** — the *+* next to a collector opens its app creation page (`/panel/collectors/<collector>/app`). Give a service name (systemd unit, kubectl resource, or any log source); leave it empty for an uptime-only app. For uptime-only monitoring, add an optional health-check URL; the collector polls it and no log collection happens. Saving returns you to `/panel`.
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
