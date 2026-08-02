# Changelog

## Unreleased

### Fixed
- Collector kubectl sources no longer pass `--prefix=true`, so `kubectl logs` emits raw container log lines. The `[pod/<pod>/<container>]` prefix was merged into the first key by the slog text tokenizer (e.g. `[pod/x/y] time=...`), which broke both `tracer-collector add` format guessing ("missing required field(s): timestamp") and runtime parsing of JSON/slog text logs
- `tracer-collector add` no longer hangs when sampling a file source: a seek switches the file source into a bounded historical read that returns `io.EOF` once drained (the daemon's follow behavior is unchanged since it never seeks), and the sampler falls back to the start of the log when the tail seek lands past every complete line
- `tracer-collector add` prompts are only shown on a real terminal (an `isatty` check replaces the character-device heuristic, which treated `/dev/null` as interactive)
- `tracer-collector add` flags now work before or after the source argument (Go's flag parser stops at the first positional, so previously `add myapp --no-confirm` silently ignored the flag); unexpected trailing arguments are rejected instead of silently dropped
- Collector kubectl and journalctl sources no longer report `SOURCE_ERROR[source_read_failure]: EOF` for a failing external process: stderr and the exit status are now captured, so the panel log shows the real reason (e.g. `error loading config file ...: permission denied`) instead of a bare `EOF`. A clean stream end (exit 0) is still `io.EOF`
- The collector no longer emits `source_read_failure` error events for a clean end of stream: `io.EOF` from a source is treated as a silent retry (with backoff) instead of an error, so a kubectl/journalctl process that exits 0 no longer spams the panel every few seconds

### Added
- `tracer-collector kubectl-setup` installs a kubeconfig at `/etc/tracer/kubeconfig` (root-owned, readable by the `tracer` group) so kubectl sources work for the collector service user, which has no home directory; it verifies the source config, copies it, and verifies the copy. `--kubeconfig <path>` overrides auto-detection (`$KUBECONFIG`, `~/.kube/config`, k3s/kubeadm locations)
- `tracer-collector add` for kubectl sources now installs the kubeconfig automatically (`--kubeconfig <path>` to override auto-detection), sets the `kubeconfig` param on the source, and fails if the installed config cannot read the resource — with a pointer to `kubectl-setup`
- `tracer-collector add` now offers follow-up actions when format guessing fails on missing fields: it lists every key present in the offending line (carried by the guesser's `MissingFieldsError`), and on a terminal interactively maps each missing field to a chosen key, retrying with the resulting `--favour`. Missing optional fields (e.g. `trace_id`) are also reported and prompted, but can be ignored with Enter instead of mapped
- `--favour` can now supply a field mapping for fields the guesser cannot find at all (previously it only resolved conflicts between lines); the mapped key must exist in the log line. This includes optional fields: mapping `trace_id_field` to a non-synonym key works
- `tracer-panel -init -force` overwrites the password of the given admin user when the panel is already initialized and exits 0 instead of 2 (existing sessions are invalidated, missing user is created as admin)
- Collector API key detail page: reveal the full key with an eye icon (POST-backed, so it is rejected by the read-only middleware — keys can never be exposed in read-only mode)
- Collector API key plaintext is now encrypted at rest (AES-256-GCM) with a master key in `{dataDir}/keys.key` (0600) or `TRACER_KEY_SECRET` env var; keys created before this version have no stored plaintext and show "Not revealable"

## [0.1.16] - 2026-06-28

### Changed
- Refactored shared packages: `pkg/setup/` → `libs/shared/install/`, `libs/updater/` → `libs/shared/update/`, all ldflags vars consolidated into `build/` package
- Dockerfiles now delegate to `make` with env vars instead of duplicating ldflags
- Removed `pkg/` directory entirely

## [0.1.15] - 2026-06-28

### Fixed
- Topology graph heartbeat wave animation not firing due to SSE event name mismatch (server sent `panel_tick`, client listened for `tick`)

## [0.1.14] - 2026-06-27

### Added
- Integration tests for resource monitoring (SaveSystemStats, GetLatestStats, GetStatsHistory, GetCollector, DeleteOldStats)
- Tests verify deduplication, change detection, partial data, retention, and since filtering

### Changed
- Refactored existing tests from unit-style to integration-style using public API methods
- Test staleness checks now use GetCollector instead of private formatCollectorStats

## [0.1.12] - 2024-06-26

### Fixed
- Topology SSE: use PersistedLogMessage directly for 'persisted' events, bypass envelope normalization

## [0.3.6] - 2026-01-10

### Added
- User API key management for LLM compatibility with auth adapter methods
- Settings UI for user API key management
- Markdown format middleware for .md suffix handling
- Markdown rendering method to templates
- LLM documentation endpoint for API integration
- Welcome and subscription notifications via telegram
- .env support in project

### Changed
- Simplified telegram notification format to level @app with message
- Included emoji in notification level field
- Updated routing for logs with markdown format support
- Renamed deploy targets to panel/collector, added full target
- Updated middleware to support user API key authentication

### Database
- Added user_api_key table migration

### Development
- Added test-endpoints make target
- Marked metrics collection as completed in docs

## [0.3.5] - Previous Release
