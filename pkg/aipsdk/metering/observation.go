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
	"unicode"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

const (
	// ObservationVersionV2 is the canonical evidence envelope version.
	ObservationVersionV2      uint32 = 2
	MaxObservationIDBytes            = 512
	MaxSourceEventKeyBytes           = 1024
	MaxStreamIDBytes                 = 512
	MaxMappingRefBytes               = 512
	MaxObservationMeasures           = 128
	MaxObservationCharges            = 128
	MaxObservationSupersedes         = 128
	MaxObservationEvidence           = 256
	MaxSafeEvidenceBytes             = 64 * 1024
	MaxSafeEvidenceFieldBytes        = 1024
)

var (
	ErrInvalidObservation  = errors.New("metering: invalid observation")
	ErrInvalidSubject      = errors.New("metering: invalid subject")
	ErrInvalidCoverage     = errors.New("metering: invalid charge coverage")
	ErrInvalidRevision     = errors.New("metering: invalid observation revision")
	ErrV1Projection        = errors.New("metering: V1 projection is non-authoritative")
	knownAcquisitionOrigin = map[string]string{
		AcquisitionLocalTokenizer:    OriginLocal,
		AcquisitionLocalTransport:    OriginLocal,
		AcquisitionLocalEstimator:    OriginLocal,
		AcquisitionProviderCountAPI:  OriginProvider,
		AcquisitionProviderResponse:  OriginProvider,
		AcquisitionProviderHeader:    OriginProvider,
		AcquisitionProviderFinalizer: OriginProvider,
		AcquisitionStatementImporter: OriginStatement,
	}
)

// Origins identify the independent evidence plane. A derived valuation must
// reference its inputs instead of claiming a new observation origin.
const (
	OriginLocal     = "local"
	OriginProvider  = "provider"
	OriginStatement = "statement"
)

// Acquisition identifies how an observation was acquired. Unknown namespaced
// values may be retained by additive decoders, while strict construction uses
// one of these known channels.
const (
	AcquisitionLocalTokenizer    = "local_tokenizer"
	AcquisitionLocalTransport    = "local_transport_measurement"
	AcquisitionLocalEstimator    = "local_estimator"
	AcquisitionProviderCountAPI  = "provider_count_api"
	AcquisitionProviderResponse  = "provider_response"
	AcquisitionProviderHeader    = "provider_header"
	AcquisitionProviderFinalizer = "provider_finalizer"
	AcquisitionStatementImporter = "statement_importer"
	// Short aliases retain the vocabulary used by adapter contracts.
	AcquisitionProviderCount    = AcquisitionProviderCountAPI
	AcquisitionLocalMeasurement = AcquisitionLocalTransport
)

const (
	AuthorityObservedClaim     = "observed_claim"
	AuthorityEstimatedClaim    = "estimated"
	AuthorityVerifiedStatement = "verified_statement"
	AuthorityUnavailableClaim  = "unavailable"
)

const (
	SemanticsDelta       = "delta"
	SemanticsCumulative  = "cumulative"
	SemanticsGauge       = "gauge"
	SemanticsReplacement = "replacement"
	SemanticsCorrection  = "correction"
)

// Measure quality describes certainty/presence independently of origin.
const (
	QualityObserved      = "observed"
	QualityEstimated     = "estimated"
	QualityUnavailable   = "unavailable"
	QualityNotApplicable = "not_applicable"
	QualityUnknown       = "unknown"
)

// SubjectKind is the tagged economic subject union. It prevents an
// account-window gauge or resource interval from being mistaken for a B-leg
// request meter.
type SubjectKind string

const (
	SubjectALeg           SubjectKind = "a_leg"
	SubjectRequest        SubjectKind = "logical_request"
	SubjectBillingCall    SubjectKind = "billing_call"
	SubjectBLeg           SubjectKind = "b_leg"
	SubjectSubmission     SubjectKind = "submission"
	SubjectProviderCharge SubjectKind = "provider_charge"
	// SubjectProviderDebit is a nonmonetary, provider-reported request debit.
	// It carries the provider account/pool/window identity and one concrete
	// request/B-leg owner; it is intentionally distinct from both
	// SubjectAccountWindow gauges and SubjectProviderCharge money.
	SubjectProviderDebit SubjectKind = "provider_debit"
	SubjectResource      SubjectKind = "resource_interval"
	SubjectAccountWindow SubjectKind = "account_window"
	SubjectStatementLine SubjectKind = "statement_line"
	// Common spelling aliases are intentionally the same tagged values.
	SubjectAleg              = SubjectALeg
	SubjectLogicalRequest    = SubjectRequest
	SubjectLogicalCall       = SubjectRequest
	SubjectCall              = SubjectBillingCall
	SubjectBleg              = SubjectBLeg
	SubjectBackendAttempt    = SubjectBLeg
	SubjectResourceInterval  = SubjectResource
	SubjectAccountPeriod     = SubjectAccountWindow
	SubjectTrustedSubmission = SubjectSubmission
)

func (k SubjectKind) IsKnown() bool {
	switch k {
	case SubjectALeg, SubjectRequest, SubjectBillingCall, SubjectBLeg, SubjectSubmission, SubjectProviderCharge, SubjectProviderDebit, SubjectResource, SubjectAccountWindow, SubjectStatementLine:
		return true
	default:
		return false
	}
}

// SubjectRef is a tagged union with trusted store scope and optional ancestry.
// Fields from a foreign subject kind are rejected by Validate.
type SubjectRef struct {
	Kind               SubjectKind `json:"kind"`
	StoreID            string      `json:"store_id"`
	TenantID           string      `json:"tenant_id,omitempty"`
	AccountID          string      `json:"account_id,omitempty"`
	ALegID             string      `json:"a_leg_id,omitempty"`
	RequestID          string      `json:"request_id,omitempty"`
	BillingCallID      string      `json:"billing_call_id,omitempty"`
	CallID             string      `json:"call_id,omitempty"`
	BLegID             string      `json:"b_leg_id,omitempty"`
	AttemptID          string      `json:"attempt_id,omitempty"`
	AttemptSeq         uint64      `json:"attempt_seq,omitempty"`
	SubmissionID       string      `json:"submission_id,omitempty"`
	ProviderAccountKey string      `json:"provider_account_key,omitempty"`
	ProviderRequestID  string      `json:"provider_request_id,omitempty"`
	ProviderChargeID   string      `json:"provider_charge_id,omitempty"`
	ResourceID         string      `json:"resource_id,omitempty"`
	PeriodID           string      `json:"period_id,omitempty"`
	PoolID             string      `json:"pool_id,omitempty"`
	WindowID           string      `json:"window_id,omitempty"`
	StatementID        string      `json:"statement_id,omitempty"`
	StatementLineID    string      `json:"statement_line_id,omitempty"`
	ResetAt            time.Time   `json:"reset_at,omitzero"`
	StartAt            time.Time   `json:"start_at,omitzero"`
	EndAt              time.Time   `json:"end_at,omitzero"`
}

