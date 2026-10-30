package codexclientcompat

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

const (
	defaultOrder    = 50
	targetBackendID = "openai-codex" // ponytail: mirrors openaicodex.ID; local const avoids feature->backend import.
)

type requestPartHook struct {
	order int
}

var _ sdk.RequestPartHook = requestPartHook{}

func NewRequestPartHook(cfg Config) sdk.RequestPartHook {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return requestPartHook{order: o}
}

func (requestPartHook) ID() string                   { return ID }
func (h requestPartHook) Order() int                 { return h.order }
func (requestPartHook) FailureMode() sdk.FailureMode { return sdk.FailOpen }

func (h requestPartHook) HandleRequestParts(_ context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
	if meta.BackendID != targetBackendID {
		return nil
	}
	if call == nil {
		return nil
	}
	applyIgnoreUnsupportedGenParams(call)
	ApplyCompat(call)
	return nil
}
