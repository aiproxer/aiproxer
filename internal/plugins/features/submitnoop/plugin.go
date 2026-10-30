package submitnoop

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// DefaultHookOrder is the default submit hook ordering for submit-noop.
const DefaultHookOrder = 100

// submitHook is a reference no-op submit hook for the standard distribution.
type submitHook struct {
	order int
}

var _ sdk.SubmitHook = submitHook{}

// NewSubmitHook returns the registered submit hook entrypoint with default ordering.
func NewSubmitHook() sdk.SubmitHook {
	return NewSubmitHookWithConfig(HookConfig{})
}

// NewSubmitHookWithConfig returns the registered submit hook entrypoint using decoded feature config.
func NewSubmitHookWithConfig(cfg HookConfig) sdk.SubmitHook {
	o := DefaultHookOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	return submitHook{order: o}
}

func (submitHook) ID() string                   { return ID }
func (h submitHook) Order() int                 { return h.order }
func (submitHook) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (submitHook) Handle(context.Context, *aipapi.Call, *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
	return sdk.SubmitDecision{}, nil
}
