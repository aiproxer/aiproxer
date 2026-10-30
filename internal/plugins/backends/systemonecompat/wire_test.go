package systemonecompat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func wireDecision() aipapi.DecisionRequest {
	return aipapi.DecisionRequest{Evidence: json.RawMessage(`{ "state" : [1, "x"] }`), Questions: []aipapi.DecisionQuestion{
		{ID: "yes", Kind: aipapi.DecisionKindNoul, Instructions: json.RawMessage(`"ask"`), TrueCriteria: json.RawMessage(`"yes"`)},
		{ID: "pick", Kind: aipapi.DecisionKindChoice, Options: []aipapi.DecisionOption{{Name: "z"}, {Name: "a", Description: json.RawMessage(`"option"`)}}},
		{ID: "grade", Kind: aipapi.DecisionKindScore, Levels: []json.RawMessage{json.RawMessage(`"lo"`), json.RawMessage(`"hi"`)}},
	}}
}

const wireResponse = `{"model":"jev-resolved","provider":"ignored","answers":{"grade":{"type":"score","score":0.75,"probabilities":{"0":0.25,"1":0.75},"legend":{"0":"lo","1":"hi"},"confidence":0.9},"pick":{"type":"choice","choice":"a","probabilities":{"a":0.8,"z":0.2},"confidence":0},"yes":{"type":"noul","noul":0.6,"extra":"ignored"}},"usage":{"input_tokens":0,"cost":0.000001}}`

func TestWire_RoundTripPreservesOrderAndUsagePresence(t *testing.T) {
	t.Parallel()
	type wireCapture struct {
		body    string
		headers http.Header
	}
	// Buffered size 1 so the handler never blocks: httptest serves each
	// request on its own goroutine and socket I/O establishes no
	// happens-before edge, so the captured request must be handed over via
	// channel receive below rather than shared variables. On an error path
	// that returns before the receive, the buffered send still completes and
	// the value is simply discarded, so no handler goroutine can leak blocked.
	captured := make(chan wireCapture, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		captured <- wireCapture{body: string(data), headers: r.Header.Clone()}
		if r.URL.Path != "/v1/systemone" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, wireResponse)
	}))
	defer srv.Close()
	events, err := evaluateWire(t.Context(), srv.Client(), srv.URL+"/v1/systemone", "operator-key", "native-model", wireDecision(), http.Header{
		"X-Session-ID": {"must-not-forward"}, "X-Title": {"operator-title"}, "Authorization": {"wrong-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := <-captured
	body, headers := got.body, got.headers
	want := `{"model":"native-model","state":{ "state" : [1, "x"] },"questions":{"yes":{"type":"noul","instructions":"ask","criteria":{"true":"yes"}},"pick":{"type":"choice","criteria":{"z":null,"a":"option"}},"grade":{"type":"score","criteria":["lo","hi"]}}}`
	if body != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
	if headers.Get("Authorization") != "Bearer operator-key" || headers.Get("Content-Type") != "application/json" || headers.Get("X-Session-ID") != "" || headers.Get("X-Title") != "operator-title" {
		t.Fatalf("unexpected headers: %v", headers)
	}
	if len(events) != 4 || events[0].Kind != aipapi.EventResponseStarted || events[1].Kind != aipapi.EventDecisionResult || events[2].Kind != aipapi.EventUsageDelta || events[3].Kind != aipapi.EventResponseFinished {
		t.Fatalf("event sequence = %+v", events)
	}
	result := events[1].Decision
	if result == nil || result.Model != "jev-resolved" || len(result.Answers) != 3 || result.Answers[0].QuestionID != "yes" || result.Answers[1].Selected != "a" || !reflect.DeepEqual(result.Answers[1].Probabilities, []float64{0.2, 0.8}) || result.Answers[1].Confidence == nil || *result.Answers[1].Confidence != 0 || result.Answers[2].Expected != 0.75 {
		t.Fatalf("result = %+v", result)
	}
	usage := events[2]
	if !usage.UsagePresence.InputTokens || usage.UsagePresence.OutputTokens || usage.InputTokens != 0 || !usage.CostPresent || usage.CostNanoUnits != 1000 || usage.Currency != "USD" || usage.CostSource != "provider_reported" {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestWire_InvalidResponseEmitsNoEvents(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"negative cost":       strings.Replace(wireResponse, `"cost":0.000001`, `"cost":-0.00000000001`, 1),
		"missing answer":      strings.Replace(wireResponse, `"yes":{"type":"noul","noul":0.6,"extra":"ignored"}`, `"other":{"type":"noul","noul":0.6}`, 1),
		"unknown option":      strings.Replace(wireResponse, `"z":0.2`, `"unknown":0.2`, 1),
		"bad distribution":    strings.Replace(wireResponse, `"a":0.8`, `"a":0.1`, 1),
		"missing probability": strings.Replace(wireResponse, `"noul":0.6,`, ``, 1),
		"null probability":    strings.Replace(wireResponse, `"noul":0.6`, `"noul":null`, 1),
		"trailing":            wireResponse + `{}`,
		"oversized":           strings.Repeat(" ", (1<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
			defer srv.Close()
			events, err := evaluateWire(t.Context(), srv.Client(), srv.URL, "", "jev", wireDecision(), nil)
			if len(events) != 0 || !aipapi.IsRecoverablePreOutput(err) {
				t.Fatalf("invalid answer: events=%d err=%v", len(events), err)
			}
		})
	}
}
