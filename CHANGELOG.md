# Changelog

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
