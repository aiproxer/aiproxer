package backendplugin

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// EconomicDecimalV2FromProto decodes an exact decimal without floating-point
// coercion. A missing message is an invalid top-level value; optional measure
// and charge values use the private pointer helper below.
func EconomicDecimalV2FromProto(p *backendpluginv1.EconomicDecimalV2) (metering.Decimal, error) {
	if p == nil {
		return metering.Decimal{}, economicInvalid("decimal")
	}
	if economicProtoHasUnknown(p) {
		return metering.Decimal{}, economicInvalid("decimal fields")
	}
	// The canonical DTO stores scale in uint8. Reject a wider wire value before
	// conversion; truncating it would change the exact monetary quantity.
	if p.GetScale() > 255 {
		return metering.Decimal{}, economicInvalid("decimal")
	}
	d := metering.Decimal{Coefficient: p.GetCoefficient(), Scale: uint8(p.GetScale())}
	n, err := d.Normalize()
	if err != nil {
		return metering.Decimal{}, economicInvalid("decimal")
	}
	return n, nil
}

// EconomicDecimalV2ToProto encodes an exact canonical decimal.
func EconomicDecimalV2ToProto(d metering.Decimal) (*backendpluginv1.EconomicDecimalV2, error) {
	n, err := d.Normalize()
	if err != nil {
		return nil, economicInvalid("decimal")
	}
	return &backendpluginv1.EconomicDecimalV2{Coefficient: n.Coefficient, Scale: uint32(n.Scale)}, nil
}

