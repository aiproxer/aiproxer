package usageevidence

import (
	"errors"
	"fmt"
	"strings"
)

// WorkloadClass is a bounded, content-free classification of a billable call.
// It is deliberately metadata only: pricing and rating remain selected by the
// existing route/model policy snapshots.
type WorkloadClass string

const (
	WorkloadClassPrimary   WorkloadClass = "primary"
	WorkloadClassAuxiliary WorkloadClass = "auxiliary"
)

// WorkloadRole identifies a trusted auxiliary role. Keep this allowlisted so
// request content cannot become a durable billing/report label.
type WorkloadRole string

const (
	WorkloadRoleCompactionContinuityExtractor   = "compaction_continuity_extractor"
	WorkloadRoleReasoningPreservationCompressor = "reasoning_preservation_compressor"
	// WorkloadRoleProxyMemoryPhase1 and WorkloadRoleProxyMemoryPhase2 are the
	// two Proxy Memory background phases. They are content-free correlation
	// labels like every other role in this closed list.
	WorkloadRoleProxyMemoryPhase1 = "proxy_memory_phase1"
	WorkloadRoleProxyMemoryPhase2 = "proxy_memory_phase2"
)

var ErrInvalidWorkloadIdentity = errors.New("billing: invalid workload identity")

// WorkloadIdentity is safe-to-persist correlation metadata. It contains no
// prompt, completion, provider payload, credential, or header content.
type WorkloadIdentity struct {
	Class WorkloadClass `json:"class,omitempty"`
	Role  WorkloadRole  `json:"role,omitempty"`
}

// WorkloadIdentityFromAuxiliaryRole projects trusted auxiliary lineage into
// the bounded billing/report identity. Unknown roles fail closed rather than
// persisting arbitrary plugin or request text.
func WorkloadIdentityFromAuxiliaryRole(role string) (WorkloadIdentity, error) {
	role = strings.TrimSpace(role)
	switch role {
	case WorkloadRoleCompactionContinuityExtractor, WorkloadRoleReasoningPreservationCompressor,
		WorkloadRoleProxyMemoryPhase1, WorkloadRoleProxyMemoryPhase2:
		return WorkloadIdentity{Class: WorkloadClassAuxiliary, Role: WorkloadRole(role)}, nil
	default:
		return WorkloadIdentity{}, fmt.Errorf("%w: unsupported auxiliary role %q", ErrInvalidWorkloadIdentity, role)
	}
}

func (w WorkloadIdentity) IsZero() bool {
	return strings.TrimSpace(string(w.Class)) == "" && strings.TrimSpace(string(w.Role)) == ""
}

func (w WorkloadIdentity) IsAuxiliary() bool {
	return strings.TrimSpace(string(w.Class)) == string(WorkloadClassAuxiliary)
}

func (w WorkloadIdentity) Validate() error {
	class := WorkloadClass(strings.TrimSpace(string(w.Class)))
	role := WorkloadRole(strings.TrimSpace(string(w.Role)))
	if class == "" && role == "" {
		return nil // legacy primary rows have no workload projection
	}
	if class != WorkloadClassPrimary && class != WorkloadClassAuxiliary {
		return fmt.Errorf("%w: unsupported class %q", ErrInvalidWorkloadIdentity, class)
	}
	switch class {
	case WorkloadClassPrimary:
		if role != "" {
			return fmt.Errorf("%w: primary workload must not carry auxiliary role", ErrInvalidWorkloadIdentity)
		}
	case WorkloadClassAuxiliary:
		switch role {
		case WorkloadRole(WorkloadRoleCompactionContinuityExtractor), WorkloadRole(WorkloadRoleReasoningPreservationCompressor),
			WorkloadRole(WorkloadRoleProxyMemoryPhase1), WorkloadRole(WorkloadRoleProxyMemoryPhase2):
		default:
			return fmt.Errorf("%w: unsupported auxiliary role %q", ErrInvalidWorkloadIdentity, role)
		}
	}
	return nil
}

func normalizeWorkloadIdentity(w WorkloadIdentity) (WorkloadIdentity, error) {
	if err := w.Validate(); err != nil {
		return WorkloadIdentity{}, err
	}
	if w.IsZero() {
		return WorkloadIdentity{}, nil
	}
	return WorkloadIdentity{
		Class: WorkloadClass(strings.TrimSpace(string(w.Class))),
		Role:  WorkloadRole(strings.TrimSpace(string(w.Role))),
	}, nil
}

// ValidateIndependentLeg is the terminal-accounting contract for a leg that
// is persisted independently (including auxiliary and failover B-legs).
// Legacy CallLegUsageRecord rows remain readable through Seal, while new
// independent delivery fails closed when sequence or evidence identity is
// absent.
func ValidateIndependentLeg(leg CallLegUsageRecord) error {
	if leg.AttemptSeq <= 0 {
		return fmt.Errorf("%w: independent B-leg requires positive attempt sequence", ErrInvalidRecord)
	}
	if err := leg.validate(); err != nil {
		return err
	}
	if leg.Evidence.Source == EvidenceSourceUnknown || leg.Evidence.Authority == EvidenceAuthorityUnknown || strings.TrimSpace(leg.Evidence.DedupeKey) == "" {
		return fmt.Errorf("%w: independent B-leg requires evidence source, authority, and dedupe key", ErrInvalidRecord)
	}
	if leg.Evidence.Source == EvidenceSourceProviderReported && !hasBillingEvidence(leg.Evidence) {
		return fmt.Errorf("%w: provider-reported independent B-leg requires usage or cost presence", ErrInvalidRecord)
	}
	return nil
}

func hasBillingEvidence(e FinalBillingEvidence) bool {
	return e.InputTokens.Present || e.OutputTokens.Present || e.CacheReadTokens.Present ||
		e.CacheWriteTokens.Present || e.ReasoningTokens.Present || e.TotalTokens.Present || e.Cost.Present
}

// DedupeKeyForBLeg returns the bounded fallback identity used when a provider
// did not report one. Provider-reported keys must remain unchanged; this helper
// only supplies a proxy-owned key that is scoped to BillingCallID and B-leg.
func DedupeKeyForBLeg(callID BillingCallID, bLegID string) (string, error) {
	key, err := CallLegUsageKey(callID, bLegID)
	if err != nil {
		return "", err
	}
	return "aip-b-leg:" + key, nil
}
