package hooks

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// RunRequestPartHooks runs request-part hooks in order and re-validates the call.
func (b *Bus) RunRequestPartHooks(ctx context.Context, call *aipapi.Call, meta sdk.PartMeta) error {
	if call == nil {
		return fmt.Errorf("hooks: nil call: %w", aipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("hooks: %w", aipapi.ErrNilContext)
	}
	reqParts := []sdk.RequestPartHook{}
	if b != nil {
		reqParts = b.requestParts
	}
	for _, h := range reqParts {
		err := safety.Call(safety.BoundaryExtension, "request_part_hook", func() error {
			return h.HandleRequestParts(ctx, call, meta)
		})
		if err != nil {
			if h.FailureMode() == sdk.FailOpen {
				logFailOpenHookPanic(ctx, "request_part_hook", h.ID(), err)
				continue
			}
			return fmt.Errorf("request part hook %q: %w", h.ID(), err)
		}
		if err := ValidateCallAfterRequestHooks(h.ID(), call); err != nil {
			return err
		}
	}
	return nil
}

// RunResponsePartHooks runs response-part hooks in order and validates each mutation.
func (b *Bus) RunResponsePartHooks(ctx context.Context, ev *aipapi.Event, meta sdk.PartMeta) error {
	if ev == nil {
		return fmt.Errorf("hooks: nil event: %w", aipapi.ErrInvalidCall)
	}
	if ctx == nil {
		return fmt.Errorf("hooks: %w", aipapi.ErrNilContext)
	}
	respParts := []sdk.ResponsePartHook{}
	if b != nil {
		respParts = b.responseParts
	}
	for _, h := range respParts {
		err := safety.Call(safety.BoundaryExtension, "response_part_hook", func() error {
			return h.HandleEvent(ctx, ev, meta)
		})
		if err != nil {
			if h.FailureMode() == sdk.FailOpen {
				logFailOpenHookPanic(ctx, "response_part_hook", h.ID(), err)
				continue
			}
			return fmt.Errorf("response part hook %q: %w", h.ID(), err)
		}
		if err := ValidateEventAfterResponseHook(h.ID(), ev); err != nil {
			return err
		}
	}
	return nil
}
