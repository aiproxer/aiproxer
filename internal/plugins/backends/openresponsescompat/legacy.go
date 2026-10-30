package openresponsescompat

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// orderedItemProjectionTarget derives the explicit legacy→ordered-items projector
// target from the instance's declared capabilities and dialect support. Call-level
// extensions may be carried only when the backend declares the exact extension type.
func orderedItemProjectionTarget(spec BackendSpec) aipapi.OrderedItemProjectionTarget {
	target := aipapi.OrderedItemProjectionTargetFromCaps(spec.Caps)
	target.SupportedExtensions = append([]aipapi.ExtensionRequirement(nil), spec.DialectSupport.ExtensionTypes...)
	return target
}

// normalizeLegacyAuthority maps a legacy message-authority call onto an
// item-authority call through the explicit canonical legacy→ordered-items
// projector (Requirement 9.13, Task 5.3). Item-authority calls pass through
// unchanged. The source call is never mutated: the projected call is a deep
// clone with the ordered items replacing the legacy Instructions/Messages.
func normalizeLegacyAuthority(id string, spec BackendSpec, call aipapi.Call) (aipapi.Call, error) {
	if call.HasItemAuthority() {
		return call, nil
	}
	items, _, err := aipapi.ProjectLegacyToOrderedItems(call, orderedItemProjectionTarget(spec))
	if err != nil {
		return aipapi.Call{}, fmt.Errorf("%s: %w", id, err)
	}
	out := aipapi.CloneCall(call)
	out.Instructions = nil
	out.Messages = nil
	out.Items = items
	return out, nil
}
