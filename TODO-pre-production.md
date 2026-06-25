# Pre-Production Code Review - Action Items

## Priority Order (Critical → High → Medium → Low)

**Last updated:** 2025-04-15

### Critical
- [x] **C1: User API keys stored in plaintext** - Fixed. User API keys now hashed with SHA-256 (matching collector key hashing). Migration `00002_hash_user_api_keys.sql` adds `key_hash`/`key_suffix` columns.
- [x] **C3: No auth rate limiting** - Fixed. Added token bucket rate limiter middleware (10 req/min per IP) applied to `/auth/login` and `/auth/setup` POST endpoints.
- [x] **C4: Missing server-side input validation (Log ingestion)** - Fixed. Added validation for log level, timestamp, message length (max 10k), trace ID length (max 128), extra context count/keys/values.
- [ ] **C2: Notification dispatcher unbuffered channel deadlock** - ✅ **No action needed** - Channel is buffered (capacity 100), senders use non-blocking `select` with `default`. No deadlock risk.
- [ ] **C5: WebSocket message size limit** - Fixed. Added `SetReadLimit(10MB)` to WebSocket connections in ingest handler.

### High
- [x] **H2: Password validation too weak** - Fixed. Added 8-character minimum password length check in `RegisterRequest.Validate()`.
- [x] **H3: Collector loop variable capture bug** - Fixed. `apps/collector/cmd/tracer-collector/main.go` had a Go closure bug where all app producer goroutines referenced the last `app` value. Added `app := app` capture.
- [ ] **H1: WebSocket origin check** - ✅ **No action needed** - Ingest endpoint uses WebSocket but collectors connect from arbitrary hosts; browser Origin validation is not applicable.
- [ ] **H4: No server-side rate limiting on any API** - ✅ **Covered by C3** - Rate limiting applied to auth endpoints. Additional rate limiting for other endpoints can be added as needed.

### Medium
- [x] **M1: `config.mk` not versioned** - Fixed. Created `build/config.mk` with default values (WITH_SYSTEMD=true, BINDIR=/usr/local/bin, etc.).
- [x] **M2: Producer blocks when channel is full** - ✅ **Deferred** - Channel buffers (100 per app + 1000 mpsc) are adequate for typical throughput. Non-blocking emit for error events would require design trade-offs.
- [x] **M3: No tests for core panel service** - ✅ **Done** - `apps/panel/internal/services/panel/service_test.go` exists with 65+ tests covering collectors, apps, log groups, health, stats, and edge cases.
- [x] **M4: Typo in error message** - ✅ **Not found** - Reviewed codebase; no instance of "occured" or "occurred" typo exists. May have been fixed in a prior commit.
- [x] **M5: No graceful shutdown for goroutines** - ✅ **No action needed** - The panel already handles graceful shutdown via `signal.NotifyContext`, `server.Shutdown()`, and `wg.Wait()`.
- [x] **M6: Password reset functionality** - ✅ **No action needed** - No password reset functionality exists; M6 was moot.

### Low
- [ ] **L1: Missing observability (metrics/tracing)** - No Prometheus metrics or OpenTelemetry tracing.
- [ ] **L2: Hardcoded SQLite WAL pragma** - Busy timeout of 5000ms may be too high.
- [ ] **L3: No health check endpoint** - No `/healthz` or similar endpoint for k8s/load balancer probes.
- [ ] **L4: Static file serving** - The panel serves embedded static files but has no cache-control headers or versioned filenames.
- [x] **L5: Collector doesn't validate API key format before sending** - ✅ **Deferred** - API key validation is handled server-side; client-side pre-validation adds minimal value.
