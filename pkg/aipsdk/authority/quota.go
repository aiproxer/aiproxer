package authority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// QuotaPolicyMethodV1 identifies the canonical provider-quota policy method.
// The method is nonfinancial: it describes admission against an allowance
// gauge and cannot create a charge, payable posting or customer debit.
const QuotaPolicyMethodV1 = "provider_quota.v1"

const (
	// QuotaPolicyMaxFreshness bounds an operator policy so a configuration
	// cannot accidentally turn a stale telemetry snapshot into an evergreen
	// admission authority.
	QuotaPolicyMaxFreshness = 365 * 24 * time.Hour
	MaxQuotaFields          = 128
	MaxQuotaThresholds      = 128
	MaxQuotaObservations    = 1024
)

var (
	ErrInvalidQuotaPolicy       = errors.New("authority: invalid quota policy")
	ErrInvalidQuotaEvaluation   = errors.New("authority: invalid quota evaluation")
	ErrInvalidQuotaEvidence     = errors.New("authority: invalid quota evidence")
	ErrQuotaEvidenceConflict    = errors.New("authority: quota evidence identity conflict")
	ErrQuotaEvidenceUnsupported = errors.New("authority: unsupported quota evidence")
)

// QuotaDecisionKind is deliberately separate from the generic authority
// DecisionKind. A quota evaluator can be indeterminate when telemetry is
// missing or stale; the C2 adapter decides how that status participates in a
// request-level authority decision.
type QuotaDecisionKind string

const (
	QuotaDecisionAllow         QuotaDecisionKind = "allow"
	QuotaDecisionDeny          QuotaDecisionKind = "deny"
	QuotaDecisionIndeterminate QuotaDecisionKind = "indeterminate"
	QuotaAllow                                   = QuotaDecisionAllow
	QuotaDeny                                    = QuotaDecisionDeny
	QuotaIndeterminate                           = QuotaDecisionIndeterminate
)

func (k QuotaDecisionKind) IsKnown() bool {
	switch k {
	case QuotaDecisionAllow, QuotaDecisionDeny, QuotaDecisionIndeterminate:
		return true
	default:
		return false
	}
}

// QuotaReason explains a quota result without implying money or request
// consumption. It is bounded so an adapter can safely expose it in metrics.
type QuotaReason string

const (
	QuotaReasonAllowed              QuotaReason = "allowed"
	QuotaReasonThresholdExceeded    QuotaReason = "threshold_exceeded"
	QuotaReasonHeadroomInsufficient QuotaReason = "headroom_insufficient"
	QuotaReasonMissingEvidence      QuotaReason = "missing_evidence"
	QuotaReasonStaleEvidence        QuotaReason = "stale_evidence"
	QuotaReasonPartialEvidence      QuotaReason = "partial_evidence"
	QuotaReasonTelemetryUnavailable QuotaReason = "telemetry_unavailable"
	QuotaReasonAccountMismatch      QuotaReason = "account_mismatch"
	QuotaReasonTenantMismatch       QuotaReason = "tenant_mismatch"
	QuotaReasonStoreMismatch        QuotaReason = "store_mismatch"
	QuotaReasonPoolMismatch         QuotaReason = "pool_mismatch"
	QuotaReasonWindowMismatch       QuotaReason = "window_mismatch"
	QuotaReasonResetMismatch        QuotaReason = "reset_mismatch"
	QuotaReasonNotYetEffective      QuotaReason = "not_yet_effective"
	QuotaReasonInvalidEvidence      QuotaReason = "invalid_evidence"
)

func (r QuotaReason) IsKnown() bool {
	switch r {
	case QuotaReasonAllowed, QuotaReasonThresholdExceeded, QuotaReasonHeadroomInsufficient,
		QuotaReasonMissingEvidence, QuotaReasonStaleEvidence, QuotaReasonPartialEvidence,
		QuotaReasonTelemetryUnavailable, QuotaReasonAccountMismatch, QuotaReasonTenantMismatch,
		QuotaReasonStoreMismatch, QuotaReasonPoolMismatch, QuotaReasonWindowMismatch,
		QuotaReasonResetMismatch, QuotaReasonNotYetEffective, QuotaReasonInvalidEvidence:
		return true
	default:
		return false
	}
}

// QuotaEvidenceStatus records why an optional telemetry authority could not
// make a complete decision. Status is retained even when the configured
// failure action is deny.
type QuotaEvidenceStatus string

const (
	QuotaEvidenceComplete    QuotaEvidenceStatus = "complete"
	QuotaEvidenceMissing     QuotaEvidenceStatus = "missing"
	QuotaEvidenceStale       QuotaEvidenceStatus = "stale"
	QuotaEvidencePartial     QuotaEvidenceStatus = "partial"
	QuotaEvidenceUnavailable QuotaEvidenceStatus = "unavailable"
	QuotaEvidenceMismatch    QuotaEvidenceStatus = "mismatch"
	QuotaEvidenceFuture      QuotaEvidenceStatus = "future"
	QuotaEvidenceInvalid     QuotaEvidenceStatus = "invalid"
)

