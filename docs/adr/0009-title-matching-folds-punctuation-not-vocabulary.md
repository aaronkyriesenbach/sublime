# Title matching folds punctuation/whitespace, not vocabulary

Scoring's title comparison (`internal/scoring/score.go`) previously required
exact (case-insensitive) string equality, which rejected legitimate matches
differing only by punctuation a release filename dropped (e.g. SubDL's
canonical `"Blue Mountain State: The Rise of Thadland"` vs. the
filename-derived `"Blue Mountain State the Rise of Thadland"` — see
`docs/subdl-smoke-test-findings.md`). Comparison now folds every run of
non-letter, non-digit characters (colons, dashes, quotes, ampersands,
whitespace) to a single space before comparing, via `foldTitle`.

Deliberately excluded from the fold: accent folding (`é` → `e`),
leading-article stripping (`"The Matrix"` vs. `"Matrix"`), `"&"` vs.
`"and"`, and numeral vs. spelled-out-number substitution (`"Part 2"` vs.
`"Part Two"`). These are vocabulary-level differences rather than
punctuation-level ones, and each carries real false-positive risk a
mechanical character strip doesn't (a mismatched sequel, a distinct
localized title) — accepting them would require judgment calls this fix
deliberately doesn't make. If any of these prove necessary in practice,
they should be evaluated individually, on their own trade-offs, rather than
folded into this same mechanism.
