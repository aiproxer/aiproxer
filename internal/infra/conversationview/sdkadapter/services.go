package sdkadapter

import (
	"fmt"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/nonforwardable"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/steering"
)

// NewNonforwardableRegistrarFromStore resolves the optional conversation-view
// Tagger capability from v and constructs a registrar. It fails deterministically
// if the capability is unavailable (Req 13.4) rather than running without
// safety/persistence.
func NewNonforwardableRegistrarFromStore(v any) (nonforwardable.Registrar, error) {
	tagger, ok := conversationview.AsTagger(v)
	if !ok || tagger == nil {
		return nil, fmt.Errorf("sdkadapter: conversation-view tagger capability not available")
	}
	return NewRegistrar(tagger)
}

// NewSteeringWriterFromStore resolves the optional SteeringStore capability from
// v and constructs a writer bound to aLegID. It fails deterministically if the
// capability is unavailable.
func NewSteeringWriterFromStore(v any, aLegID string, resolver TrajectoryResolver) (steering.Writer, error) {
	return NewSteeringWriterFromStoreWithObserver(v, aLegID, resolver, nil)
}

// NewSteeringWriterFromStoreWithObserver is like NewSteeringWriterFromStore but with an optional
// narrow observer for bounded steering mutation/cache-discontinuity diagnostics.
// The observer receives only bounded enums (operation, placement), never OverlayID/ALegID/digest/plaintext.
func NewSteeringWriterFromStoreWithObserver(v any, aLegID string, resolver TrajectoryResolver, observer conversationview.Observer) (steering.Writer, error) {
	st, ok := conversationview.AsSteeringStore(v)
	if !ok || st == nil {
		return nil, fmt.Errorf("sdkadapter: conversation-view steering capability not available")
	}
	return NewWriterWithObserver(st, aLegID, resolver, observer)
}

// NewConversationViewServices is a convenience helper that resolves both
// registrar and writer capabilities from the same store value. It returns
// deterministic errors if either capability is unavailable. Callers that only
// need one capability should use the more narrow constructors above.
func NewConversationViewServices(v any, writerALegID string, resolver TrajectoryResolver) (nonforwardable.Registrar, steering.Writer, error) {
	return NewConversationViewServicesWithObserver(v, writerALegID, resolver, nil)
}

// NewConversationViewServicesWithObserver is like NewConversationViewServices but with an optional
// narrow observer for steering mutation diagnostics wired to the writer.
func NewConversationViewServicesWithObserver(v any, writerALegID string, resolver TrajectoryResolver, observer conversationview.Observer) (nonforwardable.Registrar, steering.Writer, error) {
	tagger, ok := conversationview.AsTagger(v)
	if !ok || tagger == nil {
		return nil, nil, fmt.Errorf("sdkadapter: conversation-view tagger capability not available")
	}
	st, ok := conversationview.AsSteeringStore(v)
	if !ok || st == nil {
		return nil, nil, fmt.Errorf("sdkadapter: conversation-view steering capability not available")
	}
	reg, err := NewRegistrar(tagger)
	if err != nil {
		return nil, nil, err
	}
	wr, err := NewWriterWithObserver(st, writerALegID, resolver, observer)
	if err != nil {
		return nil, nil, err
	}
	return reg, wr, nil
}
