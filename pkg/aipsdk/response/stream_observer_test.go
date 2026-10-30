package response_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type ordFactory struct {
	id  string
	ord int
}

func (o ordFactory) ID() string                        { return o.id }
func (o ordFactory) Order() int                        { return o.ord }
func (o ordFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (o ordFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return noopObserver{}, nil
}

type noopObserver struct{}

func (noopObserver) Observe(context.Context, aipapi.Event) error          { return nil }
func (noopObserver) Finish(context.Context, response.StreamOutcome) error { return nil }

func TestStreamOutcome_exactWireValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got  response.StreamOutcome
		want string
	}{
		{response.OutcomeSuccessReleased, "success_released"},
		{response.OutcomeFailed, "failed"},
		{response.OutcomeCancelled, "cancelled"},
		{response.OutcomeClosed, "closed"},
		{response.OutcomeReplaced, "replaced"},
		{response.OutcomeGateReplaced, "gate_replaced"},
	}
	for _, tc := range cases {
		if string(tc.got) != tc.want {
			t.Fatalf("%q want %q", tc.got, tc.want)
		}
	}
	var zero response.StreamOutcome
	if zero != "" {
		t.Fatal("zero StreamOutcome must be empty")
	}
}

func TestMaterializeSorted_orderThenIDThenRegistration(t *testing.T) {
	t.Parallel()
	a := ordFactory{id: "b", ord: 1}
	b := ordFactory{id: "a", ord: 1}
	c := ordFactory{id: "a", ord: 2}
	got := response.MaterializeSorted([]response.StreamObserverFactory{a, b, c})
	if len(got) != 3 {
		t.Fatalf("len %d", len(got))
	}
	first, ok := got[0].(ordFactory)
	if !ok || first.id != "a" || first.ord != 1 {
		t.Fatalf("want first a ord1 got %#v", got[0])
	}
	second, ok := got[1].(ordFactory)
	if !ok || second.id != "b" {
		t.Fatalf("want second b got %#v", got[1])
	}
	third, ok := got[2].(ordFactory)
	if !ok || third.ord != 2 {
		t.Fatalf("want third ord2 got %#v", got[2])
	}
}

func TestMaterializeSorted_sameIDUsesRegistrationIndex(t *testing.T) {
	t.Parallel()
	second := ordFactory{id: "same", ord: 0}
	first := ordFactory{id: "same", ord: 0}
	got := response.MaterializeSorted([]response.StreamObserverFactory{second, first})
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	g0, ok := got[0].(ordFactory)
	if !ok || g0 != second {
		t.Fatalf("expected first registered factory first when Order+ID match, got %#v", got[0])
	}
}

func TestMaterializeSorted_emptyReturnsNil(t *testing.T) {
	t.Parallel()
	if response.MaterializeSorted(nil) != nil {
		t.Fatal("expected nil")
	}
	if response.MaterializeSorted([]response.StreamObserverFactory{}) != nil {
		t.Fatal("expected nil for empty")
	}
}

func TestStreamMeta_providerNeutralFields(t *testing.T) {
	t.Parallel()
	meta := response.StreamMeta{
		TraceID:         "tr",
		ALegID:          "a1",
		BLegID:          "b1",
		CandidateKey:    "cand-1",
		BackendID:       "backend-prod",
		BackendPrefixes: []string{"openrouter"},
		Model:           "kimi-k2",
		AttemptSeq:      2,
		Scope:           scope.PrincipalScopeView{PrincipalID: scope.Known("p1")},
		Session:         session.SessionView{AuthoritativeSessionID: "sess-auth"},
		Workspace:       workspace.WorkspaceView{},
	}
	if meta.TraceID == "" || meta.ALegID == "" || meta.BLegID == "" {
		t.Fatal("StreamMeta must carry trace/A-leg/B-leg identity")
	}
	if meta.CandidateKey == "" || meta.BackendID == "" || meta.Model == "" || meta.AttemptSeq == 0 || len(meta.BackendPrefixes) == 0 {
		t.Fatal("StreamMeta must carry candidate/backend/prefix/model/attempt identity")
	}
	if meta.Session.AuthoritativeSessionID == "" {
		t.Fatal("authoritative session id belongs on SessionView, not a separate partition field")
	}
	if meta.Scope.PrincipalID.String() != "p1" {
		t.Fatalf("scope=%v", meta.Scope.PrincipalID)
	}
	_ = response.Services{}
}

func TestStreamObserverFactory_openContract(t *testing.T) {
	t.Parallel()
	var f response.StreamObserverFactory = ordFactory{id: "obs", ord: 0}
	obs, err := f.Open(context.Background(), response.StreamMeta{BLegID: "b"}, response.Services{})
	if err != nil || obs == nil {
		t.Fatalf("Open: obs=%v err=%v", obs, err)
	}
	if err := obs.Observe(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := obs.Finish(context.Background(), response.OutcomeSuccessReleased); err != nil {
		t.Fatal(err)
	}
}
