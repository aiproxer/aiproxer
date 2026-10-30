package refautoappend

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// ID is the feature plugin id for YAML registration.
const ID = "ref-autoappend-file"

const defaultOrder = 40

// LabelKey is the session label upserted by the opener; the request transform
// appends on the first new-session request when this label is present.
const LabelKey = "aip_ref_autoappend"

// LabelPending marks that session-open prepared first-turn auto-append.
const LabelPending = "pending"

type opener struct{}

func (opener) ID() string { return ID + "-session-open" }

// NewSessionOpener returns a session opener that marks the first turn for auto-append.
func NewSessionOpener() session.Opener {
	return opener{}
}

func (opener) Open(_ context.Context, _ session.OpenInput) (session.OpenResult, error) {
	return session.OpenResult{
		SessionLabelUpserts: map[string]string{LabelKey: LabelPending},
	}, nil
}

type transform struct {
	order   int
	appends string
}

var _ request.Transform = transform{}

// NewRequestTransform appends [file_text] to the first user text on the first new-session request
// when the session-open label is present.
func NewRequestTransform(cfg Config) request.Transform {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return transform{order: o, appends: cfg.FileText}
}

func (t transform) ID() string                   { return ID + "-request-transform" }
func (t transform) Order() int                   { return t.order }
func (t transform) FailureMode() sdk.FailureMode { return sdk.FailOpen }

func (t transform) Handle(_ context.Context, call *aipapi.Call, meta request.RequestMeta, _ request.Services) error {
	if call == nil {
		return nil
	}
	if !meta.Session.IsNew {
		return nil
	}
	if meta.Session.Labels == nil || meta.Session.Labels[LabelKey] != LabelPending {
		return nil
	}
	if t.appends == "" {
		return nil
	}
	for i := range call.Messages {
		if call.Messages[i].Role != aipapi.RoleUser {
			continue
		}
		for j := range call.Messages[i].Parts {
			if call.Messages[i].Parts[j].Kind == aipapi.PartText {
				call.Messages[i].Parts[j].Text += t.appends
				return nil
			}
		}
	}
	return nil
}
