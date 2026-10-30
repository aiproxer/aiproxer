package metering

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// ProviderDebitVersionV1 is the version of the typed, nonmonetary provider
	// request-debit contract. It is deliberately separate from the V2
	// observation-envelope version so the evidence shape can evolve without
	// changing the journal envelope.
	ProviderDebitVersionV1 uint32 = 1
	// ProviderDebitMappingRef identifies the canonical V2 projection produced
	// by ToObservation. It is a mapping identity, not a provider price/rate.
	ProviderDebitMappingRef = "provider_debit_v1"
)

var (
	ErrInvalidProviderDebit = errors.New("metering: invalid provider debit")
	ErrProviderDebitAbsent  = errors.New("metering: provider debit absent")
)

// ProviderDebitRef is an immutable reference to a provider-debit observation
// revision. The alias keeps supersession identity identical to the canonical
// observation journal and prevents provider-local IDs from becoming authority
// on their own.
type ProviderDebitRef = ObservationRef

// ProviderDebit is authoritative provider-reported, nonmonetary request
// economics. Quantity is a provider unit (normally a credit/allowance unit),
// never a currency amount. A debit is bound to exactly one request, billing
// call and B-leg, while retaining the provider account/pool/window context in
// which the provider reported it.
//
// Account-window snapshots use SubjectAccountWindow and are intentionally not
// representable by this type. In particular, no field permits a caller to
// subtract two percentage/remaining gauges and manufacture a debit.
type ProviderDebit struct {
	Version        uint32 `json:"version"`
	ID             string `json:"id"`
	SourceEventKey string `json:"source_event_key"`
	Revision       uint64 `json:"revision"`
	StreamID       string `json:"stream_id"`
	Sequence       uint64 `json:"sequence"`

	StoreID            string    `json:"store_id"`
	TenantID           string    `json:"tenant_id,omitempty"`
	ProviderAccountKey string    `json:"provider_account_key"`
	PoolID             string    `json:"pool_id"`
	WindowID           string    `json:"window_id"`
	ResetAt            time.Time `json:"reset_at"`

	RequestID         string `json:"request_id"`
	BillingCallID     string `json:"billing_call_id"`
	BLegID            string `json:"b_leg_id"`
	ALegID            string `json:"a_leg_id,omitempty"`
	CallID            string `json:"call_id,omitempty"`
	AttemptID         string `json:"attempt_id,omitempty"`
	AttemptSeq        uint64 `json:"attempt_seq,omitempty"`
	ProviderRequestID string `json:"provider_request_id,omitempty"`

	Component  ComponentKey `json:"component"`
	Quantity   *Decimal     `json:"quantity"`
	Quality    string       `json:"quality"`
	MethodRef  string       `json:"method_ref,omitempty"`
	MappingRef string       `json:"mapping_ref,omitempty"`

	Acquisition string    `json:"acquisition"`
	Authority   string    `json:"authority"`
	Semantics   string    `json:"semantics"`
	ObservedAt  time.Time `json:"observed_at"`
	ReceivedAt  time.Time `json:"received_at"`

	Supersedes []ProviderDebitRef  `json:"supersedes,omitempty"`
	Evidence   []SafeEvidenceField `json:"evidence,omitempty"`
}

