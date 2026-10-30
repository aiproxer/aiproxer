package backendplugin

import (
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// V1EvidenceIdentity supplies trusted host-owned identity while lifting the
// representable token counters from the legacy sideband. Provider request
// evidence must carry a concrete B-leg; the bridge never invents one.
type V1EvidenceIdentity struct {
	StoreID        string
	ObservationID  string
	SourceEventKey string
	Revision       uint64
	StreamID       string
	Sequence       uint64
	Subject        metering.SubjectRef
	Correlation    metering.CorrelationV2
	Scope          scope.PrincipalScopeView
	ObservedAt     time.Time
	ReceivedAt     time.Time
	MappingRef     string
}

// LiftAccountingEvidenceV1 creates an explicitly partial V2 observation. The
// six legacy counters are losslessly mapped to directional token components;
// missing multimodal, charge, qualifier and safe-lexeme detail remains absent.
func LiftAccountingEvidenceV1(e AccountingEvidence, id V1EvidenceIdentity) (AccountingEvidenceV2, error) {
	if err := ValidateAccountingEvidence(e); err != nil {
		return AccountingEvidenceV2{}, err
	}
	id, err := normalizeV1EvidenceIdentity(id)
	if err != nil {
		return AccountingEvidenceV2{}, err
	}
	if id.SourceEventKey == "" {
		id.SourceEventKey = e.DedupeKey
	}
	if id.ObservationID == "" {
		id.ObservationID = e.DedupeKey
	}
	if id.SourceEventKey == "" || id.ObservationID == "" {
		return AccountingEvidenceV2{}, fmt.Errorf("%w: V1 evidence identity", ErrInvalidFrame)
	}
	if id.Revision == 0 {
		id.Revision = 1
	}
	if id.StreamID == "" {
		id.StreamID = id.Subject.BLegID
	}
	if id.MappingRef == "" {
		id.MappingRef = "legacy_v1_accounting"
	}
	if id.ObservedAt.IsZero() {
		id.ObservedAt = time.Unix(0, 0).UTC()
	}
	if id.ReceivedAt.IsZero() {
		id.ReceivedAt = id.ObservedAt
	}

	origin, acquisition, authority := liftProvenance(e)
	out := metering.Observation{
		Version: metering.ObservationVersionV2, ID: id.ObservationID, SourceEventKey: id.SourceEventKey,
		Revision: id.Revision, StreamID: id.StreamID, Sequence: id.Sequence,
		Origin: origin, Acquisition: acquisition, Authority: authority,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject: id.Subject, Correlation: id.Correlation, Scope: id.Scope,
		Semantics: metering.SemanticsDelta, ObservedAt: id.ObservedAt, ReceivedAt: id.ReceivedAt, MappingRef: id.MappingRef,
	}
	for _, value := range []struct {
		value *int64
		key   metering.ComponentKey
	}{
		{e.InputTokens, metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentInputToken, Unit: metering.UnitToken}},
		{e.OutputTokens, metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentOutputToken, Unit: metering.UnitToken}},
		{e.CacheReadTokens, metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken}},
		{e.CacheWriteTokens, metering.ComponentKey{Direction: metering.DirectionInput, Component: metering.ComponentCacheWriteInputToken, Unit: metering.UnitToken}},
		{e.ReasoningTokens, metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentReasoningOutputToken, Unit: metering.UnitToken}},
		{e.TotalTokens, metering.ComponentKey{Direction: metering.DirectionNone, Component: metering.ComponentTotalToken, Unit: metering.UnitToken}},
	} {
		if value.value == nil {
			continue
		}
		decimal := metering.Decimal{Coefficient: fmt.Sprint(*value.value), Scale: 0}
		out.Measures = append(out.Measures, metering.Measure{Key: value.key, Value: &decimal, Quality: metering.QualityObserved})
	}
	result := AccountingEvidenceV2{Observation: out, Coverage: EvidenceCoveragePartial, CoverageReason: "legacy V1 token-only evidence"}
	if err := result.Validate(); err != nil {
		return AccountingEvidenceV2{}, err
	}
	return result, nil
}

// AccountingEvidenceV2FromV1 is a descriptive alias for LiftAccountingEvidenceV1.
func AccountingEvidenceV2FromV1(e AccountingEvidence, id V1EvidenceIdentity) (AccountingEvidenceV2, error) {
	return LiftAccountingEvidenceV1(e, id)
}

func normalizeV1EvidenceIdentity(id V1EvidenceIdentity) (V1EvidenceIdentity, error) {
	if strings.TrimSpace(id.StoreID) == "" {
		return V1EvidenceIdentity{}, fmt.Errorf("%w: V1 store identity", ErrInvalidFrame)
	}
	if id.Subject.StoreID == "" {
		id.Subject.StoreID = id.StoreID
	}
	if id.Correlation.StoreID == "" {
		id.Correlation.StoreID = id.StoreID
	}
	if id.Subject.StoreID != id.StoreID || id.Correlation.StoreID != id.StoreID {
		return V1EvidenceIdentity{}, fmt.Errorf("%w: V1 store identity", ErrInvalidFrame)
	}
	if id.Subject.BLegID == "" || id.Correlation.BLegID == "" {
		return V1EvidenceIdentity{}, fmt.Errorf("%w: V1 bridge requires B-leg identity", ErrAccountingEvidenceV2Unsupported)
	}
	return id, nil
}

func liftProvenance(e AccountingEvidence) (origin, acquisition, authority string) {
	switch e.Source {
	case AccountingSourceProviderCountAPI:
		return metering.OriginProvider, metering.AcquisitionProviderCountAPI, metering.AuthorityObservedClaim
	case AccountingSourceLocalEstimator:
		return metering.OriginLocal, metering.AcquisitionLocalEstimator, metering.AuthorityEstimatedClaim
	case AccountingSourceLocalTokenizer:
		return metering.OriginLocal, metering.AcquisitionLocalTokenizer, metering.AuthorityEstimatedClaim
	default:
		return metering.OriginProvider, metering.AcquisitionProviderResponse, metering.AuthorityObservedClaim
	}
}
