package aipapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func mkDecisionNoulQuestion(id string) aipapi.DecisionQuestion {
	return aipapi.DecisionQuestion{
		ID:           id,
		Kind:         aipapi.DecisionKindNoul,
		Instructions: json.RawMessage(`"decide whether this is acceptable"`),
		TrueCriteria: json.RawMessage(`{"rationale":"acceptable"}`),
	}
}

func mkDecisionChoiceQuestion(id string, options ...string) aipapi.DecisionQuestion {
	q := aipapi.DecisionQuestion{
		ID:           id,
		Kind:         aipapi.DecisionKindChoice,
		Instructions: json.RawMessage(`"pick one region"`),
	}
	for _, opt := range options {
		q.Options = append(q.Options, aipapi.DecisionOption{Name: opt})
	}
	return q
}

func mkDecisionScoreQuestion(id string, levels int) aipapi.DecisionQuestion {
	q := aipapi.DecisionQuestion{
		ID:           id,
		Kind:         aipapi.DecisionKindScore,
		Instructions: json.RawMessage(`"grade the urgency"`),
	}
	for i := range levels {
		q.Levels = append(q.Levels, json.RawMessage(fmt.Sprintf(`"level-%d"`, i)))
	}
	return q
}

func mkDecisionRequest() aipapi.DecisionRequest {
	return aipapi.DecisionRequest{
		Evidence: json.RawMessage(`{"prompt":"ship the release"}`),
		Questions: []aipapi.DecisionQuestion{
			mkDecisionNoulQuestion("is-ok"),
			mkDecisionChoiceQuestion("which-region", "eu", "us"),
			mkDecisionScoreQuestion("how-urgent", 3),
		},
	}
}

func mkDecisionResult() aipapi.DecisionResult {
	return aipapi.DecisionResult{
		Model: "jev-latest",
		Answers: []aipapi.DecisionAnswer{
			{QuestionID: "is-ok", Kind: aipapi.DecisionKindNoul, PTrue: 0.9},
			{QuestionID: "which-region", Kind: aipapi.DecisionKindChoice, Selected: "us", Probabilities: []float64{0.25, 0.75}},
			{QuestionID: "how-urgent", Kind: aipapi.DecisionKindScore, Expected: 1.2, Probabilities: []float64{0.2, 0.5, 0.3}},
		},
	}
}

func assertDecisionValidationError(t *testing.T, err error, wantField string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected rejection naming field %q, got nil", wantField)
	}
	ve, ok := errors.AsType[*aipapi.ValidationError](err)
	if !ok {
		t.Fatalf("error type = %T (%v), want *aipapi.ValidationError", err, err)
	}
	if ve.Field != wantField {
		t.Fatalf("ValidationError.Field = %q (message %q), want %q", ve.Field, ve.Message, wantField)
	}
	if ve.Message == "" {
		t.Fatalf("ValidationError for %q carries no message", wantField)
	}
	if !errors.Is(err, aipapi.ErrInvalidCall) {
		t.Fatalf("error %v must wrap ErrInvalidCall", err)
	}
}

func TestDecisionRequestValidate_acceptsThreeKinds(t *testing.T) {
	t.Parallel()

	if err := mkDecisionRequest().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil for a noul, choice and score question", err)
	}
}

func TestDecisionRequestValidate_acceptsAbsentOptionalFields(t *testing.T) {
	t.Parallel()

	req := aipapi.DecisionRequest{
		Evidence: json.RawMessage(`"plain string evidence"`),
		Questions: []aipapi.DecisionQuestion{
			{ID: "noul", Kind: aipapi.DecisionKindNoul},
			{ID: "choice", Kind: aipapi.DecisionKindChoice, Options: []aipapi.DecisionOption{
				{Name: "only", Description: json.RawMessage(`null`)},
			}},
			{ID: "score", Kind: aipapi.DecisionKindScore, Levels: []json.RawMessage{
				json.RawMessage(`"low"`), json.RawMessage(`{"label":"high"}`),
			}},
		},
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil when optional fields are absent", err)
	}
}

