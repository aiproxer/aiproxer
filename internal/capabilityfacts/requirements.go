package capabilityfacts

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ErrSemanticFactBudgetExceeded indicates that normalized item/part metadata
// exceeds the configured semantic-fact budget ceiling.
var ErrSemanticFactBudgetExceeded = errors.New("largebody: semantic-fact budget exceeded (optimization decline)")

func checkBudget(maxFactBytes int64) error {
	if maxFactBytes <= 0 {
		return fmt.Errorf("largebody: max fact bytes must be > 0, got %d", maxFactBytes)
	}
	return nil
}

// TurnPartShape records the content-part kind and content size in bytes for one
// normalized turn part without retaining or materializing prompt text.
type TurnPartShape struct {
	Kind         aipapi.ContentPartKind
	ContentBytes int64
}

// Validate enforces canonical vocabulary membership and non-negative sizes.
func (p TurnPartShape) Validate() error {
	switch p.Kind {
	case aipapi.ContentPartText,
		aipapi.ContentPartImageRef,
		aipapi.ContentPartFileRef,
		aipapi.ContentPartVideoRef,
		aipapi.ContentPartRefusal,
		aipapi.ContentPartReasoning,
		aipapi.ContentPartSummary,
		aipapi.ContentPartAnnotation,
		aipapi.ContentPartAssistantRef,
		aipapi.ContentPartJSON,
		aipapi.ContentPartToolResult,
		aipapi.ContentPartExtension:
	default:
		return fmt.Errorf("largebody: unknown turn part kind %q", string(p.Kind))
	}
	if p.ContentBytes < 0 {
		return fmt.Errorf("largebody: turn part content bytes must be >= 0, got %d", p.ContentBytes)
	}
	return nil
}

// TurnItemShape describes one normalized turn item by kind, role,
// ordinal, and part shapes only.
type TurnItemShape struct {
	Kind    aipapi.ItemKind
	Role    aipapi.Role
	Ordinal int64
	Parts   []TurnPartShape
}

// Validate enforces canonical vocabulary membership and bounds part counts
// under the semantic-fact budget.
func (it TurnItemShape) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	switch it.Kind {
	case aipapi.ItemKindMessage,
		aipapi.ItemKindItemReference,
		aipapi.ItemKindToolCall,
		aipapi.ItemKindToolResult,
		aipapi.ItemKindReasoning,
		aipapi.ItemKindCompaction,
		aipapi.ItemKindExtension:
	default:
		return fmt.Errorf("largebody: unknown turn item kind %q", string(it.Kind))
	}
	if it.Kind == aipapi.ItemKindMessage {
		switch it.Role {
		case aipapi.RoleSystem,
			aipapi.RoleDeveloper,
			aipapi.RoleUser,
			aipapi.RoleAssistant,
			aipapi.RoleTool:
		default:
			return fmt.Errorf("largebody: unknown turn role %q", string(it.Role))
		}
	} else if it.Role != "" {
		switch it.Role {
		case aipapi.RoleSystem,
			aipapi.RoleDeveloper,
			aipapi.RoleUser,
			aipapi.RoleAssistant,
			aipapi.RoleTool,
			aipapi.Role(it.Kind):
		default:
			return fmt.Errorf("largebody: unknown turn role %q", string(it.Role))
		}
	}
	if it.Ordinal < 0 {
		return fmt.Errorf("largebody: turn ordinal must be >= 0, got %d", it.Ordinal)
	}
	if int64(len(it.Parts)) > maxFactBytes {
		return fmt.Errorf("%w: turn part count (%d) exceeds budget %d", ErrSemanticFactBudgetExceeded, len(it.Parts), maxFactBytes)
	}
	for i := range it.Parts {
		if err := it.Parts[i].Validate(); err != nil {
			return fmt.Errorf("largebody: turn part %d: %w", i, err)
		}
	}
	return nil
}

// TurnShape is the bounded normalized client-turn shape equivalent to
// aipapi.NormalizedItems for the certified subset: role/ordinal/content-part
// kinds and other recorder-required non-content facts (Requirement 14.3).
// Semantic-fact budget overflow selects canonical processing.
type TurnShape struct {
	Items             []TurnItemShape
	TotalContentBytes int64
}

// MetadataBytes calculates in-memory metadata size for budget validation.
func (s TurnShape) MetadataBytes() int64 {
	var total int64
	for _, it := range s.Items {
		total += int64(len(it.Kind) + len(it.Role) + 8) // 8 bytes for ordinal
		for _, p := range it.Parts {
			total += int64(len(p.Kind) + 8) // 8 bytes for ContentBytes
		}
	}
	return total
}

