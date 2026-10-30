package backendplugin

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds for provider-owned graded reasoning-effort declarations.
//
// These are size/shape constraints, never a global allowable vocabulary: the
// provider owns the value strings and the core never compiles a provider
// list. Failed validation is never converted to Unsupported.
const (
	// MaxReasoningEffortModelIDBytes bounds the exact model_id a declaration answers.
	MaxReasoningEffortModelIDBytes = 256
	// MaxReasoningEffortValues bounds the number of graded values in one declaration.
	MaxReasoningEffortValues = 32
	// MaxReasoningEffortValueBytes bounds one graded value string in bytes.
	MaxReasoningEffortValueBytes = 64
)

// ReasoningEffortValuesV1 is the optional, presence-preserving public SDK DTO
// mirroring api/backendplugin/v1 ReasoningEffortValuesV1.
//
// A nil *ReasoningEffortValuesV1 means the provider published no statement.
// Query one requested effort by exact, case-sensitive membership; never
// trim/translate a provided value into another level.
type ReasoningEffortValuesV1 struct {
	// ModelID is the exact ResolveProfile request model_id this declaration answers.
	ModelID string
	// Supported are provider-verified graded effort values, exact strings.
	Supported []string
	// Exhaustive attests this is the complete accepted graded input set.
	// It is authority, not convenience: never infer it from an empty list,
	// the Reasoning boolean, a UI list of suggested levels, or an operator
	// inventory. An empty exhaustive declaration explicitly says the
	// provider supports no graded values.
	Exhaustive bool
}

// Validate reports whether the declaration is well-formed for publication.
// A nil receiver is valid (no statement).
func (v *ReasoningEffortValuesV1) Validate() error {
	if v == nil {
		return nil
	}
	if err := validateEffortModelID(v.ModelID); err != nil {
		return err
	}
	if len(v.Supported) > MaxReasoningEffortValues {
		return fmt.Errorf("%w: reasoning effort values", ErrInvalidInvocation)
	}
	seen := make(map[string]struct{}, len(v.Supported))
	for _, value := range v.Supported {
		if err := validateEffortValue(value); err != nil {
			return err
		}
		if _, dup := seen[value]; dup {
			return fmt.Errorf("%w: duplicate reasoning effort value", ErrInvalidInvocation)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateEffortModelID(id string) error {
	if id == "" || strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: reasoning effort model id", ErrInvalidInvocation)
	}
	if len(id) > MaxReasoningEffortModelIDBytes || !utf8.ValidString(id) {
		return fmt.Errorf("%w: reasoning effort model id", ErrInvalidInvocation)
	}
	if strings.ContainsFunc(id, unicode.IsControl) {
		return fmt.Errorf("%w: reasoning effort model id", ErrInvalidInvocation)
	}
	return nil
}

func validateEffortValue(value string) error {
	if value == "" || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: reasoning effort value", ErrInvalidInvocation)
	}
	if len(value) > MaxReasoningEffortValueBytes || !utf8.ValidString(value) {
		return fmt.Errorf("%w: reasoning effort value", ErrInvalidInvocation)
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("%w: reasoning effort value", ErrInvalidInvocation)
	}
	return nil
}

// ClassifyEffortSupport answers one requested effort against a declaration.
//
// It returns (supported, decided): decided=false means Unknown — the provider
// published no fact for this value and model. An empty effort, a nil/invalid
// declaration, or a mismatched model never decides. Matching is exact and
// case-sensitive.
func ClassifyEffortSupport(decl *ReasoningEffortValuesV1, modelID, effort string) (supported, decided bool) {
	if decl == nil || effort == "" {
		return false, false
	}
	if err := decl.Validate(); err != nil {
		return false, false
	}
	if decl.ModelID != modelID {
		return false, false
	}
	if slices.Contains(decl.Supported, effort) {
		return true, true
	}
	if decl.Exhaustive {
		return false, true
	}
	return false, false
}

// ReasoningEffortValuesNegotiated reports whether the negotiated ABI can
// carry provider-owned graded effort facts.
func ReasoningEffortValuesNegotiated(neg Negotiation) bool {
	return neg.Compatible && neg.NegotiatedMinor >= ProtocolMinorReasoningEffortValues && slices.Contains(neg.EnabledFeatures, FeatureReasoningEffortValues)
}
