# Provider Suspension on quota exhaustion, not per-file failure

OpenSubtitles reports download-quota exhaustion inconsistently — a 401 with a
`reset_time_utc` field in some cases, a 406 with the reset time only embedded
in free-text `message` in others. Rather than treating this as a per-file
retrieval failure (which would permanently mark affected files Failed, per
Failed's "not retried until Changed" contract), Sublime models it as the
Provider entering a time-bounded **Suspended** state: a generic
`provider.QuotaExhaustedError` (carrying a best-effort parsed `ResumeAt`,
falling back to a fixed 1h cooldown when parsing fails) propagates out of the
concrete Provider, and `pipeline.go` leaves any (file, language) pair
Pending rather than marking it Failed while its Provider is Suspended. The
Provider itself short-circuits further Search/Download calls once
Suspended, avoiding wasted requests. Suspension is scoped per-Provider-
instance and kept in-memory only (not persisted across process restarts) —
a restart mid-cooldown costs at most one wasted call that immediately
re-establishes the Suspended state.
