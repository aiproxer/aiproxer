package parity_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

// Claims (PR #1326 re-review): adapters preserve error categories, Responses
// resource errors use its own enum, and client failure delivery precedes Close.
// Mutants: generic 4xx mapping; Close-before-failure defer order.
func streamErrorCases() []struct {
	name                                  string
	err                                   error
	chatType, anthropicType, geminiStatus string
	responseCode                          responses.ResponseErrorCode
} {
	return []struct {
		name                                  string
		err                                   error
		chatType, anthropicType, geminiStatus string
		responseCode                          responses.ResponseErrorCode
	}{
		{"400", &aipapi.RejectError{Reason: "capability rejected"}, "invalid_request_error", "invalid_request_error", "INVALID_ARGUMENT", responses.ResponseErrorCodeServerError},
		{"403", aipapi.NewPolicyDeniedError("pre_request", "provider", "denied", "denied", "denied", nil), "permission_error", "permission_error", "PERMISSION_DENIED", responses.ResponseErrorCodeServerError},
		{"429", billingport.ErrInsufficientSpendable, "insufficient_quota", "rate_limit_error", "RESOURCE_EXHAUSTED", responses.ResponseErrorCodeRateLimitExceeded},
		{"502", aipapi.ErrCapabilityReject, "api_error", "api_error", "INTERNAL", responses.ResponseErrorCodeServerError},
		{"503", aipapi.NewPolicyFailureError("pre_request", "provider", "unavailable", "unavailable", "unavailable", nil), "api_error", "api_error", "UNAVAILABLE", responses.ResponseErrorCodeServerError},
	}
}

func TestStreamFailureCategoriesAreWireSpecific(t *testing.T) {
	t.Parallel()
	for _, tc := range streamErrorCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, wire := range []string{"chat", "anthropic", "gemini"} {
				t.Run(wire, func(t *testing.T) {
					w := httptest.NewRecorder()
					s := &failureScript{err: tc.err}
					var err error
					switch wire {
					case "chat":
						err = openailegacy.WriteStreamSSE(t.Context(), w, nil, s, openailegacy.EncodeOptions{})
					case "anthropic":
						err = anthropic.WriteStreamSSE(t.Context(), w, nil, s, anthropic.EncodeOptions{})
					case "gemini":
						err = gemini.WriteStreamSSE(t.Context(), w, nil, s, gemini.EncodeOptions{})
					}
					if !errors.Is(err, tc.err) {
						t.Fatalf("lost original error: %v", err)
					}
					var failure struct{ Error struct{ Type, Status string } }
					found := false
					for line := range strings.SplitSeq(w.Body.String(), "\n") {
						if !strings.HasPrefix(line, "data: {") {
							continue
						}
						if err := json.Unmarshal([]byte(line[6:]), &failure); err != nil {
							t.Fatal(err)
						}
						if failure.Error.Type != "" || failure.Error.Status != "" {
							found = true
							break
						}
					}
					want, got := tc.chatType, failure.Error.Type
					if wire == "anthropic" {
						want = tc.anthropicType
					}
					if wire == "gemini" {
						want, got = tc.geminiStatus, failure.Error.Status
					}
					if !found || got != want {
						t.Fatalf("error category=%q want %q; body=%s", got, want, w.Body.String())
					}
				})
			}
		})
	}
}

func TestResponsesFailureMatchesOfficialErrorModel(t *testing.T) {
	t.Parallel()
	for _, tc := range streamErrorCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = openairesponses.WriteStreamSSE(r.Context(), w, nil, &failureScript{err: tc.err}, openairesponses.EncodeOptions{ResponseID: "resp_failure"})
			}))
			defer srv.Close()
			client := openai.NewClient(option.WithBaseURL(srv.URL+"/v1/"), option.WithAPIKey("test"), option.WithMaxRetries(0))
			stream := client.Responses.NewStreaming(t.Context(), responses.ResponseNewParams{Model: "test"})
			defer func() { _ = stream.Close() }()
			count := 0
			for stream.Next() {
				ev := stream.Current()
				if ev.Type != "response.failed" {
					continue
				}
				count++
				failed := ev.AsResponseFailed()
				detail := failed.Response.Error
				// The official Go SDK decodes unknown enum strings permissively.
				// Enforce the mapper's allowlisted subset using its typed schema
				// constants, not merely successful unmarshalling or substring checks.
				if !failed.Response.JSON.Error.Valid() || !detail.JSON.Code.Valid() || !detail.JSON.Message.Valid() || detail.Code != tc.responseCode {
					t.Fatalf("nonconforming ResponseError: %#v, want schema code %q", detail, tc.responseCode)
				}
				if failed.Response.ID != "resp_failure" || failed.Response.Status != "failed" || failed.SequenceNumber != 3 {
					t.Fatalf("failed envelope changed: %#v", failed)
				}
			}
			if err := stream.Err(); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("observed %d failure events", count)
			}
		})
	}
}

func TestStreamFailureReachesClientBeforeBlockedClose(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	recvErr, closeErr := &aipapi.RejectError{Reason: "capability rejected"}, errors.New("cleanup failed")
	s := &blockingCloseStream{release: release, closing: make(chan struct{}), recvErr: recvErr, closeErr: closeErr}
	result := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result <- openailegacy.WriteStreamSSE(r.Context(), w, nil, s, openailegacy.EncodeOptions{})
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(unblock)
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	<-s.closing
	reader := bufio.NewReader(resp.Body)
	found := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("failure held behind Close: %v", err)
		}
		if strings.Contains(line, `"message":"capability rejected"`) {
			found = true
		}
		if line == "data: [DONE]\n" {
			break
		}
	}
	if !found {
		t.Fatal("DONE arrived without an explicit failure")
	}
	select {
	case err := <-result:
		t.Fatalf("Close did not remain blocked: %v", err)
	default:
	}
	unblock()
	err = <-result
	if !errors.Is(err, recvErr) || !errors.Is(err, closeErr) || s.closes != 1 {
		t.Fatalf("lost error identity or close ownership: err=%v closes=%d", err, s.closes)
	}
}

type blockingCloseStream struct {
	release           <-chan struct{}
	closing           chan struct{}
	recvErr, closeErr error
	closes            int
}

func (s *blockingCloseStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, s.recvErr
}

func (s *blockingCloseStream) Close() error {
	s.closes++
	close(s.closing)
	<-s.release
	return s.closeErr
}