func (s QuotaEvidenceStatus) IsKnown() bool {
	switch s {
	case QuotaEvidenceComplete, QuotaEvidenceMissing, QuotaEvidenceStale, QuotaEvidencePartial,
		QuotaEvidenceUnavailable, QuotaEvidenceMismatch, QuotaEvidenceFuture, QuotaEvidenceInvalid:
		return true
	default:
		return false
	}
}

// QuotaFailureAction controls the result for a declared evidence failure.
// It is not a retry instruction and has no financial meaning.
type QuotaFailureAction string

const (
	QuotaFailureDeny                QuotaFailureAction = "deny"
	QuotaFailureIndeterminate       QuotaFailureAction = "indeterminate"
	QuotaFailureActionDeny                             = QuotaFailureDeny
	QuotaFailureActionIndeterminate                    = QuotaFailureIndeterminate
)

func (a QuotaFailureAction) IsKnown() bool {
	return a == QuotaFailureDeny || a == QuotaFailureIndeterminate
}

// QuotaFailurePolicy makes all non-complete telemetry cases explicit. Zero
// values normalize to deny, so an omitted failure action never silently allows
// a request.
type QuotaFailurePolicy struct {
	Missing     QuotaFailureAction `json:"missing"`
	Stale       QuotaFailureAction `json:"stale"`
	Partial     QuotaFailureAction `json:"partial"`
	Unavailable QuotaFailureAction `json:"unavailable"`
	Mismatch    QuotaFailureAction `json:"mismatch"`
	Future      QuotaFailureAction `json:"future"`
}

func (p QuotaFailurePolicy) normalize() (QuotaFailurePolicy, error) {
	out := p
	actions := []*QuotaFailureAction{&out.Missing, &out.Stale, &out.Partial, &out.Unavailable, &out.Mismatch, &out.Future}
	for _, action := range actions {
		if *action == "" {
			*action = QuotaFailureDeny
		}
		if !action.IsKnown() {
			return QuotaFailurePolicy{}, fmt.Errorf("%w: unknown failure action %q", ErrInvalidQuotaPolicy, *action)
		}
	}
	return out, nil
}

// Normalize returns the explicit failure posture, defaulting every omitted
// action to deny.
func (p QuotaFailurePolicy) Normalize() (QuotaFailurePolicy, error) { return p.normalize() }

// Validate checks failure actions without changing the caller's value.
func (p QuotaFailurePolicy) Validate() error {
	_, err := p.normalize()
	return err
}

// QuotaBinding is the complete reset-scoped provider allowance identity. An
// empty StoreID means the policy is portable across one store, but evaluation
// still rejects an ambiguous multi-store input rather than choosing one.
type QuotaBinding struct {
	StoreID            string    `json:"store_id,omitempty"`
	TenantID           string    `json:"tenant_id,omitempty"`
	ProviderAccountKey string    `json:"provider_account_key"`
	PoolID             string    `json:"pool_id"`
	WindowID           string    `json:"window_id"`
	ResetAt            time.Time `json:"reset_at"`
}

func (b QuotaBinding) normalize() (QuotaBinding, error) {
	out := b
	for field, value := range map[string]string{
		"store_id": out.StoreID, "tenant_id": out.TenantID,
		"provider_account_key": out.ProviderAccountKey, "pool_id": out.PoolID, "window_id": out.WindowID,
	} {
		if value == "" && field != "store_id" && field != "tenant_id" {
			return QuotaBinding{}, fmt.Errorf("%w: %s required", ErrInvalidQuotaPolicy, field)
		}
		if value != "" {
			if err := validateQuotaIdentity(field, value); err != nil {
				return QuotaBinding{}, fmt.Errorf("%w: %v", ErrInvalidQuotaPolicy, err)
			}
		}
	}
	if out.ResetAt.IsZero() {
		return QuotaBinding{}, fmt.Errorf("%w: reset_at required", ErrInvalidQuotaPolicy)
	}
	out.ResetAt = out.ResetAt.UTC()
	return out, nil
}

// Normalize returns a UTC reset-scoped binding copy.
func (b QuotaBinding) Normalize() (QuotaBinding, error) { return b.normalize() }

// Validate checks the complete provider account/pool/window/reset identity.
func (b QuotaBinding) Validate() error {
	_, err := b.normalize()
	return err
}

// QuotaField aliases the canonical metering component identity. Quota fields
// must be nondirectional because account-window gauges have no request-flow
// direction. The alias also keeps field identity and measure identity exact.
type QuotaField = metering.ComponentKey

func normalizeQuotaField(field QuotaField) (QuotaField, error) {
	if field.Direction != metering.DirectionNone {
		return QuotaField{}, fmt.Errorf("%w: quota field %q must be nondirectional", ErrInvalidQuotaPolicy, field.Component)
	}
	normalized, err := field.Normalize()
	if err != nil {
		return QuotaField{}, fmt.Errorf("%w: quota field: %v", ErrInvalidQuotaPolicy, err)
	}
	return normalized, nil
}

// QuotaThresholdKind identifies whether a value is a utilization ceiling or
// a headroom floor. Comparisons are exact and never derive one gauge from
// another.
type QuotaThresholdKind string

const (
	QuotaThresholdMaxUtilization     QuotaThresholdKind = "max_utilization"
	QuotaThresholdMinHeadroom        QuotaThresholdKind = "min_headroom"
	QuotaThresholdKindMaxUtilization                    = QuotaThresholdMaxUtilization
	QuotaThresholdKindMinHeadroom                       = QuotaThresholdMinHeadroom
)

