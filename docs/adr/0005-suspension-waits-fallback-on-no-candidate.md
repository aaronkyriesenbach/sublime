# Provider Suspension gates dispatch; only a no-candidate result advances the Provider Chain

Status: superseded by docs/adr/0008-tiered-provider-chain.md — this rule turned
out to describe intent that issue #65 shipped a contradiction of (Suspension
was allowed to skip the entire flat chain, not just wait); ADR 0008
reconciles the two by scoping this document's rule to a Tier boundary
instead of the whole chain.

A (file, language) pair may be attempted against an ordered Provider Chain
(e.g., highest subtitle quality first). Provider Suspension and Provider
Chain fallback are kept as independent, non-overlapping triggers: a
Suspended Provider only ever causes the Dispatcher to leave the pair waiting
on that same Provider — Suspension never advances the chain. The only thing
that advances a pair to the next Provider is its current Provider completing
a real Search and finding no Candidate clearing the scoring cutoff. This
preserves an operator's ability to pin a pair to a higher-quality Provider
even while it's temporarily Suspended, rather than silently downgrading to a
lower-quality Provider just because the preferred one is rate-limited.
Failed's `no_candidate` reason is reached only once every Provider in the
chain has been exhausted this way, not on the first Provider's miss.

**Considered options:**

- Treat Suspension as a fallback trigger too (skip a Suspended Provider and
  try the next one immediately). Rejected: conflates "temporarily
  unavailable" with "won't find a good match," silently trading match
  quality for latency without the operator asking for that trade.
- A configurable timeout escape hatch ("wait for Provider N for at most X
  hours, then fall back anyway"). Deferred, not rejected outright: it's a
  separate, speculative timing policy layered on top of a rule that hasn't
  shipped yet; today's problem is eliminating needless Pending/In Progress
  churn, not minimizing latency-to-sync.
- Persist each pair's current position in its Provider Chain now, as a new
  column. Deferred: there's exactly one real Provider implementation today,
  so the column would be inert until a second Provider exists, and
  `file_language_states`' bootstrap-only schema (see ADR 0002) means adding
  it forces another full-rescan for existing deployments — a cost worth
  paying once, when Provider #2 actually ships, not twice.
