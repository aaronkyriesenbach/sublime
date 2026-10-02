package whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
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
}

// verboseResponse mirrors the parts of whisper.cpp's verbose_json response
// Sublime uses. A failed request answers with an "error" message instead
// of segments.
type verboseResponse struct {
	Error    string    `json:"error"`
	Segments []segment `json:"segments"`
}

// transcribe posts audio to /inference. The language is always explicit —
// left to auto-detect, whisper could silently transcribe in the wrong
// language — and translation is always off.
func (c *client) transcribe(ctx context.Context, audio []byte, languageCode string) ([]segment, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "audio.wav")
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(audio); err != nil {
		return nil, err
	}
	for name, value := range map[string]string{
		"response_format": "verbose_json",
		"language":        languageCode,
		"translate":       "false",
	} {
		if err := form.WriteField(name, value); err != nil {
			return nil, err
		}
	}
	if err := form.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/inference", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var parsed verboseResponse
	decodeErr := json.Unmarshal(data, &parsed)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("sidecar returned status %d: %s", resp.StatusCode, parsed.Error)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decoding response: %w", decodeErr)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("sidecar reported an error: %s", parsed.Error)
	}
	return parsed.Segments, nil
}