func (k QuotaThresholdKind) IsKnown() bool {
	return k == QuotaThresholdMaxUtilization || k == QuotaThresholdMinHeadroom
}

// QuotaValueKind keeps percentage and absolute thresholds distinct even when
// a provider uses numerically similar values.
type QuotaValueKind string

const (
	QuotaValuePercent      QuotaValueKind = "percent"
	QuotaValueAbsolute     QuotaValueKind = "absolute"
	QuotaValueKindPercent                 = QuotaValuePercent
	QuotaValueKindAbsolute                = QuotaValueAbsolute
)

func (k QuotaValueKind) IsKnown() bool {
	return k == QuotaValuePercent || k == QuotaValueAbsolute
}

// QuotaThreshold is one exact typed comparison against one gauge field.
// Percent values are in [0,100]. Absolute values are nonnegative decimals in
// the field's native unit; no cross-field conversion is attempted.
type QuotaThreshold struct {
	Kind      QuotaThresholdKind `json:"kind"`
	Field     QuotaField         `json:"field"`
	ValueKind QuotaValueKind     `json:"value_kind"`
	Value     metering.Decimal   `json:"value"`
}

func (t QuotaThreshold) normalize() (QuotaThreshold, error) {
	if !t.Kind.IsKnown() {
		return QuotaThreshold{}, fmt.Errorf("%w: unknown threshold kind %q", ErrInvalidQuotaPolicy, t.Kind)
	}
	field, err := normalizeQuotaField(t.Field)
	if err != nil {
		return QuotaThreshold{}, err
	}
	if !t.ValueKind.IsKnown() {
		return QuotaThreshold{}, fmt.Errorf("%w: unknown threshold value kind %q", ErrInvalidQuotaPolicy, t.ValueKind)
	}
	value, err := t.Value.Normalize()
	if err != nil {
		return QuotaThreshold{}, fmt.Errorf("%w: threshold value: %v", ErrInvalidQuotaPolicy, err)
	}
	rational, err := value.ToRat()
	if err != nil {
		return QuotaThreshold{}, fmt.Errorf("%w: threshold value: %v", ErrInvalidQuotaPolicy, err)
	}
	if rational.Sign() < 0 {
		return QuotaThreshold{}, fmt.Errorf("%w: threshold value must be non-negative", ErrInvalidQuotaPolicy)
	}
	if t.ValueKind == QuotaValuePercent {
		if field.Unit != metering.UnitPercent {
			return QuotaThreshold{}, fmt.Errorf("%w: percentage threshold field %q must use percent unit", ErrInvalidQuotaPolicy, field.Component)
		}
		if rational.Cmp(big.NewRat(100, 1)) > 0 {
			return QuotaThreshold{}, fmt.Errorf("%w: percentage threshold cannot exceed 100", ErrInvalidQuotaPolicy)
		}
	} else if field.Unit == metering.UnitPercent {
		return QuotaThreshold{}, fmt.Errorf("%w: absolute threshold field %q cannot use percent unit", ErrInvalidQuotaPolicy, field.Component)
	}
	return QuotaThreshold{Kind: t.Kind, Field: field, ValueKind: t.ValueKind, Value: value}, nil
}

// Normalize returns a deep-copied threshold with exact canonical decimal
// representation.
func (t QuotaThreshold) Normalize() (QuotaThreshold, error) { return t.normalize() }

// Validate checks threshold kind, field unit and exact typed value.
func (t QuotaThreshold) Validate() error {
	_, err := t.normalize()
	return err
}

func quotaThresholdKey(t QuotaThreshold) string {
	return t.Field.CanonicalKey() + "\x00" + string(t.Kind) + "\x00" + string(t.ValueKind) + "\x00" + t.Value.CanonicalString()
}

// QuotaPolicyConfig is the source form for one immutable nonfinancial quota
// authority. It contains no price, currency, ledger, debit or customer-credit
// field by design.
type QuotaPolicyConfig struct {
	ID         string             `json:"id"`
	Version    string             `json:"version"`
	Method     string             `json:"method,omitempty"`
	Binding    QuotaBinding       `json:"binding"`
	Freshness  time.Duration      `json:"freshness"`
	Required   []QuotaField       `json:"required"`
	Thresholds []QuotaThreshold   `json:"thresholds,omitempty"`
	Failures   QuotaFailurePolicy `json:"failures"`
}

// Validate checks that the source form can be compiled into an immutable
// policy. It does not retain or mutate caller-owned slices.
func (c QuotaPolicyConfig) Validate() error {
	_, err := CompileQuotaPolicy(c)
	return err
}

// QuotaPolicyRef identifies the frozen nonfinancial policy used for one
// decision. Hash is lower-case SHA-256 over the canonical normalized policy.
type QuotaPolicyRef struct {
	Method  string `json:"method"`
	ID      string `json:"id"`
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

func (r QuotaPolicyRef) Validate() error {
	for field, value := range map[string]string{"method": r.Method, "id": r.ID, "version": r.Version, "hash": r.Hash} {
		if err := validateQuotaIdentity("quota policy "+field, value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidQuotaPolicy, err)
		}
	}
	hash := strings.ToLower(r.Hash)
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("%w: policy hash must be SHA-256 hex", ErrInvalidQuotaPolicy)
	}
	return nil
}

