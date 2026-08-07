# SubDL Provider Suspension only recognizes documented quota-exhaustion shapes

SubDL has three distinct quota surfaces: free search (documented as `429`
with `{"error":"quota_exceeded"}` plus an `X-RateLimit-Reset` header),
authenticated/paid download (assumed to share that same shape, since it's
the same key-based auth path), and anonymous per-IP download (300/day, with
no documented response shape at all for what a rejection looks like — it
isn't part of the key-based flow SubDL documents quota errors for).

Sublime's SubDL Provider only turns the one *documented* shape —
`429` + `{"error":"quota_exceeded"}`, from either search or an authenticated
download — into a `provider.QuotaExhaustedError`. `ResumeAt` is parsed from
`X-RateLimit-Reset` when present; when it isn't, the fallback is the next UTC
midnight, not a flat duration, since SubDL's quotas are documented as
calendar-day resets rather than OpenSubtitles' messier cadence. The anonymous
download path is not treated as quota-exhaustion-detectable at all in v1:
whatever it returns when the 300/day/IP cap is hit surfaces as an ordinary
per-`(file, language)` `Download` failure, not a Provider Suspension.

This mirrors OpenSubtitles' own existing precedent
(`opensubtitles/client.go`'s `quotaExhaustion`): only a positively-identified,
documented response shape earns Suspension; an unrecognized response is just
an error, never a guess. The accepted cost is that hitting the anonymous
download cap will look like scattered `Failed` files rather than a clean
Suspended pause, until its real shape is observed and handled explicitly —
the same way OpenSubtitles' 406 case was itself added later, from a
real-world discovery, not speculated upfront.
