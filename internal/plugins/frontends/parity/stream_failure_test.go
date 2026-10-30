package parity_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claims (#1315 and adversarial review): no receive/metadata/partial-output
// failure is silent; errors are sanitized; wire encoders share the same fix.
// Mutant: remove the adapter failure callback -> missing terminal assertion.
func TestStreamingFailuresAreExplicit(t *testing.T) {
	t.Parallel()
	writers := map[string]func(http.ResponseWriter, aipapi.EventStream) error{
		"chat": func(w http.ResponseWriter, s aipapi.EventStream) error {
			return openailegacy.WriteStreamSSE(t.Context(), w, nil, s, openailegacy.EncodeOptions{})
		},
		"responses": func(w http.ResponseWriter, s aipapi.EventStream) error {
			return openairesponses.WriteStreamSSE(t.Context(), w, nil, s, openairesponses.EncodeOptions{})
		},
		"anthropic": func(w http.ResponseWriter, s aipapi.EventStream) error {
			return anthropic.WriteStreamSSE(t.Context(), w, nil, s, anthropic.EncodeOptions{})
		},
		"gemini": func(w http.ResponseWriter, s aipapi.EventStream) error {
			return gemini.WriteStreamSSE(t.Context(), w, nil, s, gemini.EncodeOptions{})
		},
		"wire-chat": func(w http.ResponseWriter, s aipapi.EventStream) error {
			return openailegacy.WireWriteStreamSSE(t.Context(), w, frontendpipe.ResponseContext{}, s, false)
		},
		"wire-responses": func(w http.ResponseWriter, s aipapi.EventStream) error {
			return openairesponses.WireWriteStreamSSE(t.Context(), w, frontendpipe.ResponseContext{}, s, false)
		},
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, prefix := range []struct {
				name   string
				events []aipapi.Event
			}{
				{"first-recv", nil},
				{"response-started", []aipapi.Event{{Kind: aipapi.EventResponseStarted}}},
				{"metadata", []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventUsageDelta}}},
				{"after-content", []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "visible-prefix"}}},
			} {
				t.Run(prefix.name, func(t *testing.T) {
					s := &failureScript{events: prefix.events, err: fmt.Errorf("secret-upstream-token: %w", &aipapi.RejectError{Reason: "capability rejected"})}
					w := httptest.NewRecorder()
					if err := write(w, s); !aipapi.IsReject(err) {
						t.Fatalf("lost rejection: %v", err)
					}
					body := w.Body.String()
					if !strings.Contains(body, `"message":"capability rejected"`) || strings.Contains(body, "secret-upstream-token") {
						t.Fatalf("missing or unsafe failure: %s", body)
					}
					if strings.Contains(name, "responses") {
						if !strings.Contains(body, "event: response.failed\n") || !strings.Contains(body, `"status":"failed"`) {
							t.Fatalf("missing failed response: %s", body)
						}
					} else if name == "anthropic" && !strings.Contains(body, "event: error\n") {
						t.Fatalf("missing Anthropic error event: %s", body)
					} else if strings.Contains(name, "chat") && !strings.HasSuffix(body, "data: [DONE]\n\n") {
						t.Fatalf("missing chat termination: %s", body)
					}
					if prefix.name == "after-content" && !strings.Contains(body, "visible-prefix") {
						t.Fatalf("lost preceding content: %s", body)
					}
					if s.closes != 1 {
						t.Fatalf("close count=%d", s.closes)
					}
				})
			}
		})
	}
}

type failureScript struct {
	events []aipapi.Event
	err    error
	closes int
}

func (s *failureScript) Recv(context.Context) (aipapi.Event, error) {
	if len(s.events) == 0 {
		return aipapi.Event{}, s.err
	}
	ev := s.events[0]
	s.events = s.events[1:]
	return ev, nil
}
func (s *failureScript) Close() error { s.closes++; return nil }

// Headers and response.created must reach a real client before the first Recv
// completes. A deferred commit would fail the header deadline / ID assertion.
func TestStreamingHeadersDoNotWaitForBackend(t *testing.T) {
	t.Parallel()
	for _, frontend := range []string{"chat", "responses", "anthropic", "gemini"} {
		t.Run(frontend, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s := &blockingFailure{release: release}
				switch frontend {
				case "chat":
					_ = openailegacy.WriteStreamSSE(r.Context(), w, nil, s, openailegacy.EncodeOptions{})
				case "responses":
					_ = openairesponses.WriteStreamSSE(r.Context(), w, nil, s, openairesponses.EncodeOptions{ResponseID: "resp_cancel-before-output"})
				case "anthropic":
					_ = anthropic.WriteStreamSSE(r.Context(), w, nil, s, anthropic.EncodeOptions{})
				case "gemini":
					_ = gemini.WriteStreamSSE(r.Context(), w, nil, s, gemini.EncodeOptions{})
				}
			}))
			defer srv.Close()
			defer close(release)
			client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: time.Second}, Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			resp, err := client.Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != 200 {
				t.Fatalf("status=%d", resp.StatusCode)
			}
			if frontend == "responses" {
				buf := make([]byte, 128)
				n, err := io.ReadAtLeast(resp.Body, buf, len(buf))
				if err != nil || !strings.Contains(string(buf[:n]), "resp_cancel-before-output") {
					t.Fatalf("ID unavailable before first event: %q %v", buf[:n], err)
				}
			}
		})
	}
}

type blockingFailure struct{ release <-chan struct{} }

func (s *blockingFailure) Recv(ctx context.Context) (aipapi.Event, error) {
	select {
	case <-s.release:
		return aipapi.Event{}, errors.New("upstream unavailable")
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	}
}
func (*blockingFailure) Close() error { return nil }