// QuotaPolicy is immutable after CompileQuotaPolicy returns. Mutable caller
// slices are normalized and copied, and accessors return copies.
type QuotaPolicy struct {
	method     string
	id         string
	version    string
	binding    QuotaBinding
	freshness  time.Duration
	required   []QuotaField
	thresholds []QuotaThreshold
	failures   QuotaFailurePolicy
	canonical  []byte
	ref        QuotaPolicyRef
}

type quotaPolicyWire struct {
	Method     string             `json:"method"`
	ID         string             `json:"id"`
	Version    string             `json:"version"`
	Binding    QuotaBinding       `json:"binding"`
	Freshness  int64              `json:"freshness_ns"`
	Required   []QuotaField       `json:"required"`
	Thresholds []QuotaThreshold   `json:"thresholds,omitempty"`
	Failures   QuotaFailurePolicy `json:"failures"`
}

// CompileQuotaPolicy validates, normalizes and content-addresses a quota
// policy. The resulting hash is stable across required/threshold input order.
func CompileQuotaPolicy(config QuotaPolicyConfig) (*QuotaPolicy, error) {
	if err := validateQuotaIdentity("quota policy id", config.ID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQuotaPolicy, err)
	}
	if err := validateQuotaIdentity("quota policy version", config.Version); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQuotaPolicy, err)
	}
	method := config.Method
	if method == "" {
		method = QuotaPolicyMethodV1
	}
	if err := validateQuotaIdentity("quota policy method", method); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidQuotaPolicy, err)
	}
	binding, err := config.Binding.normalize()
	if err != nil {
		return nil, err
	}
	if config.Freshness <= 0 || config.Freshness > QuotaPolicyMaxFreshness {
		return nil, fmt.Errorf("%w: freshness must be greater than zero and at most %s", ErrInvalidQuotaPolicy, QuotaPolicyMaxFreshness)
	}
	if len(config.Required) > MaxQuotaFields {
		return nil, fmt.Errorf("%w: required field count exceeds %d", ErrInvalidQuotaPolicy, MaxQuotaFields)
	}
	if len(config.Thresholds) > MaxQuotaThresholds {
		return nil, fmt.Errorf("%w: threshold count exceeds %d", ErrInvalidQuotaPolicy, MaxQuotaThresholds)
	}
	requiredByKey := make(map[string]QuotaField, len(config.Required)+len(config.Thresholds))
	for i, field := range config.Required {
		normalized, err := normalizeQuotaField(field)
		if err != nil {
			return nil, fmt.Errorf("%w: required[%d]: %v", ErrInvalidQuotaPolicy, i, err)
		}
		key := normalized.CanonicalKey()
		if _, exists := requiredByKey[key]; exists {
			return nil, fmt.Errorf("%w: duplicate required field %q", ErrInvalidQuotaPolicy, key)
		}
		requiredByKey[key] = normalized
	}
	thresholds := make([]QuotaThreshold, 0, len(config.Thresholds))
	seenThresholds := make(map[string]struct{}, len(config.Thresholds))
	for i, threshold := range config.Thresholds {
		normalized, err := threshold.normalize()
		if err != nil {
			return nil, fmt.Errorf("%w: thresholds[%d]: %v", ErrInvalidQuotaPolicy, i, err)
		}
		key := quotaThresholdKey(normalized)
		if _, exists := seenThresholds[key]; exists {
			return nil, fmt.Errorf("%w: duplicate threshold %q", ErrInvalidQuotaPolicy, key)
		}
		seenThresholds[key] = struct{}{}
		thresholds = append(thresholds, normalized)
		fieldKey := normalized.Field.CanonicalKey()
		if _, exists := requiredByKey[fieldKey]; !exists {
			requiredByKey[fieldKey] = normalized.Field.Clone()
		}
	}
	if len(requiredByKey) == 0 {
		return nil, fmt.Errorf("%w: at least one required field or threshold is required", ErrInvalidQuotaPolicy)
	}
	required := make([]QuotaField, 0, len(requiredByKey))
	for _, field := range requiredByKey {
		required = append(required, field.Clone())
	}
	sort.Slice(required, func(i, j int) bool { return required[i].CanonicalKey() < required[j].CanonicalKey() })
	sort.Slice(thresholds, func(i, j int) bool { return quotaThresholdKey(thresholds[i]) < quotaThresholdKey(thresholds[j]) })
	failures, err := config.Failures.normalize()
	if err != nil {
		return nil, err
	}
	body := quotaPolicyWire{
		Method: method, ID: config.ID, Version: config.Version, Binding: binding,
		Freshness: config.Freshness.Nanoseconds(), Required: cloneQuotaFields(required),
		Thresholds: cloneQuotaThresholds(thresholds), Failures: failures,
	}
	canonical, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical policy: %v", ErrInvalidQuotaPolicy, err)
	}
	hash := sha256.Sum256(canonical)
	ref := QuotaPolicyRef{Method: method, ID: config.ID, Version: config.Version, Hash: hex.EncodeToString(hash[:])}
	return &QuotaPolicy{
		method: method, id: config.ID, version: config.Version, binding: binding, freshness: config.Freshness,
		required: cloneQuotaFields(required), thresholds: cloneQuotaThresholds(thresholds), failures: failures,
		canonical: append([]byte(nil), canonical...), ref: ref,
	}, nil
}