func TestDecisionRequestValidate_rejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	manyOptions := make([]aipapi.DecisionOption, aipapi.MaxDecisionChoiceOptions+1)
	for i := range manyOptions {
		manyOptions[i] = aipapi.DecisionOption{Name: fmt.Sprintf("opt-%03d", i)}
	}
	longOptionName := strings.Repeat("n", aipapi.MaxDecisionOptionNameBytes+1)

	cases := []struct {
		name  string
		req   aipapi.DecisionRequest
		field string
	}{
		{
			name:  "missing evidence",
			req:   aipapi.DecisionRequest{Questions: []aipapi.DecisionQuestion{mkDecisionNoulQuestion("q")}},
			field: "Evidence",
		},
		{
			name:  "malformed evidence json",
			req:   aipapi.DecisionRequest{Evidence: json.RawMessage(`{"prompt":`), Questions: []aipapi.DecisionQuestion{mkDecisionNoulQuestion("q")}},
			field: "Evidence",
		},
		{
			name:  "no questions",
			req:   aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`)},
			field: "Questions",
		},
		{
			name: "empty question id",
			req: aipapi.DecisionRequest{
				Evidence:  json.RawMessage(`"evidence"`),
				Questions: []aipapi.DecisionQuestion{mkDecisionNoulQuestion("")},
			},
			field: "Questions[0].ID",
		},
		{
			name: "oversized question id",
			req: aipapi.DecisionRequest{
				Evidence:  json.RawMessage(`"evidence"`),
				Questions: []aipapi.DecisionQuestion{mkDecisionNoulQuestion(strings.Repeat("q", aipapi.MaxDecisionQuestionIDBytes+1))},
			},
			field: "Questions[0].ID",
		},
		{
			name: "duplicate question id",
			req: aipapi.DecisionRequest{
				Evidence:  json.RawMessage(`"evidence"`),
				Questions: []aipapi.DecisionQuestion{mkDecisionNoulQuestion("dup"), mkDecisionNoulQuestion("dup")},
			},
			field: "Questions[1].ID",
		},
		{
			name: "missing kind",
			req: aipapi.DecisionRequest{
				Evidence:  json.RawMessage(`"evidence"`),
				Questions: []aipapi.DecisionQuestion{{ID: "q"}},
			},
			field: "Questions[0].Kind",
		},
		{
			name: "unknown kind",
			req: aipapi.DecisionRequest{
				Evidence:  json.RawMessage(`"evidence"`),
				Questions: []aipapi.DecisionQuestion{{ID: "q", Kind: aipapi.DecisionKind("boolean")}},
			},
			field: "Questions[0].Kind",
		},
		{
			name: "noul question with options",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				func() aipapi.DecisionQuestion {
					q := mkDecisionNoulQuestion("q")
					q.Options = []aipapi.DecisionOption{{Name: "a"}}
					return q
				}(),
			}},
			field: "Questions[0].Options",
		},
		{
			name: "noul question with levels",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				func() aipapi.DecisionQuestion {
					q := mkDecisionNoulQuestion("q")
					q.Levels = []json.RawMessage{json.RawMessage(`"low"`), json.RawMessage(`"high"`)}
					return q
				}(),
			}},
			field: "Questions[0].Levels",
		},
		{
			name: "choice question with levels",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				func() aipapi.DecisionQuestion {
					q := mkDecisionChoiceQuestion("q", "a", "b")
					q.Levels = []json.RawMessage{json.RawMessage(`"low"`), json.RawMessage(`"high"`)}
					return q
				}(),
			}},
			field: "Questions[0].Levels",
		},
		{
			name: "choice question with true criteria",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				func() aipapi.DecisionQuestion {
					q := mkDecisionChoiceQuestion("q", "a", "b")
					q.TrueCriteria = json.RawMessage(`"only for noul"`)
					return q
				}(),
			}},
			field: "Questions[0].TrueCriteria",
		},
		{
			name: "score question with false criteria",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				func() aipapi.DecisionQuestion {
					q := mkDecisionScoreQuestion("q", 3)
					q.FalseCriteria = json.RawMessage(`"only for noul"`)
					return q
				}(),
			}},
			field: "Questions[0].FalseCriteria",
		},
		{
			name: "score question with options",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				func() aipapi.DecisionQuestion {
					q := mkDecisionScoreQuestion("q", 3)
					q.Options = []aipapi.DecisionOption{{Name: "a"}}
					return q
				}(),
			}},
			field: "Questions[0].Options",
		},
		{
			name: "choice question without options",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				mkDecisionChoiceQuestion("q"),
			}},
			field: "Questions[0].Options",
		},
		{
			name:  "choice question above the option ceiling",
			req:   aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{{ID: "q", Kind: aipapi.DecisionKindChoice, Options: manyOptions}}},
			field: "Questions[0].Options",
		},
		{
			name: "duplicate option names",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				mkDecisionChoiceQuestion("q", "eu", "eu"),
			}},
			field: "Questions[0].Options[1].Name",
		},
		{
			name: "empty option name",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				mkDecisionChoiceQuestion("q", ""),
			}},
			field: "Questions[0].Options[0].Name",
		},
		{
			name: "option name above its byte bound",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				mkDecisionChoiceQuestion("q", longOptionName, "b"),
			}},
			field: "Questions[0].Options[0].Name",
		},
		{
			name: "malformed option description",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				{ID: "q", Kind: aipapi.DecisionKindChoice, Options: []aipapi.DecisionOption{
					{Name: "a", Description: json.RawMessage(`{"unterminated`)},
				}},
			}},
			field: "Questions[0].Options[0].Description",
		},
		{
			name: "score question without levels",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				mkDecisionScoreQuestion("q", 0),
			}},
			field: "Questions[0].Levels",
		},
		{
			name: "score question with one level",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				mkDecisionScoreQuestion("q", 1),
			}},
			field: "Questions[0].Levels",
		},
		{
			name: "score question above the level ceiling",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				mkDecisionScoreQuestion("q", aipapi.MaxDecisionScoreLevels+1),
			}},
			field: "Questions[0].Levels",
		},
		{
			name: "malformed level description",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				{ID: "q", Kind: aipapi.DecisionKindScore, Levels: []json.RawMessage{
					json.RawMessage(`"low"`), json.RawMessage(`[1,`),
				}},
			}},
			field: "Questions[0].Levels[1]",
		},
		{
			name: "malformed instructions",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				{ID: "q", Kind: aipapi.DecisionKindNoul, Instructions: json.RawMessage(`"unterminated`)},
			}},
			field: "Questions[0].Instructions",
		},
		{
			name: "malformed noul criteria",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				{ID: "q", Kind: aipapi.DecisionKindNoul, FalseCriteria: json.RawMessage(`[1,`)},
			}},
			field: "Questions[0].FalseCriteria",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertDecisionValidationError(t, tc.req.Validate(), tc.field)
		})
	}
}

