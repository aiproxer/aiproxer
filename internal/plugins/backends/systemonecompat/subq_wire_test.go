package systemonecompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func subQRequest() aipapi.DecisionRequest {
	return aipapi.DecisionRequest{Evidence: json.RawMessage(`"subq-evidence"`), Questions: []aipapi.DecisionQuestion{
		{ID: "q_noul", Kind: aipapi.DecisionKindNoul, Instructions: json.RawMessage(`"judge-it"`)},
		{ID: "q_pick", Kind: aipapi.DecisionKindChoice, Instructions: json.RawMessage(`"pick one"`), Options: []aipapi.DecisionOption{{Name: "beta"}, {Name: "alpha", Description: json.RawMessage(`"first"`)}, {Name: "gamma"}}},
		{ID: "q_grade", Kind: aipapi.DecisionKindScore, Instructions: json.RawMessage(`"grade it"`), Levels: []json.RawMessage{json.RawMessage(`"low"`), json.RawMessage(`"mid"`), json.RawMessage(`"high"`)}},
	}}
}

// TestSubQWire_SuccessMixedKindsReorderedKeysAndZeroOutput proves a SubQ-shaped
// success decodes: all three kinds in one call, reordered upstream answer keys
// still yield request-order results, probabilities/confidence survive, the
// resolved model is subq-snap-preview, output_tokens 0 is present (not merely
// zero-valued), and unknown top-level metadata is ignored.
func TestSubQWire_SuccessMixedKindsReorderedKeysAndZeroOutput(t *testing.T) {
	t.Parallel()
	const body = `{"model":"subq-snap-preview","request_id":"subq-req-123","warnings":[],"answers":{"q_grade":{"type":"score","score":1.3,"probabilities":{"0":0.2,"1":0.3,"2":0.5},"confidence":0.9},"q_noul":{"type":"noul","noul":0.7,"confidence":0.8},"q_pick":{"type":"choice","choice":"beta","probabilities":{"gamma":0.2,"beta":0.5,"alpha":0.3},"confidence":0.6}},"usage":{"input_tokens":42,"output_tokens":0}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
	defer srv.Close()
	req := subQRequest()
	events, err := evaluateWire(t.Context(), srv.Client(), srv.URL, "subq-key", "subq-snap-preview", req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].Kind != aipapi.EventResponseStarted || events[1].Kind != aipapi.EventDecisionResult || events[2].Kind != aipapi.EventUsageDelta || events[3].Kind != aipapi.EventResponseFinished {
		t.Fatalf("event sequence = %+v", events)
	}
	result := events[1].Decision
	if result == nil || result.Model != "subq-snap-preview" {
		t.Fatalf("resolved model = %+v", result)
	}
	if len(result.Answers) != 3 || result.Answers[0].QuestionID != "q_noul" || result.Answers[1].QuestionID != "q_pick" || result.Answers[2].QuestionID != "q_grade" {
		t.Fatalf("request order not preserved: %+v", result.Answers)
	}
	if result.Answers[0].PTrue != 0.7 || result.Answers[0].Confidence == nil || *result.Answers[0].Confidence != 0.8 {
		t.Fatalf("noul answer = %+v", result.Answers[0])
	}
	if result.Answers[1].Selected != "beta" || !reflect.DeepEqual(result.Answers[1].Probabilities, []float64{0.5, 0.3, 0.2}) || result.Answers[1].Confidence == nil || *result.Answers[1].Confidence != 0.6 {
		t.Fatalf("choice answer = %+v", result.Answers[1])
	}
	if result.Answers[2].Expected != 1.3 || !reflect.DeepEqual(result.Answers[2].Probabilities, []float64{0.2, 0.3, 0.5}) || result.Answers[2].Confidence == nil || *result.Answers[2].Confidence != 0.9 {
		t.Fatalf("score answer = %+v", result.Answers[2])
	}
	if err := result.ValidateFor(req); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
	usage := events[2]
	if !usage.UsagePresence.InputTokens || !usage.UsagePresence.OutputTokens || usage.InputTokens != 42 || usage.OutputTokens != 0 {
		t.Fatalf("usage presence/values = %+v", usage)
	}
}

// TestSubQWire_DetailObjectEnvelopeRejectedWithoutLeak proves a SubQ
// detail-OBJECT error envelope (not the FastAPI detail[] array the adapter
// parses) becomes a fixed generic body rejection with no vendor text crossing
// the error boundary.
func TestSubQWire_DetailObjectEnvelopeRejectedWithoutLeak(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 422} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"detail":{"error_type":"validation_error","message":"vendor-secret-detail-message-subq"}}`)
			}))
			defer srv.Close()
			be, err := BuildCompatible("decision", backendNode(t, srv.URL), srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			stream, err := be.Open(t.Context(), decisionCall(), routing.AttemptCandidate{})
			if stream != nil || err == nil || !aipapi.IsDecisionReject(err) || aipapi.IsRecoverablePreOutput(err) {
				t.Fatalf("status %d: stream=%v err=%v", status, stream, err)
			}
			rejection, ok := errors.AsType[*aipapi.DecisionRejectError](err)
			if !ok || rejection.Field != "body" || rejection.Message != "upstream rejected the decision request" {
				t.Fatalf("rejection = %+v", rejection)
			}
			if strings.Contains(err.Error(), "validation_error") || strings.Contains(err.Error(), "vendor-secret-detail-message-subq") || strings.Contains(rejection.Field, "validation_error") {
				t.Fatalf("vendor text leaked: %v", err)
			}
		})
	}
}

