# alass `--disable-fps-guessing`: mechanics, failure modes, and recommendation

**Status**: research note, not an ADR. Written to reconcile "disabling a
feature that's supposed to improve accuracy fixed a corruption" before
`--disable-fps-guessing` becomes a permanent default in Sublime's alass
invocation. This is the first file in `docs/research/` — the repo had no
prior convention for standalone research notes (see "Note on convention"
at the end).

Sources: primary only — the actual `kaegi/alass` source code (cloned from
`https://github.com/kaegi/alass`, commit `874f02d9577182752a0f969b6d6b98fd65bdf1fc`,
current `master` HEAD as of this research), its GitHub Releases, and its
GitHub Issues (fetched via the GitHub REST API). All line numbers below
refer to that commit. Sublime pins the **v2.0.0** release binary
specifically (`Dockerfile`, `ARG ALASS_VERSION=v2.0.0`) — confirmed below
that the fps-guessing code is identical between the `v2.0.0` tag and the
current `master` HEAD (nothing has touched it since `v2.0.0` shipped), so
findings here apply directly to the exact binary Sublime runs.

## 1. What does fps-guessing actually do, mechanically?

alass's default (fps-guessing enabled) pipeline, from
`alass-cli/src/main.rs`:

1. It hardcodes three "canonical" frame rates and derives six ratios
   between them (main.rs:368-372):

   ```rust
   let a = 25.;
   let b = 24.;
   let c = 23.976;
   let ratios = [a / b, a / c, b / a, b / c, c / a, c / b];
   let desc = ["25/24", "25/23.976", "24/25", "24/23.976", "23.976/25", "23.976/24"];
   ```

   These three values (25, 24, 23.976) are exactly PAL film-transfer
   (25fps), true film (24fps), and NTSC film-transfer (23.976fps) — the
   classic telecine/PAL-speedup conversion trio. **fps-guessing is not a
   general frame-rate detector; it only ever tests these 6 fixed ratios.**
   Any real-world mismatch outside that set (e.g. 30↔25, or a genuinely
   nonstandard encode) cannot be corrected by this mechanism regardless of
   whether it's enabled.

2. For each of the 6 ratios plus the "no correction" baseline (ratio 1),
   it scales every timestamp in the *candidate* subtitle's timespans by
   that ratio (`alass-core/src/time_types.rs:391-395`, `TimeSpan::scaled`,
   simple `start/end * factor`), then calls `align_nosplit` — the
   single-global-shift (no split/segmentation) aligner — to find the
   single best constant time-shift for that scaled version against the
   reference video's voice-activity-detected speech spans
   (`alass-cli/src/lib.rs:402-445`, `guess_fps_ratio`).

3. Each candidate ratio's "fit" is scored with `overlap_scoring`, not the
   normalized scorer used for the real alignment:

   ```rust
   // alass-core/src/lib.rs:69-72
   pub fn overlap_scoring(a: TimeDelta, b: TimeDelta) -> Score {
       let min: f64 = min(a, b).as_f64();
       min * 0.00001
   }
   ```

   This is just the raw overlap **duration** between a reference
   voice-activity span and a candidate subtitle span (capped at whichever
   span is shorter), scaled by a constant. It is **not** normalized by
   subtitle length, coverage, or count — it's an absolute sum of
   milliseconds of overlap across the whole file at the best single shift
   for that ratio (`alass-cli/src/lib.rs:436-441` picks whichever ratio's
   summed overlap score is strictly highest).

4. Whichever ratio (including "no correction") wins is applied
   irrevocably: the candidate subtitle's timespans are rescaled by that
   factor (main.rs:381, `fps_scaling_factor = ratios[idx]`) *before* the
   real split-aware alignment (`align()`, using the different,
   length-normalized `standard_scoring` — `alass-core/src/lib.rs:61-64`,
   `min(a,b)/max(a,b)`) ever runs. The real alignment then optimizes
   splits/offsets on top of this already-rescaled timeline; it has no
   mechanism to reconsider or veto the framerate assumption baked in
   before it started.

5. The only diagnostic emitted is one `println!` line (main.rs:383-387):

   ```
   info: 'reference file FPS/input file FPS' ratio is 25/23.976
   ```

   (or `...is 1` if no ratio beat the baseline). There is no numeric score,
   confidence, or margin printed anywhere in normal output — see §3.

