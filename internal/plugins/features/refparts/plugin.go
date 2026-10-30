package refparts

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// ID is the feature plugin id for YAML registration.
const ID = "ref-request-suffix"

const defaultOrder = 50

type hook struct {
	order  int
	suffix string
}

// NewRequestPartHook mutates the first user text part by appending a suffix (observable in backends).
func NewRequestPartHook(cfg Config) sdk.RequestPartHook {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	s := cfg.Suffix
	if s == "" {
		s = " [ref]"
	}
	return hook{order: o, suffix: s}
}

func (hook) ID() string                   { return ID }
func (h hook) Order() int                 { return h.order }
func (hook) FailureMode() sdk.FailureMode { return sdk.FailOpen }

func (h hook) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdk.PartMeta) error {
	if call == nil {
		return nil
	}
	for i := range call.Messages {
		if call.Messages[i].Role != aipapi.RoleUser {
			continue
		}
		for j := range call.Messages[i].Parts {
			if call.Messages[i].Parts[j].Kind == aipapi.PartText {
				call.Messages[i].Parts[j].Text += h.suffix
				return nil
			}
		}
	}
	return nil
}

// NewResponsePartHook prepends a marker to assistant text deltas (non-streaming-safe for tests).
func NewResponsePartHook(cfg Config) sdk.ResponsePartHook {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	p := strings.TrimSpace(cfg.ResponsePrefix)
	if p == "" {
		p = "REF:"
	}
	return respHook{order: o, prefix: p}
}

type respHook struct {
	order  int
	prefix string
}

var (
	_ sdk.RequestPartHook  = hook{}
	_ sdk.ResponsePartHook = respHook{}
)

func (respHook) ID() string                   { return ID + "-response" }
func (h respHook) Order() int                 { return h.order }
func (respHook) FailureMode() sdk.FailureMode { return sdk.FailOpen }

func (h respHook) HandleEvent(_ context.Context, ev *aipapi.Event, _ sdk.PartMeta) error {
	if ev == nil || ev.Kind != aipapi.EventTextDelta {
		return nil
	}
	ev.Delta = h.prefix + ev.Delta
	return nil
}
