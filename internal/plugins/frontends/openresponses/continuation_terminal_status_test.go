package openresponses_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/continuation"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// TestHTTPRefusalTerminalStoredAndResumable covers the refusal-as-incomplete
// bug end to end: the OpenResponses wire reports a refusal turn as completed,
// so the stored continuation record must be completed too, with or without an
// explicit upstream status. Previously the recorder marked every non-stop
// finish reason incomplete, the store rejected that record under the default
// policy, and previous_response_id could not resolve the refusal turn.
func TestHTTPRefusalTerminalStoredAndResumable(t *testing.T) {
	cases := []struct {
		name           string
		responseStatus string
	}{
		{name: "explicit_completed_status", responseStatus: "completed"},
		{name: "legacy_absent_status", responseStatus: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := continuation.NewMemoryStore()
			executor := &scriptedContinuationExecutor{stream: func() aipapi.EventStream {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "I cannot help with that"},
					{Kind: aipapi.EventResponseFinished, ResponseStatus: tc.responseStatus, FinishReason: "content_filter"},
				})
			}}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true,
				Authorizer:           continuationAuth{},
				Executor:             executor,
				ContinuationStore:    store,
			})

			first := serveContinuationRequest(t, handler, `{"model":"gpt-4o","input":"refuse me","store":true}`)
			if first.Code != http.StatusOK {
				t.Fatalf("refusal turn failed: %d %s", first.Code, first.Body.String())
			}
			var resource struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(first.Body.Bytes(), &resource); err != nil {
				t.Fatalf("parse refusal response: %v", err)
			}
			if resource.Status != "completed" {
				t.Fatalf("wire status = %q, want completed for a refusal", resource.Status)
			}

			record, err := store.Get(context.Background(), aipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}, aipcont.ResponseID(resource.ID))
			if err != nil {
				t.Fatalf("refusal turn was not stored: %v", err)
			}
			if record.Status != aipcont.RecordStatusCompleted {
				t.Fatalf("stored refusal status = %q, want completed", record.Status)
			}

			second := serveContinuationRequest(t, handler, `{"previous_response_id":"`+resource.ID+`","input":"follow-up"}`)
			if second.Code != http.StatusOK {
				t.Fatalf("previous_response_id after refusal failed: %d %s", second.Code, second.Body.String())
			}
			if len(executor.calls) != 2 {
				t.Fatalf("executor calls = %d, want 2", len(executor.calls))
			}
			items := executor.calls[1].Items
			if len(items) != 3 || items[0].Content[0].Text != "refuse me" || items[1].Content[0].Text != "I cannot help with that" || items[2].Content[0].Text != "follow-up" {
				t.Fatalf("materialized order after refusal: %+v", items)
			}
		})
	}
}
