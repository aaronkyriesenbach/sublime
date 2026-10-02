# Sublime

A self-hosted, containerized daemon that watches your media libraries and
keeps them stocked with synced subtitles. See [CONTEXT.md](CONTEXT.md) for
the full domain vocabulary and design decisions.

## Getting started

1. Copy [`config.example.yaml`](config.example.yaml) to `config.yaml` and
   list your libraries. Run `mkdir -p data` too, so the state directory is
   owned by you rather than created by Docker as root.
2. Copy `docker-compose.yml`'s volume paths to point at your real media
   directories, and set the `SUBLIME_OPENSUBTITLES_*` environment variables
   (an [OpenSubtitles.com](https://www.opensubtitles.com/) account is
   required for the default chain — Sublime is a client, not a subtitle source
   of its own). To generate subtitles from your videos' audio instead, see
   [Whisper-generated subtitles](#whisper-generated-subtitles).
3. `docker compose up -d`

Sublime starts scanning and watching your libraries immediately. Check on it
with the CLI, pointed at the running daemon:

```sh
docker compose exec sublime sublime status
docker compose exec sublime sublime libraries
docker compose exec sublime sublime reprocess /media/movies
```

Every subcommand also accepts `--json` for scripting, and the daemon exposes
the same functionality over HTTP (`/health`, `/libraries`, `/status`,
`/reprocess`) if you'd rather integrate directly.

## Whisper-generated subtitles

Besides downloading subtitles, Sublime can transcribe a video's own audio into
a subtitle using a [whisper.cpp](https://github.com/ggml-org/whisper.cpp)
server running as a sidecar container. `whisper` is a Provider like
`opensubtitles` or `subdl`: you put it in `providers.chain`, and its position
is its priority. It needs no API keys, only the sidecar's URL. Details,
model choice and measured speed are in [docs/whisper.md](docs/whisper.md).

### Quick start

1. Put `whisper` in `config.yaml` (the compose sidecar is reachable as
   `http://whisper:8080` from the `sublime` service, because both are in the
   same compose project):

   ```yaml
   providers:
     chain: [whisper]
     whisper:
       endpoint: http://whisper:8080
       worker_count: 1 # default
       chunk_length: 10m # default
   ```

2. Create the state directory and an empty `.env`. Docker would otherwise
   create `./data` owned by root, which the (non-root) Sublime container
   cannot write to, and `docker-compose.yml` requires `.env` to exist even
   with no Provider credentials:

   ```sh
   mkdir -p data && touch .env
   ```

3. Start Sublime and the sidecar. Pick one:

   ```sh
   # CPU: fine for a small library. The first start downloads a ~190 MB model.
   docker compose --profile whisper up -d

   # NVIDIA GPU: roughly 10-20x faster. Needs the NVIDIA Container Toolkit first
   # (docs/whisper.md). The first start downloads a ~1.6 GB model.
   docker compose -f docker-compose.yml -f docker-compose.gpu.yml \
     --profile whisper up -d
   ```

4. Watch it work. The sidecar's `/health` goes healthy once the model has
   loaded; `sublime status` shows progress, and the log shows each file's
   chunks (`chunk 3 of 5`). On the very first start Sublime comes up while
   the model is still downloading, so `sublime status` may list `whisper` as
   Suspended for a couple of minutes; that clears by itself once the sidecar
   answers.

   ```sh
   docker compose ps
   docker compose exec sublime sublime status
   docker compose logs -f sublime
   ```

A subtitle Sublime already made for a file is recognized by its Marker and
left alone, so a library Sublime has processed before is not redone when you
add whisper to the chain. Run `sublime reprocess` to regenerate it (see
[Switching sources](#switching-sources)). Other existing subtitles are
replaced, as with any Provider.
Expect a 45-minute episode to take about 20 seconds on an RTX 4080 SUPER and
4 to 8 minutes on 4 CPU threads.

### Chain examples

A bare name is its own Tier, and Tiers are tried in order. A nested list is one
Tier of Providers you trust equally.

| `providers.chain` | Behavior |
| --- | --- |
| `[whisper]` | Whisper only. Every subtitle is generated, and no OpenSubtitles or SubDL account is needed. Audio not in the requested language ends as `no_candidate`. |
| `[whisper, opensubtitles]` | Prefer generated subtitles; fall back to OpenSubtitles when whisper can't produce one (for example, the audio is in another language, or the transcript fails its sanity check). |
| `[opensubtitles, whisper]` | Prefer online subtitles; generate one only when OpenSubtitles has no match. |
| `[[whisper, opensubtitles]]` | One shared Tier: both are equally trusted, whisper is preferred while it is healthy, and OpenSubtitles may substitute for it. |

How capacity and Suspension interact with that order (see
[ADR 0008](docs/adr/0008-tiered-provider-chain.md) and
[ADR 0015](docs/adr/0015-capacity-never-crosses-a-tier.md)):

- Neither a Suspended Provider nor a busy one ever moves a pair to the next
  Tier. In `[whisper, opensubtitles]`, while the sidecar is unreachable
  (Suspended, cause: Unavailable) or its one worker is busy transcribing, other
  pairs stay Pending and wait for whisper rather than being given online
  subtitles. Only a miss (no usable result from whisper) advances the pair to
  OpenSubtitles.
- Likewise in `[opensubtitles, whisper]`, an OpenSubtitles quota Suspension
  makes pairs wait; they do not fall through to whisper early.
- A shared Tier is the way to allow substitution. In `[[whisper,
  opensubtitles]]`, a pair goes to OpenSubtitles when whisper is Suspended or
  all its workers are busy. Since whisper takes one file at a time, this
  lets a backlog be served by both at once, with whisper still taking its
  share.
- Whisper makes subtitles only in the language the audio is spoken in; it
  never translates. Each (file, language) pair is evaluated separately, so
  English audio gets an English subtitle from whisper while the same file's
  Portuguese pair advances to the next Provider.
- A Generated Subtitle is never Synced, since its timing comes from the audio.

### Switching sources

Changing `providers.chain` does not rewrite existing subtitles: a valid
subtitle stays as it is whichever Provider produced it, and neither source
counts as an upgrade of the other. To redo subtitles with the current chain,
run `sublime reprocess` on a file, directory or library:

```sh
docker compose exec sublime sublime reprocess /media/movies
```

## Contributing

- Go 1.26+, no CGO (state store uses a pure-Go SQLite driver).
- `go build ./...`, `go vet ./...`, `go test ./...` should all be clean
  before opening a PR; `golangci-lint run ./...` too, if you have it
  installed.
- Issues and design decisions are tracked as GitHub issues — see
  [`docs/agents/issue-tracker.md`](docs/agents/issue-tracker.md) for the
  conventions this repo follows.
