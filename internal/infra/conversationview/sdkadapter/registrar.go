package sdkadapter

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/nonforwardable"
)

// Registrar implements nonforwardable.Registrar over the authoritative
// conversation-view Tagger port. It is a trusted narrow adapter with explicit
// construction and no client/data-plane exposure.
type Registrar struct {
	tagger conversationview.Tagger
}

var _ nonforwardable.Registrar = (*Registrar)(nil)

// NewRegistrar constructs a trusted registrar over tagger.
// tagger must be non-nil.
func NewRegistrar(tagger conversationview.Tagger) (*Registrar, error) {
	if tagger == nil {
		return nil, fmt.Errorf("sdkadapter: tagger is required")
	}
	return &Registrar{tagger: tagger}, nil
}

// TagMessages validates SDK types using their own validation, maps to a batch
// TagRequest over the Tagger port, and propagates typed store errors with %w.
// Idempotency and batch atomicity are inherited from the Tagger implementation.
func (r *Registrar) TagMessages(ctx context.Context, aLeg nonforwardable.ALegRef, msgs []nonforwardable.MessageRef, reason nonforwardable.ReasonCode) error {
	if err := aLeg.Validate(); err != nil {
		return fmt.Errorf("sdkadapter: %w", err)
	}
	if err := reason.Validate(); err != nil {
		return fmt.Errorf("sdkadapter: %w", err)
	}
	for i, m := range msgs {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("sdkadapter: message ref %d: %w", i, err)
		}
	}
	// Map to TagRequest batch.
	batch := make([]conversationview.TagRequest, 0, len(msgs))
	for _, m := range msgs {
		// Normalize identity via conversationview type to ensure consistent validation
		// at the store boundary. SDK validation already checked length/non-empty;
		// store will enforce v1 identity format and reason code ascii limits via TagRequest.Validate.
		batch = append(batch, conversationview.TagRequest{
			Identity: conversationview.MessageIdentity(m.Identity),
			Reason:   conversationview.ReasonCode(reason),
		})
	}
	// Delegate to authoritative port with trimmed A-leg ID.
	aLegID := aLeg.ID
	_, err := r.tagger.TagNeverBackend(ctx, aLegID, batch)
	if err != nil {
		return fmt.Errorf("sdkadapter: tag: %w", err)
	}
	return nil
}
