package backendplugin

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Validate reports whether the invocation is well-formed and within bounds.
// It uses the same mapper as [CallFromInvocation].
func (inv Invocation) Validate() error {
	_, err := CallFromInvocation(inv)
	return err
}

func (inv Invocation) validateDTO() error {
	if strings.TrimSpace(inv.RequestID) == "" ||
		strings.TrimSpace(inv.AttemptID) == "" ||
		strings.TrimSpace(inv.ALegID) == "" ||
		strings.TrimSpace(inv.BLegID) == "" ||
		strings.TrimSpace(inv.CanonicalModelID) == "" {
		return ErrInvalidInvocation
	}
	if len(inv.ProxyOwnedSessionID) > aipapi.MaxAuthoritativeSessionIDBytes || inv.ProxyOwnedSessionID != strings.TrimSpace(inv.ProxyOwnedSessionID) {
		return fmt.Errorf("%w: proxy-owned session id is invalid", ErrInvalidInvocation)
	}
	if len(inv.Messages) == 0 && (!inv.ItemAuthority || len(inv.Items) <= 0) {
		return ErrInvalidInvocation
	}
	maxBytes := DefaultMaxRawJSONBytes
	if err := inv.Options.ResponseSchemaJSON.Validate(maxBytes); err != nil {
		return err
	}
	for i, t := range inv.Tools {
		if strings.TrimSpace(t.Name) == "" {
			return fmt.Errorf("%w: Tools[%d].Name is required", ErrInvalidInvocation, i)
		}
		if t.Name != strings.TrimSpace(t.Name) {
			return fmt.Errorf("%w: Tools[%d].Name must not contain leading or trailing whitespace", ErrInvalidInvocation, i)
		}
		if err := t.ParametersJSON.Validate(maxBytes); err != nil {
			return err
		}
	}
	for _, m := range slices.Concat(inv.Instructions, inv.Messages) {
		if m.Role == RoleUnspecified {
			return ErrUnknownEnum
		}
		for _, p := range m.Parts {
			if p.Kind == PartKindUnspecified {
				return ErrUnknownEnum
			}
			if err := p.ToolArgsJSON.Validate(maxBytes); err != nil {
				return err
			}
		}
	}
	return validateInvocationWire(inv)
}
