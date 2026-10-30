package verifier

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

type fakeCollector struct {
	calls   int
	request auxiliary.Request
	ctx     context.Context
	collect func(context.Context, auxiliary.Request) (aipapi.Collected, error)
}

func (f *fakeCollector) Collect(ctx context.Context, req auxiliary.Request) (aipapi.Collected, error) {
	f.calls++
	f.ctx = ctx
	f.request = req
	if f.collect != nil {
		return f.collect(ctx, req)
	}
	return collectedText(`{"kind":"COMPLETE","reason":"done"}`), nil
}

func collectedText(value string) aipapi.Collected {
	var collected aipapi.Collected
	collected.Text.WriteString(value)
	return collected
}

func TestVerifierBuildsBoundedDetachedPrivateRequest(t *testing.T) {
	t.Parallel()
	collector := &fakeCollector{}
	verifier := New(collector, Config{Role: "semantic-check", Timeout: time.Minute})

	verdict, err := verifier.Verify(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verdict.Kind != VerdictComplete {
		t.Fatalf("verdict kind = %q, want COMPLETE", verdict.Kind)
	}
	if collector.calls != 1 {
		t.Fatalf("collector calls = %d, want 1", collector.calls)
	}
	req := collector.request
	if req.Role != "semantic-check" {
		t.Fatalf("role = %q, want semantic-check", req.Role)
	}
	if req.Visibility != VisibilityPrivate {
		t.Fatalf("visibility = %q, want private", req.Visibility)
	}
	if req.SessionMode != auxiliary.SessionModeDetached {
		t.Fatalf("session mode = %v, want detached", req.SessionMode)
	}
	if req.ParentTraceID != "trace-1" || req.ParentALegID != "a-leg-1" || req.ParentBLegID != "b-leg-1" {
		t.Fatalf("lineage = trace %q, a-leg %q, b-leg %q", req.ParentTraceID, req.ParentALegID, req.ParentBLegID)
	}
	if len(req.DisablePlugins) != 1 || req.DisablePlugins[0] != RecursionPluginID {
		t.Fatalf("disabled plugins = %#v, want only recursion suppression", req.DisablePlugins)
	}
	if req.Call == nil || len(req.Call.Messages) != 1 {
		t.Fatalf("request call/messages = %#v, want one prompt message", req.Call)
	}
	if len(req.Call.Tools) != 0 || req.Call.ToolChoice.Mode != "" || req.Call.ToolChoice.Name != "" || len(req.Call.ToolChoice.AllowedTools) != 0 {
		t.Fatal("verifier request must expose no tools or tool choice")
	}
	if _, ok := collector.ctx.Deadline(); !ok {
		t.Fatal("verifier collector context has no bounded deadline")
	}
	if !strings.Contains(req.Call.Messages[0].Parts[0].Text, "COMPLETE|INCOMPLETE|UNCERTAIN") {
		t.Fatal("verifier prompt omitted strict result contract")
	}
}

func TestParseAcceptsStrictVerdictKinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want Kind
	}{
		{name: "complete", raw: `{"kind":"COMPLETE","reason":"done"}`, want: VerdictComplete},
		{name: "incomplete", raw: `{"kind":"INCOMPLETE","reason":"more","objective":"run tests"}`, want: VerdictIncomplete},
		{name: "uncertain", raw: `{"kind":"UNCERTAIN","reason":"unclear"}`, want: VerdictUncertain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verdict, err := Parse(tc.raw)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if verdict.Kind != tc.want {
				t.Fatalf("kind = %q, want %q", verdict.Kind, tc.want)
			}
		})
	}
}