// NewQuotaPolicy is an explicit constructor alias for CompileQuotaPolicy.
func NewQuotaPolicy(config QuotaPolicyConfig) (*QuotaPolicy, error) {
	return CompileQuotaPolicy(config)
}

// PolicyRef returns the immutable method/version/hash binding.
func (p *QuotaPolicy) PolicyRef() QuotaPolicyRef {
	if p == nil {
		return QuotaPolicyRef{}
	}
	return p.ref
}

// Ref is a concise alias for PolicyRef.
func (p *QuotaPolicy) Ref() QuotaPolicyRef { return p.PolicyRef() }

// Hash returns the canonical SHA-256 policy hash.
func (p *QuotaPolicy) Hash() string {
	if p == nil {
		return ""
	}
	return p.ref.Hash
}

// Method returns the immutable policy method.
func (p *QuotaPolicy) Method() string {
	if p == nil {
		return ""
	}
	return p.method
}

// ID returns the immutable policy identifier.
func (p *QuotaPolicy) ID() string {
	if p == nil {
		return ""
	}
	return p.id
}

// Version returns the immutable policy version.
func (p *QuotaPolicy) Version() string {
	if p == nil {
		return ""
	}
	return p.version
}

// CanonicalJSON returns a copy of the normalized policy bytes used as the
// policy hash preimage.
func (p *QuotaPolicy) CanonicalJSON() []byte {
	if p == nil {
		return nil
	}
	return append([]byte(nil), p.canonical...)
}

// MarshalJSON emits the same normalized policy body used for hashing. A
// caller that needs the immutable content hash should retain PolicyRef
// alongside the body.
func (p *QuotaPolicy) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("null"), nil
	}
	return p.CanonicalJSON(), nil
}

// Binding returns the reset-scoped provider account/pool/window binding.
func (p *QuotaPolicy) Binding() QuotaBinding {
	if p == nil {
		return QuotaBinding{}
	}
	return p.binding
}

// Freshness returns the immutable maximum age accepted by the evaluator.
func (p *QuotaPolicy) Freshness() time.Duration {
	if p == nil {
		return 0
	}
	return p.freshness
}

// RequiredFields returns a deep copy of the fields required for admission.
func (p *QuotaPolicy) RequiredFields() []QuotaField {
	if p == nil {
		return nil
	}
	return cloneQuotaFields(p.required)
}

// Thresholds returns a deep copy of the exact typed threshold set.
func (p *QuotaPolicy) Thresholds() []QuotaThreshold {
	if p == nil {
		return nil
	}
	return cloneQuotaThresholds(p.thresholds)
}

// FailurePolicy returns the normalized explicit missing/stale behavior.
func (p *QuotaPolicy) FailurePolicy() QuotaFailurePolicy {
	if p == nil {
		return QuotaFailurePolicy{}
	}
	return p.failures
}

// QuotaEvaluationInput is the deterministic input to Evaluate. At must be
// supplied by the caller; the evaluator never reads a wall clock. Observed
// gauges are the only evidence accepted, not provider debit or money records.
type QuotaEvaluationInput struct {
	At                   time.Time
	Observations         []metering.Observation
	TelemetryUnavailable bool
}

// QuotaDecision is a nonfinancial quota result. Measures and evidence refs
// are retained so an adapter can explain the decision; neither is a payable
// amount, customer balance, provider debit or ledger command.
type QuotaDecision struct {
	Kind              QuotaDecisionKind         `json:"kind"`
	Reason            QuotaReason               `json:"reason"`
	EvidenceStatus    QuotaEvidenceStatus       `json:"evidence_status"`
	Policy            QuotaPolicyRef            `json:"policy"`
	EvidenceRefs      []metering.ObservationRef `json:"evidence_refs,omitempty"`
	Measures          []metering.Measure        `json:"measures,omitempty"`
	HeadObservationID string                    `json:"head_observation_id,omitempty"`
	HeadObservedAt    time.Time                 `json:"head_observed_at,omitzero"`
	HeadReceivedAt    time.Time                 `json:"head_received_at,omitzero"`
	ResetAt           time.Time                 `json:"reset_at"`
	ReplayCount       int                       `json:"replay_count,omitempty"`
}

