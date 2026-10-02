package whisper

import (
	"fmt"
	"strings"
	"time"
)

// formatSRT renders segments as SRT cues, one per segment. Segments with
// blank text are dropped, since whisper emits them for non-speech stretches.
// It returns nil if no cue remains.
func formatSRT(segments []segment) []byte {
	var b strings.Builder
	cue := 0
	for _, s := range segments {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		cue++
		if cue > 1 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n", cue, srtTimestamp(s.Start), srtTimestamp(s.End), text)
	}
	if cue == 0 {
		return nil
	}
	return []byte(b.String())
}

func srtTimestamp(seconds float64) string {
	d := time.Duration(seconds*1000+0.5) * time.Millisecond
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	d -= s * time.Second
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, d/time.Millisecond)
}