// TestSubQWire_RetryableStatuses proves the existing policy through SubQ-shaped
// envelopes: 402/403/429/503 stay recoverable pre-output while 400/413/422 are
// terminal rejections.
func TestSubQWire_RetryableStatuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status      int
		recoverable bool
	}{
		{402, true},
		{403, true},
		{429, true},
		{503, true},
		{400, false},
		{413, false},
		{422, false},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"detail":{"error_type":"subq_vendor_type","message":"subq-vendor-message"}}`)
			}))
			defer srv.Close()
			be, err := BuildCompatible("decision", backendNode(t, srv.URL), srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			stream, err := be.Open(t.Context(), decisionCall(), routing.AttemptCandidate{})
			if stream != nil || err == nil || aipapi.IsRecoverablePreOutput(err) != tc.recoverable || aipapi.IsDecisionReject(err) == tc.recoverable {
				t.Fatalf("status %d: stream=%v err=%v", tc.status, stream, err)
			}
		})
	}
}

// TestSubQWire_MalformedAnswersEmitNoDecisionResult proves malformed SubQ
// answers are rejected before any client-visible output: no decision result
// event is emitted and the error stays recoverable pre-output.
func TestSubQWire_MalformedAnswersEmitNoDecisionResult(t *testing.T) {
	t.Parallel()
	const usage = `,"usage":{"input_tokens":7,"output_tokens":0}`
	for name, answers := range map[string]string{
		"missing answer":           `"q_grade":{"type":"score","score":1.3,"probabilities":{"0":0.2,"1":0.3,"2":0.5}},"q_noul":{"type":"noul","noul":0.7}`,
		"out-of-range probability": `"q_grade":{"type":"score","score":1.3,"probabilities":{"0":0.2,"1":0.3,"2":0.5}},"q_noul":{"type":"noul","noul":1.5},"q_pick":{"type":"choice","choice":"beta","probabilities":{"beta":0.5,"alpha":0.3,"gamma":0.2}}`,
		"bad distribution":         `"q_grade":{"type":"score","score":1.3,"probabilities":{"0":0.2,"1":0.3,"2":0.5}},"q_noul":{"type":"noul","noul":0.7},"q_pick":{"type":"choice","choice":"beta","probabilities":{"beta":0.5,"alpha":0.3,"gamma":0.1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := `{"model":"subq-snap-preview","answers":{` + answers + `}` + usage + `}`
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
			defer srv.Close()
			events, err := evaluateWire(t.Context(), srv.Client(), srv.URL, "", "subq-snap-preview", subQRequest(), nil)
			if err == nil || !aipapi.IsRecoverablePreOutput(err) {
				t.Fatalf("malformed answers: err=%v", err)
			}
			// Nothing may reach the caller: evaluateWire builds its event slice
			// only after ValidateFor succeeds, so a malformed body must yield no
			// decision result, no usage sideband and no completion event.
			if len(events) != 0 {
				t.Fatalf("malformed answers emitted %d events: %+v", len(events), events)
			}
		})
	}
}