// Validate checks the bounded, nonfinancial decision envelope. It is useful
// at the C2 adapter boundary before mapping the result to generic authority.
func (d QuotaDecision) Validate() error {
	if !d.Kind.IsKnown() {
		return fmt.Errorf("%w: unknown decision kind %q", ErrInvalidQuotaEvaluation, d.Kind)
	}
	if d.Policy.ID == "" || d.Policy.Version == "" || d.Policy.Method == "" || d.Policy.Hash == "" {
		return fmt.Errorf("%w: policy reference required", ErrInvalidQuotaEvaluation)
	}
	if err := d.Policy.Validate(); err != nil {
		return fmt.Errorf("%w: policy reference: %v", ErrInvalidQuotaEvaluation, err)
	}
	if !d.Reason.IsKnown() {
		return fmt.Errorf("%w: unknown decision reason %q", ErrInvalidQuotaEvaluation, d.Reason)
	}
	if !d.EvidenceStatus.IsKnown() {
		return fmt.Errorf("%w: unknown evidence status %q", ErrInvalidQuotaEvaluation, d.EvidenceStatus)
	}
	if d.ResetAt.IsZero() {
		return fmt.Errorf("%w: reset_at required", ErrInvalidQuotaEvaluation)
	}
	if d.ReplayCount < 0 {
		return fmt.Errorf("%w: replay count cannot be negative", ErrInvalidQuotaEvaluation)
	}
	for i, ref := range d.EvidenceRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: evidence_refs[%d]: %v", ErrInvalidQuotaEvaluation, i, err)
		}
	}
	for i, measure := range d.Measures {
		if err := measure.Validate(); err != nil {
			return fmt.Errorf("%w: measures[%d]: %v", ErrInvalidQuotaEvaluation, i, err)
		}
	}
	return nil
}

// Evaluate applies one frozen policy to persisted account-window gauge
// history. It merges present fields in observation order, never adds gauge
// values or derives request consumption, then compares exact decimals.
func (p *QuotaPolicy) Evaluate(input QuotaEvaluationInput) (QuotaDecision, error) {
	if p == nil {
		return QuotaDecision{}, ErrInvalidQuotaPolicy
	}
	decisionBase := QuotaDecision{Policy: p.ref, ResetAt: p.binding.ResetAt}
	if input.At.IsZero() {
		return decisionBase, fmt.Errorf("%w: evaluation time required", ErrInvalidQuotaEvaluation)
	}
	if len(input.Observations) > MaxQuotaObservations {
		return decisionBase, fmt.Errorf("%w: observation count exceeds %d", ErrInvalidQuotaEvaluation, MaxQuotaObservations)
	}
	if input.TelemetryUnavailable {
		return p.failure(decisionBase, p.failures.Unavailable, QuotaReasonTelemetryUnavailable, QuotaEvidenceUnavailable), nil
	}
	input.At = input.At.UTC()
	if input.At.Before(p.binding.ResetAt) {
		return p.failure(decisionBase, p.failures.Future, QuotaReasonNotYetEffective, QuotaEvidenceFuture), nil
	}
	entries, replayCount, err := canonicalQuotaObservations(input.Observations)
	if err != nil {
		return decisionBase, err
	}
	if len(entries) == 0 {
		return p.failure(decisionBase, p.failures.Missing, QuotaReasonMissingEvidence, QuotaEvidenceMissing), nil
	}

	matching := make([]quotaObservationEntry, 0, len(entries))
	futureMatch := false
	for _, entry := range entries {
		if !p.binding.matches(entry.observation) {
			continue
		}
		if entry.observation.ObservedAt.After(input.At) || entry.observation.ReceivedAt.After(input.At) {
			futureMatch = true
			continue
		}
		matching = append(matching, entry)
	}
	if len(matching) == 0 {
		if futureMatch {
			return p.failure(decisionBase, p.failures.Future, QuotaReasonNotYetEffective, QuotaEvidenceFuture), nil
		}
		reason := quotaMismatchReason(p.binding, entries)
		return p.failure(decisionBase, p.failures.Mismatch, reason, QuotaEvidenceMismatch), nil
	}
	if reason, ambiguous := quotaAmbiguousIdentity(p.binding, matching); ambiguous {
		return p.failure(decisionBase, p.failures.Mismatch, reason, QuotaEvidenceMismatch), nil
	}
	sort.SliceStable(matching, func(i, j int) bool { return quotaObservationLess(matching[i].observation, matching[j].observation) })

	state := make(map[string]metering.Measure, len(p.required))
	for _, entry := range matching {
		for _, measure := range entry.observation.Measures {
			key := measure.Key.CanonicalKey()
			if key == "" {
				return decisionBase, fmt.Errorf("%w: observation %q has empty measure key", ErrInvalidQuotaEvidence, entry.observation.ID)
			}
			if measure.Value == nil || measure.Quality == metering.QualityUnavailable || measure.Quality == metering.QualityNotApplicable {
				if _, exists := state[key]; !exists {
					state[key] = measure.Clone()
				}
				continue
			}
			state[key] = measure.Clone()
		}
	}

	refs := make([]metering.ObservationRef, 0, len(matching))
	for _, entry := range matching {
		refs = append(refs, entry.ref)
	}
	head := matching[len(matching)-1].observation
	decisionBase.EvidenceRefs = refs
	decisionBase.Measures = quotaStateMeasures(state)
	decisionBase.HeadObservationID = head.ID
	decisionBase.HeadObservedAt = head.ObservedAt
	decisionBase.HeadReceivedAt = head.ReceivedAt
	decisionBase.ReplayCount = replayCount
	if input.At.Sub(head.ObservedAt) > p.freshness || input.At.Sub(head.ReceivedAt) > p.freshness {
		return p.failure(decisionBase, p.failures.Stale, QuotaReasonStaleEvidence, QuotaEvidenceStale), nil
	}
	for _, field := range p.required {
		measure, exists := state[field.CanonicalKey()]
		if !exists || measure.Value == nil || measure.Quality != metering.QualityObserved {
			return p.failure(decisionBase, p.failures.Partial, QuotaReasonPartialEvidence, QuotaEvidencePartial), nil
		}
	}
	for _, threshold := range p.thresholds {
		measure := state[threshold.Field.CanonicalKey()]
		actual, err := measure.Value.ToRat()
		if err != nil {
			return decisionBase, fmt.Errorf("%w: measure %q value: %v", ErrInvalidQuotaEvidence, threshold.Field.CanonicalKey(), err)
		}
		limit, err := threshold.Value.ToRat()
		if err != nil {
			return decisionBase, fmt.Errorf("%w: threshold value: %v", ErrInvalidQuotaPolicy, err)
		}
		switch threshold.Kind {
		case QuotaThresholdMaxUtilization:
			if actual.Cmp(limit) >= 0 {
				return p.failure(decisionBase, QuotaFailureDeny, QuotaReasonThresholdExceeded, QuotaEvidenceComplete), nil
			}
		case QuotaThresholdMinHeadroom:
			if actual.Cmp(limit) < 0 {
				return p.failure(decisionBase, QuotaFailureDeny, QuotaReasonHeadroomInsufficient, QuotaEvidenceComplete), nil
			}
		}
	}
	decisionBase.Kind = QuotaDecisionAllow
	decisionBase.Reason = QuotaReasonAllowed
	decisionBase.EvidenceStatus = QuotaEvidenceComplete
	return decisionBase, nil
}