// Validate enforces the subject union and safe identity bounds.
func (s SubjectRef) Validate() error {
	if !s.Kind.IsKnown() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidSubject, s.Kind)
	}
	if s.AttemptSeq != 0 && s.BLegID == "" {
		return fmt.Errorf("%w: attempt_seq requires BLegID ownership", ErrInvalidSubject)
	}
	if err := validateIdentityText("subject store_id", s.StoreID, MaxSchemaIDBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSubject, err)
	}
	for name, value := range map[string]string{
		"subject tenant_id": s.TenantID, "subject account_id": s.AccountID,
		"subject a_leg_id": s.ALegID, "subject request_id": s.RequestID, "subject billing_call_id": s.BillingCallID,
		"subject call_id": s.CallID, "subject b_leg_id": s.BLegID,
		"subject attempt_id": s.AttemptID, "subject submission_id": s.SubmissionID,
		"subject provider_account_key": s.ProviderAccountKey, "subject provider_request_id": s.ProviderRequestID,
		"subject provider_charge_id": s.ProviderChargeID, "subject resource_id": s.ResourceID,
		"subject period_id": s.PeriodID, "subject pool_id": s.PoolID,
		"subject window_id": s.WindowID, "subject statement_id": s.StatementID,
		"subject statement_line_id": s.StatementLineID,
	} {
		if value != "" {
			if err := validateIdentityText(name, value, MaxSchemaIDBytes); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidSubject, err)
			}
		}
	}
	switch s.Kind {
	case SubjectALeg:
		if s.ALegID == "" {
			return fmt.Errorf("%w: a_leg_id required", ErrInvalidSubject)
		}
		if s.BillingCallID != "" || s.CallID != "" || s.BLegID != "" || s.AttemptID != "" || s.SubmissionID != "" || s.ProviderChargeID != "" || s.ResourceID != "" || s.StatementID != "" {
			return fmt.Errorf("%w: a-leg carries foreign lineage", ErrInvalidSubject)
		}
	case SubjectRequest:
		if s.RequestID == "" {
			return fmt.Errorf("%w: request_id required", ErrInvalidSubject)
		}
		if s.BLegID != "" || s.AttemptID != "" || s.ProviderChargeID != "" || s.ResourceID != "" || s.StatementID != "" {
			return fmt.Errorf("%w: request carries foreign lineage", ErrInvalidSubject)
		}
	case SubjectBillingCall:
		if s.BillingCallID == "" {
			return fmt.Errorf("%w: billing_call_id required", ErrInvalidSubject)
		}
		if s.BLegID != "" || s.AttemptID != "" || s.ProviderChargeID != "" || s.ResourceID != "" || s.StatementID != "" {
			return fmt.Errorf("%w: billing call carries foreign lineage", ErrInvalidSubject)
		}
	case SubjectBLeg:
		if s.BLegID == "" {
			return fmt.Errorf("%w: b_leg_id required", ErrInvalidSubject)
		}
		if s.ResourceID != "" || s.PoolID != "" || s.WindowID != "" || s.StatementID != "" || s.StatementLineID != "" {
			return fmt.Errorf("%w: b-leg carries foreign subject fields", ErrInvalidSubject)
		}
	case SubjectSubmission:
		if s.SubmissionID == "" {
			return fmt.Errorf("%w: submission_id required", ErrInvalidSubject)
		}
		if s.BLegID != "" || s.AttemptID != "" || s.ProviderChargeID != "" || s.ResourceID != "" || s.StatementID != "" {
			return fmt.Errorf("%w: submission carries foreign lineage", ErrInvalidSubject)
		}
	case SubjectProviderCharge:
		if s.ProviderChargeID == "" || s.ProviderAccountKey == "" || s.BLegID == "" {
			return fmt.Errorf("%w: provider_charge_id, provider_account_key and b_leg_id required", ErrInvalidSubject)
		}
		if s.ResourceID != "" || s.PoolID != "" || s.WindowID != "" || s.StatementID != "" || s.StatementLineID != "" {
			return fmt.Errorf("%w: provider charge carries foreign subject fields", ErrInvalidSubject)
		}
	case SubjectProviderDebit:
		if s.ProviderAccountKey == "" || s.PoolID == "" || s.WindowID == "" || s.RequestID == "" || s.BillingCallID == "" || s.BLegID == "" {
			return fmt.Errorf("%w: provider_account_key, pool_id, window_id, request_id, billing_call_id and b_leg_id required", ErrInvalidSubject)
		}
		if s.ResetAt.IsZero() {
			return fmt.Errorf("%w: provider debit reset_at required", ErrInvalidSubject)
		}
		if s.SubmissionID != "" || s.ProviderChargeID != "" || s.ResourceID != "" || s.StatementID != "" || s.StatementLineID != "" {
			return fmt.Errorf("%w: provider debit carries foreign subject fields", ErrInvalidSubject)
		}
	case SubjectResource:
		if s.ResourceID == "" || s.PeriodID == "" {
			return fmt.Errorf("%w: resource_id and period_id required", ErrInvalidSubject)
		}
		if s.ALegID != "" || s.BillingCallID != "" || s.CallID != "" || s.BLegID != "" || s.AttemptID != "" || s.SubmissionID != "" || s.ProviderChargeID != "" || s.PoolID != "" || s.WindowID != "" || s.StatementID != "" || s.StatementLineID != "" {
			return fmt.Errorf("%w: resource subject carries request/account-window lineage", ErrInvalidSubject)
		}
		if !s.StartAt.IsZero() && !s.EndAt.IsZero() && s.EndAt.Before(s.StartAt) {
			return fmt.Errorf("%w: resource interval end precedes start", ErrInvalidSubject)
		}
	case SubjectAccountWindow:
		if s.ProviderAccountKey == "" || s.PoolID == "" || s.WindowID == "" {
			return fmt.Errorf("%w: provider_account_key, pool_id and window_id required", ErrInvalidSubject)
		}
		if s.ResetAt.IsZero() {
			return fmt.Errorf("%w: account-window reset_at required", ErrInvalidSubject)
		}
		if s.ALegID != "" || s.BillingCallID != "" || s.CallID != "" || s.BLegID != "" || s.AttemptID != "" || s.SubmissionID != "" || s.ProviderChargeID != "" || s.ResourceID != "" || s.StatementID != "" || s.StatementLineID != "" {
			return fmt.Errorf("%w: account window carries request/resource lineage", ErrInvalidSubject)
		}
	case SubjectStatementLine:
		if s.ProviderAccountKey == "" || s.StatementID == "" || s.StatementLineID == "" {
			return fmt.Errorf("%w: provider_account_key, statement_id and statement_line_id required", ErrInvalidSubject)
		}
		if s.ALegID != "" || s.BillingCallID != "" || s.CallID != "" || s.BLegID != "" || s.AttemptID != "" || s.SubmissionID != "" || s.ProviderChargeID != "" || s.ResourceID != "" || s.PoolID != "" || s.WindowID != "" {
			return fmt.Errorf("%w: statement line carries foreign lineage", ErrInvalidSubject)
		}
	}
	return nil
}

// Clone returns a value copy. SubjectRef contains no mutable fields.
func (s SubjectRef) Clone() SubjectRef { return s }

// CorrelationV2 carries trusted lineage and store scope alongside the tagged
// subject. Runtime attribution fields are never sourced from an untrusted
// provider header.
type CorrelationV2 struct {
	StoreID            string `json:"store_id"`
	TenantID           string `json:"tenant_id,omitempty"`
	RequestID          string `json:"request_id,omitempty"`
	CallID             string `json:"call_id,omitempty"`
	BillingCallID      string `json:"billing_call_id,omitempty"`
	ALegID             string `json:"a_leg_id,omitempty"`
	BLegID             string `json:"b_leg_id,omitempty"`
	AttemptID          string `json:"attempt_id,omitempty"`
	AttemptSeq         uint64 `json:"attempt_seq,omitempty"`
	SubmissionID       string `json:"submission_id,omitempty"`
	ProviderAccountKey string `json:"provider_account_key,omitempty"`
	ProviderRequestID  string `json:"provider_request_id,omitempty"`
	ProviderChargeID   string `json:"provider_charge_id,omitempty"`
	ParentWorkID       string `json:"parent_work_id,omitempty"`
	ResourceID         string `json:"resource_id,omitempty"`
	PeriodID           string `json:"period_id,omitempty"`
}