func economicDecimalFromProto(p *backendpluginv1.EconomicDecimalV2) (*metering.Decimal, error) {
	if p == nil {
		return nil, nil
	}
	d, err := EconomicDecimalV2FromProto(p)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func economicDecimalToProto(d *metering.Decimal) (*backendpluginv1.EconomicDecimalV2, error) {
	if d == nil {
		return nil, nil
	}
	return EconomicDecimalV2ToProto(*d)
}

// EconomicObservationV2FromProto converts the typed wire envelope into the
// canonical metering model. Conversion is deliberately strict and does not
// retain unknown raw protobuf fields as economic evidence.
func EconomicObservationV2FromProto(p *backendpluginv1.EconomicObservationV2) (metering.Observation, error) {
	if p == nil {
		return metering.Observation{}, economicInvalid("observation")
	}
	if economicProtoHasUnknown(p) {
		return metering.Observation{}, economicInvalid("observation fields")
	}
	if uint64(proto.Size(p)) > DefaultMaxEconomicObservationBytes {
		return metering.Observation{}, economicInvalid("observation")
	}
	if len(p.GetMeasures()) > metering.MaxObservationMeasures || len(p.GetCharges()) > metering.MaxObservationCharges || len(p.GetSupersedes()) > metering.MaxObservationSupersedes || len(p.GetEvidence()) > metering.MaxObservationEvidence {
		return metering.Observation{}, economicInvalid("observation entries")
	}
	subject, err := economicSubjectFromProto(p.GetSubject())
	if err != nil {
		return metering.Observation{}, err
	}
	correlation, err := economicCorrelationFromProto(p.GetCorrelation())
	if err != nil {
		return metering.Observation{}, err
	}
	scopeView, err := economicScopeFromProto(p.GetScope())
	if err != nil {
		return metering.Observation{}, err
	}
	out := metering.Observation{
		Version:        p.GetVersion(),
		ID:             p.GetId(),
		SourceEventKey: p.GetSourceEventKey(),
		Revision:       p.GetRevision(),
		StreamID:       p.GetStreamId(),
		Sequence:       p.GetSequence(),
		Origin:         p.GetOrigin(),
		Acquisition:    p.GetAcquisition(),
		Authority:      p.GetAuthority(),
		Perspective:    metering.EconomicPerspective(p.GetPerspective()),
		Boundary:       metering.Boundary(p.GetBoundary()),
		Lifecycle:      metering.LifecycleScope(p.GetLifecycle()),
		Subject:        subject,
		Correlation:    correlation,
		Scope:          scopeView,
		Semantics:      p.GetSemantics(),
		ObservedAt:     economicTimeFromUnixNanos(p.GetObservedAtUnixNanos()),
		ReceivedAt:     economicTimeFromUnixNanos(p.GetReceivedAtUnixNanos()),
		MappingRef:     p.GetMappingRef(),
	}
	for _, wire := range p.GetMeasures() {
		measure, convErr := economicMeasureFromProto(wire)
		if convErr != nil {
			return metering.Observation{}, convErr
		}
		out.Measures = append(out.Measures, measure)
	}
	for _, wire := range p.GetCharges() {
		charge, convErr := economicChargeFromProto(wire)
		if convErr != nil {
			return metering.Observation{}, convErr
		}
		out.Charges = append(out.Charges, charge)
	}
	for _, wire := range p.GetSupersedes() {
		ref, convErr := economicObservationRefFromProto(wire)
		if convErr != nil {
			return metering.Observation{}, convErr
		}
		out.Supersedes = append(out.Supersedes, ref)
	}
	for _, wire := range p.GetEvidence() {
		field, convErr := economicEvidenceFieldFromProto(wire)
		if convErr != nil {
			return metering.Observation{}, convErr
		}
		out.Evidence = append(out.Evidence, field)
	}
	if err := out.Validate(); err != nil {
		return metering.Observation{}, economicInvalid("observation")
	}
	if b, err := out.CanonicalJSON(); err != nil || uint64(len(b)) > DefaultMaxEconomicObservationBytes {
		return metering.Observation{}, economicInvalid("observation")
	}
	return out, nil
}

// EconomicObservationV2ToProto encodes the canonical V2 observation with
// explicit decimal and nested-field presence.
func EconomicObservationV2ToProto(o metering.Observation) (*backendpluginv1.EconomicObservationV2, error) {
	canonical, err := o.Canonical()
	if err != nil {
		return nil, economicInvalid("observation")
	}
	if b, jsonErr := canonical.CanonicalJSON(); jsonErr != nil || uint64(len(b)) > DefaultMaxEconomicObservationBytes {
		return nil, economicInvalid("observation")
	}
	subject, err := economicSubjectToProto(canonical.Subject)
	if err != nil {
		return nil, err
	}
	correlation, err := economicCorrelationToProto(canonical.Correlation)
	if err != nil {
		return nil, err
	}
	scopeWire, err := economicScopeToProto(canonical.Scope)
	if err != nil {
		return nil, err
	}
	out := &backendpluginv1.EconomicObservationV2{
		Version:             canonical.Version,
		Id:                  canonical.ID,
		SourceEventKey:      canonical.SourceEventKey,
		Revision:            canonical.Revision,
		StreamId:            canonical.StreamID,
		Sequence:            canonical.Sequence,
		Origin:              canonical.Origin,
		Acquisition:         canonical.Acquisition,
		Authority:           canonical.Authority,
		Perspective:         string(canonical.Perspective),
		Boundary:            string(canonical.Boundary),
		Lifecycle:           string(canonical.Lifecycle),
		Subject:             subject,
		Correlation:         correlation,
		Scope:               scopeWire,
		Semantics:           canonical.Semantics,
		ObservedAtUnixNanos: economicUnixNanos(canonical.ObservedAt),
		ReceivedAtUnixNanos: economicUnixNanos(canonical.ReceivedAt),
		MappingRef:          canonical.MappingRef,
	}
	for _, measure := range canonical.Measures {
		wire, convErr := economicMeasureToProto(measure)
		if convErr != nil {
			return nil, convErr
		}
		out.Measures = append(out.Measures, wire)
	}
	for _, charge := range canonical.Charges {
		wire, convErr := economicChargeToProto(charge)
		if convErr != nil {
			return nil, convErr
		}
		out.Charges = append(out.Charges, wire)
	}
	for _, ref := range canonical.Supersedes {
		wire, convErr := economicObservationRefToProto(ref)
		if convErr != nil {
			return nil, convErr
		}
		out.Supersedes = append(out.Supersedes, wire)
	}
	for _, field := range canonical.Evidence {
		wire, convErr := economicEvidenceFieldToProto(field)
		if convErr != nil {
			return nil, convErr
		}
		out.Evidence = append(out.Evidence, wire)
	}
	if uint64(proto.Size(out)) > DefaultMaxEconomicObservationBytes {
		return nil, ErrOversizedMessage
	}
	return out, nil
}

// ObservationV2FromProto and ObservationV2ToProto are short neutral aliases.
func ObservationV2FromProto(p *backendpluginv1.EconomicObservationV2) (metering.Observation, error) {
	return EconomicObservationV2FromProto(p)
}

func ObservationV2ToProto(o metering.Observation) (*backendpluginv1.EconomicObservationV2, error) {
	return EconomicObservationV2ToProto(o)
}

// AccountingEvidenceV2FromProto converts the V2 host-only wrapper.
func AccountingEvidenceV2FromProto(p *backendpluginv1.AccountingEvidenceV2) (AccountingEvidenceV2, error) {
	if p == nil {
		return AccountingEvidenceV2{}, economicInvalid("accounting evidence")
	}
	if economicProtoHasUnknown(p) {
		return AccountingEvidenceV2{}, economicInvalid("accounting evidence fields")
	}
	if uint64(proto.Size(p)) > DefaultMaxStreamFrameBytes {
		return AccountingEvidenceV2{}, economicInvalid("accounting evidence")
	}
	observation, err := EconomicObservationV2FromProto(p.GetObservation())
	if err != nil {
		return AccountingEvidenceV2{}, err
	}
	out := AccountingEvidenceV2{Observation: observation, Coverage: EvidenceCoverage(p.GetCoverage()), CoverageReason: p.GetCoverageReason()}
	if err := out.Validate(); err != nil {
		return AccountingEvidenceV2{}, err
	}
	return out, nil
}

// AccountingEvidenceV2ToProto encodes the V2 host-only wrapper.
func AccountingEvidenceV2ToProto(e *AccountingEvidenceV2) (*backendpluginv1.AccountingEvidenceV2, error) {
	if e == nil {
		return nil, nil
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	observation, err := EconomicObservationV2ToProto(e.Observation)
	if err != nil {
		return nil, err
	}
	out := &backendpluginv1.AccountingEvidenceV2{Observation: observation, Coverage: string(e.Coverage), CoverageReason: e.CoverageReason}
	if out.Coverage == "" {
		out.Coverage = string(EvidenceCoverageComplete)
	}
	if uint64(proto.Size(out)) > DefaultMaxStreamFrameBytes {
		return nil, ErrOversizedMessage
	}
	return out, nil
}

func economicInvalid(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidFrame, field)
}

// economicProtoHasUnknown rejects fields that this V2 decoder cannot retain.
// V2 evidence is an accounting boundary: silently dropping a future field
// could change a charge or coverage graph. This strict walk is limited to the
// typed message tree and does not alter the additive V1 decoder behavior.
func economicProtoHasUnknown(message proto.Message) bool {
	if message == nil {
		return false
	}
	var walk func(protoreflect.Message) bool
	walk = func(m protoreflect.Message) bool {
		if len(m.GetUnknown()) != 0 {
			return true
		}
		fields := m.Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			field := fields.Get(i)
			if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
				continue
			}
			if field.IsMap() {
				mapValue := field.MapValue()
				if mapValue.Kind() != protoreflect.MessageKind && mapValue.Kind() != protoreflect.GroupKind {
					continue
				}
				mapValueMessages := m.Get(field).Map()
				unknown := false
				mapValueMessages.Range(func(_ protoreflect.MapKey, value protoreflect.Value) bool {
					if walk(value.Message()) {
						unknown = true
						return false
					}
					return true
				})
				if unknown {
					return true
				}
				continue
			}
			if field.IsList() {
				list := m.Get(field).List()
				for j := 0; j < list.Len(); j++ {
					if walk(list.Get(j).Message()) {
						return true
					}
				}
				continue
			}
			if m.Has(field) && walk(m.Get(field).Message()) {
				return true
			}
		}
		return false
	}
	return walk(message.ProtoReflect())
}

