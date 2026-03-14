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