func (p *QuotaPolicy) failure(base QuotaDecision, action QuotaFailureAction, reason QuotaReason, status QuotaEvidenceStatus) QuotaDecision {
	base.Reason = reason
	base.EvidenceStatus = status
	if action == QuotaFailureIndeterminate {
		base.Kind = QuotaDecisionIndeterminate
	} else {
		base.Kind = QuotaDecisionDeny
	}
	return base
}

type quotaObservationEntry struct {
	observation metering.Observation
	ref         metering.ObservationRef
	fingerprint string
}

func canonicalQuotaObservations(observations []metering.Observation) ([]quotaObservationEntry, int, error) {
	entriesByIdentity := make(map[string]quotaObservationEntry, len(observations))
	replayCount := 0
	for i, observation := range observations {
		canonical, err := observation.Canonical()
		if err != nil {
			return nil, replayCount, fmt.Errorf("%w: observations[%d]: %w", ErrInvalidQuotaEvidence, i, err)
		}
		if canonical.Subject.Kind != metering.SubjectAccountWindow {
			return nil, replayCount, fmt.Errorf("%w: %w: observation %q has subject kind %q", ErrInvalidQuotaEvidence, ErrQuotaEvidenceUnsupported, canonical.ID, canonical.Subject.Kind)
		}
		if canonical.Origin != metering.OriginProvider || canonical.Authority != metering.AuthorityObservedClaim {
			return nil, replayCount, fmt.Errorf("%w: %w: observation %q must be an observed provider claim", ErrInvalidQuotaEvidence, ErrQuotaEvidenceUnsupported, canonical.ID)
		}
		ref, err := canonical.Ref(canonical.Subject.StoreID)
		if err != nil {
			return nil, replayCount, fmt.Errorf("%w: observation %q reference: %w", ErrInvalidQuotaEvidence, canonical.ID, err)
		}
		fingerprint, err := canonical.ReplayFingerprint()
		if err != nil {
			return nil, replayCount, fmt.Errorf("%w: observation %q fingerprint: %w", ErrInvalidQuotaEvidence, canonical.ID, err)
		}
		identity := canonical.IdentityKey()
		if prior, exists := entriesByIdentity[identity]; exists {
			if prior.fingerprint != fingerprint {
				return nil, replayCount, fmt.Errorf("%w: identity %q has payloads %s and %s", ErrQuotaEvidenceConflict, identity, prior.fingerprint, fingerprint)
			}
			replayCount++
			if quotaObservationLess(canonical, prior.observation) {
				entriesByIdentity[identity] = quotaObservationEntry{observation: canonical, ref: ref, fingerprint: fingerprint}
			}
			continue
		}
		entriesByIdentity[identity] = quotaObservationEntry{observation: canonical, ref: ref, fingerprint: fingerprint}
	}
	entries := make([]quotaObservationEntry, 0, len(entriesByIdentity))
	for _, entry := range entriesByIdentity {
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool { return quotaObservationLess(entries[i].observation, entries[j].observation) })
	return entries, replayCount, nil
}

func (b QuotaBinding) matches(observation metering.Observation) bool {
	subject := observation.Subject
	if subject.Kind != metering.SubjectAccountWindow || subject.ProviderAccountKey != b.ProviderAccountKey || subject.PoolID != b.PoolID || subject.WindowID != b.WindowID || !subject.ResetAt.Equal(b.ResetAt) {
		return false
	}
	if b.StoreID != "" && subject.StoreID != b.StoreID {
		return false
	}
	if b.TenantID != "" && quotaObservationTenant(observation) != b.TenantID {
		return false
	}
	return true
}

func quotaObservationTenant(observation metering.Observation) string {
	if observation.Subject.TenantID != "" {
		return observation.Subject.TenantID
	}
	return observation.Correlation.TenantID
}

