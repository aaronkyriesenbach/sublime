# Whisper-generated subtitles are a Provider, ordered by operator preference

Status: accepted

Online subtitle quality is inconsistent, so Sublime gains a second kind of
subtitle source: transcribing the video's own audio with Whisper (a
**Generated Subtitle**, see CONTEXT.md). It is modeled as a **Provider**, not
a parallel concept, and sits in the existing Provider Chain on equal footing
with online Providers — `chain: [whisper]`, `[whisper, opensubtitles]`,
`[opensubtitles, whisper]`, and `[[whisper, opensubtitles]]` are all valid,
and order alone expresses the operator's preference. There is deliberately no
separate "whisper mode"; "online only" and "whisper only" are just chains.

The decisions that follow from that, each of which a future reader would
otherwise be tempted to "fix":

- **Generated Subtitles are never Synced.** Their cues are timed from the
  video's own audio, so alass has nothing to correct and would only burn time
  on a long file. This is a deliberate exception to "Sync is always
  performed" (CONTEXT.md, Sync).
- **Same-language only.** A Generated Subtitle is produced only when the
  audio is spoken in the requested language; Whisper's translate-to-English
  task is not used. A mismatch is an ordinary miss, so the pair advances to
  the next Provider in its chain (e.g. Japanese audio with target `en` falls
  through from `whisper` to an online Provider).
- **No new Failed reasons.** An exhausted chain still lands on
  `no_candidate`; the specific cause (audio language vs. target, degenerate
  transcript) goes in the log. Widening the `failure_reason` CHECK would force
  a state-store wipe under ADR 0002's no-migration stance, for little gain.
- **An unreachable Whisper service Suspends the Provider** (cause:
  Unavailable) instead of failing pairs with `retrieval_failed`, extending
  ADR 0003. A restart of the sidecar therefore leaves pairs Pending, and
  Tier semantics (ADR 0008) decide whether a Tier-mate may substitute.
- **Changing the chain never touches existing subtitles.** Marker-valid
  subtitles stay Synced whichever Provider produced them, and the Marker
  records no provenance. Neither source is an "upgrade" of the other; an
  operator who changes their mind runs `sublime reprocess`.
- **Transcription runs in an external `whisper.cpp` server (sidecar).** The
  Sublime image stays Alpine and CGO-free. Audio is extracted with ffmpeg and
  sent in ~10-minute chunks cut at silence, each retried and sanity-checked
  independently; chunk results are held in memory only, so an interrupted
  run restarts from the beginning.

**Considered options:**

- A separate "Generator" concept beside Provider. Rejected: every seam
  (pipeline, dispatcher, per-Provider worker pools, Suspension, `/status`,
  config) is keyed on Provider, and a parallel concept would duplicate all of
  it.
- Run alass on Generated Subtitles anyway as a safety net. Rejected: it
  cannot fix per-cue early/late appearance, which is Whisper's known timing
  quirk.
- Translate to English when audio is in another language. Rejected for v1:
  Whisper's translation is weaker than its transcription, and an online
  Provider later in the chain is the better source for that case.
- Automatically reprocess when the chain changes, or record a `generated`
  field in the Marker so one source can be bulk-replaced. Rejected: neither
  source is better, so there's nothing to upgrade, and silently re-running a
  library burns OpenSubtitles quota or days of transcription.
- Bundle Whisper into the Sublime image. Rejected: it would bloat the image,
  tie it to CPU, and prevent users from swapping in a GPU-enabled sidecar.
- Transcribe each file in one request. Rejected: whisper.cpp has documented
  hallucination loops lasting tens of minutes on long inputs, a failure
  confined to one chunk when chunked, and per-chunk retry and progress logging
  come for free.

**Consequences:**

- whisper-server serializes inference behind a global mutex, so `worker_count`
  for this Provider should stay 1 per sidecar.
- Hour-long jobs make the existing gaps around stale `In Progress` pairs,
  shutdown, and Changed-while-in-flight (the `TODO(race)` in
  `internal/store/files.go`) real; fixing them is a prerequisite, tracked
  separately.