// Validate accepts only an authoritative, complete provider request debit.
// Missing, estimated, gauge or monetary evidence is rejected at this typed
// boundary instead of being made payable by a later reducer.
func (d ProviderDebit) Validate() error {
	if d.Version != ProviderDebitVersionV1 {
		return fmt.Errorf("%w: version %d want %d", ErrInvalidProviderDebit, d.Version, ProviderDebitVersionV1)
	}
	for _, field := range []struct {
		name  string
		value string
		max   int
	}{
		{"provider debit id", d.ID, MaxObservationIDBytes},
		{"provider debit source event key", d.SourceEventKey, MaxSourceEventKeyBytes},
		{"provider debit stream id", d.StreamID, MaxStreamIDBytes},
		{"provider debit store_id", d.StoreID, MaxSchemaIDBytes},
		{"provider debit provider_account_key", d.ProviderAccountKey, MaxSchemaIDBytes},
		{"provider debit pool_id", d.PoolID, MaxSchemaIDBytes},
		{"provider debit window_id", d.WindowID, MaxSchemaIDBytes},
		{"provider debit request_id", d.RequestID, MaxSchemaIDBytes},
		{"provider debit billing_call_id", d.BillingCallID, MaxSchemaIDBytes},
		{"provider debit b_leg_id", d.BLegID, MaxSchemaIDBytes},
	} {
		if err := validateIdentityText(field.name, field.value, field.max); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidProviderDebit, err)
		}
	}
	for _, field := range []struct {
		name  string
		value string
		max   int
	}{
		{"provider debit tenant_id", d.TenantID, MaxSchemaIDBytes},
		{"provider debit a_leg_id", d.ALegID, MaxSchemaIDBytes},
		{"provider debit call_id", d.CallID, MaxSchemaIDBytes},
		{"provider debit attempt_id", d.AttemptID, MaxSchemaIDBytes},
		{"provider debit provider_request_id", d.ProviderRequestID, MaxSchemaIDBytes},
		{"provider debit method_ref", d.MethodRef, MaxMappingRefBytes},
		{"provider debit mapping_ref", d.MappingRef, MaxMappingRefBytes},
	} {
		if field.value == "" {
			continue
		}
		if err := validateIdentityText(field.name, field.value, field.max); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidProviderDebit, err)
		}
	}
	if d.Revision == 0 {
		return fmt.Errorf("%w: revision required", ErrInvalidProviderDebit)
	}
	if d.ResetAt.IsZero() {
		return fmt.Errorf("%w: reset_at required", ErrInvalidProviderDebit)
	}
	if d.ObservedAt.IsZero() || d.ReceivedAt.IsZero() {
		return fmt.Errorf("%w: observed_at and received_at required", ErrInvalidProviderDebit)
	}
	if err := d.Component.Validate(); err != nil {
		return fmt.Errorf("%w: component: %v", ErrInvalidProviderDebit, err)
	}
	if d.Component.Direction != DirectionNone {
		return fmt.Errorf("%w: component must be nondirectional", ErrInvalidProviderDebit)
	}
	if d.Component.Unit == UnitPercent {
		return fmt.Errorf("%w: percent is an account-window gauge unit", ErrInvalidProviderDebit)
	}
	if isProviderGaugeComponent(d.Component.Component) {
		return fmt.Errorf("%w: component %q is an account-window gauge", ErrInvalidProviderDebit, d.Component.Component)
	}
	if d.Quantity == nil {
		return fmt.Errorf("%w: quantity is absent", ErrInvalidProviderDebit)
	}
	quantity, err := d.Quantity.Normalize()
	if err != nil {
		return fmt.Errorf("%w: quantity: %v", ErrInvalidProviderDebit, err)
	}
	if strings.HasPrefix(quantity.Coefficient, "-") && d.Semantics != SemanticsCorrection && d.Semantics != SemanticsReplacement {
		return fmt.Errorf("%w: negative quantity requires correction/replacement semantics", ErrInvalidProviderDebit)
	}
	if strings.HasPrefix(quantity.Coefficient, "-") && d.Semantics == SemanticsReplacement {
		return fmt.Errorf("%w: replacement quantity cannot be negative", ErrInvalidProviderDebit)
	}
	if d.Quality != QualityObserved {
		return fmt.Errorf("%w: quality %q is not authoritative", ErrInvalidProviderDebit, d.Quality)
	}
	if d.Authority != AuthorityObservedClaim {
		return fmt.Errorf("%w: authority %q is not an observed provider claim", ErrInvalidProviderDebit, d.Authority)
	}
	if d.Acquisition != AcquisitionProviderCountAPI && d.Acquisition != AcquisitionProviderResponse && d.Acquisition != AcquisitionProviderHeader && d.Acquisition != AcquisitionProviderFinalizer {
		return fmt.Errorf("%w: acquisition %q is not a provider debit channel", ErrInvalidProviderDebit, d.Acquisition)
	}
	if err := validateProvenance(OriginProvider, d.Acquisition, d.Authority); err != nil {
		return fmt.Errorf("%w: provenance: %v", ErrInvalidProviderDebit, err)
	}
	if !isProviderDebitSemantics(d.Semantics) {
		return fmt.Errorf("%w: semantics %q is not a debit operation", ErrInvalidProviderDebit, d.Semantics)
	}
	if (d.Semantics == SemanticsCorrection || d.Semantics == SemanticsReplacement) && len(d.Supersedes) == 0 {
		return fmt.Errorf("%w: %s requires supersedes", ErrInvalidProviderDebit, d.Semantics)
	}
	if d.Semantics != SemanticsCorrection && d.Semantics != SemanticsReplacement && len(d.Supersedes) != 0 {
		return fmt.Errorf("%w: supersedes requires correction/replacement semantics", ErrInvalidProviderDebit)
	}
	if len(d.Supersedes) > MaxObservationSupersedes || len(d.Evidence) > MaxObservationEvidence {
		return fmt.Errorf("%w: evidence bound exceeded", ErrInvalidProviderDebit)
	}
	seenRefs := make(map[string]struct{}, len(d.Supersedes))
	for i, ref := range d.Supersedes {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: supersedes[%d]: %v", ErrInvalidProviderDebit, i, err)
		}
		if ref.StoreID != d.StoreID {
			return fmt.Errorf("%w: supersession store mismatch", ErrInvalidProviderDebit)
		}
		if ref.ObservationID == d.ID && ref.Revision == d.Revision {
			return fmt.Errorf("%w: supersession self-reference", ErrInvalidProviderDebit)
		}
		key := fmt.Sprintf("%s\x00%d\x00%s", ref.ObservationID, ref.Revision, ref.PayloadHash)
		if _, ok := seenRefs[key]; ok {
			return fmt.Errorf("%w: duplicate supersession reference", ErrInvalidProviderDebit)
		}
		seenRefs[key] = struct{}{}
	}
	for i, field := range d.Evidence {
		if err := field.Validate(); err != nil {
			return fmt.Errorf("%w: evidence[%d]: %v", ErrInvalidProviderDebit, i, err)
		}
	}
	if payload, err := json.Marshal(providerDebitWire(d)); err != nil {
		return fmt.Errorf("%w: serialization: %v", ErrInvalidProviderDebit, err)
	} else if len(payload) > MaxSafeEvidenceBytes {
		return fmt.Errorf("%w: serialized debit exceeds %d bytes", ErrInvalidProviderDebit, MaxSafeEvidenceBytes)
	}
	return nil
}