func economicSubjectFromProto(p *backendpluginv1.EconomicSubjectV2) (metering.SubjectRef, error) {
	if p == nil {
		return metering.SubjectRef{}, economicInvalid("subject")
	}
	out := metering.SubjectRef{
		Kind:               metering.SubjectKind(p.GetKind()),
		StoreID:            p.GetStoreId(),
		TenantID:           p.GetTenantId(),
		AccountID:          p.GetAccountId(),
		ALegID:             p.GetALegId(),
		RequestID:          p.GetRequestId(),
		BillingCallID:      p.GetBillingCallId(),
		CallID:             p.GetCallId(),
		BLegID:             p.GetBLegId(),
		AttemptID:          p.GetAttemptId(),
		AttemptSeq:         p.GetAttemptSeq(),
		SubmissionID:       p.GetSubmissionId(),
		ProviderAccountKey: p.GetProviderAccountKey(),
		ProviderRequestID:  p.GetProviderRequestId(),
		ProviderChargeID:   p.GetProviderChargeId(),
		ResourceID:         p.GetResourceId(),
		PeriodID:           p.GetPeriodId(),
		PoolID:             p.GetPoolId(),
		WindowID:           p.GetWindowId(),
		StatementID:        p.GetStatementId(),
		StatementLineID:    p.GetStatementLineId(),
		ResetAt:            economicTimeFromOptionalUnixNanos(p.GetResetAtUnixNanos()),
		StartAt:            economicTimeFromOptionalUnixNanos(p.GetStartAtUnixNanos()),
		EndAt:              economicTimeFromOptionalUnixNanos(p.GetEndAtUnixNanos()),
	}
	if err := out.Validate(); err != nil {
		return metering.SubjectRef{}, economicInvalid("subject")
	}
	return out, nil
}

