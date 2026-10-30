package prerequest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestDecisionAllowAndDeny(t *testing.T) {
	t.Parallel()
	if !prerequest.Allow().Allowed() {
		t.Fatal("Allow must be allowed")
	}
	deny := prerequest.Deny("blocked")
	if deny.Allowed() {
		t.Fatal("Deny must not be allowed")
	}
	if deny.DenyMessage != "blocked" {
		t.Fatalf("deny message %q", deny.DenyMessage)
	}
}

func TestRejectErrorWrapsRoot(t *testing.T) {
	t.Parallel()
	err := prerequest.NewRejectError("policy-a", "blocked")
	if !errors.Is(err, prerequest.ErrRejected) {
		t.Fatalf("errors.Is ErrRejected = false for %v", err)
	}
	var re *prerequest.RejectError
	if !errors.As(err, &re) {
		t.Fatalf("errors.As RejectError = false for %T", err)
	}
	if re.HandlerID != "policy-a" || re.Message != "blocked" {
		t.Fatalf("reject error = %+v", re)
	}
}

func TestHandlerInterfaceCompileContract(t *testing.T) {
	t.Parallel()
	var _ prerequest.Handler = handlerFunc{}
	meta := prerequest.Meta{
		TraceID:        "trace",
		Annotations:    map[string]string{},
		Principal:      execview.PrincipalView{ID: "p1"},
		Session:        session.SessionView{AuthoritativeSessionID: "s1"},
		Workspace:      workspace.WorkspaceView{ID: "w1"},
		AuxiliaryDepth: 0,
	}
	dec, err := handlerFunc{}.Handle(context.Background(), &aipapi.Call{}, meta, prerequest.Services{})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Allowed() {
		t.Fatal("handlerFunc should allow")
	}
}

type handlerFunc struct{}

func (handlerFunc) ID() string                        { return "handler" }
func (handlerFunc) Order() int                        { return 10 }
func (handlerFunc) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (handlerFunc) Handle(context.Context, *aipapi.Call, prerequest.Meta, prerequest.Services) (prerequest.Decision, error) {
	return prerequest.Allow(), nil
}
