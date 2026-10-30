package extensions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcatalog"
)

type rtxFail struct{}

func (rtxFail) ID() string                        { return "x" }
func (rtxFail) Order() int                        { return 0 }
func (rtxFail) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (rtxFail) Handle(context.Context, *aipapi.Call, request.RequestMeta, request.Services) error {
	return errors.New("boom")
}

func TestRunRequestTransformStage_failClosedStops(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartText,
				Text: "hi",
			}},
		}},
	}
	err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{rtxFail{}}, &call, request.RequestMeta{}, request.Services{
		State: state.DisabledStore{},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

type rtxAppend struct{}

func (rtxAppend) ID() string                        { return "append" }
func (rtxAppend) Order() int                        { return 0 }
func (rtxAppend) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (rtxAppend) Handle(_ context.Context, call *aipapi.Call, _ request.RequestMeta, _ request.Services) error {
	call.Messages[0].Parts[0].Text += "!"
	return nil
}

func TestRunRequestTransformStage_mutatesCall(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartText,
				Text: "hi",
			}},
		}},
	}
	if err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{rtxAppend{}}, &call, request.RequestMeta{}, request.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if call.Messages[0].Parts[0].Text != "hi!" {
		t.Fatalf("got %q", call.Messages[0].Parts[0].Text)
	}
}

type catDrop struct{}

func (catDrop) ID() string                        { return "drop" }
func (catDrop) Order() int                        { return 0 }
func (catDrop) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (catDrop) Handle(_ context.Context, call *aipapi.Call, _ toolcatalog.CatalogMeta, _ toolcatalog.Services) error {
	call.Tools = call.Tools[:0]
	return nil
}

func TestRunToolCatalogFilterStage_reconcilesToolChoice(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartText,
				Text: "hi",
			}},
		}},
		Tools: []aipapi.ToolDef{{Name: "x"}},
		ToolChoice: aipapi.ToolChoice{
			Mode: aipapi.ToolChoiceRequired,
			Name: "x",
		},
	}
	if err := extensions.RunToolCatalogFilterStage(context.Background(), nil, nil, []toolcatalog.Filter{catDrop{}}, &call, toolcatalog.CatalogMeta{}, toolcatalog.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if len(call.Tools) != 0 {
		t.Fatalf("tools %d", len(call.Tools))
	}
	if call.ToolChoice.Mode != aipapi.ToolChoiceAuto {
		t.Fatalf("choice mode %q", call.ToolChoice.Mode)
	}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
}