func economicSubjectToProto(s metering.SubjectRef) (*backendpluginv1.EconomicSubjectV2, error) {
	if err := s.Validate(); err != nil {
		return nil, economicInvalid("subject")
	}
	return &backendpluginv1.EconomicSubjectV2{
		Kind:               string(s.Kind),
		StoreId:            s.StoreID,
		TenantId:           s.TenantID,
		AccountId:          s.AccountID,
		ALegId:             s.ALegID,
		RequestId:          s.RequestID,
		BillingCallId:      s.BillingCallID,
		CallId:             s.CallID,
		BLegId:             s.BLegID,
		AttemptId:          s.AttemptID,
		AttemptSeq:         s.AttemptSeq,
		SubmissionId:       s.SubmissionID,
		ProviderAccountKey: s.ProviderAccountKey,
		ProviderRequestId:  s.ProviderRequestID,
		ProviderChargeId:   s.ProviderChargeID,
		ResourceId:         s.ResourceID,
		PeriodId:           s.PeriodID,
		PoolId:             s.PoolID,
		WindowId:           s.WindowID,
		StatementId:        s.StatementID,
		StatementLineId:    s.StatementLineID,
		ResetAtUnixNanos:   economicOptionalUnixNanos(s.ResetAt),
		StartAtUnixNanos:   economicOptionalUnixNanos(s.StartAt),
		EndAtUnixNanos:     economicOptionalUnixNanos(s.EndAt),
	}, nil
}

func economicCorrelationFromProto(p *backendpluginv1.EconomicCorrelationV2) (metering.CorrelationV2, error) {
	if p == nil {
		return metering.CorrelationV2{}, economicInvalid("correlation")
	}
	out := metering.CorrelationV2{
		StoreID:            p.GetStoreId(),
		TenantID:           p.GetTenantId(),
		RequestID:          p.GetRequestId(),
		CallID:             p.GetCallId(),
		BillingCallID:      p.GetBillingCallId(),
		ALegID:             p.GetALegId(),
		BLegID:             p.GetBLegId(),
		AttemptID:          p.GetAttemptId(),
		AttemptSeq:         p.GetAttemptSeq(),
		SubmissionID:       p.GetSubmissionId(),
		ProviderAccountKey: p.GetProviderAccountKey(),
		ProviderRequestID:  p.GetProviderRequestId(),
		ProviderChargeID:   p.GetProviderChargeId(),
		ParentWorkID:       p.GetParentWorkId(),
		ResourceID:         p.GetResourceId(),
		PeriodID:           p.GetPeriodId(),
	}
	if err := out.Validate(); err != nil {
		return metering.CorrelationV2{}, economicInvalid("correlation")
	}
	return out, nil
}

func economicCorrelationToProto(c metering.CorrelationV2) (*backendpluginv1.EconomicCorrelationV2, error) {
	if err := c.Validate(); err != nil {
		return nil, economicInvalid("correlation")
	}
	return &backendpluginv1.EconomicCorrelationV2{
		StoreId:            c.StoreID,
		TenantId:           c.TenantID,
		RequestId:          c.RequestID,
		CallId:             c.CallID,
		BillingCallId:      c.BillingCallID,
		ALegId:             c.ALegID,
		BLegId:             c.BLegID,
		AttemptId:          c.AttemptID,
		AttemptSeq:         c.AttemptSeq,
		SubmissionId:       c.SubmissionID,
		ProviderAccountKey: c.ProviderAccountKey,
		ProviderRequestId:  c.ProviderRequestID,
		ProviderChargeId:   c.ProviderChargeID,
		ParentWorkId:       c.ParentWorkID,
		ResourceId:         c.ResourceID,
		PeriodId:           c.PeriodID,
	}, nil
}

