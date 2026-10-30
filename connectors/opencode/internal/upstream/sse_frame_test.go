package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDecodeSSEFrameRejectsOversizedPayload(t *testing.T) {
	t.Parallel()

	var got map[string]any
	err := decodeSSEFrame([]byte(strings.Repeat("a", maxSSEFrameBytes+1)), &got)
	if !errors.Is(err, errSSEFrameTooLarge) {
		t.Fatalf("error=%v, want errSSEFrameTooLarge", err)
	}
	if len(got) != 0 {
		t.Fatalf("destination was materialized despite frame cap: %+v", got)
	}
}

func TestDecodeSSEFrameAdmitsExactCapPayload(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("a", maxSSEFrameBytes-8)
	body := `{"x":"` + text + `"}`
	if len(body) != maxSSEFrameBytes {
		t.Fatalf("body=%d bytes, want exact cap %d", len(body), maxSSEFrameBytes)
	}
	var got map[string]any
	if err := decodeSSEFrame([]byte(body), &got); err != nil {
		t.Fatalf("exact-cap frame rejected: %v", err)
	}
	if got["x"] != text {
		t.Fatalf("decoded value=%v, want exact payload", got["x"])
	}
}

func TestAnthropicSSEStreamRejectsOversizedFrame(t *testing.T) {
	t.Parallel()

	body := "data: " + strings.Repeat("x", maxSSEFrameBytes+1) + "\n\n"
	s := newAnthropicSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	defer func() { _ = s.Close() }()
	_, err := s.Recv(context.Background())
	if !errors.Is(err, errSSEFrameTooLarge) {
		t.Fatalf("error=%v, want errSSEFrameTooLarge", err)
	}
}

func TestGeminiSSEStreamRejectsOversizedFrame(t *testing.T) {
	t.Parallel()

	body := "data: " + strings.Repeat("x", maxSSEFrameBytes+1) + "\n\n"
	s := newGeminiSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	defer func() { _ = s.Close() }()
	_, err := s.Recv(context.Background())
	if !errors.Is(err, errSSEFrameTooLarge) {
		t.Fatalf("error=%v, want errSSEFrameTooLarge", err)
	}
}

func TestSSEStreamsAdmitExactCapPayloadWithLineTerminators(t *testing.T) {
	t.Parallel()

	// A payload at exactly maxSSEFrameBytes with an LF or CRLF line ending
	// must survive the scanner (token bound includes the terminator) and the
	// decodeSSEFrame cap check before materializing a text delta.
	for _, tc := range []struct {
		name       string
		newStream  func(*http.Response) aipapi.EventStream
		prefix     string
		suffix     string
		terminator string
	}{
		{name: "anthropic LF", newStream: newAnthropicSSEStream, prefix: `{"type":"content_block_delta","delta":{"type":"text_delta","text":"`, suffix: `"}}`, terminator: "\n"},
		{name: "anthropic CRLF", newStream: newAnthropicSSEStream, prefix: `{"type":"content_block_delta","delta":{"type":"text_delta","text":"`, suffix: `"}}`, terminator: "\r\n"},
		{name: "gemini LF", newStream: newGeminiSSEStream, prefix: `{"candidates":[{"content":{"parts":[{"text":"`, suffix: `"}]}}]}`, terminator: "\n"},
		{name: "gemini CRLF", newStream: newGeminiSSEStream, prefix: `{"candidates":[{"content":{"parts":[{"text":"`, suffix: `"}]}}]}`, terminator: "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text := strings.Repeat("t", maxSSEFrameBytes-len(tc.prefix)-len(tc.suffix))
			payload := tc.prefix + text + tc.suffix
			if len(payload) != maxSSEFrameBytes {
				t.Fatalf("payload=%d bytes, want exact cap %d", len(payload), maxSSEFrameBytes)
			}
			body := "data:" + payload + tc.terminator + tc.terminator
			s := tc.newStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
			defer func() { _ = s.Close() }()
			ev, err := s.Recv(context.Background())
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			if ev.Kind != aipapi.EventTextDelta || ev.Delta != text {
				t.Fatalf("event=%+v, want exact-cap text delta", ev)
			}
		})
	}
}

func TestAnthropicSSEStreamEmitsTextFrame(t *testing.T) {
	t.Parallel()

	body := "event: content_block_delta\n" +
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"data: [DONE]\n\n"
	s := newAnthropicSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	defer func() { _ = s.Close() }()
	ev, err := s.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != aipapi.EventTextDelta || ev.Delta != "hi" {
		t.Fatalf("event=%+v, want text delta %q", ev, "hi")
	}
}

func TestGeminiSSEStreamEmitsTextFrame(t *testing.T) {
	t.Parallel()

	body := `data: {"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}` + "\n\n"
	s := newGeminiSSEStream(&http.Response{Body: io.NopCloser(strings.NewReader(body))})
	defer func() { _ = s.Close() }()
	ev, err := s.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != aipapi.EventTextDelta || ev.Delta != "hi" {
		t.Fatalf("event=%+v, want text delta %q", ev, "hi")
	}
}
