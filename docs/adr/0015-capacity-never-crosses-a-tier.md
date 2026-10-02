# Worker capacity never crosses a Tier boundary

Status: accepted; extends docs/adr/0008-tiered-provider-chain.md

ADR 0008 made Suspension tier-scoped: a Tier-mate may substitute for a
Suspended Provider, but a pair never descends to the next Tier. Worker
capacity was left out of that rule: when every Provider in a pair's current
Tier was at capacity, the dispatcher moved on to the next Tier. For fast
online Providers that was tolerable overflow, but for a slow, serial Provider
such as whisper it quietly sent the rest of the backlog to a lower Tier while
one file transcribed for hours, contradicting the rule that chain order
expresses operator preference.

Capacity now behaves like Suspension. A pair's *current Tier* is the first
Tier holding a Provider that has not already missed for the pair. Within it, a
Tier-mate that is not Suspended and has a free worker may be used; if none
qualifies (all Suspended, all at capacity, or a mix), the pair stays Pending
and is retried on a later poll. Tiers whose Providers have all already missed
are skipped, as before.

**Behavior change:** a chain such as `[opensubtitles, subdl]` (two separate
Tiers) no longer spills to SubDL while OpenSubtitles' workers are busy.
Operators who want overflow between Providers put them in one Tier:
`[[opensubtitles, subdl]]`.

**Considered options:**

- Keep overflow across Tiers for Providers that declare themselves fast.
  Rejected: it adds a per-Provider speed attribute and a hidden ranking, when
  chain position is meant to be the only priority signal (ADR 0014).
- Make crossing a Tier on capacity configurable. Rejected: no use case
  justifies a second mode; Tier membership already expresses it.

Only the Tier-grouped dispatch mode is affected; the legacy flat `Providers`
chain is unchanged.