func economicMeasureFromProto(p *backendpluginv1.EconomicMeasureV2) (metering.Measure, error) {
	if p == nil {
		return metering.Measure{}, economicInvalid("measure")
	}
	key, err := economicComponentKeyFromProto(p.GetKey())
	if err != nil {
		return metering.Measure{}, err
	}
	value, err := economicDecimalFromProto(p.GetValue())
	if err != nil {
		return metering.Measure{}, err
	}
	out := metering.Measure{Key: key, Value: value, Quality: p.GetQuality(), MethodRef: p.GetMethodRef(), Reason: p.GetReason()}
	if err := out.Validate(); err != nil {
		return metering.Measure{}, economicInvalid("measure")
	}
	return out, nil
}

func economicMeasureToProto(m metering.Measure) (*backendpluginv1.EconomicMeasureV2, error) {
	if err := m.Validate(); err != nil {
		return nil, economicInvalid("measure")
	}
	key, err := economicComponentKeyToProto(m.Key)
	if err != nil {
		return nil, err
	}
	value, err := economicDecimalToProto(m.Value)
	if err != nil {
		return nil, err
	}
	return &backendpluginv1.EconomicMeasureV2{Key: key, Value: value, Quality: m.Quality, MethodRef: m.MethodRef, Reason: m.Reason}, nil
}

func economicComponentKeyFromProto(p *backendpluginv1.EconomicComponentKeyV2) (metering.ComponentKey, error) {
	if p == nil {
		return metering.ComponentKey{}, economicInvalid("component key")
	}
	out := metering.ComponentKey{Direction: metering.FlowDirection(p.GetDirection()), Component: p.GetComponent(), Unit: p.GetUnit(), SchemaID: p.GetSchemaId()}
	for _, wire := range p.GetDimensions() {
		if wire == nil {
			return metering.ComponentKey{}, economicInvalid("dimension")
		}
		out.Dimensions = append(out.Dimensions, metering.Dimension{Name: wire.GetName(), Value: wire.GetValue()})
	}
	if err := out.Validate(); err != nil {
		return metering.ComponentKey{}, economicInvalid("component key")
	}
	return out, nil
}

func economicComponentKeyToProto(k metering.ComponentKey) (*backendpluginv1.EconomicComponentKeyV2, error) {
	if err := k.Validate(); err != nil {
		return nil, economicInvalid("component key")
	}
	key, err := k.Normalize()
	if err != nil {
		return nil, economicInvalid("component key")
	}
	out := &backendpluginv1.EconomicComponentKeyV2{Direction: string(key.Direction), Component: key.Component, Unit: key.Unit, SchemaId: key.SchemaID}
	for _, dimension := range key.Dimensions {
		if err := dimension.Validate(); err != nil {
			return nil, economicInvalid("dimension")
		}
		out.Dimensions = append(out.Dimensions, &backendpluginv1.EconomicDimensionV2{Name: dimension.Name, Value: dimension.Value})
	}
	return out, nil
}

func economicChargeFromProto(p *backendpluginv1.EconomicChargeV2) (metering.ReportedCharge, error) {
	if p == nil {
		return metering.ReportedCharge{}, economicInvalid("charge")
	}
	var component *metering.ComponentKey
	if p.GetComponent() != nil {
		key, err := economicComponentKeyFromProto(p.GetComponent())
		if err != nil {
			return metering.ReportedCharge{}, err
		}
		component = &key
	}
	amount, err := economicDecimalFromProto(p.GetAmount())
	if err != nil {
		return metering.ReportedCharge{}, err
	}
	var payer metering.PaymentParty
	if p.GetPayer() != nil {
		payer = metering.PaymentParty{Kind: metering.PaymentPartyKind(p.GetPayer().GetKind()), ID: p.GetPayer().GetId()}
	}
	out := metering.ReportedCharge{ChargeItemID: p.GetChargeItemId(), Component: component, Amount: amount, Currency: p.GetCurrency(), Kind: metering.ChargeKind(p.GetKind()), Payer: payer}
	for _, wire := range p.GetCovers() {
		coverage, convErr := economicCoverageFromProto(wire)
		if convErr != nil {
			return metering.ReportedCharge{}, convErr
		}
		out.Covers = append(out.Covers, coverage)
	}
	if err := out.Validate(false); err != nil {
		return metering.ReportedCharge{}, economicInvalid("charge")
	}
	return out, nil
}

