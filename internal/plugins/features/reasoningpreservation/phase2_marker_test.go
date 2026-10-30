package reasoningpreservation_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestGenericFeatureDoesNotIssueCompanionTrustMarkers(t *testing.T) {
	t.Parallel()
	cfg := reasoningpreservation.Config{Action: reasoningpreservation.ActionObserve, Rules: []reasoningpreservation.RuleConfig{{ID: "backend", Backend: "backend", Enabled: new(true)}}}
	store := newMemoryStore(t, reasoningpreservation.StoreOptions{TTL: time.Hour, MaxTurnsPerSession: 4, MaxReasoningBytesPerTurn: 1024, MaxSessionBytes: 4096})
	call := aipapi.Call{Extensions: map[string]json.RawMessage{"reserved.marker": json.RawMessage(`{"eligible":true}`)}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
	_, err := reasoningpreservation.NewAttemptTransform(cfg, store).HandleAttempt(context.Background(), &call, request.AttemptMeta{BackendID: "backend", ReplaySupport: exactResponsesSupport(), Session: session.SessionView{AuthoritativeSessionID: "auth-session"}}, request.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := call.Extensions["reserved.marker"]; !ok {
		t.Fatal("generic feature unexpectedly scrubbed injected companion data")
	}
}