func isProviderDebitSemantics(semantics string) bool {
	switch semantics {
	case SemanticsDelta, SemanticsCumulative, SemanticsCorrection, SemanticsReplacement:
		return true
	default:
		return false
	}
}

func isProviderGaugeComponent(component string) bool {
	name := strings.ToLower(strings.TrimSpace(component))
	return strings.Contains(name, "percent") || strings.Contains(name, "remaining") ||
		strings.Contains(name, "utilization") || strings.Contains(name, "limit") || strings.Contains(name, "gauge")
}

// Clone returns a deep copy suitable for durable handoff.
func (d ProviderDebit) Clone() ProviderDebit {
	out := d
	out.Component = d.Component.Clone()
	if d.Quantity != nil {
		quantity := *d.Quantity
		out.Quantity = &quantity
	}
	out.Supersedes = append([]ProviderDebitRef(nil), d.Supersedes...)
	out.Evidence = append([]SafeEvidenceField(nil), d.Evidence...)
	return out
}

// Canonical returns a validated copy with exact quantity/component and
// semantically unordered supersession/evidence sets normalized.
func (d ProviderDebit) Canonical() (ProviderDebit, error) {
	if err := d.Validate(); err != nil {
		return ProviderDebit{}, err
	}
	out := d.Clone()
	component, err := out.Component.Normalize()
	if err != nil {
		return ProviderDebit{}, fmt.Errorf("%w: component: %v", ErrInvalidProviderDebit, err)
	}
	out.Component = component
	quantity, err := out.Quantity.Normalize()
	if err != nil {
		return ProviderDebit{}, fmt.Errorf("%w: quantity: %v", ErrInvalidProviderDebit, err)
	}
	out.Quantity = &quantity
	out.ResetAt = out.ResetAt.UTC()
	out.ObservedAt = out.ObservedAt.UTC()
	out.ReceivedAt = out.ReceivedAt.UTC()
	if out.MappingRef == "" {
		out.MappingRef = ProviderDebitMappingRef
	}
	for i := range out.Evidence {
		if out.Evidence[i].Path == "" {
			out.Evidence[i].Path = out.Evidence[i].Name
		}
		out.Evidence[i].Name = ""
		if out.Evidence[i].Lexeme == "" {
			out.Evidence[i].Lexeme = out.Evidence[i].Value
		}
		out.Evidence[i].Value = ""
	}
	slices.SortFunc(out.Supersedes, func(a, b ProviderDebitRef) int {
		return strings.Compare(providerDebitRefKey(a), providerDebitRefKey(b))
	})
	slices.SortFunc(out.Evidence, func(a, b SafeEvidenceField) int {
		return strings.Compare(evidenceKey(a), evidenceKey(b))
	})
	return out, nil
}

