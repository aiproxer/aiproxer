package extensions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

type atXform struct {
	id   string
	ord  int
	mode string
	fm   sdkhooks.FailureMode
}

func (t atXform) ID() string { return t.id }
func (t atXform) Order() int { return t.ord }
func (t atXform) FailureMode() sdkhooks.FailureMode {
	if t.fm == sdkhooks.FailureModeUnspecified {
		return sdkhooks.FailClosed
	}
	return t.fm
}

func (t atXform) HandleAttempt(ctx context.Context, call *aipapi.Call, _ request.AttemptMeta, _ request.Services) (request.AttemptDecision, error) {
	switch t.mode {
	case "exclude":
		return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: "unrepresentable_replay"}, nil
	case "mutate":
		if call != nil {
			call.Instructions = append(call.Instructions, aipapi.Message{
				Role:  aipapi.RoleSystem,
				Parts: []aipapi.Part{aipapi.TextPart("at-mut")},
			})
		}
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	case "mutate_err":
		if call != nil {
			call.Instructions = append(call.Instructions, aipapi.Message{
				Role:  aipapi.RoleSystem,
				Parts: []aipapi.Part{aipapi.TextPart("at-partial")},
			})
		}
		return request.AttemptDecision{}, errors.New("boom-after-mutate")
	case "mutate_exclude":
		if call != nil {
			call.Instructions = append(call.Instructions, aipapi.Message{
				Role:  aipapi.RoleSystem,
				Parts: []aipapi.Part{aipapi.TextPart("at-exclude-mut")},
			})
		}
		return request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: "unrepresentable_replay"}, nil
	case "hang_mutate":
		if call != nil {
			call.Instructions = append(call.Instructions, aipapi.Message{
				Role:  aipapi.RoleSystem,
				Parts: []aipapi.Part{aipapi.TextPart("at-timeout-mut")},
			})
		}
		<-ctx.Done()
		return request.AttemptDecision{}, ctx.Err()
	case "invalidate":
		if call != nil {
			call.Messages = nil
		}
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	case "invalid":
		return request.AttemptDecision{Kind: request.AttemptDecisionKind("nope")}, nil
	case "err":
		return request.AttemptDecision{}, errors.New("boom")
	default:
		return request.AttemptDecision{Kind: request.AttemptContinue}, nil
	}
}

func validAttemptCall() *aipapi.Call {
	return &aipapi.Call{
		ID: "at-call",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
	}
}

