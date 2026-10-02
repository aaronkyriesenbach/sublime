# Running the whisper sidecar

Sublime can generate subtitles from a video's own audio instead of (or as well
as) downloading them. Transcription runs in a separate
[whisper.cpp](https://github.com/ggml-org/whisper.cpp) server container (the
"sidecar") that Sublime reaches over HTTP. This page covers starting the
sidecar, choosing a model and image, and what to expect for speed. For chain
ordering see the [README](../README.md#whisper-generated-subtitles).

## Starting the sidecar

[`docker-compose.yml`](../docker-compose.yml) has an optional `whisper`
service (and a one-shot `whisper-models` service that downloads the models)
behind the `whisper` compose profile:

```sh
docker compose --profile whisper up -d
```

1. `whisper-models` downloads the speech model (`ggml-small-q5_1.bin`) and the
   Silero VAD model (`ggml-silero-v5.1.2.bin`) from Hugging Face into
   `./whisper-models`, skipping files that already exist.
2. `whisper` starts `whisper-server` with that model, listening on port 8080
   inside the compose network, with `-t 4` threads (the server's default is
   `min(4, cores)`; raise it to the cores you can spare, though speed flattens
   after about 4), non-speech suppression (`--suppress-nst`, drops tokens such
   as `[MUSIC]`) and `-nlp` (`--no-language-probabilities`, so a chunk with no
   speech returns an empty result instead of an HTTP 500 when `--vad` is on).
   These are start-up settings of the server; Sublime cannot set them per
   request. The service has a healthcheck on `GET /health`, which answers 503
   while the model loads.
3. Point Sublime at it in `config.yaml`:

   ```yaml
   providers:
     chain: [whisper]
     whisper:
       endpoint: http://whisper:8080
   ```

   There are no API keys. `docker-compose.yml`'s `env_file: .env` still has to
   exist for the `sublime` service; it can be empty when `whisper` is the only
   Provider.

### Optional: voice activity detection

The compose file ships with `--vad` off. Uncommenting the `--vad` and
`--vad-model` lines of the `whisper` service skips silence and music before
decoding, which made `small` about 25% faster (26.7 s vs 35.9 s per 10-minute
chunk at 12 threads) and avoided the repeated-line loops that the no-VAD run
produced in 2 of 9 chunks of a 92-minute film. Without it, expect occasional
non-speech hallucinations, which Sublime's sanity check rejects and retries.

`--vad` makes whisper.cpp report word timestamps on the shortened, speechless
timeline, so cues would land far too early (upstream
[whisper.cpp#3174](https://github.com/ggml-org/whisper.cpp/issues/3174)).
Sublime's whisper Provider shifts each segment's word times back onto the real
timeline, so enabling it is safe, with about half a second of timing jitter
that comes with VAD remapping. The VAD model is already downloaded by
`whisper-models`.

Without the `whisper` profile, `docker compose up -d` does not start the
sidecar. If the sidecar is down or restarting, Sublime marks the whisper
Provider Suspended (cause: Unavailable) and pairs waiting on it stay Pending;
it resumes by itself when the sidecar answers again.

The sidecar handles one request at a time, so `worker_count` defaults to 1.
Raising it only queues requests behind one another unless you run several
sidecars.

## Choosing a model

| Hardware | Model | Why |
| --- | --- | --- |
| CPU only | `small`, quantized (`ggml-small-q5_1.bin`) | Runs at about 3-22x real time on the measured CPU depending on threads, about 590 MiB |
| GPU | `large-v3-turbo` (`ggml-large-v3-turbo.bin`, or a quantized `-q5_0`) | Best accuracy that still runs fast on a GPU (not measured here) |

`large-v3-turbo-q5_0` on a CPU ran at 0.7-5.8x real time depending on threads
(about 980 MiB), so it only makes sense on a CPU with many fast cores. A host
slower than about 0.5x real time should pick a smaller model rather than a
shorter `chunk_length`: shorter chunks make each retry cheaper, not the work
faster. To change model, download another file from
<https://huggingface.co/ggerganov/whisper.cpp/tree/main> into
`./whisper-models` and update the `-m` flag (and the download line) in
`docker-compose.yml`.

Use an English-only model (for example `ggml-small.en-q5_1.bin`) only if
every library is English: Sublime always tells whisper which language to
transcribe, and a `.en` model can only transcribe English.

## GPU image variants

The sidecar is the only thing that changes; Sublime's image and config stay
the same. Replace the `image:` of the `whisper` service and give the container
access to the device.

| Variant | Use when | Image tag | Container access |
| --- | --- | --- | --- |
| CPU | No supported GPU, or a small NAS | `ghcr.io/ggml-org/whisper.cpp:main` | none |
| CUDA | NVIDIA GPU | `ghcr.io/ggml-org/whisper.cpp:main-cuda` | NVIDIA Container Toolkit; `deploy.resources.reservations.devices` with `driver: nvidia`, or `gpus: all` |
| Vulkan | AMD or Intel GPU where ROCm or oneAPI is unavailable; the most portable GPU option | `ghcr.io/ggml-org/whisper.cpp:main-vulkan` | `devices: [/dev/dri:/dev/dri]` |
| ROCm | Supported AMD GPU | no published image (the repository has a ROCm Dockerfile that is not published); build whisper.cpp with `GGML_HIP=1` | `devices: [/dev/kfd, /dev/dri]` and `group_add: [video]` |
| Intel (oneAPI/SYCL) | Intel Arc or integrated GPU | `ghcr.io/ggml-org/whisper.cpp:main-intel` | `devices: [/dev/dri:/dev/dri]` |

The CPU, CUDA, Vulkan and Intel tags are published (alongside `main-musa`,
`main-arm64` and `main-vulkan-arm64`); only the CPU image was run when the
numbers below were measured. `main` is a moving tag with no version tags: pin
by digest or a `main-<commit>` tag for reproducibility. Tags are listed at
<https://github.com/ggml-org/whisper.cpp/pkgs/container/whisper.cpp>.

## Measured throughput

Measured on a real `whisper-server` sidecar for one 10-minute chunk of a
public-domain film (*His Girl Friday*, 1940: fast, overlapping dialogue and
old audio), CPU only, with `--vad --suppress-nst`. Speed is a multiple of real
time (20x means a 2-hour film in 6 minutes). Full method and caveats are in
the [research note](research/whisper-server-timeouts-and-cpu-throughput.md).

Host: AMD Ryzen 9 9900X (12 cores), image `ghcr.io/ggml-org/whisper.cpp:main`,
limited with `docker run --cpus=N` and `-t N`.

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

End to end through Sublime (silence detection and chunk extraction included),
the whole 91:44 film with `small` and 12 threads took 301.8 s (18.2x) with
`--vad` and 421.2 s (13.1x) without.

Caveats:

- This is a very fast desktop CPU. `--cpus=N` limits the core count only; it
  does not reproduce a weaker core, smaller caches or slower memory, so a
  NAS or mini-PC will be slower per thread. A 4-core NAS will probably land
  near the 1-2 thread rows for `small` (about 3-7x, an extrapolation, not a
  measurement).
- GPU images were not run, and no real multi-audio-track video was tested.
- Timings are good to roughly 5-10%.
- Speed does not depend on chunk length, but memory does: about 14 MiB per
  extra minute of audio on top of the model.

Measure your own host with a 10-minute chunk (adjust `-t`); divide 600 by the
wall time for the speed multiple:

```sh
ffmpeg -ss 600 -t 600 -i film.mkv -vn -ac 1 -ar 16000 -f wav chunk.wav
docker run -d --name ws -p 8080:8080 -v ./whisper-models:/models:ro \
  --entrypoint whisper-server ghcr.io/ggml-org/whisper.cpp:main \
  --host 0.0.0.0 --port 8080 -m /models/ggml-small-q5_1.bin -t 4 --suppress-nst -nlp
time curl -s -o /dev/null -F file=@chunk.wav -F response_format=verbose_json \
  -F language=en http://127.0.0.1:8080/inference
```

The image also ships `whisper-bench`, which measures encoder speed for a
model.

Sublime cuts audio into chunks of about `chunk_length` (default 10 minutes) at
silence, and gives each request `3 x` the chunk's audio length `+ 1 minute` to
finish (31 minutes for a 10-minute chunk). The server has no time limit of its
own: its 600 s socket timeouts do not cap inference (a request ran 887 s and
succeeded), and a client disconnect aborts it. Keep the default unless you have
a reason; the timeout assumes the sidecar runs at no less than about 0.33x real
time.

## Behavior to know about

- **Same language only.** A Generated Subtitle is made only when the video's
  audio is in the language requested; Sublime never translates. Japanese
  audio with target `en` is a miss, and the pair moves on to the next Provider
  in the chain (or ends as `no_candidate` if there is none). The log says why.
- **Switching sources does not rewrite existing subtitles.** Sublime leaves a
  valid subtitle alone whichever Provider produced it, and neither source
  counts as an upgrade of the other. After changing `providers.chain`, run
  `sublime reprocess <path>` on a file, directory or library to regenerate it
  with the current chain.
- **Generated Subtitles are never Synced.** Their timing already comes from the
  video's audio, so alass is skipped.
