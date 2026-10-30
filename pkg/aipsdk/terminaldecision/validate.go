package terminaldecision

import (
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// Bounds are measured in UTF-8 encoded bytes. The values bound every string
// carried by this package, including opaque identifiers and control text.
const (
	MaxProviderIDBytes   = 128
	MaxIdentifierBytes   = 256
	MaxReasonCodeBytes   = 96
	MaxInstructionBytes  = 4096
	MaxEvidenceTextBytes = 2048
	MaxEvidenceActions   = 8
)

// ValidateProviderID checks the stable provider identity. The check is pure;
// callers should obtain ID once while composing a provider and retain that
// value for the generation lifetime.
func ValidateProviderID(id string) error {
	return validateString(ErrInvalidProvider, "provider identity", id, MaxProviderIDBytes, true)
}

// ProviderIdentity validates a provider's stable identity and returns the
// bounded value used by generation composition. Typed-nil providers and
// provider identity panics fail closed as invalid providers.
func ProviderIdentity(provider Provider) (id string, err error) {
	if isNilProvider(provider) {
		return "", ErrInvalidProvider
	}
	defer func() {
		if recover() != nil {
			id = ""
			err = fmt.Errorf("%w: provider identity unavailable", ErrInvalidProvider)
		}
	}()
	id = provider.ID()
	if err := ValidateProviderID(id); err != nil {
		return "", err
	}
	return id, nil
}

func isNilProvider(provider Provider) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// ValidateInput checks the canonical, bounded provider input.
func (in Input) Validate() error { return ValidateInput(in) }

// ValidateInput checks the canonical, bounded provider input.
func ValidateInput(in Input) error {
	if isTypedNilAuxiliary(in.Auxiliary) {
		return fmt.Errorf("%w: auxiliary client is typed nil", ErrInvalidInput)
	}
	if !in.Candidate.Cause.IsKnown() {
		return fmt.Errorf("%w: unknown candidate cause", ErrInvalidInput)
	}
	if err := validateString(ErrInvalidInput, "candidate reference", in.Candidate.Reference, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidInput, "request id", in.Request.RequestID, MaxIdentifierBytes, true); err != nil {
		return err
	}
	if err := validateString(ErrInvalidInput, "trace id", in.Request.TraceID, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidInput, "a-leg id", in.Request.ALegID, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidInput, "b-leg id", in.Request.BLegID, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidInput, "policy revision", in.Policy.Revision, MaxIdentifierBytes, true); err != nil {
		return err
	}
	if err := validateString(ErrInvalidInput, "trajectory reference", in.Continuation.TrajectoryRef, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateEvidence(in.Evidence); err != nil {
		return err
	}
	if in.Deadline.IsZero() {
		return fmt.Errorf("%w: deadline is required", ErrInvalidInput)
	}
	return nil
}

func isTypedNilAuxiliary(client auxiliary.Client) bool {
	if client == nil {
		return false
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func validateEvidence(in Evidence) error {
	if err := validateString(ErrInvalidInput, "evidence objective", in.Objective, MaxEvidenceTextBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidInput, "evidence recent text", in.RecentText, MaxEvidenceTextBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidInput, "evidence candidate text", in.CandidateText, MaxEvidenceTextBytes, false); err != nil {
		return err
	}
	if in.ActionCount > MaxEvidenceActions {
		return fmt.Errorf("%w: evidence action count exceeds %d", ErrInvalidInput, MaxEvidenceActions)
	}
	for i := 0; i < int(in.ActionCount); i++ {
		if err := validateEvidenceAction(in.Actions[i], i); err != nil {
			return err
		}
	}
	for i := int(in.ActionCount); i < len(in.Actions); i++ {
		if in.Actions[i] != (ActionFact{}) {
			return fmt.Errorf("%w: evidence action %d is populated beyond action count", ErrInvalidInput, i)
		}
	}
	if err := validateString(ErrInvalidInput, "evidence trajectory reference", in.Lineage.TrajectoryRef, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidInput, "evidence parent reference", in.Lineage.ParentRef, MaxIdentifierBytes, false); err != nil {
		return err
	}
	return validateString(ErrInvalidInput, "evidence progress reference", in.Lineage.ProgressRef, MaxIdentifierBytes, false)
}

func validateEvidenceAction(action ActionFact, index int) error {
	field := fmt.Sprintf("evidence actions[%d]", index)
	if err := validateString(ErrInvalidInput, field+" item id", action.ItemID, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidInput, field+" call id", action.CallID, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidInput, field+" name", action.Name, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if action.ItemID == "" && action.CallID == "" {
		return fmt.Errorf("%w: %s requires item id or call id", ErrInvalidInput, field)
	}
	switch action.Kind {
	case aipapi.ItemKindMessage, aipapi.ItemKindToolCall, aipapi.ItemKindToolResult:
	default:
		return fmt.Errorf("%w: %s has unknown action kind", ErrInvalidInput, field)
	}
	switch action.Status {
	case aipapi.ItemStatusInProgress, aipapi.ItemStatusCompleted, aipapi.ItemStatusIncomplete:
	default:
		return fmt.Errorf("%w: %s has unknown action status", ErrInvalidInput, field)
	}
	if (action.Kind == aipapi.ItemKindToolCall || action.Kind == aipapi.ItemKindToolResult) && action.CallID == "" {
		return fmt.Errorf("%w: %s tool action requires call id", ErrInvalidInput, field)
	}
	if (action.Kind == aipapi.ItemKindToolCall || action.Kind == aipapi.ItemKindToolResult) && strings.TrimSpace(action.Name) == "" {
		return fmt.Errorf("%w: %s tool action requires name", ErrInvalidInput, field)
	}
	return nil
}

// Validate checks the intent's bounded canonical fields.
func (in ContinuationIntent) Validate() error { return validateIntent(in) }

func validateIntent(in ContinuationIntent) error {
	if err := validateString(ErrInvalidDecision, "continuation trajectory reference", in.TrajectoryRef, MaxIdentifierBytes, true); err != nil {
		return err
	}
	if err := validateString(ErrInvalidDecision, "continuation control reference", in.ControlRef, MaxIdentifierBytes, false); err != nil {
		return err
	}
	if err := validateString(ErrInvalidDecision, "continuation instruction", in.Instruction, MaxInstructionBytes, false); err != nil {
		return err
	}
	if strings.TrimSpace(in.ControlRef) == "" && strings.TrimSpace(in.Instruction) == "" {
		return fmt.Errorf("%w: continuation control reference or instruction is required", ErrInvalidDecision)
	}
	if err := validateString(ErrInvalidDecision, "continuation provenance", in.Provenance, MaxIdentifierBytes, true); err != nil {
		return err
	}
	return validateString(ErrInvalidDecision, "continuation reason code", in.ReasonCode, MaxReasonCodeBytes, true)
}

// Validate checks that a decision has one known kind and the matching
// continuation shape.
func (d Decision) Validate() error { return ValidateDecision(d) }

// ValidateDecision checks that a decision has one known kind and the matching
// continuation shape.
func ValidateDecision(d Decision) error {
	if !d.Kind.IsKnown() {
		return fmt.Errorf("%w: unknown decision kind", ErrInvalidDecision)
	}
	if err := validateString(ErrInvalidDecision, "reason code", d.ReasonCode, MaxReasonCodeBytes, true); err != nil {
		return err
	}
	if d.Kind != DecisionContinue {
		if d.Continue != nil {
			return fmt.Errorf("%w: %s decision cannot carry continuation intent", ErrInvalidDecision, d.Kind)
		}
		return nil
	}
	if d.Continue == nil {
		return fmt.Errorf("%w: continue decision requires continuation intent", ErrInvalidDecision)
	}
	return validateIntent(*d.Continue)
}

func validateString(sentinel error, field, value string, maxBytes int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", sentinel, field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", sentinel, field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%w: %s exceeds %d bytes", sentinel, field, maxBytes)
	}
	return nil
}
