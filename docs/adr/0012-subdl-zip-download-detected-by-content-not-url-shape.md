# SubDL zip-shape downloads are detected by content, and resolved by candidate identity, not by URL shape

SubDL's docs (`https://subdl.com/api-doc`, "Downloading Subtitles") describe
two download URL shapes: `subtitles[].url` (dash-`.zip`, used by
`candidatesFromResponse` for both movie and TV candidates) returning "a zip
file", and `unpack_files[].url` (slash, no `.zip`, ADR 0011's season-pack
breakdown) returning "individual raw subtitle files". Sublime's `Download`
trusted the latter description for both shapes and returned every
download's bytes unmodified. Live-testing (issue #80) showed the dash-`.zip`
shape genuinely is a zip archive: feeding its raw bytes to alass under an
`.srt` name reproduced the exact production failure Sublime saw for every
SubDL-sourced regular-listing subtitle, movie or TV. The slash shape was
never exercised live in the session that found this (SubDL returned empty
`unpack_files` for every case queried) — whether it's also zipped remains
unconfirmed by direct observation.

`Download` therefore sniffs the response's own bytes for the zip
local-file-header magic (`PK\x03\x04`) rather than branching on which URL
shape produced it. This is deliberately generic across both shapes: SubDL's
docs already misdescribed one wire format before this discovery (the
original wire-format bug fixed in `7c4f505`), and ADR 0011 already
established this codebase's general position that SubDL's stated wire
contract is not trusted over what the API actually sends. Sniffing content
means the slash shape gets exactly the same handling with no separate code
path to add or verify independently — if it's already raw, the sniff is a
no-op; if it turns out to also be zipped, the same extraction rule below
already covers it.

When a download is zip-shaped, `extractSubtitleFromZip` resolves the one
entry that is "the subtitle", from `domain.Candidate`'s own already-known
identity — never by guessing:

1. Filter entries to `.srt` only. Sublime's pipeline (`processFile`)
   already hard-assumes SRT-format text for every Provider's downloaded
   content (it writes it to a temp file literally named `input.srt`
   regardless of source), so a non-`.srt` entry is not "the subtitle" no
   matter its name, for either a movie or TV candidate.
2. For a TV candidate (`candidate.Episode != 0`): among the `.srt`-filtered
   entries, run `media.Classify` against each entry's name and keep the
   one resolving to `candidate.Season`/`candidate.Episode` — the same
   mechanism ADR 0011 already established for `unpack_files` entries, for
   the same reason (SubDL's own structured fields are not trustworthy).
3. For a movie candidate (`candidate.Episode == 0`): Season/Episode carry
   no signal, so the sole remaining `.srt` entry is the answer. No
   filename/title substring fallback is used: each `Search` call already
   scopes SubDL's `languages` param to one specific requested language,
   and Sublime's pipeline dispatches one `(file, language)` pair at a
   time, so a legitimate zip for one Candidate should never need a second
   disambiguating signal beyond format.
4. Any other outcome — zero or more than one entry surviving the
   applicable rule — is a hard error, not a guess, mirroring ADR 0011's
   refusal to fall back to a positional guess for an unclassifiable
   `unpack_files` entry. Nothing observed live to date has produced more
   than one `.srt` entry in a single download's zip; the moment real
   evidence of a legitimate multi-entry case appears, this rule is the
   one to revisit — not before.

**Considered options:**

- Branch on URL shape (dash-`.zip` vs. slash) instead of sniffing content.
  Rejected: requires trusting SubDL's docs about which shape is zipped,
  the exact trust this codebase has already been burned by once (the
  original wire-format bug) and explicitly rejected once already (ADR
  0011); content-sniffing needs no such trust and is no more code.
- Silently pick the first or largest `.srt` entry when more than one
  matches, instead of erroring. Rejected: no live case has ever shown more
  than one legitimate `.srt` entry, so any tie-break policy would be pure
  speculation; guessing wrong here means confidently syncing the wrong
  subtitle rather than a visible failure.
- Use a filename/title substring match as a movie disambiguation
  fallback. Rejected: `Search` already scopes results to one requested
  language per call, so there is no known scenario requiring it; adding it
  speculatively would be an untested code path for a situation never
  observed.