func (c CorrelationV2) Validate() error {
	if err := validateIdentityText("correlation store_id", c.StoreID, MaxSchemaIDBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
	}
	for name, value := range map[string]string{
		"correlation tenant_id": c.TenantID, "correlation request_id": c.RequestID,
		"correlation call_id": c.CallID, "correlation billing_call_id": c.BillingCallID,
		"correlation a_leg_id": c.ALegID, "correlation b_leg_id": c.BLegID,
		"correlation attempt_id": c.AttemptID, "correlation submission_id": c.SubmissionID,
		"correlation provider_account_key": c.ProviderAccountKey, "correlation provider_request_id": c.ProviderRequestID,
		"correlation provider_charge_id": c.ProviderChargeID, "correlation parent_work_id": c.ParentWorkID,
		"correlation resource_id": c.ResourceID, "correlation period_id": c.PeriodID,
	} {
		if value != "" {
			if err := validateIdentityText(name, value, MaxSchemaIDBytes); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
			}
		}
	}
	if c.AttemptSeq != 0 && c.BLegID == "" {
		return fmt.Errorf("%w: AttemptSeq requires BLegID ownership", ErrInvalidObservation)
	}
	if c.AttemptID != "" && c.BLegID == "" {
		return fmt.Errorf("%w: AttemptID requires BLegID ownership", ErrInvalidObservation)
	}
	return nil
}

// ObservationRef identifies an immutable observation revision by store and
// payload hash. A bare provider-local ID is deliberately insufficient.
type ObservationRef struct {
	StoreID       string `json:"store_id"`
	ObservationID string `json:"observation_id"`
	Revision      uint64 `json:"revision"`
	PayloadHash   string `json:"payload_hash"`
}

func (r ObservationRef) Validate() error {
	for name, value := range map[string]string{"observation ref store_id": r.StoreID, "observation ref id": r.ObservationID, "observation ref payload_hash": r.PayloadHash} {
		if err := validateIdentityText(name, value, MaxSourceEventKeyBytes); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
		}
	}
	if r.Revision == 0 {
		return fmt.Errorf("%w: observation ref revision required", ErrInvalidObservation)
	}
	return nil
}

func (r ObservationRef) Equal(other ObservationRef) bool {
	return r.StoreID == other.StoreID && r.ObservationID == other.ObservationID && r.Revision == other.Revision && r.PayloadHash == other.PayloadHash
}

// SafeEvidenceField retains one bounded allowlisted lexeme/path without raw
// provider bodies. Lexeme is the preferred exact value; Value is a backwards-
// compatible alias for adapters that use that name.
type SafeEvidenceField struct {
	Path        string `json:"path,omitempty"`
	Name        string `json:"name,omitempty"`
	Lexeme      string `json:"lexeme,omitempty"`
	Value       string `json:"value,omitempty"`
	Present     bool   `json:"present"`
	Null        bool   `json:"null,omitempty"`
	Acquisition string `json:"acquisition"`
	// Sanitizer is the optional identity/version marker stamped by the
	// normalizer that produced sanitized evidence (name/version). It
	// participates in the deterministic content hash when present.
	Sanitizer string `json:"sanitizer,omitempty"`
}

// UnmarshalJSON rejects malformed transport bytes before encoding/json can
// replace them with U+FFFD and make a distinct evidence lexeme appear valid.
func (e *SafeEvidenceField) UnmarshalJSON(data []byte) error {
	if e == nil {
		return fmt.Errorf("safe evidence: nil destination")
	}
	type wire SafeEvidenceField
	var decoded wire
	if err := decodeV2JSON(data, "safe evidence", &decoded); err != nil {
		return err
	}
	*e = SafeEvidenceField(decoded)
	return nil
}

func (e SafeEvidenceField) Validate() error {
	path := e.Path
	if path == "" {
		path = e.Name
	}
	if err := validateIdentityText("safe evidence path", path, MaxSourceEventKeyBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
	}
	if err := validateSafeEvidenceLocation(path); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
	}
	if e.Path != "" && e.Name != "" && e.Path != e.Name {
		return fmt.Errorf("%w: safe evidence path/name mismatch", ErrInvalidObservation)
	}
	if e.Lexeme != "" && e.Value != "" && e.Lexeme != e.Value {
		return fmt.Errorf("%w: safe evidence lexeme/value mismatch", ErrInvalidObservation)
	}
	for name, value := range map[string]string{"safe evidence lexeme": e.Lexeme, "safe evidence value": e.Value} {
		if value != "" {
			if err := validateSafeEvidenceLexemeForLocation(path, name, value); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
			}
		}
	}
	if err := validateSanitizerMarker("safe evidence sanitizer", e.Sanitizer); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
	}
	if !e.Present && (e.Lexeme != "" || e.Value != "") {
		return fmt.Errorf("%w: absent safe evidence cannot carry a value", ErrInvalidObservation)
	}
	if e.Null && e.Present {
		return fmt.Errorf("%w: null safe evidence cannot be present", ErrInvalidObservation)
	}
	if err := validateIdentityText("safe evidence acquisition", e.Acquisition, MaxSourceEventKeyBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
	}
	if !isKnownSafeEvidenceAcquisition(e.Acquisition) {
		return fmt.Errorf("%w: unknown safe evidence acquisition %q", ErrInvalidObservation, e.Acquisition)
	}
	return nil
}

func (e SafeEvidenceField) canonicalLexeme() string {
	if e.Lexeme != "" {
		return e.Lexeme
	}
	return e.Value
}

func validateBoundedText(field, value string, maxBytes int) error {
	if len(value) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, maxBytes)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || !unicode.IsPrint(r) {
			return fmt.Errorf("%s contains unsafe characters", field)
		}
	}
	return nil
}

func validateCurrencyCode(value string) error {
	if value == "" || len(value) > 16 {
		return fmt.Errorf("charge currency must be a non-empty code of at most 16 bytes")
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 'A' || value[i] > 'Z' {
			return fmt.Errorf("charge currency must use uppercase ASCII letters")
		}
	}
	return nil
}

// Measure is one independent component measure. A nil Value is absent and is
// never interpreted as an observed zero.
type Measure struct {
	Key       ComponentKey `json:"key"`
	Value     *Decimal     `json:"value,omitempty"`
	Quality   string       `json:"quality"`
	MethodRef string       `json:"method_ref,omitempty"`
	Reason    string       `json:"reason,omitempty"`
}

func (m Measure) Validate() error {
	if err := m.Key.Validate(); err != nil {
		return fmt.Errorf("%w: measure key: %v", ErrInvalidObservation, err)
	}
	if !isKnownQuality(m.Quality) {
		return fmt.Errorf("%w: unknown measure quality %q", ErrInvalidObservation, m.Quality)
	}
	if m.Value == nil && (m.Quality == QualityObserved || m.Quality == QualityEstimated) {
		return fmt.Errorf("%w: %s measure requires a value", ErrInvalidObservation, m.Quality)
	}
	if m.Value != nil {
		n, err := m.Value.Normalize()
		if err != nil {
			return fmt.Errorf("%w: measure value: %v", ErrInvalidObservation, err)
		}
		// Token and count meters are discrete by contract. Native media
		// quantities such as seconds, bytes and products may remain fractional
		// when their versioned schema permits it.
		if (m.Key.Unit == UnitToken || m.Key.Unit == UnitCount) && n.Scale != 0 {
			return fmt.Errorf("%w: %s measure must be an exact integer", ErrInvalidObservation, m.Key.Unit)
		}
		if m.Quality == QualityUnavailable || m.Quality == QualityNotApplicable {
			return fmt.Errorf("%w: unavailable/not-applicable measure cannot carry a value", ErrInvalidObservation)
		}
	}
	for name, value := range map[string]string{"method_ref": m.MethodRef, "reason": m.Reason} {
		if value != "" {
			if err := validateIdentityText(name, value, MaxMappingRefBytes); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
			}
		}
	}
	return nil
}

