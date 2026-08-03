#!/usr/bin/env bash
# Regenerates the fixtures in this directory from scratch.
#
# Everything here is synthesized, not sourced from real media: real film/TV
# clips carry copyright risk even at a few seconds, and re-encodes drift
# across ffmpeg versions in ways hand-authored subtitle timings can't track.
# A synthesized clip is small, deterministic, and license-free.
#
# Requires: ffmpeg, espeak-ng.
set -euo pipefail
cd "$(dirname "$0")"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Three short spoken lines, each ~1.3-1.5s, placed at known offsets over a
# 9s silence bed so the exact speech timing is a script parameter, not
# something to measure after the fact.
espeak-ng -s 150 -v en "one two three" -w "$work/line1.wav"
espeak-ng -s 150 -v en "four five six" -w "$work/line2.wav"
espeak-ng -s 150 -v en "seven eight nine" -w "$work/line3.wav"

ffmpeg -y -v error -f lavfi -i "anullsrc=r=16000:cl=mono:d=9" \
  -i "$work/line1.wav" -i "$work/line2.wav" -i "$work/line3.wav" \
  -filter_complex "\
[1:a]adelay=500|500[a1]; \
[2:a]adelay=3500|3500[a2]; \
[3:a]adelay=6500|6500[a3]; \
[0:a][a1][a2][a3]amix=inputs=4:duration=first:dropout_transition=0,volume=3[aout]" \
  -map "[aout]" -c:a pcm_s16le "$work/mixed.wav"

# video/sample.mp4: 9s, plain gray field + the speech track above. Used as
# both an ffprobe-metadata fixture and an alass reference-video input (alass
# extracts audio + runs VAD against it; a flat color frame is irrelevant to
# that path, so the video stream is decorative).
ffmpeg -y -v error -f lavfi -i "color=c=gray:s=320x240:d=9:r=15" -i "$work/mixed.wav" \
  -c:v libx264 -preset veryfast -crf 30 -pix_fmt yuv420p -c:a aac -b:a 64k -shortest \
  video/sample.mp4

# video/tiny.mp4: silent, 1s, well under 64 KiB — exercises the Content Hash
# algorithm's first-64KiB/last-64KiB overlap case for files smaller than one
# window (see issue #12).
ffmpeg -y -v error -f lavfi -i "color=c=black:s=64x64:d=1:r=5" \
  -c:v libx264 -preset veryfast -crf 40 -pix_fmt yuv420p -an \
  video/tiny.mp4

echo "Wrote video/sample.mp4 and video/tiny.mp4."
echo "Subtitle fixtures (subs/*.srt, subs/*.ass) are hand-authored to match"
echo "the speech offsets above (0.5s / 3.5s / 6.5s) and are not regenerated"
echo "by this script — edit them directly if the speech timeline changes."
