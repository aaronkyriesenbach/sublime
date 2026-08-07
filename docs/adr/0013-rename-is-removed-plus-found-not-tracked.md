# A rename/move is treated as Removed-at-old-path + Found-at-new-path, not tracked as a rename

Detecting file deletion (issue #60) requires reacting to fsnotify events for
paths that disappear. A rename/move within a watched Library (`os.Rename`)
fires exactly the same shape of event fsnotify gives any other departure
from a path — an `IN_MOVED_FROM`-backed event at the old path — and a
separate, independent `Create` at the new path. Correlating the two into
"this was actually the same file" would need fsnotify's internal
`MOVED_FROM`/`MOVED_TO` cookie, which the version in use (v1.9.0) tracks
internally but does not expose on its public `Event` type, and no
substitute correlation (e.g. matching Content Hash against a
just-Removed row within some time window) exists elsewhere in the
codebase today.

Sublime therefore makes no attempt to recognize a rename as a rename: the
old path is Removed (its row deleted) and the new path is Found (a brand
new row), even when the underlying file — and its Content Hash — never
actually changed. A file that was fully Synced before being renamed goes
through Strip, Provider search, and Sync again from scratch at its new
path, on the next dispatch pass.

**Considered options:**

- Build a heuristic to recognize a same-file rename (e.g. pair a Removed
  path with the next Found path sharing its Content Hash within a short
  window) and carry the old row's Content Hash/Sync Status/Marker
  forward under the new path. Rejected for now: no existing mechanism in
  the codebase does this kind of cross-event correlation, it would need
  its own edge-case analysis (multiple simultaneous renames, a rename
  racing a real content change), and nothing in issue #60 asked for it.
  Revisit if renamed-file resyncs turn out to be a real operational
  annoyance rather than a theoretical one.
