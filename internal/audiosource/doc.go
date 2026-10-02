// Package audiosource wraps ffprobe and ffmpeg behind the Source interface
// so the whisper Provider can discover a video's audio streams and pull
// audio for the whisper sidecar straight out of the video. See CONTEXT.md
// (Generated Subtitle) and docs/adr/0014.
package audiosource
