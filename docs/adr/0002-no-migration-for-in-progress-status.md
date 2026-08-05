# No migration for the In Progress Sync Status; upgrading drops state

Sublime's SQLite schema is bootstrap-only (`store.go`'s `bootstrapSQL`,
applied via `CREATE TABLE IF NOT EXISTS`), with no migration framework, by
explicit design. Adding the new In Progress Sync Status requires widening
`file_language_states.status`'s CHECK constraint from
`('pending', 'synced', 'failed')` to include `'in_progress'` — but
`CREATE TABLE IF NOT EXISTS` is a no-op against an already-bootstrapped
database, so an existing deployment's `/data/sublime.db` keeps its old,
stricter constraint forever and rejects the new status value outright once
the new binary runs against it.

We're treating this as an accepted breaking change rather than writing a
migration: upgrading to a release that introduces In Progress requires
deleting `/data/sublime.db` and letting Sublime do a full rescan. A full
rescan is cheap to reason about (bounded by Library size and Provider rate
limits, no risk of partial or corrupt state) and consistent with the
project's existing "no migration framework, schema changes out of scope for
v1" stance — this decision just makes that stance concrete for a specific
change instead of leaving it implicit.

**Considered options:**

- Write a one-off migration (detect the old CHECK constraint, rebuild
  `file_language_states`, copy data across). Rejected: contradicts the
  project's stated no-migration stance, and would be the first migration of
  its kind, setting a precedent v1 deliberately avoids.
- Loosen the CHECK constraint to accept any TEXT value, pushing validation
  into `domain.SyncStatus` only. Rejected as a fix for *this* change:
  because of the same `CREATE TABLE IF NOT EXISTS` no-op behavior, this only
  helps schema changes *after* it ships — existing databases still carry the
  old, stricter constraint regardless.

**Consequences:** Operators upgrading past this release must delete their
state store and accept a full rescan; call this out prominently in release
notes.