func TestDecisionResultValidateFor_acceptsMatchingResult(t *testing.T) {
	t.Parallel()

	res := mkDecisionResult()
	if err := res.ValidateFor(mkDecisionRequest()); err != nil {
		t.Fatalf("ValidateFor() = %v, want nil for a matching three-type result", err)
	}
	// A producer that normalizes integer weights carries float round-off: 1/6,
	// 4/6 and 1/6 sum to 0.9999999999999999, so they must still be accepted
	// within the documented tolerance.
	res = mkDecisionResult()
	res.Answers[2].Probabilities = []float64{1.0 / 6.0, 4.0 / 6.0, 1.0 / 6.0}
	if err := res.ValidateFor(mkDecisionRequest()); err != nil {
		t.Fatalf("ValidateFor() = %v, want nil for a distribution summing to 1 within round-off tolerance", err)
	}
}

func TestDecisionResultValidateFor_confidenceIsOptionalAndNeverDerived(t *testing.T) {
	t.Parallel()

	req := mkDecisionRequest()
	for _, confidence := range []*float64{nil, new(0.0), new(0.5), new(1.0)} {
		res := mkDecisionResult()
		res.Answers[1].Confidence = confidence
		if err := res.ValidateFor(req); err != nil {
			t.Fatalf("ValidateFor() with confidence %v = %v, want nil", confidence, err)
		}
		// ValidateFor shares the caller's Answers backing array, so a derived
		// confidence would be visible here as a replaced pointer.
		if res.Answers[1].Confidence != confidence {
			t.Fatalf("ValidateFor() replaced confidence %v with %v; it must never derive one", confidence, res.Answers[1].Confidence)
		}
		if res.Answers[0].Confidence != nil || res.Answers[2].Confidence != nil {
			t.Fatalf("ValidateFor() derived confidence for answers that reported none: %v, %v",
				res.Answers[0].Confidence, res.Answers[2].Confidence)
		}
	}
}

func TestDecisionResultValidateFor_acceptsEitherTiedMaximumOption(t *testing.T) {
	t.Parallel()

	req := mkDecisionRequest()
	for _, selected := range []string{"eu", "us"} {
		res := mkDecisionResult()
		res.Answers[1].Selected = selected
		res.Answers[1].Probabilities = []float64{0.5, 0.5}
		if err := res.ValidateFor(req); err != nil {
			t.Fatalf("ValidateFor() selecting tied option %q = %v, want nil", selected, err)
		}
	}
}

