# E2E Demo

This demo runs a full end-to-end test that mirrors the actual user installation flow:
`./configure && make build && make install`

The container starts with the source code and runs the standard installation at boot,
ensuring the actual installation process works correctly on end-user systems.

Three systemd-managed mock apps provide logs for testing.

## Usage

```bash
make -C e2e up
```

```bash
make -C e2e check
```

The panel is exposed on `http://localhost:18081` by default.
The default login is `demo / demo123`.

## Load toggle

The three mock systemd services (`tracer-demo-api`, `tracer-demo-worker`, `tracer-demo-cron`)
can be switched into a higher-volume logging profile in the running container:

```bash
make -C e2e mock-logs-high
make -C e2e mock-logs-status
make -C e2e mock-logs-normal
```

`mock-logs-high` increases the startup burst slightly, then switches the demo services into
a jittered steady mode that emits single flows every 1-3 seconds at random so you can observe
continuous traffic on the topology page without rebuilding the image.

## Live reload

To rebuild and hot-swap the panel binary into the running e2e container whenever
panel code changes:

```bash
make -C e2e live-reload
```

This target uses `entr` to watch panel-related source files on the host, rebuilds
`tracer-panel`, copies the new binary into the running `tracer-e2e` container,
and restarts `tracer-panel.service` with systemd. Start the demo container first
with `make -C e2e up`.
