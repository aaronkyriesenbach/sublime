package audiosource

import (
	"bytes"
	"encoding/binary"
)

const (
	wavChannels      = 1
	wavBitsPerSample = 16
	wavHeaderSize    = 44
)

// encodeWAV wraps raw 16 kHz mono signed 16-bit little-endian PCM in a
// canonical WAV container. ffmpeg's own WAV muxer cannot seek back to fill
// in sizes when writing to a pipe and leaves placeholders, which strict
// WAV readers reject, so the header is written here with the true sizes.
func encodeWAV(pcm []byte) []byte {
	const byteRate = SampleRate * wavChannels * wavBitsPerSample / 8
	const blockAlign = wavChannels * wavBitsPerSample / 8

	var buf bytes.Buffer
	buf.Grow(wavHeaderSize + len(pcm))
	buf.WriteString("RIFF")
	writeLE(&buf, uint32(36+len(pcm)))
	buf.WriteString("WAVEfmt ")
	writeLE(&buf, uint32(16))
	writeLE(&buf, uint16(1))
	writeLE(&buf, uint16(wavChannels))
	writeLE(&buf, uint32(SampleRate))
	writeLE(&buf, uint32(byteRate))
	writeLE(&buf, uint16(blockAlign))
	writeLE(&buf, uint16(wavBitsPerSample))
	buf.WriteString("data")
	writeLE(&buf, uint32(len(pcm)))
	buf.Write(pcm)
	return buf.Bytes()
}

func writeLE[T uint16 | uint32](buf *bytes.Buffer, v T) {
	// bytes.Buffer writes never fail.
	_ = binary.Write(buf, binary.LittleEndian, v)
}
