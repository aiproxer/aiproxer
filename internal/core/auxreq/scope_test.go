package auxreq_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type captureRunner struct {
	got context.Context
}

func (c *captureRunner) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	c.got = ctx
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
}

// TestClient_Stream_preservesParentScopeAndMarksInternalOrigin proves auxiliary requests
// preserve the parent principal/scope attribution and mark the derived origin separately
// (requirement 4.4).
func TestClient_Stream_preservesParentScopeAndMarksInternalOrigin(t *testing.T) {
	t.Parallel()
	parent := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("parent-user"),
		TenantID:    scope.Known("t-parent"),
		Origin:      scope.OriginClient,
	}
	ctx := scope.WithScope(context.Background(), parent)

	r := &captureRunner{}
	c, ok := auxreq.NewClient(func() auxreq.ExecutorRunner { return r }).(auxreq.Client)
	if !ok {
		t.Fatal("auxreq.NewClient must return auxreq.Client when an executor is provided")
	}
	call := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	if _, err := c.Stream(ctx, auxiliary.Request{
		ParentTraceID: "trace-parent",
		Call:          call,
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	got, ok := scope.ScopeFromContext(r.got)
	if !ok {
		t.Fatal("expected scope preserved on auxiliary context")
	}
	if !got.PrincipalID.Equal(scope.Known("parent-user")) {
		t.Fatalf("aux scope PrincipalID: %+v want parent-user", got.PrincipalID)
	}
	if !got.TenantID.Equal(scope.Known("t-parent")) {
		t.Fatalf("aux scope TenantID: %+v want t-parent", got.TenantID)
	}
	if got.Origin != scope.OriginInternal {
		t.Fatalf("aux origin: got %q want internal", got.Origin)
	}
	if got.ParentTraceID.String() != "trace-parent" {
		t.Fatalf("aux ParentTraceID: got %q want trace-parent", got.ParentTraceID)
	}
}

// TestClient_Stream_noParentScopeNoDerivedScope proves auxiliary requests without a parent
// scope do not synthesize one (no scope authority is invented).
func TestClient_Stream_noParentScopeNoDerivedScope(t *testing.T) {
	t.Parallel()
	r := &captureRunner{}
	c, ok := auxreq.NewClient(func() auxreq.ExecutorRunner { return r }).(auxreq.Client)
	if !ok {
		t.Fatal("auxreq.NewClient must return auxreq.Client when an executor is provided")
	}
	call := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
	}
	if _, err := c.Stream(context.Background(), auxiliary.Request{
		ParentTraceID: "trace-parent",
		Call:          call,
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, ok := scope.ScopeFromContext(r.got); ok {
		t.Fatal("expected no scope on auxiliary context when parent had none")
	}
}
