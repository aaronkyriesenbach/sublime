# whisper.cpp sidecar: timeout behavior, abort-on-disconnect, CPU chunk throughput, and what actually works

**Status**: research note, not an ADR. Spike [#100](https://github.com/aaronkyriesenbach/sublime/issues/100)
(part of epic #95, Whisper-generated subtitles). Written 2026-10-02 from a
real `whisper-server` run, to replace the "unverified" figures in
`docs/whisper.md` and `docker-compose.yml` and to fix the chunk-length and
timeout defaults.

Every number below was measured unless it is explicitly marked as read from
source or as an extrapolation. Source line references are to whisper.cpp
`master` at commit `60c0be6ac8fa71b1a2ae2dd938a31a34a508e774` (cloned the
same day). The image under test was built a few hours earlier, so the
source reading is a guide and the measurements are authoritative.

## Summary

| Acceptance question | Answer |
| --- | --- |
| Do the server's fixed 600 s socket timeouts cap inference time? | **No.** A single request ran 887 s and returned HTTP 200 (section 1). |
| Does a client disconnect abort inference and free the server? | **Yes**, including when Sublime's own context deadline fires. Abort latency is under 1 s on a fast host and up to about 35 s on a very slow one (section 2). |
| Wall-clock and memory for a 10-minute chunk, `small` and `large-v3-turbo` (quantized)? | `small-q5_1`: 26.7 s with 12 threads, 171 s with 1 thread, about 590 MiB. `large-v3-turbo-q5_0`: 103 s with 12 threads, 887 s with 1 thread, about 980 MiB. Host: Ryzen 9 9900X (section 3). |
| Default chunk length and timeout rule? | Keep **10 minutes** and keep the implemented rule **3 x chunk audio length + 1 minute** (section 4). |
| Exact sidecar invocation? | Section 5. **Two things in the current compose file are wrong**: the model download URL returns 401, and `--vad` makes every word timestamp wrong. |
| Upload-size limit? | None found, up to a 460 MB request (section 6). |

Two findings the ticket did not ask about change the design and need a
decision (section 7):

1. **`--vad` corrupts word timestamps** (cues come out 26-155 s too early), and
   Sublime's cue shaper is driven by word timestamps.
2. **With `--vad`, any request containing no speech returns HTTP 500**, which
   Sublime would count as a failed chunk and so a failed file.

## Test setup

- **Host**: AMD Ryzen 9 9900X (12 cores / 24 threads), 30 GiB RAM, NixOS,
  Docker 29.8.1. CPU-only runs: the CPU image was used and no GPU was passed
  to the container. (The host also has an RTX 4080 SUPER, deliberately
  unused.)
- **Emulating smaller machines**: `docker run --cpus=N` plus `-t N`. This
  limits core count only. It does **not** reproduce a weaker core, smaller
  caches or slower memory, so a NAS-class CPU will be slower per thread than
  these numbers (see "What still needs a human").
- **Image**: `ghcr.io/ggml-org/whisper.cpp:main`, digest
  `sha256:5814b7a33618eedc619379df427a1a3c42630ed5b16a3b9bc9b96e43ef39d38c`
  (created 2026-10-02T04:31Z, amd64, libwhisper 1.9.4).
- **Models** (SHA-256 matches Hugging Face's published LFS hashes):
  `ggml-small-q5_1.bin` (190,085,487 B), `ggml-large-v3-turbo-q5_0.bin`
  (574,041,195 B), `ggml-silero-v5.1.2.bin` (885,098 B).
- **Audio**: *His Girl Friday* (1940, public domain, archive.org item
  `his_girl_friday`, 91:44). A deliberately hard case: very fast, overlapping
  dialogue and 1940s audio. The 10-minute chunk is minutes 10:00-20:00,
  converted the way Sublime does it (16 kHz mono 16-bit PCM WAV, 19.2 MB).
  It is an MP3 rather than a video, which exercises the same ffmpeg path.
- **Request**: the same multipart fields Sublime sends
  (`response_format=verbose_json`, `language=en`), with `--vad` and
  `--suppress-nst` on unless stated.
- **Memory** is the server process's peak RSS (`VmHWM`), read after the
  request.
- Not isolated: a few runs overlapped with other containers on the same
  host, so treat timings as good to roughly 5-10%.

## 1. Do the 600 s socket timeouts cap a request?

**No.**

From source: `server.cpp:66-67` sets `read_timeout = write_timeout = 600` and
passes them to cpp-httplib (`:1253-1254`). In cpp-httplib these apply to each
individual socket read or write (`select_read` / `select_write`), not to the
handler. By the time the handler runs, the whole multipart body has been read,
and nothing reads or writes the socket until the response is sent.
`payload_max_length` is left at cpp-httplib's default, `SIZE_MAX`.

Measured: a 10-minute chunk with `large-v3-turbo-q5_0` on 1 thread
(`--cpus=1 -t 1`) took **887 s** from request to response and returned
**HTTP 200** with 385 segments. That is 287 s past the 600 s mark, over a
connection that was idle the whole time, through Docker's published-port NAT.

So the only wall-clock cap on an inference request is the one the client
imposes. Untested: reverse proxies, which commonly have 60 s default idle
timeouts. The recommended setup has none between Sublime and the sidecar.

## 2. Does a client disconnect abort inference?

**Yes.** `server.cpp:991-1003` installs an `abort_callback` that checks
whether the HTTP connection is still alive, and logs
`client disconnected, aborted processing` (returning 499) when it fires.
Measured:

| Scenario | Result |
| --- | --- |
| `small`, 4 threads, `curl` killed 20 s into a 10-minute chunk | Server logged the abort 0.7 s later; CPU 399% to 0%; the next request (2-minute clip) was served normally in 11.7 s. |
| Sublime's real provider with a 25 s context deadline mid-chunk | Aborted within 1 s of the deadline; CPU to 0%. Cancellation is Go closing the connection, the same signal as killing curl. |
| `turbo`, 1 thread, `curl` killed at 45 s and again at 100 s | Abort took **32 s** and **16.6 s** to take effect. |
| Request queued behind another, client disconnects while waiting | The server takes a global mutex first (`server.cpp:828`), so the queued request still runs when its turn comes: VAD over the whole chunk (about 3 s at 4 threads), then aborts. Cheap, but not free. |

Abort is checked inside the encoder/decoder compute, so latency is bounded
by roughly one 30 s audio window of compute. That is under a second on a fast
CPU but 16-35 s on a one-thread `turbo` run (one window of `turbo` costs
about 36 s there; a 30 s language-detection clip took 36 s). Implication: on
a very slow host, a retry sent right after a timeout waits for the previous
inference to finish aborting before it starts. The `+ 1 minute` in the
timeout rule below covers that.

## 3. Wall-clock time and memory, 10-minute chunk, CPU only

Host: Ryzen 9 9900X; `--cpus=N -t N`; `--vad --suppress-nst` on. "Speed" is
audio seconds per wall second.

| Model | Threads | Wall time | Speed | Peak RSS |
| --- | --- | --- | --- | --- |
| `small-q5_1` | 1 | 171.2 s | 3.50x | 589 MiB |
| | 2 | 89.7 s | 6.69x | 589 MiB |
| | 4 | 50.2 s | 11.95x | 588 MiB |
| | 8 | 32.0 s | 18.74x | 587 MiB |
| | 12 | 26.7 s | 22.44x | 588 MiB |
| `large-v3-turbo-q5_0` | 1 | 887.1 s | 0.68x | 977 MiB |
| | 2 | 386.9 s | 1.55x | 977 MiB |
| | 4 | 210.8 s | 2.85x | 976 MiB |
| | 8 | 113.3 s | 5.29x | 977 MiB |
| | 12 | 103.0 s | 5.83x | 976 MiB |

Observations:

- Scaling is near-linear to about 4 threads, then flattens: 8 to 12 threads
  buys only 1.1-1.2x. whisper-server's **default is `-t min(4, cores)`**, so
  an operator with more cores gets 4 unless they set `-t`.
- Memory is dominated by the model, not threads. It does grow with chunk
  length: a 30-minute `small` chunk (12 threads, 22.0x, 81.8 s) peaked at
  865 MiB versus 588 MiB for 10 minutes, about 14 MiB per extra minute of
  audio. Chunking therefore does bound memory, as the epic assumed.
- A 30-minute chunk ran at the same speed as a 10-minute one (22.0x vs
  22.4x), so chunk length costs nothing in throughput. It is a
  robustness/latency choice only.
- `--vad` made `small` about 25% faster at 12 threads (26.7 s vs 35.9 s).

**End to end through Sublime's real `whisper` provider** (real ffprobe/ffmpeg
audio source, real sidecar, language detection from an untagged stream, 10
minute target chunks cut at silence), the whole 91:44 film with `small`,
12 threads:

| Sidecar flags | Download time | Overall speed | Chunks rejected and retried |
| --- | --- | --- | --- |
| `--vad` (timestamps wrong, see 7a) | 301.8 s | 18.2x | 0 of 9 |
| no VAD | 421.2 s | 13.1x | 2 of 9 (repeated-line loops: "why won't they listen to me", "I'm not going to let you know") |

Planned chunks came out 9.6-11.8 minutes long (cut at silence).
Silence detection and extraction cost roughly 15-20% of the pure
inference speed.

Extrapolating from the table (not measured): the speeds above are for a very
fast desktop core. A typical 4-core NAS/mini-PC CPU will probably land
nearer the 1-2 thread rows for `small` (about 3-7x) and below the 1-thread
row for `turbo`, which would make `turbo` impractical there. The
`docs/whisper.md` claim "`small` is the largest model likely to run at
several times real time" is consistent with this.

## 4. Recommended chunk length and timeout rule

**Chunk length: keep the 10-minute default.** Throughput does not depend on
it, and 10 minutes keeps requests well under the point where a single failure
costs a lot. The slowest measured request (`turbo`, 1 thread) took 14.8
minutes, which is acceptable because nothing on the server side limits it
(section 1). A host slower than about 0.5x real time should use a smaller
model rather than a much shorter chunk: shorter chunks do not make the work
go faster, only make each retry cheaper.

**Timeout rule: keep `3 x chunk audio length + 1 minute`** (already
implemented in `internal/provider/whisper/chunking.go`, `chunk.timeout`).
Reasoning:

- The server imposes no limit, so the client timeout exists only to catch a
  wedged sidecar. A false timeout is expensive (the chunk's work is thrown
  away and redone), so it should be generous.
- The slowest configuration measured needed 1.47x the chunk length. The rule
  allows 3x, which is 2x headroom over that worst case and about 36x over
  `small` on 4 threads. It assumes at least about 0.33x real time.
- The `+ 1 minute` covers upload, the VAD pass (a few seconds per 10
  minutes) and the abort tail from a prior timed-out request (section 2).
- For a 10-minute chunk this is 31 minutes, comfortably above 887 s.

The 3-minute language-detection timeout (`detectionTimeout`) is also
adequate: the slowest measured detection (`turbo`, 1 thread, 30 s clip) took
36 s, and detection is on the same single worker so it never queues behind a
transcription.

## 5. The sidecar invocation that works

The image's `ENTRYPOINT` is `bash -c`, so compose must override it
(`entrypoint: ["whisper-server"]`; binaries are in `/app/build/bin`, on
`PATH`). The image also contains `whisper-bench`, `whisper-cli` and ffmpeg.

Published CPU/GPU tags (checked against the registry and
`.github/workflows/docker.yml`): `main`, `main-cuda`, `main-vulkan`,
`main-intel`, `main-musa`, `main-arm64`, `main-vulkan-arm64`. There is **no
ROCm image** (a `main-rocm.Dockerfile` exists but is not published), matching
what `docs/whisper.md` already says. `main` is a moving tag with no version
tags; pin by digest (above) or the `main-<commit>` tags for reproducibility.

**Model download.** `docker-compose.yml`'s `whisper-models` service downloads
`https://huggingface.co/ggml-org/whisper.cpp/resolve/main/ggml-small-q5_1.bin`.
That repository does not exist (HTTP 401, curl exit 22; `set -e` makes the
service fail and the sidecar never starts). The speech models live in
**`ggerganov/whisper.cpp`**, which is also what whisper.cpp's own
`models/download-ggml-model.sh` uses. The VAD model URL
(`ggml-org/whisper-vad`) is correct.

```sh
# speech models (ggerganov/whisper.cpp, NOT ggml-org/whisper.cpp)
curl -fL -O https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small-q5_1.bin
curl -fL -O https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo-q5_0.bin
# VAD model (only needed if VAD is enabled)
curl -fL -O https://huggingface.co/ggml-org/whisper-vad/resolve/main/ggml-silero-v5.1.2.bin
```

**Invocation that works end to end with Sublime's provider today**
(verified with a corrected copy of the compose file: models download, the
service reaches `healthy`, and a transcription request returns 200):

```yaml
whisper:
  image: ghcr.io/ggml-org/whisper.cpp:main   # pin by digest for reproducibility
  entrypoint: ["whisper-server"]
  command:
    - --host
    - 0.0.0.0
    - --port
    - "8080"
    - -m
    - /models/ggml-small-q5_1.bin
    - -t
    - "4"            # default is min(4, cores); set to the cores you can spare
    - --suppress-nst # drop [MUSIC]-style tokens
    - -nlp           # --no-language-probabilities, see 7b; harmless without VAD
  volumes:
    - ./whisper-models:/models:ro
  healthcheck:
    test: ["CMD", "curl", "-fsS", "http://localhost:8080/health"]  # 503 while the model loads
    interval: 10s
    timeout: 3s
    retries: 6
    start_period: 20s
```

This deliberately **omits `--vad`**. Adding `--vad --vad-model
/models/ggml-silero-v5.1.2.bin` is faster (about 25%) and removes the
repeated-line loops, but is **not safe with the current Provider** until the
timestamp issue in 7a is fixed.

Non-speech suppression flag: `--suppress-nst` (`-sns`); I ran everything with
it on and did not isolate its effect.

Other facts confirmed against the real server:

- Language detection works with and without VAD. The Provider's request
  (`response_format=json`, `language=auto`, `detect_language=true`) returns
  `{"text":"","language":"french"}`: an English **name**, not a code, which
  the Provider's `detectedMatches` already handles.
- `GET /health` returns `{"status":"ok"}` once the model is loaded, 503
  before.
- The server holds one inference at a time (global mutex, `server.cpp:828`).
- Forcing `language=en` on French audio does not fail or skip: whisper
  produces **English text, i.e. a translation**. ADR 0014's "never translate"
  guarantee therefore rests entirely on Sublime's eligibility check (audio
  tag or detection) and not on the request's `language` field.

## 6. Upload size

No limit hit. `payload_max_length` is unset (`SIZE_MAX`). The server accepted
a 57.6 MB request (30 minutes), a 115 MB request (1 hour) and a 460.8 MB
request (4 hours). The last two were all-silence files and failed afterwards
for the unrelated reason in 7b, not on size. A 10-minute chunk is 19.2 MB
(1.92 MB per minute of audio). On the server, peak RSS for the 4-hour
request was 2,087 MiB, which is why chunking (not size limits) is what
protects a small host's memory. Sublime itself holds each chunk's WAV in
memory once for extraction and again in the multipart body, so about 40 MB
for a 10-minute chunk.

## 7. Problems found that need a decision

### 7a. `--vad` makes `words[]` timestamps wrong

With `--vad`, whisper.cpp removes non-speech audio before decoding. Segment
`start`/`end` are mapped back to the original timeline, but the **per-token
`words[].start/end` are not**: they stay on the shortened timeline
(`server.cpp` reads `token.t0/t1` directly; this is upstream
[ggml-org/whisper.cpp#3174](https://github.com/ggml-org/whisper.cpp/issues/3174),
closed as stale in 2026 without a fix, and reproduced on today's image).

Evidence, 10-minute chunk:

- On the film's first two minutes, segment 1 starts at 100.5 s (correct,
  matches `silencedetect`), but its first word starts at 0.0 s.
- Over the 10:00-20:00 chunk, `segment.start - first_word.start` ranges from
  0 to 97.5 s (median 57 s), growing as more non-speech is removed.
- Sublime's shaper builds cues from word timestamps. On the whole film, with
  VAD, only **1%** of cues landed within 1 s of the same cue produced
  without VAD; median error 45 s, p90 159 s. The first cue of the film was
  placed at 0:00 although the first speech is at 1:40.
- Without VAD, word and segment times agree to within 0.03 s.

Options:

1. Run the sidecar without `--vad` (what section 5 does). Costs about 25%
   speed and brings back non-speech hallucinations: repeated-line loops in 2
   of 9 chunks (both recovered by the Provider's temperature retry) and a
   spurious "The End." cue over the title music.
2. Keep `--vad` and fix the Provider: shift each segment's tokens by
   `segment.start - first_token.start`. Tested offline on the real output:
   error versus the non-VAD run drops from median 50 s to **0.45 s** (p90
   1.5 s). That is the same accuracy as the segment timestamps themselves
   (median 0.5 s), so about half a second of jitter is inherent to VAD
   remapping. A segment spanning two VAD spans is slightly off after the gap.
   Needs a Provider change and a test; recommended because it keeps the
   speed and loop-avoidance benefits of VAD.

### 7b. With `--vad`, a request with no speech returns HTTP 500

If VAD finds zero speech (silence, a tone, or the film's title music), a
`verbose_json` request returns **500** with the plain-text body
`basic_string::_M_construct null not valid` (observed for 30 s silence, 30 s
tone, the first 60 s of the film and 1 h of silence; failing after 0.05-23 s). It is the
language-probability code calling `whisper_lang_str_full(-2)`. Without VAD
the same inputs return 200 with empty or near-empty segments.

This matters because `transcribeChunk` accepts an empty chunk as "no speech"
(a long film can have a wordless stretch), but it only does so for a 200
response. A 500 would be retried with backoff, then count as a failed
request and fail the whole file. Any chunk of pure music or silence, for
example long end credits, would hit it.

Fix, tested: pass **`-nlp` / `--no-language-probabilities`** at server start
(or `no_language_probabilities=true` per request). All four no-speech inputs
then return 200 with `"segments":[]`, and normal requests are unchanged. The
per-request field is the more robust fix because it does not depend on how an
operator started their own sidecar; the Provider's client could send it on
every transcription request.

A related quirk: language detection on a no-speech clip with VAD returns
`"english"` (the default), not an error, so a speechless detection clip can
falsely match an `en` target. The downstream empty-transcript check catches
it, so this is low impact.

## What still needs a human

- **Real NAS / low-power hardware.** The core-count emulation above cannot
  reproduce a weaker CPU. To measure the actual host (copy-paste, adjust
  `-t`; the 10-minute chunk is `ffmpeg -ss 600 -t 600 -i film.mkv -vn -ac 1
  -ar 16000 -f wav chunk.wav`):

  ```sh
  docker run -d --name ws -p 8080:8080 -v ./whisper-models:/models:ro \
    --entrypoint whisper-server ghcr.io/ggml-org/whisper.cpp:main \
    --host 0.0.0.0 --port 8080 -m /models/ggml-small-q5_1.bin -t 4 --suppress-nst -nlp
  time curl -s -o /dev/null -F file=@chunk.wav -F response_format=verbose_json \
    -F language=en http://127.0.0.1:8080/inference
  ```

  Divide 600 by the wall time for the speed multiple. `whisper-bench` is also
  in the image.
- **GPU images.** Not run. The host has an NVIDIA GPU, so the `main-cuda`
  numbers in `docs/whisper.md` could be checked with the NVIDIA Container
  Toolkit and a multi-GB image pull.
- **A real video file** with several audio tracks and ISO 639-2 tags,
  instead of an MP3 with an untagged track, to confirm stream selection
  against the sidecar. The Provider's tag matching is already covered by its
  unit tests.
- **Reverse proxies.** If anyone puts a proxy between Sublime and the
  sidecar, idle-timeout and body-size settings are untested.

## Corrections this implies elsewhere

Not made in this change (the ticket's footprint is one doc):

- `docker-compose.yml`: fix the model URL to `ggerganov/whisper.cpp`, drop
  `--vad` (or fix 7a first), add `-nlp`, add `-t`, add the health check, and
  remove the "UNVERIFIED" banner.
- `docs/whisper.md`: remove the "Unverified until spike #100" block, replace
  the throughput table with section 3 (measured) and link this note, note
  that `-t` defaults to 4, and say `whisper-bench` ships in the image.
- Provider (follow-up ticket): the token-shift fix (7a) and per-request
  `no_language_probabilities=true` (7b).

## References

- whisper.cpp `examples/server/server.cpp` and `examples/server/httplib.h`
  (cpp-httplib 0.20.0) at `60c0be6ac8fa71b1a2ae2dd938a31a34a508e774`;
  `.devops/main.Dockerfile`; `.github/workflows/docker.yml`;
  `models/download-ggml-model.sh`.
- whisper.cpp issue [#3174](https://github.com/ggml-org/whisper.cpp/issues/3174)
  (VAD token timestamps); [#3918](https://github.com/ggml-org/whisper.cpp/issues/3918)
  (server and silent WAV with VAD, closed as not reproducible; a different
  symptom from 7b).
- Hugging Face `ggerganov/whisper.cpp` and `ggml-org/whisper-vad` file trees
  (sizes and SHA-256 compared against local downloads).
- Sublime's own `internal/provider/whisper` (`chunking.go`, `whisper.go`,
  `client.go`, `sanity.go`, `shape.go`) and `internal/audiosource`, run
  unmodified against the real sidecar through a throwaway harness (not
  committed).

## Bottom line

whisper-server's 600 s timeouts do not limit inference, a disconnect cleanly
aborts it, and a 10-minute chunk costs 27-171 s (`small`) or 103-887 s
(`turbo`) depending on threads, so the existing 10-minute default and
3x-plus-one-minute timeout rule stand. But the sidecar setup written so far
does not work: the model URL is wrong, and `--vad`, which the design leans
on, shifts every word timestamp and turns a speechless chunk into a 500.
Until the Provider is changed to cope with both, run the sidecar without
`--vad` and with `-nlp`.
