package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/aaronkyriesenbach/sublime/internal/retry"
)

// client is the HTTP transport for a whisper.cpp server's /inference
// endpoint.
type client struct {
	baseURL    string
	httpClient *http.Client
}

// segment is one transcribed span of speech, in seconds from the start of
// the submitted audio.
type segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
	// Words are whisper's token-level timings. A token that starts a word
	// carries a leading space; one that continues a word does not.
	Words []token `json:"words"`
}

// token is one timed piece of text from a segment's "words" array. Despite
// the name, whisper.cpp emits sub-word tokens there.
type token struct {
	Text  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// withWordsAtSegmentStart returns the segment with its token times shifted
// so the first token starts where the segment does. With --vad whisper.cpp
// maps segment times back to the original timeline but leaves token times
// on the shortened one (whisper.cpp#3174), so only the spacing between a
// segment's tokens can be trusted, not their position.
// isHallucinatedOverSilence spots the model's habit of filling a whole 30 s
// window of music or logos with a confident two-word phrase ("The End") whose
// words it places many seconds apart, and reports as speech with
// no_speech_prob 0, so that field cannot be used. Real speech does not leave
// ten seconds of silence inside a few tokens. The gap is measured between word
// starts so a final word whose end is merely stretched is not mistaken for it.
func (s segment) isHallucinatedOverSilence() bool {
	if len(s.units()) >= hallucinationMinTokens {
		return false
	}
	for i := 1; i < len(s.Words); i++ {
		if s.Words[i].Start-s.Words[i-1].Start > hallucinationMinGap {
			return true
		}
	}
	return false
}

// boilerplateHallucinations are whole-segment phrases the model produces for
// music, logos and silence because its training data ended that way. Seen on
// a real library as lone cues at the start of 1 in 10 files.
var boilerplateHallucinations = map[string]bool{
	"the end":                             true,
	"end the":                             true,
	"the":                                 true,
	"transcription":                       true,
	"by castingwords":                     true,
	"transcription by castingwords":       true,
	"thanks for watching":                 true,
	"thank you for watching":              true,
	"subtitles by the amaraorg community": true,
}

func (s segment) isBoilerplateHallucination() bool {
	var words []string
	for _, u := range s.units() {
		words = append(words, u.text)
	}
	normalized := strings.Join(strings.FieldsFunc(strings.ToLower(strings.Join(words, "")), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' '
	}), "")
	return boilerplateHallucinations[strings.Join(strings.Fields(normalized), " ")]
}

// hasSpokenText is false for a segment of only punctuation, such as the lone
// "." the model emits for silence at the start of a file.
func (s segment) hasSpokenText() bool {
	for _, u := range s.units() {
		if strings.IndexFunc(u.text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			return true
		}
	}
	return false
}

const (
	hallucinationMinGap    = 8.0
	hallucinationMinTokens = 4
)

func (s segment) withWordsAtSegmentStart() segment {
	if len(s.Words) == 0 {
		return s
	}
	shift := s.Start - s.Words[0].Start
	words := make([]token, len(s.Words))
	for i, w := range s.Words {
		words[i] = token{Text: w.Text, Start: w.Start + shift, End: w.End + shift}
	}
	s.Words = words
	return s
}

// sidecarError is a non-2xx answer from the sidecar. Receiving one proves
// the sidecar is reachable, so it is a request failure, never an outage.
type sidecarError struct {
	status  int
	message string
}

func (e *sidecarError) Error() string {
	return fmt.Sprintf("sidecar returned status %d: %s", e.status, e.message)
}

// verboseResponse mirrors the parts of whisper.cpp's /inference response
// Sublime uses. A failed request answers with an "error" message instead
// of segments. A language-detection request answers with the detected
// language in "detected_language" and/or "language"; which of them a server
// version fills in, and whether as a code or a name, is not stable.
type verboseResponse struct {
	Error            string    `json:"error"`
	Segments         []segment `json:"segments"`
	Language         string    `json:"language"`
	DetectedLanguage string    `json:"detected_language"`
}

// transcribe posts audio to /inference. The language is always explicit —
// left to auto-detect, whisper could silently transcribe in the wrong
// language — and translation is always off. The verbose_json format is what
// makes the sidecar return per-token timestamps for cue shaping. A zero
// temperature leaves the sidecar's default decoding; a retry raises it.
func (c *client) transcribe(ctx context.Context, audio []byte, languageCode string, temperature float64) ([]segment, error) {
	fields := map[string]string{
		"response_format": "verbose_json",
		"language":        languageCode,
		"translate":       "false",
		// Without it a server running --vad answers a chunk with no speech
		// with a 500 instead of an empty transcript.
		"no_language_probabilities": "true",
		// Carrying text from one 30 s window into the next lets a single
		// hallucination ("The End" over music) repeat for the rest of the
		// chunk. On real TV audio this took recall from 0.56 to 0.88 on a
		// chunk that otherwise looped 369 times.
		"max_context": "0",
	}
	if temperature > 0 {
		fields["temperature"] = strconv.FormatFloat(temperature, 'f', -1, 64)
	}
	parsed, err := c.inference(ctx, audio, fields)
	if err != nil {
		return nil, err
	}
	kept := parsed.Segments[:0]
	for _, seg := range parsed.Segments {
		if seg.isHallucinatedOverSilence() || !seg.hasSpokenText() || seg.isBoilerplateHallucination() {
			continue
		}
		kept = append(kept, seg.withWordsAtSegmentStart())
	}
	return kept, nil
}

// detectLanguage asks the sidecar which language is spoken in a short clip
// and returns it as the sidecar names it: a code ("en") or, depending on
// the server version, an English name ("english").
func (c *client) detectLanguage(ctx context.Context, clip []byte) (string, error) {
	parsed, err := c.inference(ctx, clip, map[string]string{
		"response_format": "json",
		"language":        "auto",
		"detect_language": "true",
		"translate":       "false",
	})
	if err != nil {
		return "", err
	}
	for _, detected := range []string{parsed.DetectedLanguage, parsed.Language} {
		if detected = strings.TrimSpace(detected); detected != "" {
			return detected, nil
		}
	}
	return "", errors.New("sidecar response names no detected language")
}

func (c *client) inference(ctx context.Context, audio []byte, fields map[string]string) (verboseResponse, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "audio.wav")
	if err != nil {
		return verboseResponse{}, err
	}
	if _, err := file.Write(audio); err != nil {
		return verboseResponse{}, err
	}
	for name, value := range fields {
		if err := form.WriteField(name, value); err != nil {
			return verboseResponse{}, err
		}
	}
	if err := form.Close(); err != nil {
		return verboseResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/inference", &body)
	if err != nil {
		return verboseResponse{}, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return verboseResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return verboseResponse{}, fmt.Errorf("reading response: %w", err)
	}

	var parsed verboseResponse
	decodeErr := json.Unmarshal(data, &parsed)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		statusErr := &sidecarError{status: resp.StatusCode, message: parsed.Error}
		if resp.StatusCode >= 500 {
			return verboseResponse{}, retry.Transient(statusErr)
		}
		return verboseResponse{}, statusErr
	}
	if decodeErr != nil {
		return verboseResponse{}, fmt.Errorf("decoding response: %w", decodeErr)
	}
	if parsed.Error != "" {
		return verboseResponse{}, fmt.Errorf("sidecar reported an error: %s", parsed.Error)
	}
	return parsed, nil
}
