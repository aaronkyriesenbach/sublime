# Integration-test fixtures

Small fixture files for the real ffmpeg/ffprobe and alass integration tests
described in the [Testing philosophy](https://github.com/aaronkyriesenbach/sublime/issues/11)
decision. Resolves [Integration-test fixtures](https://github.com/aaronkyriesenbach/sublime/issues/20).

All fixtures are **synthesized**, not sourced from real film/TV content —
see `generate.sh` for why. Regenerate with `./generate.sh` (requires
`ffmpeg` and `espeak-ng`).

## Inventory

| File | Purpose |
|---|---|
| `video/sample.mp4` | 9s, 320x240, gray field + a 3-line synthesized-speech audio track (spoken at 0.5s / 3.5s / 6.5s). Real voice content (not a tone), so alass's WebRTC VAD has genuine speech to lock onto. Used as: an ffprobe metadata fixture, and an alass reference-video input. |
| `video/tiny.mp4` | Silent, 1s, well under 64 KiB. Exercises the [Content Hash algorithm](https://github.com/aaronkyriesenbach/sublime/issues/12)'s first/last-64KiB overlap case for files smaller than one window. |
| `subs/sample.srt` | Correctly-timed transcript of `sample.mp4`'s speech (`00:00.5`, `00:03.5`, `00:06.5`) — the alignment target. |
| `subs/sample.shifted.srt` | Same text, shifted +2.5s — the "incorrect" input alass is expected to re-align back toward `sample.srt`'s timing. |
| `subs/sample.ass` | Same text/timing as `sample.srt` in `.ass` format — exercises alass's extension-based format dispatch on a second format, and Sublime's "write output in the candidate's native format" rule ([Output format](https://github.com/aaronkyriesenbach/sublime/issues/8)). |

## Deliberately not included

- **`.idx`/`.sub` (VobSub) and `.vob` fixtures.** These are image-based
  formats; hand-authoring a valid pair is disproportionately complex for
  what they'd cover, and Sublime's own pipeline neither produces nor
  (knowingly) needs to consume them operationally. Add if a real Candidate
  in one of these formats turns up in practice.
- **A "garbage alignment" fixture** (audio unrelated to the subtitle text,
  to exercise alass's "always returns *a* delta, never a confidence
  signal" behavior from the
  [alass CLI contract](https://github.com/aaronkyriesenbach/sublime/issues/14)).
  Left out because what counts as "garbage" is exactly what
  [Sync outcome classification](https://github.com/aaronkyriesenbach/sublime/issues/26)
  hasn't decided yet — add a fixture once that ticket fixes a concrete
  plausibility check to test against, rather than guessing at one now.
