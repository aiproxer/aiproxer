package gemini_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
)

func TestParseGenerateContentPath_nonStream(t *testing.T) {
	t.Parallel()
	m, stream, ok := gemini.ParseGenerateContentPath("/v1beta/models/gemini-2.0-flash:generateContent")
	if !ok || stream || m != "gemini-2.0-flash" {
		t.Fatalf("got %q stream=%v ok=%v", m, stream, ok)
	}
}

func TestParseGenerateContentPath_stream(t *testing.T) {
	t.Parallel()
	m, stream, ok := gemini.ParseGenerateContentPath("/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse")
	if !ok || !stream || m != "gemini-2.0-flash" {
		t.Fatalf("got %q stream=%v ok=%v", m, stream, ok)
	}
}

func TestParseGenerateContentPath_vertexStyle(t *testing.T) {
	t.Parallel()
	m, stream, ok := gemini.ParseGenerateContentPath("v1beta1/projects/p/locations/us-central1/models/gemini-pro:generateContent")
	if !ok || stream || m != "gemini-pro" {
		t.Fatalf("got %q stream=%v ok=%v", m, stream, ok)
	}
}

func TestParseGenerateContentPath_invalid(t *testing.T) {
	t.Parallel()
	_, _, ok := gemini.ParseGenerateContentPath("/healthz")
	if ok {
		t.Fatal("expected not ok")
	}
}