func TestRunCandidateAttemptTransformStage_continueMutatesAndValidates(t *testing.T) {
	t.Parallel()
	call := validAttemptCall()
	res, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil,
		[]request.AttemptTransform{atXform{id: "m", mode: "mutate"}},
		call, request.AttemptMeta{BackendID: "b", CandidateKey: "b:m", Model: "m"}, request.Services{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.Excluded {
		t.Fatal("want continue")
	}
	if len(call.Instructions) == 0 {
		t.Fatal("want mutation")
	}
}

func TestRunCandidateAttemptTransformStage_excludeCandidate(t *testing.T) {
	t.Parallel()
	call := validAttemptCall()
	res, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil,
		[]request.AttemptTransform{atXform{id: "x", mode: "exclude"}},
		call, request.AttemptMeta{}, request.Services{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Excluded || res.ReasonCode != "unrepresentable_replay" || res.ProviderID != "x" {
		t.Fatalf("res=%+v", res)
	}
}

func TestRunCandidateAttemptTransformStage_invalidDecisionFails(t *testing.T) {
	t.Parallel()
	_, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil,
		[]request.AttemptTransform{atXform{id: "bad", mode: "invalid"}},
		validAttemptCall(), request.AttemptMeta{}, request.Services{},
	)
	if err == nil {
		t.Fatal("want invalid decision error")
	}
}

func TestRunCandidateAttemptTransformStage_failOpenContinues(t *testing.T) {
	t.Parallel()
	call := validAttemptCall()
	res, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil,
		[]request.AttemptTransform{
			atXform{id: "e", mode: "err", fm: sdkhooks.FailOpen},
			atXform{id: "m", mode: "mutate", fm: sdkhooks.FailClosed},
		},
		call, request.AttemptMeta{}, request.Services{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.Excluded || len(call.Instructions) == 0 {
		t.Fatalf("fail-open must continue to mutate; res=%+v instr=%d", res, len(call.Instructions))
	}
}

func TestRunCandidateAttemptTransformStage_failOpenDiscardsPartialMutation(t *testing.T) {
	t.Parallel()
	call := validAttemptCall()
	res, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil,
		[]request.AttemptTransform{
			atXform{id: "partial", mode: "mutate_err", fm: sdkhooks.FailOpen},
			atXform{id: "ok", mode: "continue", fm: sdkhooks.FailClosed},
		},
		call, request.AttemptMeta{}, request.Services{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.Excluded {
		t.Fatal("want continue")
	}
	for _, msg := range call.Instructions {
		for _, p := range msg.Parts {
			if p.Text == "at-partial" {
				t.Fatal("fail-open error path must discard partial mutations")
			}
		}
	}
}

func TestRunCandidateAttemptTransformStage_excludeDiscardsPartialMutation(t *testing.T) {
	t.Parallel()
	call := validAttemptCall()
	_, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil,
		[]request.AttemptTransform{atXform{id: "ex", mode: "mutate_exclude", fm: sdkhooks.FailClosed}},
		call, request.AttemptMeta{}, request.Services{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(call.Instructions) != 0 {
		t.Fatalf("exclude must not keep mutations: %#v", call.Instructions)
	}
}

func TestRunCandidateAttemptTransformStage_timeoutDiscardsPartialMutation(t *testing.T) {
	t.Parallel()
	call := validAttemptCall()
	ctx := extensions.WithDecisionEvidence(t.Context(), &extensions.DecisionEvidence{
		TimeoutBudget: extensions.StaticTimeoutBudgetSource{Budget: 25 * time.Millisecond},
	})
	res, err := extensions.RunCandidateAttemptTransformStage(
		ctx, nil, nil,
		[]request.AttemptTransform{
			atXform{id: "hung", mode: "hang_mutate", fm: sdkhooks.FailOpen},
			atXform{id: "ok", mode: "continue", fm: sdkhooks.FailClosed},
		},
		call, request.AttemptMeta{}, request.Services{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.Excluded {
		t.Fatal("want continue after fail-open timeout")
	}
	for _, msg := range call.Instructions {
		for _, p := range msg.Parts {
			if p.Text == "at-timeout-mut" {
				t.Fatal("timeout path must discard partial mutations")
			}
		}
	}
}

func TestRunCandidateAttemptTransformStage_nonZeroTimeoutErrorDiscardsPartialMutation(t *testing.T) {
	t.Parallel()
	call := validAttemptCall()
	ctx := extensions.WithDecisionEvidence(t.Context(), &extensions.DecisionEvidence{
		TimeoutBudget: extensions.StaticTimeoutBudgetSource{Budget: time.Second},
	})
	res, err := extensions.RunCandidateAttemptTransformStage(
		ctx, nil, nil,
		[]request.AttemptTransform{
			atXform{id: "partial", mode: "mutate_err", fm: sdkhooks.FailOpen},
			atXform{id: "ok", mode: "continue", fm: sdkhooks.FailClosed},
		},
		call, request.AttemptMeta{}, request.Services{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.Excluded {
		t.Fatal("want continue")
	}
	for _, msg := range call.Instructions {
		for _, p := range msg.Parts {
			if p.Text == "at-partial" {
				t.Fatal("non-zero timeout error path must discard clone commit on failure")
			}
		}
	}
}

func TestRunCandidateAttemptTransformStage_invalidContinueRestoresBeforeNext(t *testing.T) {
	t.Parallel()
	call := validAttemptCall()
	_, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil,
		[]request.AttemptTransform{
			atXform{id: "bad", mode: "invalidate", fm: sdkhooks.FailClosed},
			atXform{id: "later", mode: "mutate", fm: sdkhooks.FailClosed},
		},
		call, request.AttemptMeta{}, request.Services{},
	)
	if err == nil {
		t.Fatal("want validate error after invalid continue")
	}
	if len(call.Messages) == 0 {
		t.Fatal("invalid intermediate must restore original before abort")
	}
	if len(call.Instructions) != 0 {
		t.Fatal("later transform must not run after invalid intermediate")
	}
}

func TestRunCandidateAttemptTransformStage_failClosedStops(t *testing.T) {
	t.Parallel()
	_, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, nil,
		[]request.AttemptTransform{atXform{id: "e", mode: "err", fm: sdkhooks.FailClosed}},
		validAttemptCall(), request.AttemptMeta{}, request.Services{},
	)
	if err == nil {
		t.Fatal("want fail-closed error")
	}
}

func TestRunCandidateAttemptTransformStage_nilCall(t *testing.T) {
	t.Parallel()
	_, err := extensions.RunCandidateAttemptTransformStage(t.Context(), nil, nil, nil, nil, request.AttemptMeta{}, request.Services{})
	if err == nil {
		t.Fatal("want nil call error")
	}
}

func TestRunCandidateAttemptTransformStage_absentParticipantsNoStageObservation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		ctx        context.Context
		transforms []request.AttemptTransform
	}{
		{"nil_slice", t.Context(), nil},
		{"empty_slice", t.Context(), []request.AttemptTransform{}},
		{"all_nil", t.Context(), []request.AttemptTransform{nil, nil}},
		{"all_suppressed", execctx.WithSuppressedPluginIDs(t.Context(), []string{"a", "b"}), []request.AttemptTransform{
			atXform{id: "a", mode: "mutate"},
			atXform{id: "b", mode: "mutate"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &recordingCountByteMetrics{}
			_, err := extensions.RunCandidateAttemptTransformStage(
				tc.ctx, nil, m, tc.transforms, validAttemptCall(), request.AttemptMeta{}, request.Services{},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(m.stages) != 0 || len(m.counts) != 0 || len(m.bytes) != 0 {
				t.Fatalf("absent participants must be observation no-op stages=%#v counts=%#v bytes=%#v", m.stages, m.counts, m.bytes)
			}
		})
	}
}

func TestRunCandidateAttemptTransformStage_recordsSafeReasoningBytes(t *testing.T) {
	t.Parallel()
	m := &recordingCountByteMetrics{}
	call := &aipapi.Call{
		ID: "at-bytes",
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}},
			{
				Role: aipapi.RoleAssistant,
				Parts: []aipapi.Part{{
					Kind: aipapi.PartReasoning,
					Reasoning: &aipapi.ReasoningPart{
						Dialect:   aipapi.ReasoningDialectOpenAIChatTextV1,
						Text:      "think",
						Signature: "sig",
					},
				}},
			},
		},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
	}
	_, err := extensions.RunCandidateAttemptTransformStage(
		t.Context(), nil, m,
		[]request.AttemptTransform{atXform{id: "m", mode: "continue"}},
		call, request.AttemptMeta{}, request.Services{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.stages) != 1 || len(m.counts) != 1 || m.counts[0] != 1 {
		t.Fatalf("stages=%#v counts=%#v", m.stages, m.counts)
	}
	if len(m.bytes) != 1 || m.bytes[0] != 8 {
		t.Fatalf("bytes=%#v want [8]", m.bytes)
	}
}