func (m Measure) Clone() Measure {
	out := m
	out.Key = m.Key.Clone()
	if m.Value != nil {
		v := *m.Value
		out.Value = &v
	}
	return out
}

func isKnownQuality(q string) bool {
	switch q {
	case QualityObserved, QualityEstimated, QualityUnavailable, QualityNotApplicable, QualityUnknown:
		return true
	default:
		return false
	}
}

// ChargeKind classifies provider monetary evidence without implying a
// component decomposition.
type ChargeKind string

const (
	ChargeKindComponent  ChargeKind = "component"
	ChargeKindAggregate  ChargeKind = "aggregate"
	ChargeKindSurcharge  ChargeKind = "surcharge"
	ChargeKindTax        ChargeKind = "tax"
	ChargeKindAdjustment ChargeKind = "adjustment"
	ChargeKindCredit     ChargeKind = "credit"
)

func (k ChargeKind) IsKnown() bool {
	switch k {
	case ChargeKindComponent, ChargeKindAggregate, ChargeKindSurcharge, ChargeKindTax, ChargeKindAdjustment, ChargeKindCredit:
		return true
	default:
		return false
	}
}

// CoverageRelation describes whether a referenced child is already included
// in a parent charge or remains separately payable.
type CoverageRelation string

const (
	CoverageInclusive         CoverageRelation = "inclusive"
	CoverageAdditive          CoverageRelation = "additive"
	CoverageRelationInclusive                  = CoverageInclusive
	CoverageRelationAdditive                   = CoverageAdditive
)

func (r CoverageRelation) IsKnown() bool {
	return r == CoverageInclusive || r == CoverageAdditive
}

// ChargeRef resolves a charge item to one exact observation revision.
type ChargeRef struct {
	StoreID       string `json:"store_id"`
	ObservationID string `json:"observation_id"`
	Revision      uint64 `json:"revision"`
	ChargeItemID  string `json:"charge_item_id"`
}

func (r ChargeRef) Validate() error {
	for name, value := range map[string]string{"charge ref store_id": r.StoreID, "charge ref observation_id": r.ObservationID, "charge ref charge_item_id": r.ChargeItemID} {
		if err := validateIdentityText(name, value, MaxSourceEventKeyBytes); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidCoverage, err)
		}
	}
	if r.Revision == 0 {
		return fmt.Errorf("%w: charge ref revision required", ErrInvalidCoverage)
	}
	return nil
}

func (r ChargeRef) key() string {
	return r.StoreID + "\x00" + r.ObservationID + "\x00" + strconv.FormatUint(r.Revision, 10) + "\x00" + r.ChargeItemID
}

// ChargeCoverageRef is one typed edge in the provider charge coverage graph.
type ChargeCoverageRef struct {
	Ref      ChargeRef        `json:"ref"`
	Relation CoverageRelation `json:"relation"`
}

func (r ChargeCoverageRef) Validate() error {
	if err := r.Ref.Validate(); err != nil {
		return err
	}
	if !r.Relation.IsKnown() {
		return fmt.Errorf("%w: unknown relation %q", ErrInvalidCoverage, r.Relation)
	}
	return nil
}

// ReportedCharge is provider/statement money or a charge claim. Component may
// be nil for a genuine aggregate; no lower-level allocation is invented.
type ReportedCharge struct {
	ChargeItemID string              `json:"charge_item_id"`
	Component    *ComponentKey       `json:"component,omitempty"`
	Amount       *Decimal            `json:"amount,omitempty"`
	Currency     string              `json:"currency,omitempty"`
	Kind         ChargeKind          `json:"kind"`
	Payer        PaymentParty        `json:"payer,omitzero"`
	Covers       []ChargeCoverageRef `json:"covers,omitempty"`
}

func (c ReportedCharge) Validate(allowNegative bool) error {
	if err := validateIdentityText("charge item id", c.ChargeItemID, MaxSourceEventKeyBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
	}
	if !c.Kind.IsKnown() {
		return fmt.Errorf("%w: unknown charge kind %q", ErrInvalidObservation, c.Kind)
	}
	if err := c.Payer.Validate(); err != nil {
		return fmt.Errorf("%w: payer: %v", ErrInvalidObservation, err)
	}
	if c.Kind == ChargeKindComponent && c.Component == nil {
		return fmt.Errorf("%w: component charge requires component key", ErrInvalidObservation)
	}
	if c.Component != nil {
		if err := c.Component.Validate(); err != nil {
			return fmt.Errorf("%w: charge component: %v", ErrInvalidObservation, err)
		}
	}
	if c.Amount != nil {
		n, err := c.Amount.Normalize()
		if err != nil {
			return fmt.Errorf("%w: charge amount: %v", ErrInvalidObservation, err)
		}
		if !allowNegative && strings.HasPrefix(n.Coefficient, "-") {
			return fmt.Errorf("%w: negative charge requires correction/credit semantics", ErrInvalidObservation)
		}
		if c.Currency == "" {
			return fmt.Errorf("%w: charge currency required when amount is present", ErrInvalidObservation)
		}
		if err := validateCurrencyCode(c.Currency); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
		}
	} else if c.Currency != "" {
		return fmt.Errorf("%w: absent charge amount cannot carry currency", ErrInvalidObservation)
	}
	seen := make(map[string]CoverageRelation, len(c.Covers))
	for i, edge := range c.Covers {
		if err := edge.Validate(); err != nil {
			return fmt.Errorf("%w: covers[%d]: %v", ErrInvalidObservation, i, err)
		}
		key := edge.Ref.key()
		if prior, ok := seen[key]; ok {
			if prior != edge.Relation {
				return fmt.Errorf("%w: contradictory relation for %s", ErrInvalidCoverage, key)
			}
			return fmt.Errorf("%w: duplicate relation for %s", ErrInvalidCoverage, key)
		}
		seen[key] = edge.Relation
	}
	return nil
}

func (c ReportedCharge) Clone() ReportedCharge {
	out := c
	if c.Component != nil {
		key := c.Component.Clone()
		out.Component = &key
	}
	if c.Amount != nil {
		amount := *c.Amount
		out.Amount = &amount
	}
	out.Covers = append([]ChargeCoverageRef(nil), c.Covers...)
	return out
}

// Observation is the immutable V2 evidence envelope. Local measurements,
// provider claims and statement records use the same journal family but retain
// distinct Origin/Acquisition/Authority values.
type Observation struct {
	Version        uint32                   `json:"version"`
	ID             string                   `json:"id"`
	SourceEventKey string                   `json:"source_event_key"`
	Revision       uint64                   `json:"revision"`
	StreamID       string                   `json:"stream_id"`
	Sequence       uint64                   `json:"sequence"`
	Origin         string                   `json:"origin"`
	Acquisition    string                   `json:"acquisition"`
	Authority      string                   `json:"authority"`
	Perspective    EconomicPerspective      `json:"perspective"`
	Boundary       Boundary                 `json:"boundary"`
	Lifecycle      LifecycleScope           `json:"lifecycle"`
	Subject        SubjectRef               `json:"subject"`
	Correlation    CorrelationV2            `json:"correlation"`
	Scope          scope.PrincipalScopeView `json:"scope"`
	Semantics      string                   `json:"semantics"`
	ObservedAt     time.Time                `json:"observed_at"`
	ReceivedAt     time.Time                `json:"received_at"`
	MappingRef     string                   `json:"mapping_ref"`
	Measures       []Measure                `json:"measures,omitempty"`
	Charges        []ReportedCharge         `json:"charges,omitempty"`
	Supersedes     []ObservationRef         `json:"supersedes,omitempty"`
	Evidence       []SafeEvidenceField      `json:"evidence,omitempty"`
	// legacyProviderRequest is set only by ObservationFromFact for a historical
	// V1 request-level provider record. It is deliberately not serialized and
	// cannot be supplied by an external V2 decoder.
	legacyProviderRequest bool
	// legacyV1Trusted is set only by the explicit V1 lift/read paths. It is not
	// serialized, so generic JSON decoding cannot authorize V1-only semantics.
	legacyV1Trusted bool
}

