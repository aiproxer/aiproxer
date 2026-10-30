package request_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type ordAttemptTransform struct {
	id  string
	ord int
}

func (o ordAttemptTransform) ID() string                        { return o.id }
func (o ordAttemptTransform) Order() int                        { return o.ord }
func (o ordAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (o ordAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

func TestAttemptDecision_kindContract(t *testing.T) {
	t.Parallel()
	if request.AttemptContinue != "continue" {
		t.Fatalf("AttemptContinue=%q", request.AttemptContinue)
	}
	if request.AttemptExcludeCandidate != "exclude_candidate" {
		t.Fatalf("AttemptExcludeCandidate=%q", request.AttemptExcludeCandidate)
	}
	_ = request.AttemptDecision{Kind: request.AttemptContinue}
	_ = request.AttemptDecision{Kind: request.AttemptExcludeCandidate, ReasonCode: "optional"}
	_ = request.AttemptDecision{Kind: request.AttemptExcludeCandidate}
	zero := request.AttemptDecision{}
	if zero.Kind != "" {
		t.Fatal("zero AttemptDecision.Kind must be empty (malformed for runners)")
	}
}

func TestMaterializeAttemptsSorted_orderThenIDThenRegistration(t *testing.T) {
	t.Parallel()
	a := ordAttemptTransform{id: "b", ord: 1}
	b := ordAttemptTransform{id: "a", ord: 1}
	c := ordAttemptTransform{id: "a", ord: 2}
	got := request.MaterializeAttemptsSorted([]request.AttemptTransform{a, b, c})
	if len(got) != 3 {
		t.Fatalf("len %d", len(got))
	}
	first, ok := got[0].(ordAttemptTransform)
	if !ok || first.id != "a" || first.ord != 1 {
		t.Fatalf("want first a ord1 got %#v", got[0])
	}
	second, ok := got[1].(ordAttemptTransform)
	if !ok || second.id != "b" {
		t.Fatalf("want second b got %#v", got[1])
	}
	third, ok := got[2].(ordAttemptTransform)
	if !ok || third.ord != 2 {
		t.Fatalf("want third ord2 got %#v", got[2])
	}
}

func TestMaterializeAttemptsSorted_sameIDUsesRegistrationIndex(t *testing.T) {
	t.Parallel()
	second := ordAttemptTransform{id: "same", ord: 0}
	first := ordAttemptTransform{id: "same", ord: 0}
	got := request.MaterializeAttemptsSorted([]request.AttemptTransform{second, first})
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	g0, ok := got[0].(ordAttemptTransform)
	if !ok || g0 != second {
		t.Fatalf("expected first registered transform first when Order+ID match, got %#v", got[0])
	}
}

func TestMaterializeAttemptsSorted_emptyReturnsNil(t *testing.T) {
	t.Parallel()
	if request.MaterializeAttemptsSorted(nil) != nil {
		t.Fatal("expected nil")
	}
	if request.MaterializeAttemptsSorted([]request.AttemptTransform{}) != nil {
		t.Fatal("expected nil for empty")
	}
}

func TestAttemptMeta_providerNeutralFields(t *testing.T) {
	t.Parallel()
	meta := request.AttemptMeta{
		TraceID:         "tr",
		ALegID:          "a1",
		CandidateKey:    "cand-1",
		BackendID:       "backend-prod",
		BackendPrefixes: []string{"openrouter/", "openai/"},
		Model:           "kimi-k2",
		ReplaySupport: aipapi.ReasoningReplaySupport{
			Dialects: []aipapi.ReasoningDialect{aipapi.ReasoningDialectOpenAIChatTextV1},
		},
		Scope:     scope.PrincipalScopeView{PrincipalID: scope.Known("p1")},
		Session:   session.SessionView{},
		Workspace: workspace.WorkspaceView{},
	}
	if meta.BackendID == "" || meta.CandidateKey == "" || meta.Model == "" || len(meta.BackendPrefixes) == 0 {
		t.Fatal("AttemptMeta must carry backend/model/candidate identity")
	}
	if len(meta.ReplaySupport.Dialects) != 1 {
		t.Fatal("AttemptMeta must carry ReplaySupport")
	}
	if meta.Scope.PrincipalID.String() != "p1" {
		t.Fatalf("scope=%v", meta.Scope.PrincipalID)
	}
}
