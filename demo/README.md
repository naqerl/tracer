# Demo Stack

This demo runs fully in Podman with one panel and two collector/generator bundles.

## Run

```bash
make -C demo up
```

Live reload mode (bind-mount current workspace, run panel/collectors with `air`):

```bash
make -C demo up-live-reload
```

Panel is exposed at `http://localhost:18081`.
Default credentials: `demo` / `demo123`.

## Stop

```bash
make -C demo down
```

## Restart only log generators

```bash
make -C demo restart-loggers
```

You can also control them separately:

```bash
make -C demo stop-loggers
make -C demo start-loggers
```

## Reset all demo data

```bash
make -C demo reset
```

## Notes

- This demo focuses on file-based sources with multiple formats (JSON + regex) and multiple collectors.
- Demo uptime uses one `mmock` container inside the pod. `alpha-json` and `beta-json` stay healthy; `alpha-regex` and `beta-regex` alternate between `200` and `500` responses so uptime bars show mixed history.
- Journalctl sources in a pure Podman-only setup are possible but not reliable with stable `-u <unit>` mapping without additional systemd wiring.