func TestDecisionResultValidateFor_rejectsInvalidResults(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		result aipapi.DecisionResult
		field  string
	}{
		{
			name:   "missing model",
			result: aipapi.DecisionResult{Answers: mkDecisionResult().Answers},
			field:  "Model",
		},
		{
			name: "oversized model",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) {
				r.Model = strings.Repeat("m", aipapi.MaxRefStringBytes+1)
			}),
			field: "Model",
		},
		{
			name:   "missing answer",
			result: aipapi.DecisionResult{Model: "jev-latest", Answers: mkDecisionResult().Answers[:2]},
			field:  "Answers",
		},
		{
			name: "extra answer",
			result: aipapi.DecisionResult{Model: "jev-latest", Answers: append(mkDecisionResult().Answers,
				aipapi.DecisionAnswer{QuestionID: "extra", Kind: aipapi.DecisionKindNoul, PTrue: 0.5})},
			field: "Answers",
		},
		{
			name: "answers out of request order",
			result: aipapi.DecisionResult{Model: "jev-latest", Answers: []aipapi.DecisionAnswer{
				mkDecisionResult().Answers[1],
				mkDecisionResult().Answers[0],
				mkDecisionResult().Answers[2],
			}},
			field: "Answers[0].QuestionID",
		},
		{
			name: "duplicate answer for the same question",
			result: aipapi.DecisionResult{Model: "jev-latest", Answers: []aipapi.DecisionAnswer{
				mkDecisionResult().Answers[0],
				mkDecisionResult().Answers[0],
				mkDecisionResult().Answers[2],
			}},
			field: "Answers[1].QuestionID",
		},
		{
			name:   "answer names an unknown question",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[0].QuestionID = "is-fine" }),
			field:  "Answers[0].QuestionID",
		},
		{
			name:   "answer kind disagrees with the question",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[1].Kind = aipapi.DecisionKindNoul }),
			field:  "Answers[1].Kind",
		},
		{
			name:   "nan ptrue",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[0].PTrue = math.NaN() }),
			field:  "Answers[0].PTrue",
		},
		{
			name:   "infinite ptrue",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[0].PTrue = math.Inf(1) }),
			field:  "Answers[0].PTrue",
		},
		{
			name:   "ptrue above one",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[0].PTrue = 1.5 }),
			field:  "Answers[0].PTrue",
		},
		{
			name:   "ptrue below zero",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[0].PTrue = -0.5 }),
			field:  "Answers[0].PTrue",
		},
		{
			name:   "choice probability count mismatch",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[1].Probabilities = []float64{1} }),
			field:  "Answers[1].Probabilities",
		},
		{
			name:   "nan choice probability",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[1].Probabilities = []float64{math.NaN(), 1} }),
			field:  "Answers[1].Probabilities[0]",
		},
		{
			name:   "negative choice probability",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[1].Probabilities = []float64{-0.25, 1.25} }),
			field:  "Answers[1].Probabilities[0]",
		},
		{
			name:   "choice probability above one",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[1].Probabilities = []float64{1.25, -0.25} }),
			field:  "Answers[1].Probabilities[0]",
		},
		{
			name:   "choice distribution does not sum to one",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[1].Probabilities = []float64{0.25, 0.5} }),
			field:  "Answers[1].Probabilities",
		},
		{
			name:   "selected option was never requested",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[1].Selected = "apac" }),
			field:  "Answers[1].Selected",
		},
		{
			name: "selected option is not the maximum",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) {
				r.Answers[1].Selected = "us"
				r.Answers[1].Probabilities = []float64{0.8, 0.2}
			}),
			field: "Answers[1].Selected",
		},
		{
			name:   "score probability count mismatch",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[2].Probabilities = []float64{0.5, 0.5} }),
			field:  "Answers[2].Probabilities",
		},
		{
			name:   "score distribution does not sum to one",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[2].Probabilities = []float64{0.2, 0.5, 0.2} }),
			field:  "Answers[2].Probabilities",
		},
		{
			name:   "nan expected score",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[2].Expected = math.NaN() }),
			field:  "Answers[2].Expected",
		},
		{
			name:   "expected score below zero",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[2].Expected = -0.1 }),
			field:  "Answers[2].Expected",
		},
		{
			name:   "expected score above the top level",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) { r.Answers[2].Expected = 2.5 }),
			field:  "Answers[2].Expected",
		},
		{
			name: "nan confidence",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) {
				r.Answers[1].Confidence = new(math.NaN())
			}),
			field: "Answers[1].Confidence",
		},
		{
			name: "confidence below zero",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) {
				r.Answers[1].Confidence = new(-0.1)
			}),
			field: "Answers[1].Confidence",
		},
		{
			name: "confidence above one",
			result: mutateDecisionResult(mkDecisionResult(), func(r *aipapi.DecisionResult) {
				r.Answers[1].Confidence = new(1.1)
			}),
			field: "Answers[1].Confidence",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertDecisionValidationError(t, tc.result.ValidateFor(mkDecisionRequest()), tc.field)
		})
	}
}

