# SubDL result coverage comes from paging, not from asking for full_season

Live-testing against the operator's real library found that SubDL's default
`/subtitles` search page (10 results) frequently truncates away an
individual-episode or movie upload that already exists: a full-season pack,
or simply a less-relevant upload, sorts ahead of it in SubDL's default
ordering, so Sublime's scoring pipeline never even sees the good result.
What looked like "SubDL has nothing for this episode" was really "SubDL has
something, but Sublime never asked for enough of the response to see it."

Two ways to close that gap were on the table: ask SubDL for its
`full_season` results directly (a single param, `full_season=1`), or fetch
more of the ordinary per-item response via SubDL's page size and pagination
params (`subs_per_page`, `page`). Sublime does the latter: every `Search`
call requests `subs_per_page=30` (SubDL's documented maximum) and walks
additional pages — via the response's own `totalPages`/`currentPage`
fields — until the response reports no further pages exist, up to a fixed
cap of 3 pages total, merging every page's results into the one candidate
list `Search` returns. `full_season` is never sent, for either a movie or a
TV query.

The two approaches solve overlapping but distinct problems. `full_season`
would surface the pack itself as a `Candidate` — a whole-`.zip`, per-show
item unrelated to any specific episode Sublime's Downloader can act on
directly (season-pack unpacking, when it exists, is separate work — see
issue #79/ADR 0011). Paging, by contrast, directly widens the plain
per-item response Sublime already parses today, with no new item shape to
handle: the fix already covers the concrete regression this was found from
(a real individual upload existing but sorted past position 10), and it
applies uniformly to movie and TV queries alike, whereas `full_season` only
means anything for a TV query. Paging was chosen as the narrower, lower-risk
fix for the coverage gap actually observed; `full_season` remains available
to revisit specifically for season-pack unpacking, on its own terms.

The page cap (`maxSearchPages = 3`, i.e. up to 90 results per `Search` call
at 30/page) was set from the same live-testing, not derived from
documentation: it comfortably covers the volume of per-title results
observed even for heavily-subtitled shows, while keeping a worst-case
`Search` call bounded rather than walking every page SubDL is willing to
report. A `Search` whose first page already satisfies `totalPages` — the
common, today's-behavior case — still issues exactly one request; the cap
only matters for the minority of titles with enough uploads to need it.

If any page request in the walk fails, `Search` returns that error for the
whole call and discards any candidates already gathered from earlier pages
in the same call — no partial-success return shape. This mirrors how a
single-page `Search` failure has always been handled; a mid-walk failure is
just as much a real failure as a first-page one, and returning a partial
list would silently hide from callers (and from scoring) that more results
existed but couldn't be fetched.
