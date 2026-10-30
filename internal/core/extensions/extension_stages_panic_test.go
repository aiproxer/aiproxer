package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/routehint"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
)

type sessionPanicOpener struct{}

func (sessionPanicOpener) ID() string { return "panic-opener" }

func (sessionPanicOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	panic("session open panic")
}

type sessionLabelOpener struct{}

func (sessionLabelOpener) ID() string { return "label-opener" }

func (sessionLabelOpener) Open(context.Context, session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{SessionLabelUpserts: map[string]string{"k": "v"}}, nil
}

func TestRunSessionOpenStage_panicFailOpenContinues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	in := session.OpenInput{TraceID: "t1"}
	got := extensions.RunSessionOpenStage(ctx, nil, nil, []session.Opener{
		sessionPanicOpener{},
		sessionLabelOpener{},
	}, in)
	if got.SessionLabelUpserts == nil || got.SessionLabelUpserts["k"] != "v" {
		t.Fatalf("expected label from second opener, got %#v", got.SessionLabelUpserts)
	}
}

type toolPanicFilter struct{}

func (toolPanicFilter) ID() string { return "panic-filter" }

func (toolPanicFilter) Order() int { return 0 }

func (toolPanicFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (toolPanicFilter) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	panic("tool catalog panic")
}

type toolNopFilter struct{}

func (toolNopFilter) ID() string { return "nop" }

func (toolNopFilter) Order() int { return 1 }

func (toolNopFilter) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (toolNopFilter) Handle(context.Context, *aipapi.Call, toolcatalog.CatalogMeta, toolcatalog.Services) error {
	return nil
}

func validUserCall() *aipapi.Call {
	return &aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartText,
				Text: "hi",
			}},
		}},
	}
}

func TestRunToolCatalogFilterStage_panicFailOpenContinues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	call := validUserCall()
	meta := toolcatalog.CatalogMeta{TraceID: "t1", Annotations: map[string]string{}}
	svc := toolcatalog.Services{State: state.DisabledStore{}, Aux: auxiliary.DisabledClient{}}
	err := extensions.RunToolCatalogFilterStage(ctx, nil, nil, []toolcatalog.Filter{
		toolPanicFilter{},
		toolNopFilter{},
	}, call, meta, svc)
	if err != nil {
		t.Fatal(err)
	}
}

type transformPanic struct{}

func (transformPanic) ID() string { return "panic-tx" }

func (transformPanic) Order() int { return 0 }

func (transformPanic) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }

func (transformPanic) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	panic("transform panic")
}

func TestRunRequestTransformStage_panicFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	call := validUserCall()
	meta := request.RequestMeta{TraceID: "t1", Annotations: map[string]string{}}
	svc := request.Services{State: state.DisabledStore{}, Aux: auxiliary.DisabledClient{}}
	err := extensions.RunRequestTransformStage(ctx, nil, nil, []request.Transform{
		transformPanic{},
	}, call, meta, svc)
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *safety.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("want *safety.PanicError, got %T: %v", err, err)
	}
	if pe.Boundary() != safety.BoundaryExtension {
		t.Fatalf("boundary=%s", pe.Boundary())
	}
}

type routePanicProv struct{}

func (routePanicProv) ID() string { return "panic-prov" }

func (routePanicProv) Order() int { return 0 }

func (routePanicProv) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (routePanicProv) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	panic("route hint panic")
}

type routeOKProv struct{}

func (routeOKProv) ID() string { return "ok-prov" }

func (routeOKProv) Order() int { return 1 }

func (routeOKProv) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (routeOKProv) Hint(context.Context, routehint.Input) (routehint.Result, error) {
	return routehint.Result{PreferredCandidateKeys: []string{"backend-a"}}, nil
}

func TestRunRouteHintStage_panicFailOpenContinues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	call := validUserCall()
	meta := routehint.Input{TraceID: "t1", Call: call}
	got, err := extensions.RunRouteHintStage(ctx, nil, []routehint.Provider{
		routePanicProv{},
		routeOKProv{},
	}, call, meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "backend-a" {
		t.Fatalf("got %#v", got)
	}
}

type gateFailOpenPanic struct{}

func (gateFailOpenPanic) ID() string                        { return "panic-gate" }
func (gateFailOpenPanic) Order() int                        { return 0 }
func (gateFailOpenPanic) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateFailOpenPanic) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	panic("gate panic")
}

type gateReplaceAfterPanic struct{}

func (gateReplaceAfterPanic) ID() string                        { return "rep-after" }
func (gateReplaceAfterPanic) Order() int                        { return 1 }
func (gateReplaceAfterPanic) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateReplaceAfterPanic) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.ReplaceOutcome([]aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "replaced"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

// Completion-gate panics surface immediately (even for fail-open gates) so the runtime stream
// mapper can classify pre-output vs post-output failures; later gates must not run.
func TestApplyCompletionGateChain_failOpenPanicStopsChain(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "a"},
		{Kind: aipapi.EventResponseFinished},
	}
	_, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{
		gateFailOpenPanic{},
		gateReplaceAfterPanic{},
	}, completion.Meta{}, orig, false, completion.Services{
		State: state.DisabledStore{},
		Aux:   auxiliary.DisabledClient{},
	}, nil)
	if err == nil {
		t.Fatal("expected panic error")
	}
	if _, ok := errors.AsType[*safety.PanicError](err); !ok {
		t.Fatalf("want *safety.PanicError, got %T", err)
	}
}

type gateFailClosedPanic struct{}

func (gateFailClosedPanic) ID() string                        { return "fc-panic" }
func (gateFailClosedPanic) Order() int                        { return 0 }
func (gateFailClosedPanic) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (gateFailClosedPanic) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	panic("closed panic")
}

func TestApplyCompletionGateChain_failClosedPanicSurfaces(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{{Kind: aipapi.EventResponseFinished}}
	_, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateFailClosedPanic{}},
		completion.Meta{}, orig, false, completion.Services{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var pe *safety.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("want panic error, got %v", err)
	}
	if pe.Boundary() != safety.BoundaryExtension {
		t.Fatalf("boundary=%s", pe.Boundary())
	}
}
