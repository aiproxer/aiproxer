package refautoappend_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/plugins/features/refautoappend"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

func TestOpener_upsertsPendingLabel(t *testing.T) {
	t.Parallel()
	o := refautoappend.NewSessionOpener()
	if o.ID() != refautoappend.ID+"-session-open" {
		t.Fatalf("id %q", o.ID())
	}
	got, err := o.Open(context.Background(), session.OpenInput{
		TraceID: "t1",
		Session: session.SessionView{ClientSessionHint: "s1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionLabelUpserts[refautoappend.LabelKey] != refautoappend.LabelPending {
		t.Fatalf("labels %+v", got.SessionLabelUpserts)
	}
}

func TestRequestTransform_appendsOnNewSessionWithLabel(t *testing.T) {
	t.Parallel()
	rtx := refautoappend.NewRequestTransform(refautoappend.Config{FileText: " FROM_FILE"})
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartText,
				Text: "hello",
			}},
		}},
	}
	meta := request.RequestMeta{
		Session: session.SessionView{
			IsNew:  true,
			Labels: map[string]string{refautoappend.LabelKey: refautoappend.LabelPending},
		},
	}
	if err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{rtx}, &call, meta, request.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if call.Messages[0].Parts[0].Text != "hello FROM_FILE" {
		t.Fatalf("got %q", call.Messages[0].Parts[0].Text)
	}
}

func TestRequestTransform_noOpWhenNotNew(t *testing.T) {
	t.Parallel()
	rtx := refautoappend.NewRequestTransform(refautoappend.Config{FileText: "X"})
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}},
		}},
	}
	// Session opener may upsert pending on every request; IsNew from secure-session distinguishes
	// the first logical session request from later turns (Requirement 4.1).
	meta := request.RequestMeta{
		Session: session.SessionView{
			IsNew:  false,
			Labels: map[string]string{refautoappend.LabelKey: refautoappend.LabelPending},
		},
	}
	if err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{rtx}, &call, meta, request.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if call.Messages[0].Parts[0].Text != "hi" {
		t.Fatalf("mutated: %q", call.Messages[0].Parts[0].Text)
	}
}

func TestRequestTransform_noOpWhenLabelMissing(t *testing.T) {
	t.Parallel()
	rtx := refautoappend.NewRequestTransform(refautoappend.Config{FileText: "X"})
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}},
		}},
	}
	meta := request.RequestMeta{Session: session.SessionView{IsNew: true, Labels: map[string]string{}}}
	if err := extensions.RunRequestTransformStage(context.Background(), nil, nil, []request.Transform{rtx}, &call, meta, request.Services{
		State: state.DisabledStore{},
	}); err != nil {
		t.Fatal(err)
	}
	if call.Messages[0].Parts[0].Text != "hi" {
		t.Fatalf("mutated: %q", call.Messages[0].Parts[0].Text)
	}
}
