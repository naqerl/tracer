# New Demo

This demo bakes the local `bin/` artifacts into a Debian Bookworm image with systemd
and runs `install.sh` automatically on boot via a systemd oneshot unit.

## Usage

```bash
make -C newdemo up
```

The panel is exposed on `http://localhost:18080` by default.