func quotaMismatchReason(binding QuotaBinding, entries []quotaObservationEntry) QuotaReason {
	account := false
	tenant := binding.TenantID == ""
	store := binding.StoreID == ""
	pool := false
	window := false
	reset := false
	for _, entry := range entries {
		subject := entry.observation.Subject
		if subject.ProviderAccountKey != binding.ProviderAccountKey {
			continue
		}
		account = true
		if binding.TenantID != "" && quotaObservationTenant(entry.observation) != binding.TenantID {
			continue
		}
		tenant = true
		if binding.StoreID != "" && subject.StoreID != binding.StoreID {
			continue
		}
		store = true
		if subject.PoolID != binding.PoolID {
			continue
		}
		pool = true
		if subject.WindowID != binding.WindowID {
			continue
		}
		window = true
		if !subject.ResetAt.Equal(binding.ResetAt) {
			continue
		}
		reset = true
	}
	switch {
	case !account:
		return QuotaReasonAccountMismatch
	case !tenant:
		return QuotaReasonTenantMismatch
	case !store:
		return QuotaReasonStoreMismatch
	case !pool:
		return QuotaReasonPoolMismatch
	case !window:
		return QuotaReasonWindowMismatch
	case !reset:
		return QuotaReasonResetMismatch
	default:
		return QuotaReasonInvalidEvidence
	}
}

func quotaObservationLess(a, b metering.Observation) bool {
	if a.ObservedAt.Before(b.ObservedAt) {
		return true
	}
	if b.ObservedAt.Before(a.ObservedAt) {
		return false
	}
	if a.ReceivedAt.Before(b.ReceivedAt) {
		return true
	}
	if b.ReceivedAt.Before(a.ReceivedAt) {
		return false
	}
	if a.StreamID != b.StreamID {
		return a.StreamID < b.StreamID
	}
	if a.Sequence != b.Sequence {
		return a.Sequence < b.Sequence
	}
	if a.SourceEventKey != b.SourceEventKey {
		return a.SourceEventKey < b.SourceEventKey
	}
	if a.ID != b.ID {
		return a.ID < b.ID
	}
	if a.Revision != b.Revision {
		return a.Revision < b.Revision
	}
	// The fields above are the human-readable chronology. Equal chronology is
	// still possible for distinct source claims, so finish with the source
	// identity (which includes acquisition and the complete normalized lineage)
	// and replay fingerprint. Without this tie-breaker, stable sorting preserves
	// map iteration order and can select a different gauge head on each replay.
	aKey := quotaObservationOrderKey(a)
	bKey := quotaObservationOrderKey(b)
	return aKey < bKey
}

func quotaObservationOrderKey(observation metering.Observation) string {
	identity := observation.IdentityKey()
	fingerprint, err := observation.ReplayFingerprint()
	if err != nil {
		// Callers canonicalize before sorting, so this is defensive only. The
		// identity still provides a deterministic order for an invalid value;
		// its invalidity is reported by canonicalQuotaObservations before any
		// policy decision is made.
		return identity + "\x00" + observation.Fingerprint()
	}
	return identity + "\x00" + fingerprint
}

func quotaStateMeasures(state map[string]metering.Measure) []metering.Measure {
	keys := make([]string, 0, len(state))
	for key := range state {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]metering.Measure, 0, len(keys))
	for _, key := range keys {
		out = append(out, state[key].Clone())
	}
	return out
}

// quotaAmbiguousIdentity rejects a portable (store/tenant-unbound) policy
// when the input contains more than one matching scope. Choosing one scope
// would make the decision depend on an unrelated store's arrival history.
func quotaAmbiguousIdentity(binding QuotaBinding, entries []quotaObservationEntry) (QuotaReason, bool) {
	stores := make(map[string]struct{})
	tenantIDs := make(map[string]struct{})
	for _, entry := range entries {
		stores[entry.observation.Subject.StoreID] = struct{}{}
		tenantIDs[quotaObservationTenant(entry.observation)] = struct{}{}
	}
	if binding.StoreID == "" && len(stores) > 1 {
		return QuotaReasonStoreMismatch, true
	}
	if binding.TenantID == "" && len(tenantIDs) > 1 {
		return QuotaReasonTenantMismatch, true
	}
	return "", false
}

func cloneQuotaFields(fields []QuotaField) []QuotaField {
	if fields == nil {
		return nil
	}
	out := make([]QuotaField, len(fields))
	for i, field := range fields {
		out[i] = field.Clone()
	}
	return out
}

func cloneQuotaThresholds(thresholds []QuotaThreshold) []QuotaThreshold {
	if thresholds == nil {
		return nil
	}
	out := make([]QuotaThreshold, len(thresholds))
	for i, threshold := range thresholds {
		out[i] = threshold
		out[i].Field = threshold.Field.Clone()
	}
	return out
}

func validateQuotaIdentity(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s required", field)
	}
	if len(value) > metering.MaxSchemaIDBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, metering.MaxSchemaIDBytes)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not have surrounding whitespace", field)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || !unicode.IsPrint(r) {
			return fmt.Errorf("%s contains unsafe characters", field)
		}
	}
	return nil
}
