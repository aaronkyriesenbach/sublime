# Sublime

A self-hosted, containerized daemon that watches your media libraries and
keeps them stocked with synced subtitles. See [CONTEXT.md](CONTEXT.md) for
the full domain vocabulary and design decisions.

## Getting started

1. Copy [`config.example.yaml`](config.example.yaml) to `config.yaml` and
   list your libraries.
2. Copy `docker-compose.yml`'s volume paths to point at your real media
   directories, and set the `SUBLIME_OPENSUBTITLES_*` environment variables
   (an [OpenSubtitles.com](https://www.opensubtitles.com/) account is
   required — Sublime is a client, not a subtitle source of its own).
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

## Contributing

- Go 1.26+, no CGO (state store uses a pure-Go SQLite driver).
- `go build ./...`, `go vet ./...`, `go test ./...` should all be clean
  before opening a PR; `golangci-lint run ./...` too, if you have it
  installed.
- Issues and design decisions are tracked as GitHub issues — see
  [`docs/agents/issue-tracker.md`](docs/agents/issue-tracker.md) for the
  conventions this repo follows.