func economicChargeToProto(c metering.ReportedCharge) (*backendpluginv1.EconomicChargeV2, error) {
	if err := c.Validate(false); err != nil {
		return nil, economicInvalid("charge")
	}
	var component *backendpluginv1.EconomicComponentKeyV2
	if c.Component != nil {
		var err error
		component, err = economicComponentKeyToProto(*c.Component)
		if err != nil {
			return nil, err
		}
	}
	amount, err := economicDecimalToProto(c.Amount)
	if err != nil {
		return nil, err
	}
	var payer *backendpluginv1.EconomicPaymentPartyV2
	if c.Payer.Kind != "" || c.Payer.ID != "" {
		if err := c.Payer.Validate(); err != nil {
			return nil, economicInvalid("payer")
		}
		payer = &backendpluginv1.EconomicPaymentPartyV2{Kind: string(c.Payer.Kind), Id: c.Payer.ID}
	}
	out := &backendpluginv1.EconomicChargeV2{ChargeItemId: c.ChargeItemID, Component: component, Amount: amount, Currency: c.Currency, Kind: string(c.Kind), Payer: payer}
	for _, coverage := range c.Covers {
		wire, convErr := economicCoverageToProto(coverage)
		if convErr != nil {
			return nil, convErr
		}
		out.Covers = append(out.Covers, wire)
	}
	return out, nil
}

func economicCoverageFromProto(p *backendpluginv1.EconomicCoverageV2) (metering.ChargeCoverageRef, error) {
	if p == nil || p.GetRef() == nil {
		return metering.ChargeCoverageRef{}, economicInvalid("charge coverage")
	}
	ref := metering.ChargeRef{StoreID: p.GetRef().GetStoreId(), ObservationID: p.GetRef().GetObservationId(), Revision: p.GetRef().GetRevision(), ChargeItemID: p.GetRef().GetChargeItemId()}
	out := metering.ChargeCoverageRef{Ref: ref, Relation: metering.CoverageRelation(p.GetRelation())}
	if err := out.Validate(); err != nil {
		return metering.ChargeCoverageRef{}, economicInvalid("charge coverage")
	}
	return out, nil
}

func economicCoverageToProto(c metering.ChargeCoverageRef) (*backendpluginv1.EconomicCoverageV2, error) {
	if err := c.Validate(); err != nil {
		return nil, economicInvalid("charge coverage")
	}
	return &backendpluginv1.EconomicCoverageV2{Ref: &backendpluginv1.EconomicChargeRefV2{StoreId: c.Ref.StoreID, ObservationId: c.Ref.ObservationID, Revision: c.Ref.Revision, ChargeItemId: c.Ref.ChargeItemID}, Relation: string(c.Relation)}, nil
}

func economicObservationRefFromProto(p *backendpluginv1.EconomicObservationRefV2) (metering.ObservationRef, error) {
	if p == nil {
		return metering.ObservationRef{}, economicInvalid("supersession reference")
	}
	out := metering.ObservationRef{StoreID: p.GetStoreId(), ObservationID: p.GetObservationId(), Revision: p.GetRevision(), PayloadHash: p.GetPayloadHash()}
	if err := out.Validate(); err != nil {
		return metering.ObservationRef{}, economicInvalid("supersession reference")
	}
	return out, nil
}

func economicObservationRefToProto(r metering.ObservationRef) (*backendpluginv1.EconomicObservationRefV2, error) {
	if err := r.Validate(); err != nil {
		return nil, economicInvalid("supersession reference")
	}
	return &backendpluginv1.EconomicObservationRefV2{StoreId: r.StoreID, ObservationId: r.ObservationID, Revision: r.Revision, PayloadHash: r.PayloadHash}, nil
}

func economicEvidenceFieldFromProto(p *backendpluginv1.EconomicEvidenceFieldV2) (metering.SafeEvidenceField, error) {
	if p == nil {
		return metering.SafeEvidenceField{}, economicInvalid("safe evidence")
	}
	out := metering.SafeEvidenceField{Path: p.GetPath(), Name: p.GetName(), Lexeme: p.GetLexeme(), Value: p.GetValue(), Present: p.GetPresent(), Null: p.GetNull(), Acquisition: p.GetAcquisition()}
	if err := out.Validate(); err != nil {
		return metering.SafeEvidenceField{}, economicInvalid("safe evidence")
	}
	return out, nil
}

