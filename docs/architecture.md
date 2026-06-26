# Tracer Architecture

This document defines the domain-first project structure, ownership boundaries, and import constraints for the Tracer project.

## Domain Layout

```text
/apps
  /collector        # Log collection agent
    /cmd/tracer-collector
    /internal
      /config       # Collector configuration
      /watch        # File watching
      /parsing      # Log parsing logic
      /transport    # WebSocket client
      /stats        # System stats collection

  /panel            # Web UI and API server
    /cmd/tracer-panel
    /internal
      /config       # Panel configuration
      /http         # HTTP handlers
      /routes       # Route definitions
      /auth         # Authentication & sessions
      /collectors   # Collector registry
      /notifiers    # Notification service
      /storage      # Database access layer
      /retention    # Data retention policies
    /sql            # SQL migrations (panel-only)
      /panel        # Panel schema
      /collector    # Collector schema
      /app          # App logs schema
      /queries      # SQL queries
    /sqlc.yaml      # SQLC configuration
    /db             # Generated SQLC code

/libs
  /shared           # Shared utilities (no business logic)
    /logx           # Logging utilities
    /httpx          # HTTP helpers
    /configx        # Config utilities
    /types          # Common types

/scripts            # Build and utility scripts
/docs               # Documentation
/demo               # Demo configuration
/deploy             # Deployment configs
```

## Root Cleanliness

Allowed at root:
- `go.mod`, `go.sum` - Go module definition
- `Makefile` - Top-level build orchestration
- `.github/`, `.gitignore` - Git configuration
- `README.md`, `CHANGELOG.md` - Project documentation
- `apps/`, `libs/`, `scripts/`, `docs/`, `e2e/`, `deploy/` - Domain folders

NOT allowed at root:
- Business logic or domain code
- SQL migrations (panel-only)
- Database code (panel-only)
- Configuration for specific apps

## Ownership Matrix

| Component | Owner | Description |
|-----------|-------|-------------|
| File watcher | collector | Watching log files for changes |
| Log parsing | collector | Parsing various log formats |
| WebSocket client | collector | Sending logs to panel |
| Stats agent | collector | System metrics collection |
| Collector config | collector | Collector-specific configuration |
| API ingest | panel | Receiving logs via WebSocket |
| Auth/session | panel | User authentication and sessions |
| Notifier stack | panel | Alert notifications |
| Collector registry | panel | Tracking connected collectors |
| Retention | panel | Data cleanup policies |
| Database lifecycle | panel | All SQL/DB/sqlc operations |
| Generic helpers | shared | Non-domain utilities |

## Import Constraints

### Allowed Imports

1. **collector** can import:
   - `libs/shared/*`
   - Standard library packages
   - External dependencies

2. **panel** can import:
   - `libs/shared/*`
   - `apps/panel/*` (internal)
   - Standard library packages
   - External dependencies

3. **shared** can import:
   - Standard library packages
   - External dependencies
   - **NOT** any app domain code

### Forbidden Imports

- `apps/collector/*` → `apps/panel/*` (circular dependency)
- `libs/shared/*` → `apps/*/internal/*` (shared must be generic)
- Root-level business code imports (use domain paths)

## Boundary Enforcement

Use the boundary check script to validate imports:

```bash
./scripts/check_boundaries.sh
```

This script fails if any forbidden imports are detected.

## Adding New Code

When adding new functionality:

1. Determine which domain owns the feature
2. Place code in the appropriate `/apps/<domain>/internal/` folder
3. Only extract to `libs/shared` if it's truly generic (no business logic)
4. Run boundary checks before committing
5. Update this document if ownership changes

## Migration Notes

This architecture was established during the refactoring tracked in `tasks/todo.md`. The goal was to enforce strict domain boundaries and keep the repository root minimal.
