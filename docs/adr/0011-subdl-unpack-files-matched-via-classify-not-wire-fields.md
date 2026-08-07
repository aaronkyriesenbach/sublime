# SubDL unpack_files entries are matched via media.Classify, never SubDL's own season/episode fields

When `unpack=1` is set, a SubDL `full_season` pack item carries an
`unpack_files[]` breakdown — one entry per individual episode file saved
inside the pack, each with its own documented `season`/`episode` integer
fields. Live-testing SubDL's actual responses against real shows in this
library (see `docs/subdl-smoke-test-findings.md`) showed these structured
fields are reliable only when the file's own name already carries an
explicit `S01E02`-style tag, and are otherwise wrong or zeroed: Andor's pack
reported its real episode 11 as `season: 11, episode: 1`, its real episode 8
("Narkina 5") as `episode: 5` (apparently misparsing the "5" out of the
episode's own title), and its real episode 12 (the finale, "Rix Road") as
`season: 0, episode: 0`. Death Note's largest pack reported all 37 of its
files as `season: 0, episode: 0` with an empty `release_name`, despite
filenames like `"Death Note E13 [ESub].srt"` plainly encoding the episode.
The one pack tested whose files were named with clean `S01E02` tags
throughout (Peaky Blinders) had entirely correct structured fields — the
unreliability tracks the filename's own tagging, not randomness.

Sublime therefore never reads an `unpack_files` entry's own `season`/
`episode` fields, not even as a tiebreak. Each entry's identity is resolved
by running `internal/media.Classify` — the same strict
`\bS(\d{1,2})E(\d{1,3})\b` / `\b(\d{1,2})x(\d{1,3})\b` classifier already
used for video filenames — against the entry's `name`, falling back to its
`release_name`. An entry `Classify` can't confidently tag as `Episode` is
discarded outright; there is no fallback to guessing from a leading ordinal
number in an informally-named file (e.g. `"02 That Would Be Me.en.srt"`),
since that coincides with the true episode number only when a pack's files
are contiguous and unshifted by non-episode content — which real packs
violate (Andor's own bonus "07 Announcement" featurette is interleaved
between real episodes 6 and 8). The pack's outer `full_season: true` item is
never itself turned into a Candidate — only entries resolved this way are —
so no zip-archive download/extraction path is needed anywhere in Sublime.

**Considered options:**

- Trust `unpack_files`' own `season`/`episode` fields directly, as SubDL's
  docs describe them. Rejected: measurably wrong across most of the packs
  live-tested against a real library, in a way that would produce either
  silent misses or — worse — Sublime confidently handing back a subtitle
  timed for the wrong episode.
- Fall back to parsing the leading numeric prefix of an informally-named
  file when no `S0xEyy` tag exists. Rejected: relies on an unstated
  contiguous-numbering assumption real packs violate; reusing the existing
  strict classifier costs nothing extra and never has to guess.
