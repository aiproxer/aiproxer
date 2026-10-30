package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/completion"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

type gateReplace struct{}

func (gateReplace) ID() string                        { return "rep" }
func (gateReplace) Order() int                        { return 0 }
func (gateReplace) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateReplace) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.ReplaceOutcome([]aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "x"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func TestApplyCompletionGateChain_replace(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "a"},
		{Kind: aipapi.EventResponseFinished},
	}
	result, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateReplace{}}, completion.Meta{}, orig, false, completion.Services{
		State: state.DisabledStore{},
		Aux:   auxiliary.DisabledClient{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 2 || result.Events[0].Delta != "x" {
		t.Fatalf("got %#v", result.Events)
	}
	if !result.Replaced {
		t.Fatal("expected Replaced=true")
	}
}

func TestApplyCompletionGateChain_replaceEqualContentStillReplaced(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "a"},
		{Kind: aipapi.EventResponseFinished},
	}
	result, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateReplaceEqual{}}, completion.Meta{}, orig, false, completion.Services{
		State: state.DisabledStore{},
		Aux:   auxiliary.DisabledClient{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 2 || result.Events[0].Delta != "a" {
		t.Fatalf("got %#v", result.Events)
	}
	if !result.Replaced {
		t.Fatal("equal-content replace must set Replaced=true")
	}
}

type gateReplaceEqual struct{}

func (gateReplaceEqual) ID() string                        { return "rep-eq" }
func (gateReplaceEqual) Order() int                        { return 0 }
func (gateReplaceEqual) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateReplaceEqual) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.ReplaceOutcome([]aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "a"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func TestApplyCompletionGateChain_replaceIgnoredWhenCommitted(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{
		{Kind: aipapi.EventTextDelta, Delta: "a"},
		{Kind: aipapi.EventResponseFinished},
	}
	result, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateReplace{}}, completion.Meta{}, orig, true, completion.Services{
		State: state.DisabledStore{},
		Aux:   auxiliary.DisabledClient{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 2 || result.Events[0].Delta != "a" {
		t.Fatalf("expected original, got %#v", result.Events)
	}
	if result.Replaced {
		t.Fatal("committed replace must not set Replaced")
	}
}

type gateFailOpenErr struct{}

func (gateFailOpenErr) ID() string                        { return "e" }
func (gateFailOpenErr) Order() int                        { return 0 }
func (gateFailOpenErr) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateFailOpenErr) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.Outcome{}, errors.New("boom")
}

func TestApplyCompletionGateChain_handlerErrorFailOpen(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{{Kind: aipapi.EventResponseFinished}}
	result, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateFailOpenErr{}}, completion.Meta{}, orig, false, completion.Services{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].Kind != aipapi.EventResponseFinished {
		t.Fatalf("got %#v", result.Events)
	}
}

type gateFailClosedErr struct{}

func (gateFailClosedErr) ID() string                        { return "fc" }
func (gateFailClosedErr) Order() int                        { return 0 }
func (gateFailClosedErr) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (gateFailClosedErr) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.Outcome{}, errors.New("closed")
}

func TestApplyCompletionGateChain_handlerErrorFailClosed(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{{Kind: aipapi.EventResponseFinished}}
	_, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateFailClosedErr{}}, completion.Meta{}, orig, false, completion.Services{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

type gateReject struct{}

func (gateReject) ID() string                        { return "rj" }
func (gateReject) Order() int                        { return 0 }
func (gateReject) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (gateReject) Handle(context.Context, completion.Meta, completion.Buffered, completion.Services) (completion.Outcome, error) {
	return completion.RejectOutcome(errGateRejectCause), nil
}

var errGateRejectCause = errors.New("nope")

func TestApplyCompletionGateChain_reject(t *testing.T) {
	t.Parallel()
	orig := []aipapi.Event{{Kind: aipapi.EventResponseFinished}}
	_, err := extensions.ApplyCompletionGateChain(context.Background(), []completion.Gate{gateReject{}}, completion.Meta{}, orig, false, completion.Services{}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !aipapi.IsPolicyDenied(err) {
		t.Fatalf("reject must surface a stable policy denied error, got %v", err)
	}
	if !errors.Is(err, errGateRejectCause) {
		t.Fatalf("reject must preserve cause, got %v", err)
	}
}

func TestCompletionGateBufferExceeded(t *testing.T) {
	t.Parallel()
	lim := completion.BufferLimits{MaxEvents: 2}
	if !extensions.CompletionGateBufferExceeded(lim, 3) {
		t.Fatal("overflow")
	}
}

func TestStreamFinished(t *testing.T) {
	t.Parallel()
	if extensions.StreamFinished(nil) {
		t.Fatal("nil")
	}
	if extensions.StreamFinished([]aipapi.Event{{Kind: aipapi.EventTextDelta}}) {
		t.Fatal("not finished")
	}
	if !extensions.StreamFinished([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}) {
		t.Fatal("finished")
	}
}