func TestParseRejectsMalformedOrUnboundedResponses(t *testing.T) {
	t.Parallel()
	const secret = "verifier-secret"
	cases := map[string]string{
		"leading prose":        `prefix {"kind":"COMPLETE"}`,
		"trailing prose":       `{"kind":"COMPLETE"} trailing`,
		"multiple values":      `{"kind":"COMPLETE"}{"kind":"UNCERTAIN"}`,
		"unknown field":        `{"kind":"COMPLETE","` + secret + `":"x"}`,
		"duplicate field":      `{"kind":"COMPLETE","kind":"UNCERTAIN"}`,
		"wrong field type":     `{"kind":["COMPLETE"]}`,
		"invalid kind":         `{"kind":"MAYBE"}`,
		"incomplete objective": `{"kind":"INCOMPLETE","reason":"more"}`,
		"oversized response":   `{"kind":"COMPLETE","reason":"` + strings.Repeat("x", MaxResponseBytes) + `"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(raw)
			if err == nil {
				t.Fatal("Parse() unexpectedly accepted malformed response")
			}
			if _, ok := errors.AsType[*ParseError](err); !ok {
				t.Fatalf("error = %T %v, want ParseError", err, err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), raw) {
				t.Fatal("parse error leaked response content")
			}
		})
	}
}

func TestParseBoundsReasonAndObjectiveWithoutChainOfThought(t *testing.T) {
	t.Parallel()
	raw := `{"kind":"INCOMPLETE","reason":"` + strings.Repeat("r", MaxReasonBytes+100) + `","objective":"` + strings.Repeat("o", MaxObjectiveBytes+100) + `"}`
	verdict, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(verdict.Reason) > MaxReasonBytes || len(verdict.Objective) > MaxObjectiveBytes {
		t.Fatalf("verdict bounds exceeded: reason=%d objective=%d", len(verdict.Reason), len(verdict.Objective))
	}
}

func TestVerifierErrorsAndTimeoutsBecomeUncertain(t *testing.T) {
	t.Parallel()
	cases := map[string]func(context.Context, auxiliary.Request) (aipapi.Collected, error){
		"transport error": func(_ context.Context, _ auxiliary.Request) (aipapi.Collected, error) {
			return aipapi.Collected{}, errors.New("transport detail")
		},
		"timeout": func(ctx context.Context, _ auxiliary.Request) (aipapi.Collected, error) {
			<-ctx.Done()
			return aipapi.Collected{}, ctx.Err()
		},
		"malformed": func(_ context.Context, _ auxiliary.Request) (aipapi.Collected, error) {
			return collectedText(`{"kind":"MAYBE"}`), nil
		},
	}
	for name, collect := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			collector := &fakeCollector{collect: collect}
			verifier := New(collector, Config{Timeout: time.Millisecond})
			verdict, err := verifier.Verify(context.Background(), validInput())
			if verdict.Kind != VerdictUncertain {
				t.Fatalf("kind = %q, want UNCERTAIN", verdict.Kind)
			}
			if err == nil {
				t.Fatal("Verify() error = nil, want bounded failure evidence")
			}
		})
	}
}

func TestVerifierCanceledContextDoesNotCallCollector(t *testing.T) {
	t.Parallel()
	collector := &fakeCollector{}
	verifier := New(collector, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	verdict, err := verifier.Verify(ctx, validInput())
	if verdict.Kind != VerdictUncertain || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Verify() = %#v, %v; want UNCERTAIN/context.Canceled", verdict, err)
	}
	if collector.calls != 0 {
		t.Fatalf("collector calls = %d, want 0", collector.calls)
	}
}

func TestVerifierInvalidInputDoesNotCallCollector(t *testing.T) {
	t.Parallel()
	collector := &fakeCollector{}
	verifier := New(collector, Config{})
	in := validInput()
	in.Request.RequestID = ""

	verdict, err := verifier.Verify(context.Background(), in)
	if verdict.Kind != VerdictUncertain || err == nil {
		t.Fatalf("invalid Verify() = %#v, %v; want UNCERTAIN/error", verdict, err)
	}
	if collector.calls != 0 {
		t.Fatalf("collector calls = %d, want 0", collector.calls)
	}
}

func validInput() terminaldecision.Input {
	return terminaldecision.Input{
		Candidate: terminaldecision.CanonicalTerminalCandidate{
			Cause:     terminaldecision.CandidateCauseNormal,
			Reference: "candidate-1",
		},
		Request: terminaldecision.RequestIdentity{
			RequestID: "request-1",
			TraceID:   "trace-1",
			ALegID:    "a-leg-1",
			BLegID:    "b-leg-1",
		},
		Policy: terminaldecision.PolicySnapshot{
			Revision:                "policy-1",
			MaxContinuationAttempts: 2,
		},
		Continuation: terminaldecision.ContinuationEvidence{TrajectoryRef: "trajectory-1", Attempt: 1},
		Evidence: terminaldecision.Evidence{
			Objective:     "finish the requested change",
			RecentText:    "run the focused tests",
			CandidateText: "the implementation is ready",
			Actions: [terminaldecision.MaxEvidenceActions]terminaldecision.ActionFact{{
				ItemID: "item-1",
				Kind:   aipapi.ItemKindMessage,
				Status: aipapi.ItemStatusInProgress,
			}},
			ActionCount: 1,
			Lineage: terminaldecision.EvidenceLineage{
				TrajectoryRef: "trajectory-1",
				ProgressRef:   "progress-1",
				Attempt:       1,
			},
		},
		Deadline: time.Date(2035, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}