// Validate bounds item counts under the semantic-fact budget.
func (s TurnShape) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if s.TotalContentBytes < 0 {
		return fmt.Errorf("largebody: total content bytes must be >= 0, got %d", s.TotalContentBytes)
	}
	if int64(len(s.Items)) > maxFactBytes {
		return fmt.Errorf("%w: turn item count (%d) exceeds budget %d", ErrSemanticFactBudgetExceeded, len(s.Items), maxFactBytes)
	}
	if s.MetadataBytes() > maxFactBytes {
		return fmt.Errorf("%w: turn metadata bytes (%d) exceeds budget %d", ErrSemanticFactBudgetExceeded, s.MetadataBytes(), maxFactBytes)
	}
	for i := range s.Items {
		if err := s.Items[i].Validate(maxFactBytes); err != nil {
			return fmt.Errorf("largebody: turn item %d: %w", i, err)
		}
	}
	return nil
}

// ControlRequirements carries request-level control flags and settings needed
// to derive required capabilities under canonical aipapi semantics without
// materializing a aipapi.Call.
type ControlRequirements struct {
	Delivery          aipapi.DeliveryMode
	HasTools          bool
	ParallelToolCalls *bool
	ReasoningEffort   string
	StructuredOutputs bool
	ItemAuthoritative bool
}

// DeriveRequiredCapabilities derives a deduplicated, bounded slice of aipapi.Capability
// required by the client turn and control requirements, matching canonical
// aipapi.RequiredCapabilities semantics.
func DeriveRequiredCapabilities(turn TurnShape, ctrl ControlRequirements) []aipapi.Capability {
	out := []aipapi.Capability{}
	add := func(c aipapi.Capability) {
		if slices.Contains(out, c) {
			return
		}
		out = append(out, c)
	}

	if ctrl.Delivery == aipapi.DeliveryModeStreaming {
		add(aipapi.CapabilityStreaming)
	}

	for _, it := range turn.Items {
		if (!ctrl.ItemAuthoritative && it.Role == aipapi.RoleTool) || it.Kind == aipapi.ItemKindToolCall || it.Kind == aipapi.ItemKindToolResult {
			add(aipapi.CapabilityTools)
		}
		for _, p := range it.Parts {
			switch p.Kind {
			case aipapi.ContentPartImageRef:
				add(aipapi.CapabilityVision)
			case aipapi.ContentPartFileRef:
				add(aipapi.CapabilityDocuments)
			case aipapi.ContentPartVideoRef:
				add(aipapi.CapabilityVideoInput)
			case aipapi.ContentPartReasoning:
				add(aipapi.CapabilityReasoningReplay)
			case aipapi.ContentPartExtension:
				add(aipapi.CapabilityOpaqueExtensions)
			}
		}
	}
	if ctrl.ItemAuthoritative {
		add(aipapi.CapabilityOrderedItems)
	}

	if ctrl.HasTools {
		add(aipapi.CapabilityTools)
	}
	if ctrl.StructuredOutputs {
		add(aipapi.CapabilityStructuredOutputs)
	}
	if strings.TrimSpace(ctrl.ReasoningEffort) != "" {
		add(aipapi.CapabilityReasoning)
	}
	if ctrl.ParallelToolCalls != nil && *ctrl.ParallelToolCalls {
		add(aipapi.CapabilityParallelToolCalls)
	}

	return out
}

// CapabilitiesDigest returns a deterministic SHA-256 digest of the given capabilities.
// It is order-independent and always produces a valid non-zero 32-byte hash.
func CapabilitiesDigest(caps []aipapi.Capability) [32]byte {
	if len(caps) == 0 {
		return sha256.Sum256([]byte("aipapi.caps:empty"))
	}
	sorted := make([]string, len(caps))
	for i, c := range caps {
		sorted[i] = string(c)
	}
	slices.Sort(sorted)
	h := sha256.New()
	for _, s := range sorted {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// ValidateCapabilities validates that every capability name is non-empty and bounded.
func ValidateCapabilities(caps []aipapi.Capability, maxFactBytes int64) error {
	for i, c := range caps {
		if strings.TrimSpace(string(c)) == "" {
			return fmt.Errorf("largebody: required capability %d must not be empty", i)
		}
		if int64(len(c)) > maxFactBytes {
			return fmt.Errorf("largebody: required capability %d exceeds %d bytes", i, maxFactBytes)
		}
	}
	return nil
}
