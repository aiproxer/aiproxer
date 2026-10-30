package controlplane

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// forbiddenHTTPSecretSubstrings are raw secret/credential/payload markers that
// must never appear in protected control-plane HTTP query responses
// (requirements 4.4, 4.5, 4.6, 4.7, 10.5; task 6.4).
var forbiddenHTTPSecretSubstrings = []string{
	"bearer ",
	"api key",
	"api-key",
	"apikey:",
	"oauth ",
	"authorization:",
	"resume token",
	"resume_token",
	"secret:",
	"password:",
	"raw_usage_json",
	"raw_payload",
	"raw_headers",
	"sk-",
}

// TestHTTPPrivacyScan_QueryResponsesContainNoRawSecrets proves task 6.4: the
// protected HTTP query responses (status, sessions, attempts, usage, events,
// policy-audit) contain no raw bearer tokens, API keys, OAuth tokens, resume
// tokens, credential secrets, raw transport headers, or raw request/response
// payloads (requirements 4.4-4.8, 10.5).
func TestHTTPPrivacyScan_QueryResponsesContainNoRawSecrets(t *testing.T) {
	t.Parallel()
	queries, _, store := newTestQueryService(t, true)

	now := time.Now().UTC()
	events := []cp.Event{
		{
			Category: cp.CategorySession, OccurredAt: now, RecordedAt: now,
			Source:     cp.SourceRef{Name: "privacy-scan", Version: "v1"},
			Visibility: cp.VisibilityDefault, EvidenceState: cp.EvidenceRecorded, RedactionState: cp.RedactionNone,
			Correlation: cp.Correlation{SessionID: "sess-scan", TraceID: "trace-scan"},
			Summary:     "session started for principal-scan",
			Detail:      &cp.SessionDetail{Action: cp.SessionActionCreated, Certainty: "known", SessionID: "sess-scan"},
		},
		{
			Category: cp.CategoryAttempt, OccurredAt: now, RecordedAt: now,
			Source:     cp.SourceRef{Name: "privacy-scan", Version: "v1"},
			Visibility: cp.VisibilityDefault, EvidenceState: cp.EvidenceRecorded, RedactionState: cp.RedactionNone,
			Correlation: cp.Correlation{SessionID: "sess-scan", TraceID: "trace-scan", ALegID: "aleg-scan", BLegID: "bleg-scan", AttemptSeq: 1, BackendID: "openai", Model: "gpt-4o"},
			Detail:      &cp.AttemptDetail{Surfaced: cp.AttemptSurfacedSurfaced, Outcome: cp.AttemptOutcomeSucceeded, BackendID: "openai", Model: "gpt-4o"},
		},
		{
			Category: cp.CategoryUsage, OccurredAt: now, RecordedAt: now,
			Source:     cp.SourceRef{Name: "privacy-scan", Version: "v1"},
			Visibility: cp.VisibilityDefault, EvidenceState: cp.EvidenceRecorded, RedactionState: cp.RedactionNone,
			Correlation: cp.Correlation{SessionID: "sess-scan", TraceID: "trace-scan", BLegID: "bleg-scan", AttemptSeq: 1, BackendID: "openai", Model: "gpt-4o"},
			Detail:      &cp.UsageDetail{Plane: cp.UsagePlaneObserved, Availability: cp.UsageAvailabilityObserved, InputTokens: 100, OutputTokens: 50, TotalTokens: 150},
		},
	}
	for _, ev := range events {
		if _, err := store.Append(context.Background(), ev); err != nil {
			t.Fatalf("seed append: %v", err)
		}
	}

	h := NewHandler(Options{Queries: queries})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	paths := []string{
		"/status",
		"/sessions",
		"/attempts",
		"/usage",
		"/events",
		"/policy-audit",
	}
	for _, p := range paths {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("read %s body: %v", p, err)
		}
		low := strings.ToLower(string(body))
		for _, bad := range forbiddenHTTPSecretSubstrings {
			if strings.Contains(low, bad) {
				t.Fatalf("%s response leaked forbidden substring %q in: %s", p, bad, string(body))
			}
		}
	}

	// Confirm the handler never emits raw infrastructure error classification
	// text for a classified query error.
	badResp, err := http.Get(srv.URL + "/sessions?limit=99999")
	if err != nil {
		t.Fatalf("GET too-broad: %v", err)
	}
	badBody, err := io.ReadAll(badResp.Body)
	_ = badResp.Body.Close()
	if err != nil {
		t.Fatalf("read too-broad body: %v", err)
	}
	if strings.Contains(strings.ToLower(string(badBody)), "sql") || strings.Contains(strings.ToLower(string(badBody)), "dsn") {
		t.Fatalf("too-broad response leaked infra detail: %s", string(badBody))
	}
	_ = controlplane.ErrUnavailable
}