## 2. Why would this ever misfire and produce a WORSE alignment than doing nothing?

**Two independent, compounding reasons, one structural (from reading the
code) and one empirical (from the maintainer's own issue comments):**

### 2a. Structural: the ratio-selection scorer optimizes total overlap coverage, not alignment correctness

Because `overlap_scoring` sums raw overlap *duration* rather than a
normalized fit quality, and because `guess_fps_ratio` only ever tests a
**single global shift** per ratio (`align_nosplit`, not the real
split-capable `align`), a ratio that happens to make subtitle spans wider
or reposition them to spuriously straddle more voice-activity spans can
score higher than the untouched (ratio = 1) baseline even when the
original timing was already correct. The check is a coarse,
un-cross-validated proxy run once per candidate ratio on a simplified
(single-offset) version of the problem — it has no built-in skepticism
about the "no correction needed" case, and nothing compares the winning
ratio's score against some absolute plausibility threshold before
committing to it. It is a strict "highest score of these 7 options wins,"
even if all 7 are mediocre or the margin between them is tiny (see §3 —
this margin is never surfaced).

### 2b. Empirical: the maintainer confirms fps-guessing as the most common cause of "wrongly desynced" reports, across multiple independent issues

- **Issue #9**, ["Properly synced sub is 'de-synced'"](https://github.com/kaegi/alass/issues/9)
  (closed): a user fed alass a subtitle that was *already correctly
  synced* and got back a badly desynced result with negative timestamps.
  Maintainer (`kaegi`) response:

  > "This means the algorithm thinks that the subtitle gets somehow more
  > optimal by de-syncing it in a specific way. [...] it is probably
  > caused by framerate guessing (try `--disable-fps-guessing`)"

  Follow-up from the reporter: **"the `--disable-fps-guessing` did solve
  my issue for that file, thanks."** This is the closest primary-source
  precedent to Sublime's own finding — an already-correct file getting
  wrongly rescaled by fps-guessing — and it was reported and fixed the
  same way back in 2019.

- **Issue #7**, ["Troubleshooting wrong alignment"](https://github.com/kaegi/alass/issues/7)
  (open): a different user's Dutch-subtitle alignment came out "totally
  wrong." Maintainer:

  > "If you know that the framerate is correct in the original subtitle
  > file, you could try `--disable-fps-guessing`. **This is usually the
  > step that goes wrong.**"

  He also states in the same thread there is no verbose/debug flag: "There
  is no `--verbose` flag or anything. If the framerate guessing is indeed
  the step that went wrong, printing the scores for the 7 tested framerate
  ratios might provide some insight (giving the confidence of the guess).
  I don't think there is any other usable information for a human." — i.e.
  the maintainer himself acknowledges the *scores* (which would show how
  confident/marginal the winning guess was) are computed but not surfaced
  (they're commented out in the shipped code, see §3), and that this is a
  known gap.

- **Issue #47**, ["Synchronized subtitles way off"](https://github.com/kaegi/alass/issues/47)
  (open, unresolved as of this research): a user tried `--disable-fps-guessing`
  and it did **not** fix their case (a home TV recording with commercials
  manually cut out), showing fps-guessing is not the *only* source of bad
  alignments — consistent with Sublime's own taxonomy (`sync-reliability-investigation-handoff.md`)
  identifying it as one of (at least) three distinct root causes, not a
  universal explanation.

### Version history: has this changed?

fps-guessing was introduced wholesale in **v2.0.0** ("Alass 2:
framerate correction, statistics, thesis," commit `c438ff6`, released
2019-10-10). The GitHub release notes for v2.0.0 state:

> - implement correction of common framerates
> - about twice as fast as before
> - choose default configuration based on statistics from real data

`git log` shows `alass-cli/src/lib.rs` (where `guess_fps_ratio` and
`overlap_scoring` live) has exactly **one** commit touching it ever — the
v2.0.0 commit that introduced it. Nothing in the three commits made after
v2.0.0 (`b9450c7` audio-index support, `c28f8f6` compiler-warning fixes,
`874f02d` subtitle-encoding auto-detect — the current `master` HEAD) touch
the fps-guessing code path at all. There have been no releases since
v2.0.0 (2019-10-10); the repo is not archived but has had no commits since
2021-04-11. **The version does not matter here** — every version of alass
that has fps-guessing at all (v2.0.0 through current HEAD, including the
exact v2.0.0 release binary Sublime uses) has the identical algorithm,
scoring function, and hardcoded ratio set described in §1.

## 3. When should fps-guessing be trusted vs. not?

**No confidence signal is exposed today.** The only stdout evidence is the
single `info: 'reference file FPS/input file FPS' ratio is X` line
(main.rs:383-387) — it says *which* of the 6 hardcoded ratios won (or `1`
for none), but not the winning score, the runner-up score, or the margin
between them. The per-ratio overlap scores are computed
(`alass-cli/src/lib.rs:436`, `if score > opt_score`) but the `println!`
lines that would print them are commented out in shipped code:

```rust
// alass-cli/src/lib.rs (comments in shipped source, not active)
//let desc = ["25/24", "25/23.976", "24/25", "24/23.976", "23.976/25", "23.976/24"];
//println!("score 1: {}", score);
...
//println!("score {}: {}", desc[scale_factor_idx], score);
```

The maintainer confirms (issue #7) there is no `--verbose` flag and no
other way to get this confidence information out of the shipped CLI
without patching and recompiling alass yourself. Sublime's own existing
documentation (`internal/syncengine/alass/alass.go`'s doc comment: "alass
always produces a best-scoring delta and never signals alignment quality")
and its handoff doc's note that "alass's own stdout warning turned out to
be an insufficient signal on its own" (re: negative-timestamp warnings,
a *different* diagnostic) are consistent with this.

**What Sublime *can* check without touching alass itself:** the one
stdout line alass does print is directly parseable and semantically
meaningful — `desc[idx]` for ratio `a/b` is labeled `"<reference-fps>/<input-fps>"`
(main.rs:372, 384-387), meaning the printed string literally states what
alass *believes* the reference video's frame rate to be (the first term)
and what it believes the candidate subtitle's frame rate to be (the second
term). The reference video's real frame rate is independently and cheaply
verifiable — Sublime already shells out to `ffprobe` elsewhere
(`internal/strip/embedded.go`) — via e.g.:

```sh
ffprobe -v error -select_streams v:0 -show_entries stream=r_frame_rate \
  -of default=nw=1:nk=1 <reference-video>
```

which returns a fraction like `24000/1001` (≈23.976), `25/1`, or `24/1`.

This gives a **falsifiable cross-check that needs no subtitle-side ground
truth at all**: if alass's printed ratio claims the *reference* side is,
say, 25fps, but `ffprobe` measures the actual reference video's real frame
rate as ≈23.976, that is a direct contradiction — proof the guess is
wrong, independent of anything about the candidate subtitle. If the two
agree (within a small tolerance, e.g. ±0.5%, to allow for rounding), the
guess is at least *plausible* on the reference side (not proof it's
correct — the candidate/input-side assumption in the same ratio could
still be wrong, and there is no independent way to check that side for
plain `.srt`/`.ass`/`.ssa` candidates, which carry no embedded frame-rate
metadata at all — only MicroDVD `.sub` files do, handled separately by
alass's own `--sub-fps-ref`/`--sub-fps-inc` flags).

**Unverified in this research pass**: whether Sublime's subtitle provider
(OpenSubtitles) supplies any per-release frame-rate metadata (the legacy
OpenSubtitles.org XML-RPC API is known to have had a `MovieFPS` field
historically; whether the REST API Sublime actually calls,
`internal/provider/opensubtitles/opensubtitles.go`, exposes an equivalent
field was not confirmed here — no request against the real API with a
valid key was made, and Sublime's current provider code parses no
`fps`-shaped field at all). If such a field exists and is populated, it
would let Sublime additionally cross-check the *candidate* side of the
ratio the same way; this is a prerequisite to verify before relying on it,
not a settled fact.

## 4. Community consensus: how do other tools handle this?

- **Bazarr** (`morpheus65535/bazarr`, the most prominent subtitle-sync
  management tool) does **not** wrap `alass` at all — it bundles and uses
  **ffsubsync**, a different (Python, audio-energy/VAD-based, different
  algorithm family) synchronizer. Confirmed by inspecting Bazarr's vendored
  dependencies (`libs/ffsubsync-0.5.0.dist-info/METADATA`), which itself
  lists `kaegi/alass` only as a "related project," not something it
  invokes. There is therefore no direct Bazarr precedent for "does a
  well-known wrapper disable alass fps-guessing by default" — the two
  major open-source subtitle-sync ecosystems (Bazarr/ffsubsync and
  alass) are largely disjoint, and no third-party alass wrapper with
  documented fps-guessing handling was found via GitHub issue/code search
  in this pass (GitHub code search requires authentication not available
  in this environment, and general web search for `"disable-fps-guessing"`
  turned up only alass's own repository issues — no wrapper projects).
- Within alass's **own** issue tracker, every case where a maintainer
  diagnosed a bad-alignment report that turned out to be fps-guessing
  (issues #7, #9) resulted in the same standing advice: try
  `--disable-fps-guessing` first, "since this is usually the step that
  goes wrong." That is the closest thing to a documented community
  consensus that exists for this specific tool — the tool's own author
  recommending against trusting the default in troubleshooting contexts,
  without ever changing the default itself.

## 5. Final recommendation

**(c) — decide per-file, using a concrete, ffprobe-based plausibility
check on alass's own printed ratio; do not blanket-disable.**

Rationale against the two blanket options:

- **Against (a) "always pass `--disable-fps-guessing`"**: fps-guessing is
  a real feature solving a real, common problem (PAL/NTSC telecine-style
  frame-rate mismatches between a subtitle release and a video release),
  and Sublime's own investigation already observed a symptom consistent
  with a *genuine* uncorrected frame-rate mismatch after forcing `-g`:
  the handoff doc notes *Sunny* S11E08 still showed **growing** (not
  constant) segment shifts (24s → 47s → 83s → 187s) even with fps-guessing
  disabled — the classic signature of an uncorrected linear-drift
  frame-rate mismatch, not a fixed offset. Blanket-disabling risks trading
  one failure mode (occasional false-positive rescale corruption) for
  another (guaranteed-uncorrected true rescale need) on some unknown
  fraction of files. The sample size establishing `-g` as a fix (per the
  task's own framing) is a handful of files — not large enough to conclude
  the feature is *never* needed for Sublime's actual candidate mix.
- **Against (b) "never pass it, trust the default"**: the algorithm's own
  scoring (§2a) has a demonstrated, source-code-legible structural bias
  (unnormalized overlap-duration scoring on a single-shift probe, with no
  plausibility floor), and the tool's own maintainer independently
  confirms (§2b) it is "usually the step that goes wrong" when alignment
  looks wrong — trusting it unconditionally means periodically shipping
  silently-corrupted timing with exit code 0, which is the exact failure
  mode motivating this whole investigation.

### Concrete check for (c)

1. Run `alass` with fps-guessing **enabled** (i.e. don't pass `-g`) as
   today, capturing its stdout (Sublime's `Engine.Sync` already discards
   stdout on the success path — `internal/syncengine/alass/alass.go` would
   need to start capturing it there, same way `RunError.Output` already
   captures it on the failure path).
2. Parse the one diagnostic line:
   `info: 'reference file FPS/input file FPS' ratio is (.+)`.
   - If the captured group is `1`, no correction was applied — nothing
     further to check, trust the result as today.
   - Otherwise, parse the two fps values out of the matched description
     (one of the six literal strings `25/24`, `25/23.976`, `24/25`,
     `24/23.976`, `23.976/25`, `23.976/24` — main.rs:372) — the first
     number is alass's assumed **reference** (video) fps.
3. Independently measure the reference video's real frame rate via
   `ffprobe -v error -select_streams v:0 -show_entries stream=r_frame_rate
   -of default=nw=1:nk=1 <video>` (parse the returned fraction to a float).
4. Compare: if the ffprobe-measured fps and alass's assumed reference fps
   are **not** within a small tolerance (e.g. ±0.5%) of the same one of
   {23.976, 24, 25}, the guess is provably wrong on its own terms — treat
   this Sync as untrustworthy: re-run alass with `--disable-fps-guessing`
   and use that result instead (or, at minimum, don't mark the file
   `synced` without flagging it for review).
   - If they **do** agree, the reference-side assumption is plausible.
     This is not proof of correctness (the candidate/input side of the
     ratio is still unverified for `.srt`/`.ass`/`.ssa` — see §3), but it
     removes the one contradiction that's cheaply and unambiguously
     checkable today, without waiting on unverified subtitle-provider
     metadata.
5. As a follow-up (not blocking implementation of the above): verify
   whether OpenSubtitles' API response actually includes a usable
   per-release frame-rate field (§3, "Unverified in this research pass"),
   which would let step 4 also cross-check the candidate/input side of the
   ratio instead of only the reference side.

This keeps fps-guessing's real, working use case (true frame-rate
mismatches) intact, uses a signal Sublime already has the tooling for
(`ffprobe`, already shelled out to elsewhere in the codebase) and a
diagnostic alass already prints for free, and only falls back to the
already-validated `--disable-fps-guessing` escape hatch for the specific
files where alass's own guess is self-contradictory.

## Note on convention

This repo had no existing `docs/research/` (or equivalent) directory
before this note — `docs/` previously contained only `docs/adr/` and
`docs/agents/`. This establishes `docs/research/` as the location for
this kind of standalone investigative write-up; no existing index or
`README.md` links to it yet.

## References

- alass source (cloned): `https://github.com/kaegi/alass`, commit
  `874f02d9577182752a0f969b6d6b98fd65bdf1fc` (current `master` HEAD)
  - `alass-cli/src/main.rs` (CLI arg parsing, ratio table, fps-scaling
    application — lines 140, 216-220, 278, 368-390)
  - `alass-cli/src/lib.rs` (`guess_fps_ratio` — lines 402-446)
  - `alass-core/src/lib.rs` (`standard_scoring` lines 61-64,
    `overlap_scoring` lines 69-72)
  - `alass-core/src/time_types.rs` (`TimeSpan::scaled` — lines 391-395)
  - `alass-core/README.md`, `README.md` (top-level docs)
- alass releases: `https://github.com/kaegi/alass/releases`
  (v1.0.0, v1.0.1, v1.0.2, v2.0.0 — v2.0.0 is the release that introduced
  fps-guessing; no releases since)
- alass issues (GitHub REST API, unauthenticated):
  - `https://github.com/kaegi/alass/issues/7` — "Troubleshooting wrong
    alignment" (open)
  - `https://github.com/kaegi/alass/issues/9` — "Properly synced sub is
    'de-synced' - win" (closed, resolved by `--disable-fps-guessing`)
  - `https://github.com/kaegi/alass/issues/47` — "Synchronized subtitles
    way off" (open, `--disable-fps-guessing` did **not** help this case)
- Bazarr (`morpheus65535/bazarr`), cloned to confirm it uses ffsubsync,
  not alass: `libs/ffsubsync-0.5.0.dist-info/METADATA`
- Sublime's own repo (read, not modified): `Dockerfile` (pins
  `ALASS_VERSION=v2.0.0`), `internal/syncengine/alass/alass.go`,
  `internal/provider/opensubtitles/opensubtitles.go`,
  `internal/strip/embedded.go`, `sync-reliability-investigation-handoff.md`

## Bottom line

fps-guessing works by scaling the candidate subtitle's timestamps by
whichever of 6 hardcoded PAL/NTSC-style ratios (or "none") maximizes raw,
unnormalized overlap-duration against a single best global shift versus
detected voice activity — a coarse proxy that structurally has no
skepticism toward false positives and, by the tool's own maintainer's
repeated account across multiple issues since 2019, is "usually the step
that goes wrong" when alignment looks wrong. That reconciles the
paradox: it's not that the feature never helps, it's that its internal
selection heuristic can and does prefer a wrong rescale over "leave it
alone," with zero built-in check against that, and alass exposes only one
weak diagnostic (which ratio it picked) and no confidence signal. Sublime
should not blanket-trust or blanket-disable it; it should keep
fps-guessing on, capture alass's one diagnostic line, and cross-check the
reference-fps half of its guess against a real `ffprobe` measurement of
the actual video — rejecting (and retrying with `--disable-fps-guessing`)
only the self-contradictory cases.
