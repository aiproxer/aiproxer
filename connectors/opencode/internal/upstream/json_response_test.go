package upstream

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNewAnthropicJSONStreamRejectsOversizedResponse(t *testing.T) {
	t.Parallel()

	body := `{"content":[{"type":"text","text":"` + strings.Repeat("x", maxNonStreamResponseBytes) + `"}]}`
	_, err := newAnthropicJSONStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	if !errors.Is(err, errNonStreamResponseTooLarge) {
		t.Fatalf("error=%v, want bounded response error", err)
	}
}

func TestNewGeminiJSONStreamRejectsOversizedResponse(t *testing.T) {
	t.Parallel()

	body := `{"candidates":[{"content":{"parts":[{"text":"` + strings.Repeat("x", maxNonStreamResponseBytes) + `"}]}}]}`
	_, err := newGeminiJSONStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	if !errors.Is(err, errNonStreamResponseTooLarge) {
		t.Fatalf("error=%v, want bounded response error", err)
	}
}
