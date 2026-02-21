# Demo Stack

This demo runs fully in Podman with one panel and two collector/generator bundles.

## Run

```bash
make -C demo up
```

Panel is exposed at `http://localhost:18081`.
Default credentials: `demo` / `demo123`.

## Stop

```bash
make -C demo down
```

## Reset all demo data

```bash
make -C demo reset
```

## Notes

- This demo focuses on file-based sources with multiple formats (JSON + regex) and multiple collectors.
- Journalctl sources in a pure Podman-only setup are possible but not reliable with stable `-u <unit>` mapping without additional systemd wiring.