func economicEvidenceFieldToProto(e metering.SafeEvidenceField) (*backendpluginv1.EconomicEvidenceFieldV2, error) {
	if err := e.Validate(); err != nil {
		return nil, economicInvalid("safe evidence")
	}
	return &backendpluginv1.EconomicEvidenceFieldV2{Path: e.Path, Name: e.Name, Lexeme: e.Lexeme, Value: e.Value, Present: e.Present, Null: e.Null, Acquisition: e.Acquisition}, nil
}

func economicScopeValueFromProto(p *backendpluginv1.EconomicScopeValueV2) (scope.Value, error) {
	if p == nil {
		return scope.Unknown(), nil
	}
	if !utf8.ValidString(p.GetValue()) || len(p.GetValue()) > int(metering.MaxSchemaIDBytes) || (!p.GetKnown() && p.GetValue() != "") {
		return scope.Value{}, economicInvalid("scope value")
	}
	return scope.Value{Known: p.GetKnown(), Value: p.GetValue()}, nil
}

func economicScopeValueToProto(v scope.Value) (*backendpluginv1.EconomicScopeValueV2, error) {
	if !utf8.ValidString(v.Value) || len(v.Value) > int(metering.MaxSchemaIDBytes) || (!v.Known && v.Value != "") {
		return nil, economicInvalid("scope value")
	}
	if !v.Known && v.Value == "" {
		return nil, nil
	}
	return &backendpluginv1.EconomicScopeValueV2{Known: v.Known, Value: v.Value}, nil
}

func economicScopeFromProto(p *backendpluginv1.EconomicScopeV2) (scope.PrincipalScopeView, error) {
	if p == nil {
		return scope.PrincipalScopeView{}, nil
	}
	if len(p.GetRoles()) > 128 || len(p.GetSafeClaims()) > 128 || len(p.GetPolicyLabels()) > 128 {
		return scope.PrincipalScopeView{}, economicInvalid("scope entries")
	}
	view := scope.PrincipalScopeView{SubjectKind: scope.SubjectKind(p.GetSubjectKind()), Origin: scope.Origin(p.GetOrigin()), Roles: append([]string(nil), p.GetRoles()...), SafeClaims: map[string]string{}, PolicyLabels: map[string]string{}}
	if !validEconomicText(string(view.SubjectKind), int(metering.MaxSchemaIDBytes)) || !validEconomicText(string(view.Origin), int(metering.MaxSchemaIDBytes)) {
		return scope.PrincipalScopeView{}, economicInvalid("scope identity")
	}
	for _, role := range view.Roles {
		if !validEconomicText(role, int(metering.MaxSchemaIDBytes)) {
			return scope.PrincipalScopeView{}, economicInvalid("scope role")
		}
	}
	var err error
	if view.PrincipalID, err = economicScopeValueFromProto(p.GetPrincipalId()); err != nil {
		return scope.PrincipalScopeView{}, err
	}
	if view.DisplayName, err = economicScopeValueFromProto(p.GetDisplayName()); err != nil {
		return scope.PrincipalScopeView{}, err
	}
	if view.AuthMethod, err = economicScopeValueFromProto(p.GetAuthMethod()); err != nil {
		return scope.PrincipalScopeView{}, err
	}
	if view.CredentialID, err = economicScopeValueFromProto(p.GetCredentialId()); err != nil {
		return scope.PrincipalScopeView{}, err
	}
	if view.TenantID, err = economicScopeValueFromProto(p.GetTenantId()); err != nil {
		return scope.PrincipalScopeView{}, err
	}
	if view.OrganizationID, err = economicScopeValueFromProto(p.GetOrganizationId()); err != nil {
		return scope.PrincipalScopeView{}, err
	}
	if view.WorkspaceID, err = economicScopeValueFromProto(p.GetWorkspaceId()); err != nil {
		return scope.PrincipalScopeView{}, err
	}
	if view.ProjectID, err = economicScopeValueFromProto(p.GetProjectId()); err != nil {
		return scope.PrincipalScopeView{}, err
	}
	if view.DepartmentID, err = economicScopeValueFromProto(p.GetDepartmentId()); err != nil {
		return scope.PrincipalScopeView{}, err
	}
	if view.CostCenterID, err = economicScopeValueFromProto(p.GetCostCenterId()); err != nil {
		return scope.PrincipalScopeView{}, err
	}
	if view.ParentTraceID, err = economicScopeValueFromProto(p.GetParentTraceId()); err != nil {
		return scope.PrincipalScopeView{}, err
	}
	for k, v := range p.GetSafeClaims() {
		if !economicSafeScopeText(k) || !economicSafeScopeText(v) {
			return scope.PrincipalScopeView{}, economicInvalid("scope claim")
		}
		view.SafeClaims[k] = v
	}
	for k, v := range p.GetPolicyLabels() {
		if !economicSafeScopeText(k) || !economicSafeScopeText(v) {
			return scope.PrincipalScopeView{}, economicInvalid("scope label")
		}
		view.PolicyLabels[k] = v
	}
	if len(view.SafeClaims) == 0 {
		view.SafeClaims = nil
	}
	if len(view.PolicyLabels) == 0 {
		view.PolicyLabels = nil
	}
	return view, nil
}

