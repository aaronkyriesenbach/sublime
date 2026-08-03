package marker

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/unicode"

	"github.com/aaronkyriesenbach/sublime/internal/media"
)

// zeroTimestamp is the SRT timestamp used for the marker cue when content
// has no pre-existing cues to anchor it to.
const zeroTimestamp = "00:00:00,000"

var (
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
	utf16LEBOM = []byte{0xFF, 0xFE}
	utf16BEBOM = []byte{0xFE, 0xFF}

	// timestampLineRE matches an SRT cue's "start --> end" timing line.
	timestampLineRE = regexp.MustCompile(`(?m)^(\d{2}:\d{2}:\d{2},\d{3}) --> (\d{2}:\d{2}:\d{2},\d{3})`)

	// indexLineRE matches an SRT cue's numeric index line on its own.
	indexLineRE = regexp.MustCompile(`(?m)^(\d+)\s*$`)

	// markerLineRE matches a previously-written marker cue's text line.
	markerLineRE = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(Prefix) + `:(.*)$`)

	// hashRE validates an embedded hash against media.ContentHash's actual
	// encoding (lowercase 16-char hex, see internal/media's ComputeContentHash),
	// so a truncated or garbled marker line is caught as Corrupt rather than
	// silently accepted.
	hashRE = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// srtCodec is the MarkerCodec for .srt sidecars: it appends the Marker as
// an extra zero-duration cue after the file's last real cue.
type srtCodec struct{}

var _ MarkerCodec = srtCodec{}

// Write implements MarkerCodec.
func (srtCodec) Write(content []byte, m Marker) ([]byte, error) {
	normalized, err := normalizeToUTF8(content)
	if err != nil {
		return nil, fmt.Errorf("marker: normalizing srt content to utf-8: %w", err)
	}

	index, lastEnd := nextCueIndexAndLastEnd(normalized)
	cue := fmt.Sprintf("%d\n%s --> %s\n%s:%s\n", index, lastEnd, lastEnd, Prefix, m.ContentHash)

	trimmed := strings.TrimRight(string(normalized), "\n")
	if trimmed == "" {
		return []byte(cue), nil
	}
	return []byte(trimmed + "\n\n" + cue), nil
}

// Read implements MarkerCodec.
func (srtCodec) Read(content []byte) (Marker, Presence) {
	normalized, err := normalizeToUTF8(content)
	if err != nil {
		return Marker{}, Corrupt
	}

	matches := markerLineRE.FindAllSubmatch(normalized, -1)
	if len(matches) == 0 {
		return Marker{}, Absent
	}

	hash := strings.TrimSpace(string(matches[len(matches)-1][1]))
	if !hashRE.MatchString(hash) {
		return Marker{}, Corrupt
	}

	return Marker{ContentHash: media.ContentHash(hash)}, Present
}

// nextCueIndexAndLastEnd scans content's pre-existing SRT cues, returning
// the index the next appended cue should use (one past the highest index
// line found, or 1 if none) and the end timestamp of the last cue (or
// zeroTimestamp if content has no timing lines).
func nextCueIndexAndLastEnd(content []byte) (int, string) {
	lastEnd := zeroTimestamp
	if matches := timestampLineRE.FindAllSubmatch(content, -1); len(matches) > 0 {
		lastEnd = string(matches[len(matches)-1][2])
	}

	maxIndex := 0
	for _, m := range indexLineRE.FindAllSubmatch(content, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil && n > maxIndex {
			maxIndex = n
		}
	}

	return maxIndex + 1, lastEnd
}

// normalizeToUTF8 converts content to UTF-8: a UTF-16 (LE or BE) BOM is
// decoded, and a UTF-8 BOM is stripped. Content with neither is assumed to
// already be UTF-8, matching how Sublime itself writes sidecars.
func normalizeToUTF8(content []byte) ([]byte, error) {
	switch {
	case bytes.HasPrefix(content, utf8BOM):
		return content[len(utf8BOM):], nil
	case bytes.HasPrefix(content, utf16LEBOM):
		return decodeUTF16(content, unicode.LittleEndian)
	case bytes.HasPrefix(content, utf16BEBOM):
		return decodeUTF16(content, unicode.BigEndian)
	default:
		return content, nil
	}
}

func decodeUTF16(content []byte, endianness unicode.Endianness) ([]byte, error) {
	decoded, err := unicode.UTF16(endianness, unicode.ExpectBOM).NewDecoder().Bytes(content)
	if err != nil {
		return nil, fmt.Errorf("decoding utf-16: %w", err)
	}
	return decoded, nil
}