func providerDebitRefKey(ref ProviderDebitRef) string {
	return ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10) + "\x00" + ref.PayloadHash
}

type providerDebitWire ProviderDebit

// CanonicalJSON is the deterministic wire form for typed debit evidence.
func (d ProviderDebit) CanonicalJSON() ([]byte, error) {
	normalized, err := d.Canonical()
	if err != nil {
		return nil, err
	}
	return json.Marshal(providerDebitWire(normalized))
}

func (d ProviderDebit) MarshalJSON() ([]byte, error) { return d.CanonicalJSON() }

// ReplayFingerprint excludes receipt arrival time while retaining source
// revision, subject/account-window binding and the complete quantity payload.
func (d ProviderDebit) ReplayFingerprint() string {
	normalized, err := d.Canonical()
	if err != nil {
		return ""
	}
	normalized.ReceivedAt = normalized.ObservedAt
	payload, err := json.Marshal(providerDebitWire(normalized))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Fingerprint returns the hash of the canonical typed debit, including receipt
// metadata. Durable supersession references use ReplayFingerprint instead.
func (d ProviderDebit) Fingerprint() string {
	payload, err := d.CanonicalJSON()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// IdentityKey returns the source-event identity without the quantity payload.
// A changed payload under the same key is therefore a conflict, never a new
// debit.
func (d ProviderDebit) IdentityKey() string {
	observation, err := d.ToObservation()
	if err != nil {
		return ""
	}
	return observation.IdentityKey()
}

// ToObservation projects the typed debit onto the canonical V2 journal. The
// projection has one measure and no ReportedCharge, so it cannot become
// provider money without an explicit later valuation/conversion operation.
func (d ProviderDebit) ToObservation() (Observation, error) {
	normalized, err := d.Canonical()
	if err != nil {
		return Observation{}, err
	}
	quantity := *normalized.Quantity
	observation := Observation{
		Version:        ObservationVersionV2,
		ID:             normalized.ID,
		SourceEventKey: normalized.SourceEventKey,
		Revision:       normalized.Revision,
		StreamID:       normalized.StreamID,
		Sequence:       normalized.Sequence,
		Origin:         OriginProvider,
		Acquisition:    normalized.Acquisition,
		Authority:      normalized.Authority,
		Perspective:    PerspectiveOperator,
		Boundary:       BoundaryBackendEgress,
		Lifecycle:      LifecycleBackendAttempt,
		Subject: SubjectRef{
			Kind:               SubjectProviderDebit,
			StoreID:            normalized.StoreID,
			TenantID:           normalized.TenantID,
			ALegID:             normalized.ALegID,
			RequestID:          normalized.RequestID,
			BillingCallID:      normalized.BillingCallID,
			CallID:             normalized.CallID,
			BLegID:             normalized.BLegID,
			AttemptID:          normalized.AttemptID,
			AttemptSeq:         normalized.AttemptSeq,
			ProviderAccountKey: normalized.ProviderAccountKey,
			ProviderRequestID:  normalized.ProviderRequestID,
			PoolID:             normalized.PoolID,
			WindowID:           normalized.WindowID,
			ResetAt:            normalized.ResetAt,
		},
		Correlation: CorrelationV2{
			StoreID:            normalized.StoreID,
			TenantID:           normalized.TenantID,
			RequestID:          normalized.RequestID,
			CallID:             normalized.CallID,
			BillingCallID:      normalized.BillingCallID,
			ALegID:             normalized.ALegID,
			BLegID:             normalized.BLegID,
			AttemptID:          normalized.AttemptID,
			AttemptSeq:         normalized.AttemptSeq,
			ProviderAccountKey: normalized.ProviderAccountKey,
			ProviderRequestID:  normalized.ProviderRequestID,
		},
		Semantics:  normalized.Semantics,
		ObservedAt: normalized.ObservedAt,
		ReceivedAt: normalized.ReceivedAt,
		Measures:   []Measure{{Key: normalized.Component, Value: &quantity, Quality: normalized.Quality, MethodRef: normalized.MethodRef}},
		Supersedes: append([]ObservationRef(nil), normalized.Supersedes...),
		Evidence:   append([]SafeEvidenceField(nil), normalized.Evidence...),
	}
	observation.MappingRef = normalized.MappingRef
	if observation.MappingRef == "" {
		observation.MappingRef = ProviderDebitMappingRef
	}
	return observation.Canonical()
}

// Observation is a convenience alias for ToObservation at adapter seams.
func (d ProviderDebit) Observation() (Observation, error) { return d.ToObservation() }

// Ref returns the canonical immutable observation reference used by a later
// correction/replacement.
func (d ProviderDebit) Ref() (ProviderDebitRef, error) {
	observation, err := d.ToObservation()
	if err != nil {
		return ProviderDebitRef{}, err
	}
	return observation.Ref(d.StoreID)
}

// ProviderDebitFromObservation accepts only the dedicated debit subject. An
// account-window observation, even one with a credit-looking measure, cannot
// be converted by differencing or request association.
func ProviderDebitFromObservation(observation Observation) (ProviderDebit, error) {
	if observation.Subject.Kind != SubjectProviderDebit {
		return ProviderDebit{}, fmt.Errorf("%w: subject kind %q is not provider_debit", ErrInvalidProviderDebit, observation.Subject.Kind)
	}
	if err := observation.Validate(); err != nil {
		return ProviderDebit{}, fmt.Errorf("%w: observation: %w", ErrInvalidProviderDebit, err)
	}
	if len(observation.Measures) != 1 || len(observation.Charges) != 0 {
		return ProviderDebit{}, fmt.Errorf("%w: observation must contain exactly one measure and no charges", ErrInvalidProviderDebit)
	}
	measure := observation.Measures[0]
	if measure.Value == nil {
		return ProviderDebit{}, fmt.Errorf("%w: observation measure is absent", ErrInvalidProviderDebit)
	}
	tenantID := observation.Subject.TenantID
	if tenantID == "" {
		tenantID = observation.Correlation.TenantID
	}
	return (ProviderDebit{
		Version:            ProviderDebitVersionV1,
		ID:                 observation.ID,
		SourceEventKey:     observation.SourceEventKey,
		Revision:           observation.Revision,
		StreamID:           observation.StreamID,
		Sequence:           observation.Sequence,
		StoreID:            observation.Subject.StoreID,
		TenantID:           tenantID,
		ProviderAccountKey: observation.Subject.ProviderAccountKey,
		PoolID:             observation.Subject.PoolID,
		WindowID:           observation.Subject.WindowID,
		ResetAt:            observation.Subject.ResetAt,
		RequestID:          observation.Subject.RequestID,
		BillingCallID:      observation.Subject.BillingCallID,
		BLegID:             observation.Subject.BLegID,
		ALegID:             observation.Subject.ALegID,
		CallID:             observation.Subject.CallID,
		AttemptID:          observation.Subject.AttemptID,
		AttemptSeq:         observation.Subject.AttemptSeq,
		ProviderRequestID:  observation.Subject.ProviderRequestID,
		Component:          measure.Key,
		Quantity:           measure.Value,
		Quality:            measure.Quality,
		MethodRef:          measure.MethodRef,
		MappingRef:         observation.MappingRef,
		Acquisition:        observation.Acquisition,
		Authority:          observation.Authority,
		Semantics:          observation.Semantics,
		ObservedAt:         observation.ObservedAt,
		ReceivedAt:         observation.ReceivedAt,
		Supersedes:         append([]ProviderDebitRef(nil), observation.Supersedes...),
		Evidence:           append([]SafeEvidenceField(nil), observation.Evidence...),
	}).Canonical()
}
