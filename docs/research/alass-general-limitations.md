# alass general limitations and algorithm research

**Scope**: background research into `alass` (github.com/kaegi/alass), the CLI
Sublime shells out to for subtitle sync, done in support of the sync-reliability
investigation tracked in issues #86/#87/#88. This document does **not**
re-litigate the three already-established failure categories (fps-guessing
misfire, different-cut candidate, VAD-hard content) — see
`sync-reliability-investigation-handoff.md` and the linked issues for those.
It also does not cover alass's fps-guessing algorithm in the depth that a
parallel research task is already covering; fps-guessing is mentioned here
only where it intersects with other findings.

**Method**: read alass's actual source (cloned locally, commit
`874f02d9577182752a0f969b6d6b98fd65bdf1fc`, the tip of `master`/tag `v2.0.0`
plus one post-release commit), its README, its GitHub issue tracker (via the
GitHub REST API), and the source of adjacent/competing tools (`ffsubsync`,
`lapse`) and of a real production wrapper (`AutoSubSync`) to see how the wider
ecosystem handles alass's rough edges. File+line citations use permalinks to
that exact commit so they stay valid even if `master` moves. This is the first
file under `docs/research/` in this repo — establishing that as the home for
this kind of investigation note going forward.

**Meta-finding up front**: alass is effectively unmaintained. Its last GitHub
commit is from **2021-04-11** and its last tagged release is **v2.0.0**
(2019-10-10) — confirmed directly from the repo's own commit/release history
(<https://github.com/kaegi/alass/commits/master>,
<https://github.com/kaegi/alass/releases>). Nothing found in this research
suggests any of the issues below are being actively worked on upstream.

---

## 1. How alass's core alignment algorithm actually works

### Two independent pieces: "make two timespan lists", then "align two timespan lists"

alass's core crate (`alass-core`) knows nothing about video, audio, or
subtitles — it has one job: given a `reference` list and an `incorrect` list
of `TimeSpan`s (start/end pairs), find the delta(s) that best align
`incorrect` onto `reference`
([alass-core/src/lib.rs](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-core/src/lib.rs#L28-L32)).
`alass-cli` is responsible for turning a video file and a subtitle file into
two such lists.

- **Subtitle → timespans**: trivially, each cue's start/end
  ([alass-cli/src/lib.rs#L215-L240](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/lib.rs#L215-L240)).
- **Video → timespans ("the VAD approach")**: alass does **its own** audio
  analysis; it does not rely on any external speech/ASR signal. It shells out
  to `ffmpeg` to extract the audio track as 8kHz mono 16-bit PCM
  ([alass-cli/src/video_decoder/ffmpeg_binary.rs#L232-L253](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/video_decoder/ffmpeg_binary.rs#L232-L253)),
  then feeds consecutive 10ms/80-sample frames into Google's **WebRTC VAD**
  (via the `webrtc-vad` crate, itself maintained by alass's author) to get a
  per-frame speech/non-speech boolean
  ([alass-cli/src/lib.rs#L246-L266](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/lib.rs#L246-L266)).
  Consecutive "voice" frames are merged into "voice segments", which become
  the reference timespan list
  ([alass-cli/src/lib.rs#L268-L307](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/lib.rs#L268-L307)),
  filtered to a minimum length of 500ms
  ([alass-cli/src/main.rs#L263-L271](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/main.rs#L263-L271)).

  **The WebRTC VAD aggressiveness mode is hardcoded to `Quality`** — the
  *least* aggressive of WebRTC VAD's four modes (`Quality`, `LowBitrate`,
  `Aggressive`, `VeryAggressive`), i.e. the mode most likely to classify noisy
  audio as speech
  (`Vad::new_with_rate` →
  [`VadMode::Quality`](https://github.com/kaegi/webrtc-vad/blob/master/src/lib.rs#L48-L57)).
  There is **no CLI flag to change this**. For content where non-speech
  audio (music, sound effects, crowd noise) tends to get misclassified as
  voice — which is exactly the "VAD-hard content" category already
  identified for anime — this is a real, structural, un-tunable limitation
  in the shipped binary; the only way around it is patching/recompiling
  alass with a different VAD mode. Also worth noting: the segment-merge step
  literally computes `combine_with_distance_lower_than = 0 / 10` (integer
  division, always `0`)
  ([alass-cli/src/lib.rs#L303](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/lib.rs#L303)) —
  reading as a dead/never-wired-up "merge nearby voice segments within N
  frames" knob that always evaluates to "no merging tolerance": a single
  10ms non-voice frame is enough to split what should be one continuous
  utterance into two voice segments. This isn't necessarily a bug (it's
  consistent since v1), but it means the reference timeline can be far more
  finely fragmented than the subtitle's own cue boundaries, which the DP
  then has to reconcile.

- **Audio track selection is a silent footgun.** When no `--index` is given,
  alass automatically selects the audio stream with the **fewest channels**
  in the file:
  `audio_streams.min_by_key(|s| s.channels.unwrap())`
  ([alass-cli/src/video_decoder/ffmpeg_binary.rs#L207-L214](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/video_decoder/ffmpeg_binary.rs#L207-L214)).
  This has nothing to do with "default" track flags, language, or dialogue
  content — a 2.0 commentary track, a 2.0 foreign-dub track, or a mono
  descriptive-audio track will all be preferred over the main 5.1 track
  simply because they have fewer channels. This is exactly the bug reported
  and fixed-with-an-opt-in-flag (not a default-behavior fix) in
  [issue #14](https://github.com/kaegi/alass/issues/14): *"my reference
  video had multiple audio streams in different languages, and the
  alignment would be slightly different depending on the language I was
  aligning against."* The flag that resulted is `--index <ffprobe stream
  index>` (not `--audio-index` as the flag's *internal* name suggests — the
  actual CLI long-option, per
  [alass-cli/src/main.rs#L221-L227](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/main.rs#L221-L227),
  is `.long("index")`, so it's invoked as `--index N`, and `N` is the raw
  `ffprobe`-reported container stream index, confirmed in the issue
  thread). **This default heuristic is unchanged today** — if a video has
  multiple audio tracks, whichever one alass picks by "fewest channels" is
  used for VAD unless `--index` is passed explicitly.

### The DP formulation ("split mode")

The reference implementation is a dynamic program over subtitle lines,
walked left-to-right through the `incorrect` list, that at each line decides
between "keep the same offset as the previous line" (no split) or "jump to
whatever offset scores best from here on, minus a penalty" (split) —
classic piecewise-constant segmented regression
([alass-core/src/alass.rs#L318-L420, `align_with_splits`](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-core/src/alass.rs#L318-L420)).
Concretely, for each subtitle line the algorithm maintains a piecewise-linear
"cumulative rating as a function of offset" curve; at each step it computes
two candidate curves — `nosplit_offsets` (extend the previous best offset
unchanged) and `best_split_offsets` (the left-to-right running maximum of the
*previous* curve, with `split_penalty` subtracted, i.e. "the best offset seen
so far pays a fixed cost to switch to") — and takes the pointwise maximum of
the two, then adds this line's own single-span rating contribution
(`single_span_ratings`). This is why the CLI's `--split-penalty` doc string
("1000 means all lines share one offset, 0.01 produces MANY segments") is
literally true: `split_penalty` is denormalized into a `RatingDelta`
(`denormalize_split_penalty`,
[alass-core/src/lib.rs#L52-L54](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-core/src/lib.rs#L52-L54))
and directly subtracted from the "switch to a new offset" branch's rating
every time a split is considered
([alass-core/src/alass.rs#L367-L376](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-core/src/alass.rs#L367-L376)) —
it is the fixed cost, in the same rating units as alignment quality, of
introducing one more break in the piecewise-constant offset function. Once
the whole file is processed, the best final offset is picked and then walked
*backwards* through per-line "offset buffers" to recover the actual delta for
every original line
([alass-core/src/alass.rs#L389-L417](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-core/src/alass.rs#L389-L417)).

Rating itself is fixed-point (`i64` with a 2^32 scaling factor,
[alass-core/src/rating_type.rs#L133-L136](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-core/src/rating_type.rs#L133-L136)),
so there is no floating-point rounding drift in the exact-search path — this
matters for section 2 below.

### `--speed-optimization` is a lossy curve-simplification whose tolerance *grows through the file*

This is the single most consequential thing found in this research and is a
strong, concretely-sourced candidate explanation for the "unexplained
residual drift" from the *Sunny* S11E08 debugging.

`--speed-optimization` is **on by default** (default value `1`; only `0`/
omitting a positive value disables it —
[alass-cli/src/main.rs#L197-L203](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/main.rs#L197-L203),
[alass-cli/src/main.rs#L249-L253](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/main.rs#L249-L253)).
When enabled, after processing each subtitle line the DP's cumulative rating
curve is passed through `aggressive_simplifiy_ratings_push_iter`, a
Douglas-Peucker-style lossy simplification that merges adjacent curve
segments as long as the approximation error stays within a tolerance
`epsilon`
([alass-core/src/segments.rs#L1751-L1873, `AggressiveSimplifyRatingPushIterator`](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-core/src/segments.rs#L1751-L1873)).
This is exactly what the function docs call "sacrificing some accuracy" to
keep the buffer size bounded (without it, buffer size can grow unboundedly
long for long, highly-fragmented reference/incorrect lists) — it's not a
sampling shortcut, it's a genuine bound-the-error-and-throw-away-precision
step applied incrementally, line by line, through the whole file.

The tolerance is computed per line as:

```rust
let progress_factor = (line_nr + 1) as f64 / in_spans.len() as f64;
let epsilon = Rating::convert_from_f64(speed_optimization * 0.05 * (progress_factor * 0.8 + 0.2));
```

([alass-core/src/alass.rs#L430](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-core/src/alass.rs#L430))

`progress_factor` runs from ~0 near the start of the subtitle to `1.0` at the
last line. With the default `speed_optimization = 1`, this makes `epsilon`
**five times larger at the end of the file than at the beginning** (≈0.01 at
the first line vs. 0.05 at the last). In other words: *the amount of
approximation error alass is willing to tolerate in its own internal
alignment-quality bookkeeping is deliberately and monotonically increased as
it works its way through the subtitle file.* Whether that increasing
tolerance ever actually shows up as a *positional* (millisecond) error
depends on how flat/steep the rating curve is at each point (the same rating
tolerance corresponds to a wider time-window wherever the curve is shallow),
but the mechanism is structurally exactly the shape needed to produce a
monotonically-growing segment-to-segment drift over the course of a long
file, which is what was observed on *Sunny* S11E08 (24s → 47s → 83s → 187s)
— growing, not oscillating, not a one-time jump. This was **not** tested
against that file in this research session (no reproduction available); it
is a mechanistic hypothesis grounded directly in the DP implementation, not
a confirmed cause. See the "actionable leads" section for how to test it
directly (`--speed-optimization 0`).

Separately, the CLI docstring for `--speed-optimization` claims accuracy
"greatly degrades" only after values above `10`, and recommends `~3` for a
speed/accuracy tradeoff
([alass-core/src/lib.rs#L118-L122](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-core/src/lib.rs#L118-L122)) —
i.e. even the author's own guidance implies the *default* value of `1` was
chosen as a "good enough, still fast" compromise, not as "no meaningfully
observable accuracy cost." No upstream benchmark isolating `speed-optimization`
specifically was found (alass's issue tracker and README performance numbers
don't break this out) — the only accuracy-vs-speed data available on alass as
a whole comes from a third party (`lapse`'s benchmark, see §5).

---

## 2. What could explain the growing/monotonic segment-to-segment drift

Two source-grounded, non-fps-guessing candidate mechanisms, both consistent
with "disabling fps-guessing didn't fix it":

1. **The `--speed-optimization` growing-epsilon approximation** described in
   detail above (§1). Directly testable with `--speed-optimization 0` (falls
   back to the exact/non-approximated search path — confirmed by
   `speed_optimization_opt.unwrap_or(0.0)` in
   [alass-core/src/alass.rs#L349](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-core/src/alass.rs#L349),
   which is the value that becomes the `epsilon` multiplier).

2. **A real framerate ratio outside the six the auto-guesser tries, that
   survives even with `--disable-fps-guessing` off *and* on.** alass's
   fps-guessing only ever tests six fixed ratios, built from exactly three
   reference rates — 25, 24, and 23.976 fps:

   ```rust
   let a = 25.; let b = 24.; let c = 23.976;
   let ratios = [a / b, a / c, b / a, b / c, c / a, c / b];
   ```

   ([alass-cli/src/main.rs#L368-L372](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/main.rs#L368-L372)).
   These cover PAL⇄film-rate conversions only. **NTSC rates (29.97/30fps) are
   never considered at all**, guessed or not. *Sunny* is an American sitcom
   plausibly sourced from a broadcast/NTSC pipeline at some point in its
   distribution chain; if the true ratio between the reference video's audio
   timeline and the candidate subtitle's timeline is something like
   `29.97/23.976` (≈1.25) or a similar NTSC-adjacent ratio, `--disable-fps-guessing`
   does nothing to fix or reveal this (it only turns off the *guessing step*,
   it doesn't add a way to specify an arbitrary ratio for
   `.srt`/`.ass` timestamps — `--sub-fps-ref`/`--sub-fps-inc` only affect
   MicroDVD `.sub` frame-number interpretation, per their own help text:
   *"Specifies the frames-per-second for the accompanying video of MicroDVD
   `.sub` files"*
   ([alass-cli/src/main.rs#L176-L185](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/main.rs#L176-L185)) —
   they are a no-op for `.srt` input). A genuine, uncorrected, small
   linear-rate mismatch between reference and incorrect timelines is exactly
   what produces an offset that *grows roughly proportionally to elapsed
   time* — matching the observed pattern far better than random noise would.
   This is also precisely the failure mode that a different tool, `lapse`,
   was built to specifically target (see §5): its README explicitly
   advertises "linear drift caused by framerate mismatches" as a named
   category it detects via linear regression, which alass's DP-with-fixed-guesses
   approach has no mechanism to discover if the true ratio isn't one of
   the six above (or a multiple of 1.0 exactly).

Neither of these is confirmed against the actual *Sunny* S11E08 file in this
research pass — both are concrete, source-grounded hypotheses to test next
(see final section).

Two more (unrelated to *Sunny*, but same general "not fps, not the anime
case" bucket) surfaced from alass's own issue tracker:

- [**Issue #34** — "Not work well with parts with no sound between
  scenes"](https://github.com/kaegi/alass/issues/34): reporter observed the
  alignment "fixes one part of the video, but not others" specifically
  around long silent/black-screen scene transitions. alass's author
  suggested using the debug mode (`alass video.mkv _ out.srt`, which dumps
  the raw detected voice-activity timespans as a subtitle file for manual
  inspection — a genuinely useful diagnostic not documented anywhere except
  this issue thread and [issue #7](https://github.com/kaegi/alass/issues/7))
  but the issue was never resolved and remains open with no root cause
  identified.
- [**Issue #50** — "No way to force preserve order"](https://github.com/kaegi/alass/issues/50):
  *"I've had a few cases where everything was within ±3s but a chunk was
  moved by a minute, not only being completely wrong, but also scrambling
  the order of subtitles."* This is functionally the same symptom already
  documented for *Death Note* (segment-to-segment shifts see-sawing by up to
  a minute) — confirming it's a recognized, unresolved, non-anime-specific
  DP weakness, not something unique to VAD-hard content. See
  [issue #43](https://github.com/kaegi/alass/issues/43) ("Parameter to limit
  max offset") for the companion open feature request — a maximum-plausible-offset
  clamp does not exist in alass today.

---

## 3. Known open issues/limitations beyond fps-guessing

Surveyed the full open+closed issue list (56 issues total), read the highest-comment
threads in full. Patterns found, grouped by theme:

**Audio track / channel handling**

- Default audio-stream selection picks the *fewest-channel* stream with no
  way to know this happened unless you already knew to check (§1). Flag:
  `--index N` (ffprobe stream index) to override — added in
  [issue #14](https://github.com/kaegi/alass/issues/14) but the *default*
  is unchanged.
- [**Issue #49** — "Sync by audio track?"](https://github.com/kaegi/alass/issues/49)
  and [**issue #54**](https://github.com/kaegi/alass/pull/54) (open PR,
  never merged, proposing ffmpeg center-channel (`pan=c0=FC`) extraction for
  5.1/7.1 audio "to contain all the dialogue with less background noise")
  both point at the same underlying gap: alass extracts and downmixes
  whatever channel layout the selected stream has
  (`-ac 1` mono downmix in
  [alass-cli/src/video_decoder/ffmpeg_binary.rs#L246-L249](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/video_decoder/ffmpeg_binary.rs#L246-L249)),
  with no way to isolate the dialogue-carrying center channel of a
  surround mix. The PR author explicitly notes this "slightly increases
  accuracy... downside is this may cause worse detection in some cases" — an
  unresolved, never-shipped tradeoff.

**"Signs and songs" / hard-of-hearing subtitle noise**

- [**Issue #33**](https://github.com/kaegi/alass/issues/33) and its
  discussion: subtitle files with lots of "♪♪～" lines, OP/ED song lines, or
  on-screen-text description lines (common in anime fansubs and
  hard-of-hearing tracks) throw off the global optimum even though,
  conceptually, alass's split mechanism *should* be able to treat them like
  a mini ad-break. Author's own words: *"the introduced noise by these extra
  subtitle lines unfortunately might sometimes be great enough to throw off
  the global optimum."* No fix landed; users report writing their own
  scripts to strip non-dialogue lines before running alass. Directly
  relevant to the *Death Note* category-3 finding — this is corroborating
  evidence from independent anime users hitting the same class of problem,
  not a one-off.

**Silence / scene-transition handling** — issue #34, covered in §2.

**Order-preservation / max-offset** — issues #50 and #43, covered in §2.

**No confidence signal, ever** — alass has no notion of "how sure am I".
Confirmed directly in code: the *only* thing alass prints that could be
read as a warning signal is the negative-timestamp check
(`corrected_timespans.iter().any(|ts| ts.start.is_negative())`,
[alass-cli/src/main.rs#L483](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/main.rs#L483)),
which fires only if the *specific arithmetic result* happens to go negative
— **not** a general "does this alignment look plausible" check. This is
exactly why the *Death Note* case produced no warning despite being
unusable: the wild segment-to-segment shifts there never happened to produce
a negative start timestamp, so the one and only built-in signal never
fired. This independently confirms issue #87's finding that alass's own
stdout is an insufficient detection signal — it's not a heuristic that
happens to miss some cases, it's structurally only checking for one
specific arithmetic condition, unrelated to alignment quality in general.
This is also stated plainly by a third party's benchmark of alass (see §5):
*"alass has no confidence measure, so nothing was reported"* even in a case
where alass's own output was badly wrong.

**Negative-timestamp handling is a per-cue clamp, not a block-preserving shift**
Confirmed directly in code — this is the mechanism behind the
"≥3 cues collapsed to `00:00:00,000`" corruption signature already found in
the repo-wide scan (already tracked in #86/#87, re-derived here from source
for completeness): when negative timestamps are *not* allowed (the default —
no `-n`), **every individual cue** whose corrected start is negative is
independently shifted so its own start becomes exactly `0`:

```rust
for corrected_timespan in &mut corrected_timespans {
    if corrected_timespan.start.is_negative() {
        let offset = subparse::timetypes::TimePoint::from_secs(0) - corrected_timespan.start;
        corrected_timespan.start = corrected_timespan.start + offset;
        corrected_timespan.end = corrected_timespan.end + offset;
    }
}
```

([alass-cli/src/main.rs#L494-L499](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/main.rs#L494-L499)).
This is *per-cue*, not "find the most-negative cue and shift the whole
block forward by that amount to preserve relative timing" — so any group of
cues with different negative offsets collapses to the same `0` start time,
destroying their relative order/spacing. This matches the handoff's own
note that `--allow-negative-timestamps` would need Sublime to implement its
own order-preserving forward shift on top, since alass's built-in handling
is not block-preserving.

**Format support gaps** — `.vtt`, `.sub` (MicroDVD, write), `.sup` (PGS),
`.smi`, `.ttml`/`.dfxp` are all unsupported (only `.srt`, `.ass`/`.ssa`,
`.idx` are handled — confirmed in
[alass-cli/src/lib.rs#L338](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/lib.rs#L338)
and cross-checked against `lapse`'s comparison table in §5). `.idx` is
read-only-as-reference (timing only, no text). Not likely relevant to
Sublime's current failures (which are all `.srt`) but worth knowing if
candidate formats ever expand.

**Non-ASCII paths** — not upstream-documented, but see §6:
`AutoSubSync` (a real-world wrapper) found that alass errors out
(`could not convert string to float`) on file paths containing `[` or `]`
characters and works around it by renaming paths before invoking alass. Not
an alignment-quality issue, but a real operational gotcha for anyone
building automation around the alass binary (worth a defensive check in
Sublime's own alass-invocation code if candidate/media paths can ever
contain brackets).

---

## 4. Untested flags — concrete hypotheses

| Flag | What it actually does (from source) | Which known category it plausibly helps | Confidence |
| --- | --- | --- | --- |
| `--allow-negative-timestamps` / `-n` | Skips the per-cue clamp-to-zero entirely; negative start times are written as-is to the output file ([main.rs#L483-L501](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/main.rs#L483-L501)). Does **not** fix the underlying misalignment — it just stops the destructive per-cue collapse from happening, replacing "corrupted-looking but exit-0" with "honestly negative, likely still wrong, and possibly unparseable by some players/`.srt` consumers." | Detection/mitigation for category 1 (fps-guessing misfire) and any other case that currently manifests as the collapse signature — **not** a fix for the misalignment itself. Would need Sublime-side post-processing (order-preserving forward shift) to be useful in production, exactly as #87 already concluded. | High (mechanism is fully understood from source) |
| `--split-penalty` / `-p` (default 7) | Directly, linearly controls the fixed rating-cost of introducing one more offset-change in the DP (§1). Higher = fewer, more conservative splits; lower = more, noisier splits. Author's own guidance in issue #7: *"Another thing you should try is `--split-penalty` for values like 1,2,5,10,20,30 or 50... default is 7 which is rather low."* | Plausibly relevant to *both* the *Death Note* chaos (very high `split_penalty`, e.g. 30-50, would suppress spurious mid-file split explosions, at the cost of also suppressing real ones) and to the "signs and songs" noise problem from issue #33 (which the author attributes partly to split penalty being too low to avoid spurious splits around noisy single lines). **Not** expected to help the *Sunny* growing-drift case, since that's described as smooth/monotonic growth rather than chaotic segment-to-segment jumps. | Medium — mechanism is clear, but the *right* value is data-dependent and untested here |
| `--speed-optimization` / `-O` (default 1; `0` = exact/slower) | Controls the growing-epsilon curve simplification described in detail in §1. `0` disables all approximation (exact DP search, `epsilon` always 0). | Top candidate for the *unexplained growing-drift* case (§2). Also worth trying on *Death Note* to rule out approximation noise as a contributor to the see-sawing (separate from the VAD-input-quality problem, which `speed-optimization` cannot fix — the VAD segments themselves would still be wrong). | Highest priority — directly testable, cheap (just slower), and has a specific, well-understood mechanism that structurally matches the observed symptom |
| `--sub-fps-ref` / `--sub-fps-inc` | **Not a general fps-scaling override.** Only affects how MicroDVD `.sub`-format frame-numbers are converted to times ([main.rs#L176-L185](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/main.rs#L176-L185)); confirmed by the argument's own help text and by its only use-site being `subparse`'s `.sub` parser invocation ([lib.rs#L219-L221](https://github.com/kaegi/alass/blob/874f02d9577182752a0f969b6d6b98fd65bdf1fc/alass-cli/src/lib.rs#L219-L221)). Since all of Sublime's affected files are `.srt`, **these flags are no-ops for the current investigation** — this corrects the handoff doc's assumption that they're an "explicit fps override" for the residual-drift case. | None, for `.srt`-only workflows | High (confirmed from source: dead-end for this investigation) |
| `--index` (misnamed `--audio-index` in the handoff notes; confirmed actual long-flag is `--index`) | Explicitly pick the ffprobe-reported audio stream index to extract, overriding the default "fewest channels" heuristic (§1, §3). | Worth testing across the whole affected-file list, not just one category — any multi-audio-track file (dubbed shows, commentary tracks, descriptive-audio tracks) is at risk of alass silently syncing against the wrong-language or wrong-content audio track today, independent of fps-guessing, candidate-cut, or VAD-difficulty. This is effectively a **fourth, previously uncharacterized failure mode** worth a dedicated pass: run `ffprobe -show_streams` against a sample of the affected-file list and check whether any of them have more than one audio stream. | High mechanism confidence; untested whether any of Sublime's actual affected files even have multiple audio tracks |

---

## 5. Is there a fundamentally better approach?

**Within alass's own issue tracker**: no discussion found of using ASR/speech-to-text
as a ground truth instead of VAD (searched issues/comments for "whisper",
"speech recognition", "ASR", "ffsubsync" — zero hits). The closest related
thread is [issue #11](https://github.com/kaegi/alass/issues/11)
("Sync not working against video directly but working perfectly against subs
generated from pytranscribe"), where a user found that first transcribing the
video with `pyTranscriber` (a Python speech-to-text wrapper) and using *that*
transcript's timing as the reference subtitle, instead of syncing directly
against the raw video, gave much better results — because a real speech
transcript "ignores loud noises and music" that raw VAD does not. This is a
real, if informal, data point in favor of ASR-derived reference timing being
more robust than VAD for exactly the noisy-audio case (anime, music-heavy
concert/dialogue-heavy films) already implicated in category 3.

**`--audio-index`/`--index` itself was community-contributed**, not
author-designed (issue #14) — reinforcing that multi-track handling was
never a first-class design concern.

**Version matters, but not the way you'd hope**: alass has had exactly one
major algorithm change (v1→v2, October 2019, adding fps-guessing/statistics —
commit `c438ff6`, "Alass 2: framerate correction, statistics, thesis") and
nothing since. There is no newer release with, e.g., a better VAD, ASR
integration, or drift-correction improvements to upgrade to. The project is
not dead (repo still exists, no explicit deprecation notice) but is
effectively frozen at v2.0.0/2021.

**Actual fundamentally-different approaches exist, external to alass**:

- **[`ffsubsync`](https://github.com/smacke/ffsubsync)** (the tool Bazarr
  actually uses — see §6) takes a meaningfully different algorithmic
  approach: it discretizes both video and subtitle into binary
  speech/non-speech strings at 10ms resolution (also via WebRTC VAD by
  default) and finds the best alignment via **FFT-based cross-correlation**
  (`O(n log n)`) rather than alass's segmented-DP
  (its own README:
  <https://github.com/smacke/ffsubsync/blob/master/README.md> — "How It Works"
  section, confirmed present verbatim in Bazarr's vendored copy at
  `bazarr/libs/ffsubsync-0.5.0.dist-info/METADATA`). Notably, `ffsubsync`'s
  own README explicitly documents accuracy/robustness workarounds that
  alass has no equivalent of: `--vad=auditok` (a non-VAD "any audio, not
  just voice" detector, useful for low-quality audio), `--vad=fused`
  (combines WebRTC with the neural Silero VAD), and
  `--skip-sync-on-low-quality` (a confidence gate that leaves a file
  untouched rather than overwriting it with a bad sync) — this last one is
  precisely the "detect before trusting a Sync" capability that #87 is
  trying to build *for* alass, already built into a sibling tool. Its own
  documented "Limitations" section states plainly that handling splits/cuts
  in the *middle* of a file (not just at the start/end) is unsolved future
  work — the opposite tradeoff profile from alass, which handles mid-file
  splits (that's its whole "split mode") but has no confidence gate.
- **[`lapse`](https://github.com/Schwponaco-org/lapse)** ("Language-Agnostic
  Playback Synchronization Engine") is a newer (active, per its README)
  C++ tool built specifically to also handle **linear framerate drift**
  (its README's own words: *"Detects how far off your subtitles are and
  corrects them, including linear drift caused by framerate mismatches
  between the video and the subtitle file"* —
  <https://github.com/Schwponaco-org/lapse#readme>) via what its docs describe
  as a from-scratch linear-regression-based drift model, distinct from
  alass's segmented-constant-offset DP. It also has a first-class
  confidence-verdict system (`solid`/`unsure`/`nothing`) and leaves files
  alone by default when unsure — again, the confidence-gating that alass
  lacks entirely.

  **Caveat on the following data**: `lapse`'s own published benchmark
  (<https://github.com/Schwponaco-org/lapse/blob/main/docs/benchmarks.md>) is
  self-published by a competing tool's author, not neutral, and explicitly
  says "results will vary" and that alass's numbers there "should still be
  a reasonably accurate picture" given alass's lack of ongoing maintenance.
  With that caveat, its methodology (39 real feature films picked
  specifically to be *hard* — long runtimes, long silences, heavy score,
  multi-cut releases — offsets/splits synthetically injected, all three
  tools run identically) is transparent and its findings corroborate this
  research's independent, source-derived conclusions rather than
  contradicting them:
  - alass **has no confidence measure and will silently overwrite a subtitle
    with a bad result**: in their "wrong subtitle entirely" test (an
    Oppenheimer-documentary subtitle run against the film *Oppenheimer*
    itself), alass shifted the file by -6:53 at a guessed 25/23.976 ratio,
    clamped the resulting negative timestamps, and overwrote the file —
    *"alass has no confidence measure, so nothing was reported."* This is an
    almost exact structural analogue of Sublime's category 2/3 failures
    (bad candidate or bad content → exit 0, no warning, file overwritten).
  - alass **crashed outright** on one film (*Come and See*) in their test
    set (`0.09s ✗ (crashed)`), with no further detail — an unstudied
    third failure surface (process-level crash, not bad output) not seen
    yet in Sublime's own investigation but worth keeping in mind
    generally.
  - In their split-mode results, alass's own split-mode passed 27/39
    (+1 partial) vs 32/39 (+3 partial) for `lapse` forced-split and 17/39
    (+1 partial) for `ffsubsync` with `--split-penalty` — i.e. even on a
    task alass is specifically designed for, a purpose-built alternative
    beats it by a meaningful margin on hard content.
  - On their one drift-specific injected case (`Seven Samurai`, `drift
    x1.04167` ≈ 25/24), alass landed within 38ms — fine when the injected
    ratio happens to be one of the six alass tests for. No case in their
    published set specifically probes an NTSC-adjacent ratio, so this
    doesn't directly confirm or refute the §2 hypothesis, but it does
    confirm alass performs well exactly when the true ratio is one it
    checks for, and offers no data on what happens when it isn't.

---

## 6. How other projects wrap alass in production

**Important correction to this task's premise**: contrary to the assumption
that Bazarr uses alass, **Bazarr does not use alass at all** — it exclusively
wraps `ffsubsync`. Confirmed directly from Bazarr's own source
(`bazarr/subtitles/tools/subsyncer.py`, which imports
`from ffsubsync.ffsubsync import run, make_parser` and constructs `ffsubsync`
CLI args — <https://github.com/morpheus65535/bazarr/blob/master/bazarr/subtitles/tools/subsyncer.py>),
and independently confirmed by searching Bazarr's own GitHub issue tracker
for "alass" (0 results, vs. 39 results for "ffsubsync" —
<https://api.github.com/search/issues?q=repo:morpheus65535/bazarr+alass> /
`...+ffsubsync`). No evidence was found anywhere (Bazarr's source, Bazarr's
issues, alass's own issues) that Bazarr ever supported alass as an
alternative backend. This should be corrected wherever this assumption is
repeated elsewhere in the investigation's tracking.

**A real production wrapper of alass does exist**: **AutoSubSync**
(<https://github.com/denizsafak/AutoSubSync>, ~1,177 GitHub stars, actively
maintained), a cross-platform GUI/CLI tool that lets users choose between
`ffsubsync`, `alass`, `autosubsync`, and `lapse` as interchangeable sync
backends. Its own alass invocation
(`main/constants.py`,
<https://github.com/denizsafak/AutoSubSync/blob/main/main/constants.py>) uses
the **plain default command line** — `alass {reference} {subtitle} {output}`
with no extra flags by default (`"cmd_structure": ["{reference}",
"{subtitle}", "{output}"]`) — i.e. it does not tune `--split-penalty`,
`--speed-optimization`, or `--disable-fps-guessing` out of the box either;
users have to opt into those via its GUI. Two things it *does* document as
workarounds, both operational rather than alignment-quality fixes:

- **Bracket-character path bug**: alass errors with `could not convert
  string to float` when given file/folder paths containing `[` or `]`.
  AutoSubSync auto-renames affected path components (`[`→`(`, `]`→`)`)
  before invoking alass, or prompts the user
  (`main/sync_core.py`, error handling around
  `"could not convert string to float"` and `texts.ALASS_BRACKETS_ERROR`).
  Not directly relevant to Sublime's Linux/container-mounted media paths
  unless episode/movie folder names ever contain brackets (some release
  groups do use them, e.g. `[Group] Show - 01.mkv`), but worth a defensive
  check.
- **Independent corroboration that `--speed-optimization 0` trades speed for
  accuracy**, matching this research's §1 finding: AutoSubSync's own UI
  copy for the option reads *"Disable speed optimization for better
  accuracy. This will increase the processing time"*
  (`main/constants.py`, the `disable_speed_optimization` option's
  description text). This is a third party independently reaching the same
  conclusion as the source-code analysis in §1, without necessarily having
  read alass's internals — likely from their own empirical testing, and
  consistent with alass's own docstring guidance (§1).

No other genuinely production-grade alass wrapper was found in a (time-boxed,
non-exhaustive) search of GitHub for repos referencing alass; most hits were
small (<50-star) personal scripts. `alass-core`'s own crates.io reverse-dependency
list confirms this from the Rust-ecosystem side too: only the author's own
`alass-cli`, `alass-util`, and `alass-ffi` crates depend on it
(<https://crates.io/api/v1/crates/alass-core/reverse_dependencies>) — i.e.
essentially nobody has built a from-scratch Rust integration on top of the
library; every real-world consumer (including Sublime) shells out to the
compiled `alass-cli` binary.

---

## Actionable leads for next investigation session

Ranked by (expected value) × (how cheap it is to test):

1. **Re-run *Sunny* S11E08 with `--speed-optimization 0` (in addition to the
   already-validated `--disable-fps-guessing`).** Directly tests the §1/§2
   growing-epsilon hypothesis. Cheapest possible test (one extra flag,
   slower but still seconds), and has the clearest mechanistic story of
   anything in this document for the specific "growing, not jumping" drift
   shape already observed. If the growth disappears, that's the answer nailed.
   If it persists, that isolates it to something else (most likely the
   NTSC-ratio hypothesis below).

2. **Check whether `--index` (audio-track selection) is even relevant to any
   of the currently-affected files.** Run `ffprobe -show_streams` (or
   equivalent) against a sample from the 251-file affected-file list — GoT,
   Peaky Blinders, and the Sunny episodes are the highest-value candidates
   given their genre/release conventions (some Western TV releases carry
   commentary or descriptive-audio tracks). If any affected file has more
   than one audio stream, that's a previously-uncharacterized fourth failure
   mode worth its own reproduction, independent of fps/candidate-cut/VAD-difficulty.
   Trivial to check, potentially high-value if it hits.

3. **For the *Sunny* growing-drift case specifically, also check for a
   genuine NTSC-adjacent framerate ratio** independent of alass's guesser:
   compare the reference video's actual frame rate (`ffprobe`) against
   23.976/24/25 and against 29.97/30, and consider whether the candidate
   subtitle's original release was plausibly sourced from an NTSC broadcast
   capture. alass has no flag to fix this even if found (§2) — but knowing
   whether it's the cause changes whether "growing drift after
   `--disable-fps-guessing`" should be treated as a residual bug to route
   around (e.g. Sublime doing its own pre-scaling before invoking alass) or
   as evidence that this specific file/candidate pair should just be
   rejected as unfixable by alass.

4. **Test `--split-penalty` at higher values (20-50) specifically on the
   *Death Note* case**, per the alass author's own advice in issue #7 and
   the parallel pattern in issue #33 ("signs and songs" causing spurious
   splits) — separate from and complementary to whatever the parallel
   fps-guessing research task finds. Won't fix VAD-quality-on-noisy-audio
   itself, but could reduce the chaotic segment-count/segment-jump severity
   even if the underlying VAD signal stays bad.

5. **Consider `--allow-negative-timestamps` only as part of a Sublime-side
   post-processing pipeline** (order-preserving forward shift applied after
   alass, per #87's existing conclusion) — not a standalone fix. Lower
   priority than 1-4 since it doesn't address any root cause, only changes
   corruption's presentation from "silent collapse" to "honest negative
   timestamps," which Sublime would then need new logic to handle anyway.

6. **Longer-horizon, higher-effort**: prototype an ASR-based (e.g.
   Whisper) reference-timeline generator as an alternative to raw-VAD
   video input for category-3 (VAD-hard) content, per the informal but
   real precedent in alass's own issue #11 and the design direction of
   `ffsubsync`'s more tunable VAD options (`--vad=fused`) and `lapse`'s
   from-scratch approach. This is a bigger lift than anything else on this
   list (would mean generating a synthetic reference subtitle via
   speech-to-text and using *that* as alass's reference argument instead of
   the raw video — actually straightforward to try once framed that way,
   since alass already supports subtitle-to-subtitle alignment natively)
   but is the only lead here that could plausibly fix category 3 rather
   than just work around it. Worth a narrow, single-file spike (Death Note
   S01E26) before any broader investment.

## References

- alass source (cloned locally for this research; commit
  `874f02d9577182752a0f969b6d6b98fd65bdf1fc` = tip of `master`):
  <https://github.com/kaegi/alass>
- alass issue tracker (all issues read via GitHub REST API; highest-comment
  and content-flagged threads read in full): <https://github.com/kaegi/alass/issues>
  — specifically #7, #9, #11, #14, #33, #34, #43, #47, #49, #50, #54
- `webrtc-vad` crate (VAD binding used by alass, same author):
  <https://github.com/kaegi/webrtc-vad>
- `ffsubsync`: <https://github.com/smacke/ffsubsync> (README/algorithm
  description; also vendored inside Bazarr at
  `bazarr/libs/ffsubsync-0.5.0.dist-info/METADATA`)
- `lapse`: <https://github.com/Schwponaco-org/lapse> (README +
  `docs/benchmarks.md`)
- Bazarr source (confirms ffsubsync-only, no alass):
  <https://github.com/morpheus65535/bazarr/blob/master/bazarr/subtitles/tools/subsyncer.py>
- `AutoSubSync` (real alass production wrapper):
  <https://github.com/denizsafak/AutoSubSync>
- crates.io reverse-dependency data for `alass-core`:
  <https://crates.io/api/v1/crates/alass-core/reverse_dependencies>
- alass's bachelor's-thesis/slides exist at
  `documentation/thesis.pdf` / `documentation/slides.pdf` in the alass repo
  but could not be text-extracted in this environment (no PDF tooling
  available, and installing one was out of scope for this read-only
  research task); all algorithmic claims in this document were instead
  verified directly against the actual shipped implementation in
  `alass-core`/`alass-cli`, which is authoritative for behavior regardless
  of what the thesis says.