// Validate checks identity, provenance, bounds, presence and coverage. It does
// not mutate caller-owned values; use Canonical for a normalized copy.
func (o Observation) Validate() error {
	return o.validate(legacyProviderRequestAllowed(o))
}

// validate is the shared envelope validator. The legacy compatibility bridge
// may opt into the one historical request-level provider shape that V1 could
// represent; public V2 construction always uses the strict B-leg rule.
func (o Observation) validate(allowLegacyProviderRequest bool) error {
	if o.Version != ObservationVersionV2 {
		return fmt.Errorf("%w: version %d want %d", ErrInvalidObservation, o.Version, ObservationVersionV2)
	}
	for _, field := range []struct {
		name  string
		value string
		max   int
	}{
		{"observation id", o.ID, MaxObservationIDBytes},
		{"source event key", o.SourceEventKey, MaxSourceEventKeyBytes},
		{"stream id", o.StreamID, MaxStreamIDBytes},
		{"mapping ref", o.MappingRef, MaxMappingRefBytes},
	} {
		if err := validateIdentityText(field.name, field.value, field.max); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
		}
	}
	if o.Revision == 0 {
		return fmt.Errorf("%w: revision required", ErrInvalidObservation)
	}
	if err := validateIdentityText("origin", o.Origin, 64); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
	}
	if o.Origin != OriginLocal && o.Origin != OriginProvider && o.Origin != OriginStatement {
		return fmt.Errorf("%w: unknown origin %q", ErrInvalidObservation, o.Origin)
	}
	if err := validateIdentityText("acquisition", o.Acquisition, 128); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
	}
	if err := validateObservationAuthority(o.Authority); err != nil {
		return err
	}
	if err := o.Perspective.Validate(); err != nil {
		return fmt.Errorf("%w: perspective: %v", ErrInvalidObservation, err)
	}
	if err := o.Boundary.Validate(); err != nil {
		return fmt.Errorf("%w: boundary: %v", ErrInvalidObservation, err)
	}
	if err := o.Lifecycle.Validate(); err != nil {
		return fmt.Errorf("%w: lifecycle: %v", ErrInvalidObservation, err)
	}
	if err := validateProvenance(o.Origin, o.Acquisition, o.Authority); err != nil {
		return err
	}
	if !isKnownSemantics(o.Semantics) {
		return fmt.Errorf("%w: unknown semantics %q", ErrInvalidObservation, o.Semantics)
	}
	if o.ObservedAt.IsZero() || o.ReceivedAt.IsZero() {
		return fmt.Errorf("%w: observed_at and received_at required", ErrInvalidObservation)
	}
	if err := o.Subject.Validate(); err != nil {
		return err
	}
	if err := o.Correlation.Validate(); err != nil {
		return err
	}
	if o.Subject.StoreID != o.Correlation.StoreID {
		return fmt.Errorf("%w: subject/correlation store mismatch", ErrInvalidObservation)
	}
	if err := validateSubjectCorrelation(o.Subject, o.Correlation); err != nil {
		return err
	}
	if err := validateObservationOwnership(o, allowLegacyProviderRequest); err != nil {
		return err
	}
	if err := validateObservationSubjectSemantics(o); err != nil {
		return err
	}
	if len(o.Measures) > MaxObservationMeasures || len(o.Charges) > MaxObservationCharges || len(o.Supersedes) > MaxObservationSupersedes || len(o.Evidence) > MaxObservationEvidence {
		return fmt.Errorf("%w: observation entry bound exceeded", ErrInvalidObservation)
	}
	if len(o.Measures) == 0 && len(o.Charges) == 0 && len(o.Evidence) == 0 && o.Authority != AuthorityUnavailableClaim {
		return fmt.Errorf("%w: observation must retain a measure, charge or safe evidence field", ErrInvalidObservation)
	}
	if o.Semantics != SemanticsCorrection && o.Semantics != SemanticsReplacement && len(o.Supersedes) != 0 {
		return fmt.Errorf("%w: supersedes requires correction/replacement semantics", ErrInvalidObservation)
	}
	if (o.Semantics == SemanticsCorrection || o.Semantics == SemanticsReplacement) && len(o.Supersedes) == 0 {
		return fmt.Errorf("%w: correction/replacement requires supersedes", ErrInvalidObservation)
	}
	seenMeasures := make(map[string]struct{}, len(o.Measures))
	for i, measure := range o.Measures {
		if err := measure.Validate(); err != nil {
			return fmt.Errorf("%w: measures[%d]: %v", ErrInvalidObservation, i, err)
		}
		if measure.Value != nil {
			n, _ := measure.Value.Normalize()
			if strings.HasPrefix(n.Coefficient, "-") && o.Semantics != SemanticsCorrection && o.Semantics != SemanticsReplacement {
				return fmt.Errorf("%w: measures[%d]: negative value requires correction/replacement semantics", ErrInvalidObservation, i)
			}
		}
		key := measure.Key.CanonicalKey()
		if _, ok := seenMeasures[key]; ok {
			return fmt.Errorf("%w: duplicate measure key", ErrInvalidObservation)
		}
		seenMeasures[key] = struct{}{}
	}
	allowNegative := o.Semantics == SemanticsCorrection || o.Semantics == SemanticsReplacement
	seenCharges := make(map[string]struct{}, len(o.Charges))
	for i, charge := range o.Charges {
		if err := charge.Validate(allowNegative || charge.Kind == ChargeKindCredit || charge.Kind == ChargeKindAdjustment); err != nil {
			return fmt.Errorf("%w: charges[%d]: %v", ErrInvalidObservation, i, err)
		}
		if _, ok := seenCharges[charge.ChargeItemID]; ok {
			return fmt.Errorf("%w: duplicate charge item id %q", ErrInvalidObservation, charge.ChargeItemID)
		}
		seenCharges[charge.ChargeItemID] = struct{}{}
		for _, edge := range charge.Covers {
			if edge.Ref.StoreID != o.Subject.StoreID {
				return fmt.Errorf("%w: cross-store charge reference", ErrInvalidCoverage)
			}
			if edge.Ref.ObservationID == o.ID && edge.Ref.Revision == o.Revision && edge.Ref.ChargeItemID == charge.ChargeItemID {
				return fmt.Errorf("%w: charge coverage self-reference", ErrInvalidCoverage)
			}
		}
	}
	for i, ref := range o.Supersedes {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: supersedes[%d]: %v", ErrInvalidObservation, i, err)
		}
		if ref.StoreID != o.Subject.StoreID {
			return fmt.Errorf("%w: cross-store supersession reference", ErrInvalidObservation)
		}
		if ref.ObservationID == o.ID && ref.Revision == o.Revision {
			return fmt.Errorf("%w: supersession self-reference", ErrInvalidObservation)
		}
	}
	seenSupersedes := make(map[string]struct{}, len(o.Supersedes))
	seenSupersededRevisions := make(map[string]string, len(o.Supersedes))
	for _, ref := range o.Supersedes {
		if ref.PayloadHash == LegacyV1UnknownHash && !legacyV1TrustedTuple(o) {
			return fmt.Errorf("%w: %w: legacy unknown supersession hash requires trusted V1 provenance", ErrInvalidRevision, ErrInvalidObservation)
		}
		key := observationRefKey(ref)
		if _, exists := seenSupersedes[key]; exists {
			return fmt.Errorf("%w: duplicate supersession reference", ErrInvalidObservation)
		}
		seenSupersedes[key] = struct{}{}
		revisionKey := ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10)
		if priorHash, exists := seenSupersededRevisions[revisionKey]; exists && priorHash != ref.PayloadHash {
			return fmt.Errorf("%w: contradictory supersession payload hashes", ErrInvalidObservation)
		}
		seenSupersededRevisions[revisionKey] = ref.PayloadHash
	}
	seenEvidence := make(map[string]struct{}, len(o.Evidence))
	for i, field := range o.Evidence {
		if err := field.Validate(); err != nil {
			return fmt.Errorf("%w: evidence[%d]: %v", ErrInvalidObservation, i, err)
		}
		key := safeEvidenceLocationKey(field) + "\x00" + field.Acquisition
		if _, exists := seenEvidence[key]; exists {
			return fmt.Errorf("%w: duplicate safe evidence field", ErrInvalidObservation)
		}
		seenEvidence[key] = struct{}{}
	}
	if b, err := json.Marshal(observationWire(o)); err != nil || len(b) > MaxSafeEvidenceBytes {
		if err != nil {
			return fmt.Errorf("%w: serialization: %v", ErrInvalidObservation, err)
		}
		return fmt.Errorf("%w: normalized observation exceeds %d bytes", ErrInvalidObservation, MaxSafeEvidenceBytes)
	}
	return nil
}

