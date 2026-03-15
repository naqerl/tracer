# E2E Demo

This demo bakes the local `bin/` artifacts into a Debian Bookworm image with systemd
and runs `install.sh` automatically on boot via a systemd oneshot unit.
Three systemd-managed mock apps are enabled in the image and a follow-up oneshot
unit registers them in the collector via the guesser.

## Usage

```bash
make -C e2e up
```

```bash
make -C e2e check
```

The panel is exposed on `http://localhost:18081` by default.
The default login is `demo / demo123`.

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
