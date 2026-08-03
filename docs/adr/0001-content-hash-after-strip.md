# Content Hash is captured after Strip's embedded-stream removal, not before

Content Hash used to be computed once, at the top of `processFile`, before
`Stripper.StripEmbedded` ran. Since `StripEmbedded`'s ffmpeg remux genuinely
changes a video's bytes whenever it removes an embedded subtitle stream, the
hash bound into that sync's Marker and state-store record went stale the
moment stripping happened — every following scan (e.g. after a restart)
misread Sublime's own edit as an external content change and needlessly
reprocessed the file. `StripEmbedded` is now called on its own, before the
Content Hash used for the Marker and the store is computed, so both reflect
the file's settled post-Strip state.

A Content Hash can now change for two distinct reasons: an external content
change (reset the file's sync state, as before) or Sublime's own Strip
mutation (don't reset it). `Store.UpsertFile` is renamed to
`ObserveFileContentHash`, and a new `Store.UpdateContentHash` skips the
reset — so the two intents are distinguishable by name, not just by reading
each call site's surrounding context.

**Considered options:** Reconciling the hash and rewriting the Marker
*after* `Swap` completes — smaller diff, no `Stripper` interface change —
was rejected because it leaves a real window where the sidecar briefly
carries a stale hash, and `Swap`'s foreign-sidecar trust check would use the
pre-strip hash, correct only by coincidence (a foreign sidecar never
carries a Marker, so it doesn't matter today, but it's not a real
guarantee).