func validateObservationAuthority(value string) error {
	switch value {
	case AuthorityObservedClaim, AuthorityEstimatedClaim, AuthorityVerifiedStatement, AuthorityUnavailableClaim:
		return nil
	default:
		return fmt.Errorf("%w: unknown authority %q", ErrInvalidObservation, value)
	}
}

func validateProvenance(origin, acquisition, authority string) error {
	if expected, known := knownAcquisitionOrigin[acquisition]; known && expected != origin {
		return fmt.Errorf("%w: acquisition %q is incompatible with origin %q", ErrInvalidObservation, acquisition, origin)
	}
	if authority == AuthorityVerifiedStatement && origin != OriginStatement {
		return fmt.Errorf("%w: verified statement authority requires statement origin", ErrInvalidObservation)
	}
	return nil
}

func isKnownSemantics(value string) bool {
	switch value {
	case SemanticsDelta, SemanticsCumulative, SemanticsGauge, SemanticsReplacement, SemanticsCorrection:
		return true
	default:
		return false
	}
}

func validateSubjectCorrelation(s SubjectRef, c CorrelationV2) error {
	pairs := [][3]string{
		{"request_id", s.RequestID, c.RequestID},
		{"a_leg_id", s.ALegID, c.ALegID},
		{"billing_call_id", s.BillingCallID, c.BillingCallID},
		{"b_leg_id", s.BLegID, c.BLegID},
		{"attempt_id", s.AttemptID, c.AttemptID},
		{"submission_id", s.SubmissionID, c.SubmissionID},
		{"provider_account_key", s.ProviderAccountKey, c.ProviderAccountKey},
		{"provider_request_id", s.ProviderRequestID, c.ProviderRequestID},
		{"provider_charge_id", s.ProviderChargeID, c.ProviderChargeID},
		{"resource_id", s.ResourceID, c.ResourceID},
		{"period_id", s.PeriodID, c.PeriodID},
	}
	for _, pair := range pairs {
		if pair[1] != "" && pair[2] != "" && pair[1] != pair[2] {
			return fmt.Errorf("%w: subject/correlation %s mismatch", ErrInvalidObservation, pair[0])
		}
	}
	if s.AttemptSeq != 0 && c.AttemptSeq != 0 && s.AttemptSeq != c.AttemptSeq {
		return fmt.Errorf("%w: subject/correlation attempt_seq mismatch", ErrInvalidObservation)
	}
	return nil
}

// Clone returns a deep copy of all nested V2 fields.
func (o Observation) Clone() Observation {
	out := o
	out.Subject = o.Subject.Clone()
	out.Scope = o.Scope.Clone()
	if o.Measures != nil {
		out.Measures = make([]Measure, len(o.Measures))
		for i, measure := range o.Measures {
			out.Measures[i] = measure.Clone()
		}
	}
	if o.Charges != nil {
		out.Charges = make([]ReportedCharge, len(o.Charges))
		for i, charge := range o.Charges {
			out.Charges[i] = charge.Clone()
		}
	}
	out.Supersedes = append([]ObservationRef(nil), o.Supersedes...)
	out.Evidence = append([]SafeEvidenceField(nil), o.Evidence...)
	return out
}

// Canonical returns a normalized deep copy with semantically unordered sets
// sorted. Source sequence and revision remain explicit identity fields.
func (o Observation) Canonical() (Observation, error) {
	if err := o.Validate(); err != nil {
		return Observation{}, err
	}
	out := o.Clone()
	for i := range out.Measures {
		key, err := out.Measures[i].Key.Normalize()
		if err != nil {
			return Observation{}, err
		}
		out.Measures[i].Key = key
		if out.Measures[i].Value != nil {
			n, err := out.Measures[i].Value.Normalize()
			if err != nil {
				return Observation{}, err
			}
			out.Measures[i].Value = &n
		}
	}
	for i := range out.Charges {
		if out.Charges[i].Component != nil {
			key, err := out.Charges[i].Component.Normalize()
			if err != nil {
				return Observation{}, err
			}
			out.Charges[i].Component = &key
		}
		if out.Charges[i].Amount != nil {
			n, err := out.Charges[i].Amount.Normalize()
			if err != nil {
				return Observation{}, err
			}
			out.Charges[i].Amount = &n
		}
		slices.SortFunc(out.Charges[i].Covers, func(a, b ChargeCoverageRef) int {
			return strings.Compare(coverageRefKey(a), coverageRefKey(b))
		})
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
	slices.SortFunc(out.Measures, func(a, b Measure) int { return strings.Compare(a.Key.CanonicalKey(), b.Key.CanonicalKey()) })
	slices.SortFunc(out.Charges, func(a, b ReportedCharge) int { return strings.Compare(a.ChargeItemID, b.ChargeItemID) })
	slices.SortFunc(out.Supersedes, func(a, b ObservationRef) int {
		return strings.Compare(observationRefKey(a), observationRefKey(b))
	})
	slices.SortFunc(out.Evidence, func(a, b SafeEvidenceField) int {
		return strings.Compare(evidenceKey(a), evidenceKey(b))
	})
	return out, nil
}

func observationRefKey(ref ObservationRef) string {
	return ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10) + "\x00" + ref.PayloadHash
}

func evidenceKey(field SafeEvidenceField) string {
	return field.Path + "\x00" + field.Name + "\x00" + field.canonicalLexeme() + "\x00" + strconv.FormatBool(field.Present) + "\x00" + strconv.FormatBool(field.Null) + "\x00" + field.Acquisition
}

func coverageRefKey(ref ChargeCoverageRef) string {
	return ref.Ref.StoreID + "\x00" + ref.Ref.ObservationID + "\x00" + fmt.Sprint(ref.Ref.Revision) + "\x00" + ref.Ref.ChargeItemID + "\x00" + string(ref.Relation)
}

