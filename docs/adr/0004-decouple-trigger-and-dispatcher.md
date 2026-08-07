# Decouple file discovery (Trigger) from subtitle-fetch dispatch (Dispatcher)

Trigger (initial scan, live fsnotify watch, manual reprocess) previously ran
registration and dispatch in the same blocking call
(`pipeline.Pipeline.Run`/`RunFile`), so a Provider-Suspended backlog forced
every Pending (file, language) pair to attempt real work immediately: mark
In Progress, get rejected, reset to Pending, repeated across the entire
backlog on every restart. Sublime now splits these into two independent
goroutines inside the single daemon process, communicating only through the
state store (Trigger writes Pending rows; Dispatcher reads/claims them) —
never a direct in-process handoff. Dispatch is centralized in one loop that
walks each pair's Provider Chain and claims/hands off to that Provider's own
worker pool, rather than one independent dispatch loop per Provider racing
the store for the same rows; the loop wakes on a fixed polling interval
rather than an event-driven signal from Trigger. `Run`/`RunFile`/`Reprocess`
now return only a registration-facing summary (files scanned/found/changed)
— dispatch outcomes (Synced/Failed/etc.) are no longer collected
synchronously and are observable only via `GET /status`'s live query and the
existing per-transition structured logs.

**Considered options:**

- N independent per-Provider dispatch loops, each polling the full Pending
  set and racing to claim rows (resolved purely by an atomic claim in the
  store). Rejected: priority order across Providers becomes emergent rather
  than guaranteed — a lower-priority, available Provider can win a claim a
  higher-priority Provider would have preferred, purely because it polled
  first.
- Event-driven wakeup: Trigger pushes an in-process signal on every
  registration, plus a timer armed for each Suspended Provider's
  `resumeAt`. Rejected for now: two new wake sources to keep correct, and a
  periodic fallback poll would still be needed as a safety net for missed
  signals after a crash-restart — so it doesn't actually remove the polling
  loop, just adds complexity on top of it. Suspension windows last hours and
  dispatch throughput is bounded by `WorkerCount` regardless, so the latency
  cost of polling-only is negligible.
- Keep Trigger and Dispatcher as literally separate processes/deployables.
  Rejected: the current deployment is a single container running one binary
  with one shared `Pipeline`/`Provider`; nothing motivates the added
  deploy/IPC surface.

**Consequences:** Centralizing dispatch also fixes a latent bug: today, each
Library's independent `Watcher` goroutine calls `Run` with its own fresh
semaphore, so real concurrency across N Libraries was `N × WorkerCount`, not
the single `WorkerCount` budget the field's doc comment implies.
`MarkInProgress` needed an atomic conditional claim (`WHERE status =
'pending'`, surfaced as `ErrNotPending`) to stay correct now that more than
one path can look at the same Pending row.
