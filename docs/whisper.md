# Running the whisper sidecar

Sublime can generate subtitles from a video's own audio instead of (or as well
as) downloading them. Transcription runs in a separate
[whisper.cpp](https://github.com/ggml-org/whisper.cpp) server container (the
"sidecar") that Sublime reaches over HTTP. This page covers starting the
sidecar, choosing a model and image, and what to expect for speed. For chain
ordering see the [README](../README.md#whisper-generated-subtitles).

> **Unverified until spike #100 completes.** The exact `whisper-server`
> invocation, the image tags, the model file names and every throughput figure
> below were written from whisper.cpp's documentation and published
> benchmarks, not from running them. Spike
> [#100](https://github.com/aaronkyriesenbach/sublime/issues/100) (start a
> real sidecar, confirm long requests are not cut off by its socket timeouts,
> measure CPU chunk throughput) has not been done yet; this page will be
> corrected from its findings. Until then, if the sidecar does not start,
> check the [whisper.cpp server
> README](https://github.com/ggml-org/whisper.cpp/tree/master/examples/server)
> for the current flags and image tags.

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
   inside the compose network. Voice activity detection (`--vad`) and
   non-speech suppression (`--suppress-nst`) are enabled: they stop whisper
   from inventing text over silence, music and effects. Both are start-up
   settings of the server; Sublime cannot set them per request.
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
| CPU only | `small`, quantized (`ggml-small-q5_1.bin`) | Small enough for a NAS, and the largest model likely to run at several times real time on a modern CPU |
| GPU | `large-v3-turbo` (`ggml-large-v3-turbo.bin`, or a quantized `-q5_0`) | Best accuracy that still runs fast on a GPU |

`medium`, `large` and `turbo` on a CPU are expected to run around real time or
slower (extrapolated, not measured), which is rarely practical for a library.
To change model, download another file from
<https://huggingface.co/ggml-org/whisper.cpp/tree/main> into `./whisper-models`
and update the `-m` flag (and the download line) in `docker-compose.yml`.

Use an English-only model (for example `ggml-small.en-q5_1.bin`) only if
every library is English: Sublime always tells whisper which language to
transcribe, and a `.en` model can only transcribe English.

## GPU image variants

The sidecar is the only thing that changes; Sublime's image and config stay
the same. Replace the `image:` of the `whisper` service and give the container
access to the device.

| Variant | Use when | Image tag (unverified) | Container access |
| --- | --- | --- | --- |
| CPU | No supported GPU, or a small NAS | `ghcr.io/ggml-org/whisper.cpp:main` | none |
| CUDA | NVIDIA GPU | `ghcr.io/ggml-org/whisper.cpp:main-cuda` | NVIDIA Container Toolkit; `deploy.resources.reservations.devices` with `driver: nvidia`, or `gpus: all` |
| Vulkan | AMD or Intel GPU where ROCm or oneAPI is unavailable; the most portable GPU option | `ghcr.io/ggml-org/whisper.cpp:main-vulkan` | `devices: [/dev/dri:/dev/dri]` |
| ROCm | Supported AMD GPU | no official image known; build whisper.cpp with `GGML_HIP=1` | `devices: [/dev/kfd, /dev/dri]` and `group_add: [video]` |
| Intel (oneAPI/SYCL) | Intel Arc or integrated GPU | `ghcr.io/ggml-org/whisper.cpp:main-intel` | `devices: [/dev/dri:/dev/dri]` |

Which of these tags the project actually publishes is unverified; list the
tags at <https://github.com/ggml-org/whisper.cpp/pkgs/container/whisper.cpp>
and check [whisper.cpp's `.devops`
directory](https://github.com/ggml-org/whisper.cpp/tree/master/.devops) before
relying on one.

## Expected throughput

Speed is stated as a multiple of real time (40x means a 2-hour film in about
3 minutes). These are ranges from published benchmarks for other hardware, not
measurements of Sublime or of this compose setup:

| Hardware class | Model | Expected speed | Basis |
| --- | --- | --- | --- |
| Mid-range NVIDIA GPU | `large-v3-turbo` | about 40x real time or better (a film in a few minutes) | published benchmarks |
| Modern 8-core CPU | `small` | about 6-8x real time (about 15-20 minutes per film) | published benchmarks |
| CPU | `medium` or `turbo` | about 1-2x real time | extrapolated, not measured |
| N100-class mini PC, integrated GPU | any | unknown | no credible benchmark found |

For a 375-hour backlog that is roughly 9 hours at 40x, 62 hours at 6x and 15
days at 1x. Results vary a lot with CPU generation, thread count, quantization
and the audio itself; do not plan around these numbers without measuring.
whisper.cpp ships a `whisper-bench` tool (also in its container images) that
measures encoder speed for a model on the host you will actually run on
(build it from the whisper.cpp source; whether the container images include it
is unverified).

Sources:

- [faster-whisper README benchmark tables](https://github.com/SYSTRAN/faster-whisper#benchmark)
  and [faster-whisper issue 1030](https://github.com/SYSTRAN/faster-whisper/issues/1030)
- whisper.cpp [benchmark scripts](https://github.com/ggml-org/whisper.cpp/tree/master/scripts)
  and issues [89](https://github.com/ggml-org/whisper.cpp/issues/89) and
  [3752](https://github.com/ggml-org/whisper.cpp/issues/3752)
- [OpenAI Whisper discussion 2363](https://github.com/openai/whisper/discussions/2363)
- Spike #100's research note (CPU chunk throughput measured on a real
  sidecar): not yet written. Link it here when it lands.

Sublime cuts audio into chunks of about `chunk_length` (default 10 minutes) at
silence. On a slow CPU, set a shorter `chunk_length` so one request does not
run for as long.

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