func TestIsDecisionReject_detectsClientSafeDecisionRejections(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("systemone decode: %w", &aipapi.DecisionRejectError{
		Field:   "questions[0].options",
		Message: "at most 255 options",
	})
	if !aipapi.IsDecisionReject(wrapped) {
		t.Fatal("IsDecisionReject must recognize a wrapped DecisionRejectError")
	}
	rej, ok := errors.AsType[*aipapi.DecisionRejectError](wrapped)
	if !ok {
		t.Fatalf("error type = %T, want *aipapi.DecisionRejectError", wrapped)
	}
	if rej.Field != "questions[0].options" || rej.Message != "at most 255 options" {
		t.Fatalf("DecisionRejectError = %+v, want the rejected field and bounded message", rej)
	}
	if got, want := rej.Error(), "questions[0].options: at most 255 options"; got != want {
		t.Fatalf("DecisionRejectError.Error() = %q, want %q", got, want)
	}
	if !errors.Is(wrapped, aipapi.ErrInvalidCall) {
		t.Fatal("a decision reject must unwrap to ErrInvalidCall")
	}
	if aipapi.IsReject(wrapped) {
		t.Fatal("a decision reject must not be a capability reject")
	}
	if aipapi.IsDecisionReject(&aipapi.ValidationError{Field: "Messages"}) {
		t.Fatal("IsDecisionReject must not claim canonical validation errors")
	}
	if aipapi.IsDecisionReject(nil) {
		t.Fatal("IsDecisionReject(nil) must be false")
	}
	if aipapi.IsDecisionReject(errors.New("boom")) {
		t.Fatal("IsDecisionReject must be false for an unrelated error")
	}
}

// TestDecisionRequestValidate_rejectsOversizedPayloadFields proves each
// JSON-valued decision payload is bounded: evidence, instructions, noul
// criteria, choice option descriptions and score levels above their per-field
// byte bound are rejected naming the offending field, so one client request
// cannot carry an unbounded payload into the runtime.
func TestDecisionRequestValidate_rejectsOversizedPayloadFields(t *testing.T) {
	t.Parallel()

	oversized := json.RawMessage(`"` + strings.Repeat("x", aipapi.MaxDecisionEvidenceBytes) + `"`)
	cases := []struct {
		name  string
		req   aipapi.DecisionRequest
		field string
	}{
		{
			name:  "oversized evidence",
			req:   aipapi.DecisionRequest{Evidence: oversized, Questions: []aipapi.DecisionQuestion{mkDecisionNoulQuestion("q")}},
			field: "Evidence",
		},
		{
			name: "oversized instructions",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				{ID: "q", Kind: aipapi.DecisionKindNoul, Instructions: oversized},
			}},
			field: "Questions[0].Instructions",
		},
		{
			name: "oversized noul true criteria",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				{ID: "q", Kind: aipapi.DecisionKindNoul, TrueCriteria: oversized},
			}},
			field: "Questions[0].TrueCriteria",
		},
		{
			name: "oversized noul false criteria",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				{ID: "q", Kind: aipapi.DecisionKindNoul, FalseCriteria: oversized},
			}},
			field: "Questions[0].FalseCriteria",
		},
		{
			name: "oversized option description",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				{ID: "q", Kind: aipapi.DecisionKindChoice, Options: []aipapi.DecisionOption{
					{Name: "a", Description: oversized},
					{Name: "b"},
				}},
			}},
			field: "Questions[0].Options[0].Description",
		},
		{
			name: "oversized score level",
			req: aipapi.DecisionRequest{Evidence: json.RawMessage(`"evidence"`), Questions: []aipapi.DecisionQuestion{
				{ID: "q", Kind: aipapi.DecisionKindScore, Levels: []json.RawMessage{
					json.RawMessage(`"low"`), json.RawMessage(`"high"`), oversized,
				}},
			}},
			field: "Questions[0].Levels[2]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertDecisionValidationError(t, tc.req.Validate(), tc.field)
		})
	}
}

func mutateDecisionResult(res aipapi.DecisionResult, mutate func(*aipapi.DecisionResult)) aipapi.DecisionResult {
	mutate(&res)
	return res
}