// CanonicalJSON serializes the normalized V2 envelope deterministically.
func (o Observation) CanonicalJSON() ([]byte, error) {
	n, err := o.Canonical()
	if err != nil {
		return nil, err
	}
	return json.Marshal(observationWire(n))
}

// MarshalJSON uses canonical ordering so durable hashes do not depend on input
// slice order.
func (o Observation) MarshalJSON() ([]byte, error) { return o.CanonicalJSON() }

// UnmarshalJSON decodes an observation without granting legacy provider
// authority. Historical V1-lifted records must pass through the explicit
// ReadLegacyV1Observation reader after trusted durable-record selection.
func (o *Observation) UnmarshalJSON(data []byte) error {
	if o == nil {
		return fmt.Errorf("%w: nil observation destination", ErrInvalidObservation)
	}
	var wire observationWire
	if err := decodeV2JSON(data, "observation", &wire); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidObservation, err)
	}
	decoded := Observation(wire)
	*o = decoded
	return nil
}

// Fingerprint returns SHA-256 over canonical JSON.
func (o Observation) Fingerprint() string {
	b, err := o.CanonicalJSON()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ReplayFingerprint returns the canonical semantic payload hash used for
// replay identity and immutable observation references. ReceivedAt records
// transport arrival metadata and Subject/Correlation are approved alternate
// lineage carriers, so both are normalized before hashing; equivalent source
// evidence must retain one identity.
func (o Observation) ReplayFingerprint() (string, error) {
	canonical, err := o.Canonical()
	if err != nil {
		return "", err
	}
	canonical.ReceivedAt = canonical.ObservedAt
	lineage := canonical.effectiveLineage()
	canonical.Subject = lineage.Subject
	canonical.Correlation = CorrelationV2{StoreID: canonical.Correlation.StoreID, ParentWorkID: lineage.ParentWorkID}
	b, err := json.Marshal(observationWire(canonical))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// IdentityKey returns a source-event identity preimage that excludes values;
// replay equality uses the full canonical payload/fingerprint.
func (o Observation) IdentityKey() string {
	// Sequence is a semantic ordering member, not source-event identity. The
	// source event key is explicit so two events with equal quantities cannot
	// collide, while revision remains a separate replay member.
	lineage := o.effectiveLineage()
	return "v" + strconv.FormatUint(uint64(o.Version), 10) + "\x00" + lineage.Subject.StoreID + "\x00" + lineage.Subject.TenantID + "\x00" + lineage.Subject.ProviderAccountKey + "\x00" + o.Origin + "\x00" + o.Acquisition + "\x00" + lineage.identity() + "\x00" + o.StreamID + "\x00" + o.SourceEventKey + "\x00" + strconv.FormatUint(o.Revision, 10)
}

// SourceEventIdentity is an alias used by journal adapters.
func (o Observation) SourceEventIdentity() string { return o.IdentityKey() }

// IdempotencyKey is an explicit journal spelling for the source identity.
func (o Observation) IdempotencyKey() string { return o.IdentityKey() }

// Ref returns the immutable store-scoped reference for this observation. The
// payload hash is computed from the canonical semantic envelope with receipt
// metadata normalized, not from a raw transport response.
func (o Observation) Ref(storeID string) (ObservationRef, error) {
	if err := validateIdentityText("observation ref store_id", storeID, MaxSchemaIDBytes); err != nil {
		return ObservationRef{}, err
	}
	if err := o.Validate(); err != nil {
		return ObservationRef{}, err
	}
	if o.Subject.StoreID != storeID {
		return ObservationRef{}, fmt.Errorf("%w: observation/ref store mismatch", ErrInvalidObservation)
	}
	hash, err := o.ReplayFingerprint()
	if err != nil {
		return ObservationRef{}, fmt.Errorf("%w: observation replay fingerprint: %v", ErrInvalidObservation, err)
	}
	return ObservationRef{StoreID: storeID, ObservationID: o.ID, Revision: o.Revision, PayloadHash: hash}, nil
}

func (o Observation) SubjectIdentity() string {
	b, _ := json.Marshal(o.effectiveLineage().Subject)
	return string(b)
}

type effectiveLineage struct {
	Subject      SubjectRef `json:"subject"`
	ParentWorkID string     `json:"parent_work_id,omitempty"`
}

func (o Observation) effectiveLineage() effectiveLineage {
	subject := o.Subject
	subject.TenantID = firstNonEmpty(subject.TenantID, o.Correlation.TenantID)
	subject.RequestID = firstNonEmpty(subject.RequestID, o.Correlation.RequestID)
	subject.CallID = firstNonEmpty(subject.CallID, o.Correlation.CallID)
	subject.BillingCallID = firstNonEmpty(subject.BillingCallID, o.Correlation.BillingCallID)
	subject.ALegID = firstNonEmpty(subject.ALegID, o.Correlation.ALegID)
	subject.BLegID = firstNonEmpty(subject.BLegID, o.Correlation.BLegID)
	subject.AttemptID = firstNonEmpty(subject.AttemptID, o.Correlation.AttemptID)
	subject.AttemptSeq = firstNonZero(subject.AttemptSeq, o.Correlation.AttemptSeq)
	subject.SubmissionID = firstNonEmpty(subject.SubmissionID, o.Correlation.SubmissionID)
	subject.ProviderAccountKey = firstNonEmpty(subject.ProviderAccountKey, o.Correlation.ProviderAccountKey)
	subject.ProviderRequestID = firstNonEmpty(subject.ProviderRequestID, o.Correlation.ProviderRequestID)
	subject.ProviderChargeID = firstNonEmpty(subject.ProviderChargeID, o.Correlation.ProviderChargeID)
	subject.ResourceID = firstNonEmpty(subject.ResourceID, o.Correlation.ResourceID)
	subject.PeriodID = firstNonEmpty(subject.PeriodID, o.Correlation.PeriodID)
	return effectiveLineage{Subject: subject, ParentWorkID: o.Correlation.ParentWorkID}
}

func (l effectiveLineage) identity() string {
	encoded, _ := json.Marshal(l)
	return string(encoded)
}

// NormalizedLineageIdentity returns the canonical request/account/resource
// lineage after merging fields that may be carried by either trusted Subject
// or Correlation. The two carriers are intentionally accepted as a placement
// detail; a value present in only one carrier has the same effective identity
// as the equivalent value present in the other. Validation still rejects a
// contradictory value when both carriers provide one.
func (o Observation) NormalizedLineageIdentity() string {
	return o.effectiveLineage().identity()
}

type observationWire Observation

// ValidateCoverageGraph validates cross-observation charge references, cycles,
// contradictory edges and overlapping inclusive parents. Unresolved references
// are allowed here because late statement/charge observations can arrive in a
// later batch; callers requiring a closed graph should check their resolver.
func ValidateCoverageGraph(observations []Observation) error {
	type node struct {
		store, observation string
		revision           uint64
		item               string
	}
	type observationNode struct {
		store, observation string
		revision           uint64
	}
	type edge struct {
		parent node
		child  node
	}
	knownObservations := make(map[observationNode]struct{}, len(observations))
	known := make(map[node]struct{})
	edges := make(map[node][]node)
	edgeRelations := make(map[edge]CoverageRelation)
	inclusiveParents := make(map[node]node)
	relations := make(map[node]map[CoverageRelation]struct{})
	for _, observation := range observations {
		if err := observation.Validate(); err != nil {
			return err
		}
		observationKey := observationNode{observation.Subject.StoreID, observation.ID, observation.Revision}
		if _, exists := knownObservations[observationKey]; exists {
			return fmt.Errorf("%w: duplicate or conflicting observation revision", ErrInvalidCoverage)
		}
		knownObservations[observationKey] = struct{}{}
		for _, charge := range observation.Charges {
			parent := node{observation.Subject.StoreID, observation.ID, observation.Revision, charge.ChargeItemID}
			known[parent] = struct{}{}
		}
	}
	for _, observation := range observations {
		for _, charge := range observation.Charges {
			parent := node{observation.Subject.StoreID, observation.ID, observation.Revision, charge.ChargeItemID}
			for _, coverage := range charge.Covers {
				child := node{coverage.Ref.StoreID, coverage.Ref.ObservationID, coverage.Ref.Revision, coverage.Ref.ChargeItemID}
				edgeKey := edge{parent: parent, child: child}
				if priorRelation, exists := edgeRelations[edgeKey]; exists {
					if priorRelation != coverage.Relation {
						return fmt.Errorf("%w: contradictory coverage edge", ErrInvalidCoverage)
					}
					return fmt.Errorf("%w: duplicate coverage edge", ErrInvalidCoverage)
				}
				edgeRelations[edgeKey] = coverage.Relation
				edges[parent] = append(edges[parent], child)
				if relations[child] == nil {
					relations[child] = make(map[CoverageRelation]struct{}, 2)
				}
				relations[child][coverage.Relation] = struct{}{}
				if len(relations[child]) > 1 {
					return fmt.Errorf("%w: child is both inclusive and additive", ErrInvalidCoverage)
				}
				if coverage.Relation == CoverageInclusive {
					if prior, ok := inclusiveParents[child]; ok && prior != parent {
						return fmt.Errorf("%w: ambiguous overlapping inclusive parents", ErrInvalidCoverage)
					}
					inclusiveParents[child] = parent
				}
			}
		}
	}
	state := make(map[node]uint8, len(known))
	type frame struct {
		node node
		next int
	}
	for start := range known {
		if state[start] != 0 {
			continue
		}
		state[start] = 1
		stack := []frame{{node: start}}
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			children := edges[top.node]
			if top.next >= len(children) {
				state[top.node] = 2
				stack = stack[:len(stack)-1]
				continue
			}
			child := children[top.next]
			top.next++
			if _, ok := known[child]; !ok {
				continue
			}
			switch state[child] {
			case 1:
				return fmt.Errorf("%w: coverage cycle", ErrInvalidCoverage)
			case 2:
				continue
			default:
				state[child] = 1
				stack = append(stack, frame{node: child})
			}
		}
	}
	return nil
}

// ValidateChargeCoverage is a compatibility alias for ValidateCoverageGraph.
func ValidateChargeCoverage(observations []Observation) error {
	return ValidateCoverageGraph(observations)
}

// ValidateSupersessionGraph checks known revision links without requiring all
// referenced revisions to be present in the current batch. A late correction
// may therefore remain pending, while a resolved link must retain the same
// subject and origin and must name the referenced payload hash exactly (legacy
// V1 links use the explicit unknown-hash marker).
func ValidateSupersessionGraph(observations []Observation) error {
	type node struct {
		store, observation string
		revision           uint64
	}
	known := make(map[node]Observation, len(observations))
	for _, observation := range observations {
		if err := observation.Validate(); err != nil {
			return err
		}
		key := node{observation.Subject.StoreID, observation.ID, observation.Revision}
		if _, exists := known[key]; exists {
			return fmt.Errorf("%w: %w: duplicate revision identity", ErrInvalidRevision, ErrInvalidObservation)
		}
		known[key] = observation
	}
	edges := make(map[node][]node)
	for _, observation := range observations {
		from := node{observation.Subject.StoreID, observation.ID, observation.Revision}
		for _, ref := range observation.Supersedes {
			to := node{ref.StoreID, ref.ObservationID, ref.Revision}
			prior, resolved := known[to]
			if !resolved {
				continue // Late evidence may resolve this reference in a later batch.
			}
			if ref.PayloadHash == LegacyV1UnknownHash {
				if !legacyV1TrustedTuple(observation) || !legacyV1TrustedTuple(prior) {
					return fmt.Errorf("%w: %w: legacy unknown supersession hash requires trusted V1 records", ErrInvalidRevision, ErrInvalidObservation)
				}
			} else if ref.PayloadHash != prior.Fingerprint() {
				// New references use the replay-stable payload hash, while the
				// full canonical hash remains accepted for durable historical
				// references created before receipt metadata was normalized.
				replayHash, replayErr := prior.ReplayFingerprint()
				if replayErr != nil || ref.PayloadHash != replayHash {
					return fmt.Errorf("%w: %w: superseded payload hash mismatch", ErrInvalidRevision, ErrInvalidObservation)
				}
			}
			if !sameSupersessionScope(prior, observation) {
				return fmt.Errorf("%w: %w: supersession crosses source or charge scope", ErrInvalidRevision, ErrInvalidObservation)
			}
			edges[from] = append(edges[from], to)
		}
	}
	state := make(map[node]uint8, len(known))
	type frame struct {
		node node
		next int
	}
	for start := range known {
		if state[start] != 0 {
			continue
		}
		state[start] = 1
		stack := []frame{{node: start}}
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			parents := edges[top.node]
			if top.next >= len(parents) {
				state[top.node] = 2
				stack = stack[:len(stack)-1]
				continue
			}
			parent := parents[top.next]
			top.next++
			switch state[parent] {
			case 1:
				return fmt.Errorf("%w: %w: supersession cycle", ErrInvalidRevision, ErrInvalidObservation)
			case 2:
				continue
			default:
				state[parent] = 1
				stack = append(stack, frame{node: parent})
			}
		}
	}
	return nil
}

