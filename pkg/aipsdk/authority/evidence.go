package authority

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// SafeEvidence is denial/allow evidence without secrets, raw payloads, or
// credentials (requirements 12.8, 14.x). Prefer stable categories and identifiers.
type SafeEvidence struct {
	Category       string                    `json:"category,omitempty"`
	Code           string                    `json:"code,omitempty"`
	Message        string                    `json:"message,omitempty"`
	RuleID         string                    `json:"rule_id,omitempty"`
	ProviderID     string                    `json:"provider_id,omitempty"`
	Attrs          map[string]string         `json:"attrs,omitempty"`
	QuotaPolicyRef *QuotaPolicyRef           `json:"quota_policy_ref,omitempty"`
	EvidenceRefs   []metering.ObservationRef `json:"evidence_refs,omitempty"`
}

// EvidenceSink projects authority outcomes into policy-decision and control-plane
// evidence without exposing storage or HTTP concerns (requirement 12.1).
// Closed enterprise modules inject implementations through aipruntime.Options.
type EvidenceSink interface {
	RecordPolicyDecision(ctx context.Context, record policydecision.Record) error
	RecordAccountingAuthority(ctx context.Context, event controlplane.Event) error
}
