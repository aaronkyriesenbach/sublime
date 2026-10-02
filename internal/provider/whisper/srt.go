package whisper

import (
	"fmt"
	"strings"
	"time"
)

// formatSRT renders cues as SRT. It returns nil if there are none.
func formatSRT(cues []cue) []byte {
	var b strings.Builder
	for i, c := range cues {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n", i+1, srtTimestamp(c.Start), srtTimestamp(c.End), strings.Join(c.Lines, "\n"))
	}
	if len(cues) == 0 {
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
