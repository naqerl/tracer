# DB Write Queue Refactor

## Goal
Replace `MaxOpenConns(1)` with a proper write queue + read-only proxy for each DB handle, so that WAL mode actually enables concurrent reads while serializing writes explicitly.

## Status — ✅ Complete

All core changes implemented and tested. All existing tests pass with `-race`.

## Implementation Summary

### Phase 1 — Core types in `dbmanager/`

- [x] `readonly.go` — `readOnlyDB` struct wrapping `*sql.DB`, implementing `db.DBTX`, returning `ErrReadOnly` from `ExecContext`
- [x] `handle.go` — `DBHandle` struct with:
  - `Read()` → returns `readOnlyDB`
  - `Write(ctx, fn)` → queues callback, executes in a transaction on a dedicated worker goroutine
  - Deadlock guard (detect nested `Write()` on same handle via worker goroutine ID)
- [x] `dbmanager.go` — Changes:
  - `GetAppDB`, `CreateAppDB`, `GetCollectorDB`, `CreateCollectorDB` return `*DBHandle` instead of `*sql.DB`
  - `openSQLiteDB` uses `MaxOpenConns(4)` + `MaxIdleConns(4)` to benefit from WAL
  - Cache maps store `*DBHandle` instead of `*sql.DB`

### Phase 2 — Update service layer callers

- [x] `ingest/service.go` — All write operations use `handle.Write()` with single transaction
- [x] `panel/service.go` — `queriesForApp()` uses `handle.Read()`; `DeleteLogGroup` uses `handle.Write()`
- [x] `retention/retention.go` — Use `handle.Write()` for all deletions in a single transaction

### Phase 3 — Tests

- [x] `handle_test.go` — Write serialization, error propagation, context cancellation, deadlock guard, read-during-write
- [x] `readonly_test.go` — ReadOnlyDB rejects Exec, allows Query/QueryRow/Prepare
- [x] `concurrency_test.go` — Concurrent reads + writes with `-race` (shared in-memory SQLite)

### Phase 4 — Verify

- [x] `make vet` passes
- [x] `make test` passes (with `-race`)
