# Proposal: cap log growth at ingestion

The maintenance pass reclaims space a database already wasted. It does not stop
the same data from being written again, so a chatty service can refill the file
between passes. Two write-side caps would remove most of that growth:

- Coalesce an identical repeated line into the existing log row (bump its count
  and last-seen time) instead of appending a new row for every occurrence.
- Do not insert a health-check row whose result equals the previous result;
  extend the open health period instead.

Both changes alter what ingestion stores and how counters are derived, so they
need their own schema/query work and alerting review. They are deliberately not
part of the maintenance pass.

## Measurement note

The live 3.4 GB database cited in the maintenance request was not reachable from
the development environment where this pass was built, so its before/after
numbers were not re-measured here. The pass logs `bytes_before`, `bytes_after`,
`wal_before`, `wal_after` and `reclaimed_bytes` for every app database, which is
the source of truth on the host.