// supersessionScope is the complete trusted source and charge identity used
// when a resolved correction/replacement points at a predecessor. It is built
// from effective values because older records may carry lineage in Subject
// while newer records carry the same lineage in Correlation.
type supersessionScope struct {
	storeID            string
	tenantID           string
	requestID          string
	callID             string
	billingCallID      string
	aLegID             string
	bLegID             string
	attemptID          string
	attemptSeq         uint64
	submissionID       string
	providerAccountKey string
	providerRequestID  string
	providerChargeID   string
	parentWorkID       string
	resourceID         string
	periodID           string
	origin             string
	acquisition        string
	perspective        EconomicPerspective
	boundary           Boundary
	lifecycle          LifecycleScope
	subject            string
	streamID           string
}

func sameSupersessionScope(left, right Observation) bool {
	return supersessionScopeFor(left) == supersessionScopeFor(right)
}

func supersessionScopeFor(observation Observation) supersessionScope {
	lineage := observation.effectiveLineage()
	subject := lineage.Subject
	return supersessionScope{
		storeID:            subject.StoreID,
		tenantID:           subject.TenantID,
		requestID:          subject.RequestID,
		callID:             subject.CallID,
		billingCallID:      subject.BillingCallID,
		aLegID:             subject.ALegID,
		bLegID:             subject.BLegID,
		attemptID:          subject.AttemptID,
		attemptSeq:         subject.AttemptSeq,
		submissionID:       subject.SubmissionID,
		providerAccountKey: subject.ProviderAccountKey,
		providerRequestID:  subject.ProviderRequestID,
		providerChargeID:   subject.ProviderChargeID,
		parentWorkID:       lineage.ParentWorkID,
		resourceID:         subject.ResourceID,
		periodID:           subject.PeriodID,
		origin:             observation.Origin,
		acquisition:        observation.Acquisition,
		perspective:        observation.Perspective,
		boundary:           observation.Boundary,
		lifecycle:          observation.Lifecycle,
		subject:            lineage.identity(),
		streamID:           observation.StreamID,
	}
}

func firstNonEmpty(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func firstNonZero(primary, fallback uint64) uint64 {
	if primary != 0 {
		return primary
	}
	return fallback
}
