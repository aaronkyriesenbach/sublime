# syntax=docker/dockerfile:1
#
# Two-stage, linux/amd64-only image (see issue #19 for the full decision
# record; issues #14 and #27 for the alass packaging and base-image
# research it builds on).
#
# Both base images below are pinned by tag *and* digest (resolved
# 2026-08-03) so a rebuild months from now can't silently pick up a
# different Alpine patch release or Go point release underneath it.
# Refresh both the tag and digest together, deliberately, rather than
# letting either float.

# --platform=linux/amd64 is hardcoded deliberately, not left to the build
# host's native arch: this image is amd64-only (alass has no arm64 build,
# see issue #14), so pinning here guarantees a correct amd64 image even
# when built on an arm64 host. BuildKit warns on a constant --platform
# value ("FromPlatformFlagConstDisallowed") — that warning is expected
# and intentionally not addressed.
FROM --platform=linux/amd64 golang:1.26.5-alpine3.24@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# CGO_ENABLED=0: internal/store uses modernc.org/sqlite (pure Go, no CGO),
# so the builder stage never needs a C toolchain.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/sublime ./cmd/sublime

FROM --platform=linux/amd64 alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# ffmpeg: alass shells out to real ffmpeg/ffprobe binaries for audio
# extraction (see issue #14) — alpine's package is far leaner than
# debian's transitive dependency tree (see issue #27).
# gcompat: lets the prebuilt glibc alass-linux64 release binary run under
# musl-based Alpine (verified working end-to-end in issue #27).
RUN apk add --no-cache ffmpeg gcompat curl

# alass v2.0.0 (2019-10-10, dormant upstream, no releases since): fetched
# at build time rather than vendored, verified against a hardcoded SHA256
# (no upstream signing, so the checksum pin is the tamper/CDN-swap guard),
# then renamed from its literal release filename `alass-linux64` to
# `alass` — the release asset's name does not match the invocation name
# Sublime shells out to (see issue #14).
ARG ALASS_VERSION=v2.0.0
ARG ALASS_SHA256=7bd0b9ae7e035d3ba940eacffb21243614df36231d47f21f0b4ce42001ab7fcd
RUN curl -fL -o /tmp/alass-linux64 \
      "https://github.com/kaegi/alass/releases/download/${ALASS_VERSION}/alass-linux64" \
    && echo "${ALASS_SHA256}  /tmp/alass-linux64" | sha256sum -c - \
    && install -m 0755 /tmp/alass-linux64 /usr/local/bin/alass \
    && rm -f /tmp/alass-linux64 \
    && apk del curl

COPY --from=builder /out/sublime /usr/local/bin/sublime

# Fixed non-root user/group, not runtime-remapped (PUID/PGID deferred past
# v1) — operators bind-mounting Library/data/config paths must ensure
# those paths are accessible to 1000:1000.
RUN addgroup -g 1000 sublime \
    && adduser -D -H -u 1000 -G sublime sublime \
    && mkdir -p /config /data \
    && chown -R sublime:sublime /config /data

USER 1000:1000

# Bare liveness check against the /health route (see internal/api) —
# busybox wget ships in Alpine's base, no extra package needed.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O- http://localhost:8080/health || exit 1

ENTRYPOINT ["sublime"]
CMD ["serve"]
