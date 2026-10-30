package backendplugin

import (
	"fmt"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// RequireOrderedItemABISupport rejects ordered-item calls when negotiation did not reach minor 2.
func RequireOrderedItemABISupport(neg Negotiation, call aipapi.Call) error {
	if !call.HasItemAuthority() {
		return nil
	}
	if !neg.Compatible {
		return fmt.Errorf("%w: negotiation incompatible", ErrOrderedItemsUnsupported)
	}
	if neg.NegotiatedMinor < ProtocolMinorOrderedItems {
		return fmt.Errorf("%w: negotiated minor %d", ErrOrderedItemsUnsupported, neg.NegotiatedMinor)
	}
	if slices.Contains(neg.EnabledFeatures, FeatureOrderedItems) {
		return nil
	}
	return fmt.Errorf("%w: feature %q not enabled", ErrOrderedItemsUnsupported, FeatureOrderedItems)
}
