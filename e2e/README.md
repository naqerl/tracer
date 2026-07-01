# E2E Demo

End-to-end test environment that runs a full tracer stack inside a Debian container with systemd. Three mock services (`demo-api`, `demo-worker`, `demo-cron`) continuously generate log traffic.

## Usage

```bash
# Build image and start container (panel at localhost:18081)
make -C e2e up

# Run health checks against the running container
make -C e2e check
```

Default login: `demo` / `demo1234`.

## Log profiles

Switch the mock services between logging profiles:

```bash
make -C e2e mock-logs-high     # higher volume, ~1-3s intervals
make -C e2e mock-logs-normal   # default low volume
make -C e2e mock-logs-status   # show current profile
```

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
