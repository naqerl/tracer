# E2E Demo

End-to-end test environment that runs a full tracer stack inside a Debian container with systemd. Three mock services (`demo-api`, `demo-worker`, `demo-cron`) continuously generate log traffic, and a fourth source (`demo-conflict`) emits intentionally inconsistent logs for the add-app conflict resolution UI.

## Usage

```bash
# Build image and start container (panel at localhost:18081)
make -C e2e up

# Run health checks against the running container
make -C e2e check
```

Default login: `demo` / `demo1234`.

## Read-only panel

The demo panel itself is always writable. Read-only enforcement lives in the
**nginx** reverse proxy that fronts the public demo panel on port 8081: by
default it rejects every state-changing request (e.g. creating or deleting
notifiers). Session routes (`/auth/*`) are exempt so you can log in to a
read-only demo.

To start the panel with write access through the proxy:

```bash
E2E_PANEL_READONLY=0 make -C e2e up
```

`E2E_PANEL_READONLY` defaults to `1`; the value is passed into the container
and read by the boot script, which installs the matching nginx config. It also
applies to `make -C e2e up-release`. The collector connects to the panel on the
internal port, so it can always push config and data regardless of the proxy's
read-only state.

## Log profiles

Switch the mock services between logging profiles:

```bash
make -C e2e mock-logs-high     # higher volume, ~1-3s intervals
make -C e2e mock-logs-normal   # default low volume
make -C e2e mock-logs-status   # show current profile
```

## Add-app format guess conflict

`demo-conflict.service` interleaves two JSON log shapes that share
`time`/`msg` keys but disagree on the level key (`level` vs `severity`). It is
not pre-registered as a collector app, so the panel's **Add app** form can be
used to exercise the detection flow end-to-end:

1. Start the panel with write access through the proxy (the form posts the
   guess request, which the read-only nginx gate rejects):
   ```bash
   E2E_PANEL_READONLY=0 make -C e2e up
   ```
2. Log in as `demo` / `demo1234`, open the collector's apps page, and click the
   `+` card.
3. Type `demo-conflict` in the name field and blur — the panel asks the
   collector to guess the format and renders the inconsistency:
   "The log format is inconsistent on the level_field field. Choose which value
   to favour:"
4. Click **favour level** or **favour severity** — the detection re-runs with
   that favour and resolves into a `journalctl` / `json` format with sample
   lines; submit to save the app.

The source keeps rotating lines, so the conflict reproduces on any fresh guess.

## Live reload

Rebuild the panel binary and hot-swap it into the running container on code changes:

```bash
make -C e2e live-reload
```

Uses `entr` to watch panel source files, rebuilds `tracer-panel`, copies it into the container, and restarts the service.

## Release mode

To test the release install flow (downloads pre-built binaries instead of using local ones):

```bash
make -C e2e up-release
```

## Other commands

```bash
make -C e2e logs     # follow container logs
make -C e2e shell    # open bash in the container
make -C e2e down     # stop and remove the container
```
