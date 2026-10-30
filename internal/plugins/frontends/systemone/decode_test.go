package systemone

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDecode_PreservesEvidenceAndQuestionOrder(t *testing.T) {
	t.Parallel()
	for _, state := range []string{`"text"`, `{ "x" : [1,2] }`, `["a", {"b":2}]`} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			body := `{"model":"decision:jev","state":` + state + `,"questions":{"yes":{"type":"noul","instructions":"ask","criteria":{"true":"ok","false":null}},"pick":{"type":"choice","criteria":{"z":null,"a":"first"}},"grade":{"type":"score","criteria":["low",{"high":true}]}}}`
			call, err := decodeRequest([]byte(body), 256)
			if err != nil {
				t.Fatal(err)
			}
			if call.Decision == nil || string(call.Decision.Evidence) != state || call.Invocation.Operation != aipapi.OperationDecisionEvaluate || call.Invocation.DeliveryMode != aipapi.DeliveryModeNonStreaming || call.Route.Selector != "decision:jev" || call.Messages != nil || call.Tools != nil {
				t.Fatalf("decoded call = %+v", call)
			}
			qs := call.Decision.Questions
			if len(qs) != 3 || qs[0].ID != "yes" || qs[1].ID != "pick" || qs[2].ID != "grade" || qs[1].Options[0].Name != "z" || qs[1].Options[1].Name != "a" || string(qs[2].Levels[1]) != `{"high":true}` || string(qs[0].FalseCriteria) != "null" {
				t.Fatalf("question order/types not preserved: %+v", qs)
			}
		})
	}
}

func TestDecode_PreservesExactQuestionAndOptionNames(t *testing.T) {
	t.Parallel()
	call, err := decodeRequest([]byte(`{"model":"jev","state":"x","questions":{" q ":{"type":"choice","criteria":{" option ":null}}}}`), 256)
	if err != nil {
		t.Fatal(err)
	}
	if call.Decision.Questions[0].ID != " q " || call.Decision.Questions[0].Options[0].Name != " option " {
		t.Fatal("client labels were normalized")
	}
}

func TestDecode_RejectsInvalidFieldsAndLimits(t *testing.T) {
	t.Parallel()
	wantFields := map[string]string{
		"missing model": "body.model", "unknown field": "body.provider", "unknown question field": "body.questions.q.extra",
		"unknown type": "body.questions.q.type", "missing criteria": "body.questions.q.criteria", "empty choice": "body.questions[0].criteria",
		"one score level": "body.questions.q.criteria", "numeric state": "body.state", "null state": "body.state",
		"duplicate": "body", "trailing": "body", "deep": "body",
	}
	for name, body := range map[string]string{
		"missing model":          `{"state":"x","questions":{"q":{"type":"noul"}}}`,
		"unknown field":          `{"model":"jev","state":"x","questions":{"q":{"type":"noul"}},"provider":"bad"}`,
		"unknown question field": `{"model":"jev","state":"x","questions":{"q":{"type":"noul","extra":true}}}`,
		"unknown type":           `{"model":"jev","state":"x","questions":{"q":{"type":"bool"}}}`,
		"missing criteria":       `{"model":"jev","state":"x","questions":{"q":{"type":"choice"}}}`,
		"empty choice":           `{"model":"jev","state":"x","questions":{"q":{"type":"choice","criteria":{}}}}`,
		"one score level":        `{"model":"jev","state":"x","questions":{"q":{"type":"score","criteria":["one"]}}}`,
		"numeric state":          `{"model":"jev","state":1,"questions":{"q":{"type":"noul"}}}`,
		"null state":             `{"model":"jev","state":null,"questions":{"q":{"type":"noul"}}}`,
		"duplicate":              `{"model":"jev","state":"x","questions":{"q":{"type":"noul"},"q":{"type":"noul"}}}`,
		"trailing":               `{"model":"jev","state":"x","questions":{"q":{"type":"noul"}}}{}`,
		"deep":                   `{"model":"jev","state":` + strings.Repeat("[", 65) + `0` + strings.Repeat("]", 65) + `,"questions":{"q":{"type":"noul"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			call, err := decodeRequest([]byte(body), 256)
			rejection, ok := errors.AsType[*aipapi.DecisionRejectError](err)
			if call != nil || !ok || rejection.Field != wantFields[name] || rejection.Message == "" {
				t.Fatalf("invalid request: call=%v err=%v", call, err)
			}
		})
	}
	body := `{"model":"jev","state":"x","questions":{"one":{"type":"noul"},"two":{"type":"noul"}}}`
	_, err := decodeRequest([]byte(body), 1)
	rejection, ok := errors.AsType[*aipapi.DecisionRejectError](err)
	if !ok || rejection.Field != "body.questions" {
		t.Fatalf("question limit: %v", err)
	}
}

func FuzzDecode_NoPanicOrLossyAuthority(f *testing.F) {
	f.Add([]byte(`{"model":"jev","state":"x","questions":{"q":{"type":"noul"}}}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, body []byte) {
		call, err := decodeRequest(body, 256)
		if err != nil {
			return
		}
		if call == nil || call.Decision == nil || call.Validate() != nil {
			t.Fatal("successful decode has invalid decision authority")
		}
		clone := aipapi.CloneCall(*call)
		if !reflect.DeepEqual(clone.Decision, call.Decision) {
			t.Fatal("decision changed during clone")
		}
	})
}
