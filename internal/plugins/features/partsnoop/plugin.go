package partsnoop

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

const hookOrder = 100

type requestPartHook struct{}

type responsePartHook struct{}

var (
	_ sdk.RequestPartHook  = requestPartHook{}
	_ sdk.ResponsePartHook = responsePartHook{}
)

// NewRequestPartHook returns the registered request-part hook entrypoint for parts-noop.
func NewRequestPartHook() sdk.RequestPartHook { return requestPartHook{} }

// NewResponsePartHook returns the registered response-part hook entrypoint for parts-noop.
func NewResponsePartHook() sdk.ResponsePartHook { return responsePartHook{} }

func (requestPartHook) ID() string                   { return ID }
func (requestPartHook) Order() int                   { return hookOrder }
func (requestPartHook) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (requestPartHook) HandleRequestParts(context.Context, *aipapi.Call, sdk.PartMeta) error {
	return nil
}

func (responsePartHook) ID() string                   { return ID }
func (responsePartHook) Order() int                   { return hookOrder }
func (responsePartHook) FailureMode() sdk.FailureMode { return sdk.FailOpen }
func (responsePartHook) HandleEvent(context.Context, *aipapi.Event, sdk.PartMeta) error {
	return nil
}