func economicScopeToProto(v scope.PrincipalScopeView) (*backendpluginv1.EconomicScopeV2, error) {
	out := &backendpluginv1.EconomicScopeV2{SubjectKind: string(v.SubjectKind), Roles: append([]string(nil), v.Roles...), SafeClaims: map[string]string{}, PolicyLabels: map[string]string{}, Origin: string(v.Origin)}
	if len(v.Roles) > 128 || len(v.SafeClaims) > 128 || len(v.PolicyLabels) > 128 {
		return nil, economicInvalid("scope entries")
	}
	for _, role := range v.Roles {
		if !economicSafeScopeText(role) {
			return nil, economicInvalid("scope role")
		}
	}
	var err error
	if out.PrincipalId, err = economicScopeValueToProto(v.PrincipalID); err != nil {
		return nil, err
	}
	if out.DisplayName, err = economicScopeValueToProto(v.DisplayName); err != nil {
		return nil, err
	}
	if out.AuthMethod, err = economicScopeValueToProto(v.AuthMethod); err != nil {
		return nil, err
	}
	if out.CredentialId, err = economicScopeValueToProto(v.CredentialID); err != nil {
		return nil, err
	}
	if out.TenantId, err = economicScopeValueToProto(v.TenantID); err != nil {
		return nil, err
	}
	if out.OrganizationId, err = economicScopeValueToProto(v.OrganizationID); err != nil {
		return nil, err
	}
	if out.WorkspaceId, err = economicScopeValueToProto(v.WorkspaceID); err != nil {
		return nil, err
	}
	if out.ProjectId, err = economicScopeValueToProto(v.ProjectID); err != nil {
		return nil, err
	}
	if out.DepartmentId, err = economicScopeValueToProto(v.DepartmentID); err != nil {
		return nil, err
	}
	if out.CostCenterId, err = economicScopeValueToProto(v.CostCenterID); err != nil {
		return nil, err
	}
	if out.ParentTraceId, err = economicScopeValueToProto(v.ParentTraceID); err != nil {
		return nil, err
	}
	for k, value := range v.SafeClaims {
		if !economicSafeScopeText(k) || !economicSafeScopeText(value) {
			return nil, economicInvalid("scope claim")
		}
		out.SafeClaims[k] = value
	}
	for k, value := range v.PolicyLabels {
		if !economicSafeScopeText(k) || !economicSafeScopeText(value) {
			return nil, economicInvalid("scope label")
		}
		out.PolicyLabels[k] = value
	}
	if len(out.SafeClaims) == 0 {
		out.SafeClaims = nil
	}
	if len(out.PolicyLabels) == 0 {
		out.PolicyLabels = nil
	}
	return out, nil
}

func economicSafeScopeText(value string) bool {
	return validEconomicText(value, int(metering.MaxSchemaIDBytes)) && !strings.ContainsRune(value, '\x00')
}

func economicTimeFromUnixNanos(n int64) time.Time { return time.Unix(0, n).UTC() }

func economicTimeFromOptionalUnixNanos(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return economicTimeFromUnixNanos(n)
}

func economicUnixNanos(t time.Time) int64 { return t.UnixNano() }

func economicOptionalUnixNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return economicUnixNanos(t)
}
