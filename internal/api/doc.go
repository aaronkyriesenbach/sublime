// Package api implements Sublime's local HTTP API: the daemon-side half of
// the CLI/HTTP surface an operator uses to check health, list Libraries,
// inspect sync status, and force a reprocess without touching logs or the
// state store directly.
//
// Route shape is flat and noun-named (RPC-ish, not REST resource nesting):
//
//   - GET  /health     — bare liveness only, no dependency checks.
//   - GET  /libraries  — configured Libraries with aggregate sync counts.
//   - GET  /status     — query-param scoped (?library=, ?path=), paginated,
//     default-filtered to pending/failed files.
//   - POST /reprocess  — always asynchronous: enqueues a forced reprocess
//     and returns 202 immediately; poll /status for the outcome.
//
// Every non-2xx response uses the same JSON error envelope (see
// writeError). Config is the source of truth for which Libraries exist —
// the state store only knows about files it has already seen — so
// Server.libraries (supplied at construction from config) gates which
// library/path scopes are valid, and the store's own counts fill in the
// numbers.
package api
