# Tiered Provider Chain: equal-trust groups, and completing no-candidate chain-advance

Status: accepted; supersedes docs/adr/0005-suspension-waits-fallback-on-no-candidate.md

The flat, strictly-ordered Provider Chain (`chain: [opensubtitles, subdl]`) is
widened into an ordered sequence of **Tiers**, each holding one or more
Providers the operator trusts equally
(`chain: [[opensubtitles, subdl], [addic7ed]]`). This reconciles two problems
at once:

1. Issue #65 shipped Suspension-driven fallback across the *entire* flat
   chain (skip a Suspended Provider #1 straight to #2), directly
   contradicting ADR 0005's decision that Suspension should never advance
   the chain, specifically to avoid silently trading match quality for
   latency. Tiers restore that original intent, scoped correctly this time:
   Suspension may skip to a Tier-mate (an equally-trusted substitute, no
   quality cost) but never across a Tier boundary. Every Provider in the
   current Tier being Suspended leaves the pair waiting on that Tier, same
   as ADR 0005 always intended for the whole chain.
2. Issue #65 also explicitly deferred the no-candidate chain-advance that
   CONTEXT.md and ADR 0005 both describe ("`FailureNoCandidate` keeps its
   current terminal-on-first-miss behavior") — a pair never actually got a
   second Provider's attempt. This now ships: a pair remembers which
   Providers it has already tried and missed (a new persisted column,
   following the no-migration/bootstrap-only precedent of ADR 0002 —
   upgrading past this release requires deleting the state store, same
   accepted cost as before), and a no-candidate miss resets the pair to
   Pending — mirroring how `QuotaExhaustedError` already avoids marking
   Failed — rather than landing on Failed immediately. Only a miss against
   the last Provider of the last Tier lands on `Failed(no_candidate)`.

Within a Tier, list order is a soft preference, not load-balancing: the
first healthy Provider is always preferred; a Tier-mate is used only when
the preferred one is Suspended or has already missed.

**Considered options:**

- Load-balance across Tier-mates even when neither is Suspended. Rejected:
  the motivating scenario is quota redundancy, not request-volume
  spreading, and it reopens issue #65's deferred worker-capacity-split
  question for no benefit here.
- Express ordering as a `priority` number on each Provider's own config
  block instead of nesting the `chain` list. Rejected: the nested list is a
  smaller diff from today's schema and keeps one field as the single
  source of truth for both "is this Provider active" and "where does it
  rank" — a numeric priority would need a separate opt-out mechanism for
  "configured but not chained."
- Try the whole remaining walk synchronously within one dispatch attempt,
  needing no persisted per-pair state. Rejected: breaks the existing
  per-Provider worker-pool isolation (each Provider's own Pipeline/capacity
  budget), and a Suspended Tier-mate's wait can span far longer than one
  process lifetime.
