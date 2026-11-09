package metering

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	usageevidence "github.com/aiproxer/aiproxer/internal/core/usageevidence"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// LateEconomicEvidenceKind identifies the authenticated producer of an
// observation appended after execution closure. The kind is checked against
// the V2 provenance tuple so a provider response cannot be relabelled as a
// statement (or vice versa) at this boundary.
type LateEconomicEvidenceKind string

const (
	LateEconomicProviderFinalizer LateEconomicEvidenceKind = "provider_finalizer"
	LateEconomicStatement         LateEconomicEvidenceKind = "statement"
	LateEconomicCorrection        LateEconomicEvidenceKind = "correction"
)

var (
	// ErrLateEconomicAppend classifies failures at the post-execution economic
	// append seam. It never represents a request/attempt reopen failure because
	// this seam has no lifecycle mutation capability.
	ErrLateEconomicAppend = errors.New("metering: late economic append rejected")
	// ErrLateEconomicSinkCapability identifies a sink that cannot guarantee one
	// transaction commits both the observation and its economic-work trigger.
	// A plain observation sink, including an observation-only atomic sink, is
	// intentionally insufficient for late evidence.
	ErrLateEconomicSinkCapability = errors.New("metering: late economic sink lacks atomic economic outbox capability")
	// ErrLateEconomicStoreMismatch prevents a caller from using a journal or
	// billing store belonging to another isolation namespace.
	ErrLateEconomicStoreMismatch = errors.New("metering: late economic store mismatch")
	// ErrLateEconomicLineageMismatch prevents attribution to a different
	// BillingCallID, B-leg, attempt sequence, or resolved correction scope.
	ErrLateEconomicLineageMismatch = errors.New("metering: late economic lineage mismatch")
	// ErrLateEconomicAuthorityMismatch prevents a provider/account authority
	// from being changed while referring to an immutable closed B-leg.
	ErrLateEconomicAuthorityMismatch = errors.New("metering: late economic authority mismatch")
	// ErrLateEconomicProvenanceMismatch prevents a late producer from using a
	// provenance tuple that does not match its declared evidence kind.
	ErrLateEconomicProvenanceMismatch = errors.New("metering: late economic provenance mismatch")
	// ErrLateEconomicLegNotClosed means the durable current-record checkpoint is
	// not yet closed. Late append is intentionally not an alternate terminal
	// handoff path.
	ErrLateEconomicLegNotClosed = errors.New("metering: late economic B-leg is not closed")
)

// LateEconomicIdentity is the immutable identity that a late observation must
// echo. ProviderID is the closed record's provider authority; the remaining
// fields are the trusted B-leg/economic lineage carried by the observation.
type LateEconomicIdentity struct {
	StoreID            string
	BillingCallID      usageevidence.BillingCallID
	ALegID             string
	BLegID             string
	AttemptID          string
	AttemptSeq         uint64
	ProviderID         string
	ProviderAccountKey string
	ProviderRequestID  string
	ProviderChargeID   string
}

func (i LateEconomicIdentity) validate(configuredStoreID string) error {
	if strings.TrimSpace(configuredStoreID) == "" || strings.TrimSpace(i.StoreID) == "" || i.StoreID != strings.TrimSpace(i.StoreID) || i.StoreID != configuredStoreID {
		return fmt.Errorf("%w: expected=%q actual=%q", ErrLateEconomicStoreMismatch, configuredStoreID, i.StoreID)
	}
	if err := i.BillingCallID.Validate(); err != nil {
		return fmt.Errorf("%w: billing call: %v", ErrLateEconomicLineageMismatch, err)
	}
	if strings.TrimSpace(i.BLegID) == "" || i.BLegID != strings.TrimSpace(i.BLegID) {
		return fmt.Errorf("%w: B-leg identity is required", ErrLateEconomicLineageMismatch)
	}
	if i.AttemptSeq == 0 {
		return fmt.Errorf("%w: attempt sequence is required", ErrLateEconomicLineageMismatch)
	}
	if strings.TrimSpace(i.ProviderID) == "" || i.ProviderID != strings.TrimSpace(i.ProviderID) {
		return fmt.Errorf("%w: provider ID is required", ErrLateEconomicAuthorityMismatch)
	}
	if strings.TrimSpace(i.ProviderAccountKey) == "" || i.ProviderAccountKey != strings.TrimSpace(i.ProviderAccountKey) {
		return fmt.Errorf("%w: provider account is required", ErrLateEconomicAuthorityMismatch)
	}
	for field, value := range map[string]string{
		"A-leg": i.ALegID, "attempt": i.AttemptID, "provider request": i.ProviderRequestID, "provider charge": i.ProviderChargeID,
	} {
		if value != "" && value != strings.TrimSpace(value) {
			return fmt.Errorf("%w: %s has surrounding whitespace", ErrLateEconomicLineageMismatch, field)
		}
	}
	return nil
}

// LateEconomicEvidence is a complete, already-normalized V2 observation plus
// the trusted closed B-leg identity supplied by the caller. It deliberately
// contains no terminal sink, attempt allocator, or stream handle.
type LateEconomicEvidence struct {
	Kind        LateEconomicEvidenceKind
	Identity    LateEconomicIdentity
	Observation sdkmetering.Observation
}

// EconomicObservationSink is the consumer-owned late-economics capability.
// AppendEconomicObservationWithOutbox must commit the immutable observation
// and its required economic-work trigger in one durable transaction, preserve
// the journal's exact replay/ambiguity semantics, and return the same result
// for an exact retry. A sink implementing only ObservationSink or
// AtomicObservationSink does not satisfy this contract.
type EconomicObservationSink interface {
	sdkmetering.ObservationSink
	AppendEconomicObservationWithOutbox(context.Context, sdkmetering.Observation) error
}

func (e LateEconomicEvidence) validate(configuredStoreID string, observation sdkmetering.Observation) error {
	if err := e.Identity.validate(configuredStoreID); err != nil {
		return err
	}
	switch e.Kind {
	case LateEconomicProviderFinalizer:
		if observation.Origin != sdkmetering.OriginProvider || observation.Acquisition != sdkmetering.AcquisitionProviderFinalizer || observation.Subject.Kind != sdkmetering.SubjectBLeg {
			return fmt.Errorf("%w: finalizer requires provider/provider_finalizer", ErrLateEconomicProvenanceMismatch)
		}
	case LateEconomicStatement:
		if observation.Origin != sdkmetering.OriginStatement || observation.Acquisition != sdkmetering.AcquisitionStatementImporter || observation.Authority != sdkmetering.AuthorityVerifiedStatement || observation.Subject.Kind != sdkmetering.SubjectStatementLine {
			return fmt.Errorf("%w: statement requires statement/statement_importer/verified_statement", ErrLateEconomicProvenanceMismatch)
		}
	case LateEconomicCorrection:
		if observation.Semantics != sdkmetering.SemanticsCorrection && observation.Semantics != sdkmetering.SemanticsReplacement {
			return fmt.Errorf("%w: correction requires correction or replacement semantics", ErrLateEconomicProvenanceMismatch)
		}
		switch observation.Origin {
		case sdkmetering.OriginProvider:
			// Provider corrections may be emitted by the response/finalizer/count
			// API acquisition path; the provider adapter remains responsible for
			// normalizing that source before this seam.
			if observation.Subject.Kind != sdkmetering.SubjectBLeg {
				return fmt.Errorf("%w: provider correction requires B-leg subject", ErrLateEconomicProvenanceMismatch)
			}
		case sdkmetering.OriginStatement:
			if observation.Acquisition != sdkmetering.AcquisitionStatementImporter || observation.Authority != sdkmetering.AuthorityVerifiedStatement || observation.Subject.Kind != sdkmetering.SubjectStatementLine {
				return fmt.Errorf("%w: statement correction requires statement_importer/verified_statement", ErrLateEconomicProvenanceMismatch)
			}
		default:
			return fmt.Errorf("%w: correction origin is not provider or statement", ErrLateEconomicProvenanceMismatch)
		}
	default:
		return fmt.Errorf("%w: unknown late evidence kind %q", ErrLateEconomicProvenanceMismatch, e.Kind)
	}
	return nil
}

// ClosedBLegReader resolves the immutable terminal usage record. The reader
// has no append or allocation method by design, which keeps this seam unable
// to reopen execution or manufacture a replacement B-leg.
type ClosedBLegReader interface {
	GetCallLegUsage(context.Context, string) (usageevidence.CallLegUsageRecord, error)
}

// ObservationResolver resolves exact immutable predecessors for corrections.
// A correction cannot be accepted merely because its reference is syntactically
// valid; the predecessor must be present and hash-identical in the same store.
type ObservationResolver interface {
	GetObservation(context.Context, string, uint64) (sdkmetering.Observation, error)
}

// LateEconomicAppenderConfig wires the two read/append ports needed by the
// post-terminal economic path. StoreID is explicit so the appender cannot infer
// isolation from an untrusted observation.
type LateEconomicAppenderConfig struct {
	StoreID string
	Legs    ClosedBLegReader
	// Sink remains compatibility-typed for composition boundaries; the
	// constructor rejects it unless its dynamic implementation exposes the
	// consumer-owned EconomicObservationSink capability.
	Sink     sdkmetering.ObservationSink
	Resolver ObservationResolver
}

// LateEconomicAppender is the production append seam for provider finalizer,
// statement and correction evidence after execution closure.
type LateEconomicAppender struct {
	storeID  string
	legs     ClosedBLegReader
	sink     EconomicObservationSink
	resolver ObservationResolver
}

// NewLateEconomicAppender constructs a late evidence appender with explicit
// ownership ports. It returns a concrete type so composition remains visible.
func NewLateEconomicAppender(config LateEconomicAppenderConfig) (*LateEconomicAppender, error) {
	storeID := strings.TrimSpace(config.StoreID)
	if storeID == "" {
		return nil, fmt.Errorf("%w: store ID is required", ErrLateEconomicAppend)
	}
	if config.Legs == nil {
		return nil, fmt.Errorf("%w: closed B-leg reader is required", ErrLateEconomicAppend)
	}
	if config.Sink == nil || isNilLateEconomicSink(config.Sink) {
		return nil, fmt.Errorf("%w: %w", ErrLateEconomicAppend, ErrLateEconomicSinkCapability)
	}
	economicSink, ok := config.Sink.(EconomicObservationSink)
	if !ok || isNilLateEconomicSink(economicSink) {
		return nil, fmt.Errorf("%w: %w", ErrLateEconomicAppend, ErrLateEconomicSinkCapability)
	}
	return &LateEconomicAppender{storeID: storeID, legs: config.Legs, sink: economicSink, resolver: config.Resolver}, nil
}

func isNilLateEconomicSink(sink any) bool {
	if sink == nil {
		return true
	}
	value := reflect.ValueOf(sink)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// AppendLateEconomicEvidence validates a closed B-leg identity and appends one
// immutable observation. The only mutation available after the closed-record
// read is the observation sink; no terminal usage record, stream, attempt, or
// B-leg allocator is reachable from this method.
func (a *LateEconomicAppender) AppendLateEconomicEvidence(ctx context.Context, evidence LateEconomicEvidence) error {
	if a == nil || a.legs == nil || a.sink == nil {
		return fmt.Errorf("%w: incomplete appender", ErrLateEconomicAppend)
	}
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrLateEconomicAppend)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	canonical, err := evidence.Observation.Canonical()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLateEconomicAppend, err)
	}
	if err := evidence.validate(a.storeID, canonical); err != nil {
		return fmt.Errorf("%w: %w", ErrLateEconomicAppend, err)
	}
	if err := validateLateEconomicLineage(evidence.Identity, canonical); err != nil {
		return fmt.Errorf("%w: %w", ErrLateEconomicAppend, err)
	}
	key, err := usageevidence.CallLegUsageKey(evidence.Identity.BillingCallID, evidence.Identity.BLegID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLateEconomicAppend, err)
	}
	record, err := a.legs.GetCallLegUsage(ctx, key)
	if err != nil {
		return fmt.Errorf("%w: closed B-leg lookup: %w", ErrLateEconomicAppend, err)
	}
	if record.FinishedAt.IsZero() {
		return fmt.Errorf("%w: %w", ErrLateEconomicAppend, ErrLateEconomicLegNotClosed)
	}
	if err := validateClosedBLegIdentity(evidence.Identity, record); err != nil {
		return fmt.Errorf("%w: %w", ErrLateEconomicAppend, err)
	}
	if err := validateTrustedProviderAuthority(evidence.Identity, canonical, record); err != nil {
		return fmt.Errorf("%w: %w", ErrLateEconomicAppend, err)
	}
	if err := a.validateSupersession(ctx, canonical); err != nil {
		return fmt.Errorf("%w: %w", ErrLateEconomicAppend, err)
	}
	if err := a.sink.AppendEconomicObservationWithOutbox(ctx, canonical); err != nil {
		return fmt.Errorf("%w: durable observation append: %w", ErrLateEconomicAppend, err)
	}
	return nil
}

func validateLateEconomicLineage(identity LateEconomicIdentity, observation sdkmetering.Observation) error {
	lineage := observation.Subject
	correlation := observation.Correlation
	if lineage.StoreID != correlation.StoreID || lineage.StoreID != identity.StoreID {
		return fmt.Errorf("%w: observation store", ErrLateEconomicStoreMismatch)
	}
	if effective := firstLateNonEmpty(lineage.BillingCallID, correlation.BillingCallID); effective != identity.BillingCallID.String() {
		return fmt.Errorf("%w: BillingCallID expected=%q actual=%q", ErrLateEconomicLineageMismatch, identity.BillingCallID, effective)
	}
	if effective := firstLateNonEmpty(lineage.BLegID, correlation.BLegID); effective != identity.BLegID {
		return fmt.Errorf("%w: B-leg expected=%q actual=%q", ErrLateEconomicLineageMismatch, identity.BLegID, effective)
	}
	if lineage.CallID != "" && correlation.CallID != "" && lineage.CallID != correlation.CallID {
		return fmt.Errorf("%w: CallID subject/correlation mismatch", ErrLateEconomicLineageMismatch)
	}
	if effective := firstLateNonEmpty(lineage.CallID, correlation.CallID); effective != "" && effective != identity.BillingCallID.String() {
		return fmt.Errorf("%w: CallID expected BillingCallID=%q actual=%q", ErrLateEconomicLineageMismatch, identity.BillingCallID, effective)
	}
	if identity.ALegID != "" {
		if effective := firstLateNonEmpty(lineage.ALegID, correlation.ALegID); effective != identity.ALegID {
			return fmt.Errorf("%w: A-leg expected=%q actual=%q", ErrLateEconomicLineageMismatch, identity.ALegID, effective)
		}
	}
	if identity.AttemptID != "" {
		if effective := firstLateNonEmpty(lineage.AttemptID, correlation.AttemptID); effective != identity.AttemptID {
			return fmt.Errorf("%w: attempt expected=%q actual=%q", ErrLateEconomicLineageMismatch, identity.AttemptID, effective)
		}
	}
	if effective := firstLateNonZero(lineage.AttemptSeq, correlation.AttemptSeq); effective != identity.AttemptSeq {
		return fmt.Errorf("%w: attempt sequence expected=%d actual=%d", ErrLateEconomicLineageMismatch, identity.AttemptSeq, effective)
	}
	if effective := firstLateNonEmpty(lineage.ProviderAccountKey, correlation.ProviderAccountKey); effective != identity.ProviderAccountKey {
		return fmt.Errorf("%w: provider account", ErrLateEconomicAuthorityMismatch)
	}
	for _, field := range []struct {
		name, expected, subject, corr string
	}{
		{"provider request", identity.ProviderRequestID, lineage.ProviderRequestID, correlation.ProviderRequestID},
		{"provider charge", identity.ProviderChargeID, lineage.ProviderChargeID, correlation.ProviderChargeID},
	} {
		if field.expected != "" && firstLateNonEmpty(field.subject, field.corr) != field.expected {
			return fmt.Errorf("%w: %s", ErrLateEconomicAuthorityMismatch, field.name)
		}
	}
	return nil
}

func validateClosedBLegIdentity(identity LateEconomicIdentity, record usageevidence.CallLegUsageRecord) error {
	if record.CallID != identity.BillingCallID || record.BLegID != identity.BLegID || record.AttemptSeq <= 0 || uint64(record.AttemptSeq) != identity.AttemptSeq {
		return fmt.Errorf("%w: closed record call/B-leg/attempt sequence", ErrLateEconomicLineageMismatch)
	}
	expectedKey, err := usageevidence.CallLegUsageKey(record.CallID, record.BLegID)
	if err != nil || record.Key != expectedKey || strings.TrimSpace(record.Fingerprint) == "" {
		return fmt.Errorf("%w: closed record key or fingerprint is not sealed", ErrLateEconomicLineageMismatch)
	}
	fingerprint, err := record.SemanticFingerprint()
	if err != nil || fingerprint != record.Fingerprint {
		return fmt.Errorf("%w: closed record fingerprint is not immutable", ErrLateEconomicLineageMismatch)
	}
	if identity.ALegID != "" && record.ALegID != identity.ALegID {
		return fmt.Errorf("%w: closed record A-leg", ErrLateEconomicLineageMismatch)
	}
	if record.ProviderID != identity.ProviderID {
		return fmt.Errorf("%w: closed record provider expected=%q actual=%q", ErrLateEconomicAuthorityMismatch, identity.ProviderID, record.ProviderID)
	}
	return nil
}

type trustedProviderAuthority struct {
	providerID string
	accountKey string
	requestID  string
	chargeID   string
}

// validateTrustedProviderAuthority compares the submitted provider context
// with provider-origin, observed V2 evidence already sealed on the B-leg.
// The caller's identity and observation are claims only; neither can create
// authority when the immutable closed record has no matching evidence.
func validateTrustedProviderAuthority(identity LateEconomicIdentity, observation sdkmetering.Observation, record usageevidence.CallLegUsageRecord) error {
	accountKey, requestID, chargeID := providerAuthorityFields(observation)
	if accountKey == "" {
		return fmt.Errorf("%w: submitted provider account is absent", ErrLateEconomicAuthorityMismatch)
	}
	if identity.ProviderAccountKey != accountKey {
		return fmt.Errorf("%w: submitted provider account does not match identity", ErrLateEconomicAuthorityMismatch)
	}
	if identity.ProviderRequestID != "" && identity.ProviderRequestID != requestID {
		return fmt.Errorf("%w: submitted provider request does not match identity", ErrLateEconomicAuthorityMismatch)
	}
	if identity.ProviderChargeID != "" && identity.ProviderChargeID != chargeID {
		return fmt.Errorf("%w: submitted provider charge does not match identity", ErrLateEconomicAuthorityMismatch)
	}

	for _, trusted := range trustedProviderAuthorities(identity.StoreID, record) {
		if trusted.providerID != identity.ProviderID || trusted.accountKey != accountKey {
			continue
		}
		if requestID != "" && trusted.requestID != requestID {
			continue
		}
		if chargeID != "" && trusted.chargeID != chargeID {
			continue
		}
		return nil
	}
	if len(record.Observations) == 0 {
		return fmt.Errorf("%w: closed B-leg has no trusted provider evidence", ErrLateEconomicAuthorityMismatch)
	}
	return fmt.Errorf("%w: submitted provider context has no trusted closed-leg match", ErrLateEconomicAuthorityMismatch)
}

func trustedProviderAuthorities(storeID string, record usageevidence.CallLegUsageRecord) []trustedProviderAuthority {
	if len(record.Observations) == 0 {
		return nil
	}
	authorities := make([]trustedProviderAuthority, 0, len(record.Observations))
	for _, original := range record.Observations {
		if original.Origin != sdkmetering.OriginProvider || original.Authority != sdkmetering.AuthorityObservedClaim {
			continue
		}
		observation, err := original.Canonical()
		if err != nil {
			continue
		}
		if observation.Subject.StoreID != storeID || observation.Correlation.StoreID != storeID {
			continue
		}
		accountKey, requestID, chargeID := providerAuthorityFields(observation)
		if accountKey == "" {
			continue
		}
		authorities = append(authorities, trustedProviderAuthority{
			providerID: record.ProviderID, accountKey: accountKey, requestID: requestID, chargeID: chargeID,
		})
	}
	return authorities
}

func providerAuthorityFields(observation sdkmetering.Observation) (accountKey, requestID, chargeID string) {
	return firstLateNonEmpty(observation.Subject.ProviderAccountKey, observation.Correlation.ProviderAccountKey),
		firstLateNonEmpty(observation.Subject.ProviderRequestID, observation.Correlation.ProviderRequestID),
		firstLateNonEmpty(observation.Subject.ProviderChargeID, observation.Correlation.ProviderChargeID)
}

func (a *LateEconomicAppender) validateSupersession(ctx context.Context, observation sdkmetering.Observation) error {
	if len(observation.Supersedes) == 0 {
		return nil
	}
	if a.resolver == nil {
		return fmt.Errorf("%w: predecessor resolver is required", ErrLateEconomicLineageMismatch)
	}
	known := make([]sdkmetering.Observation, 0, len(observation.Supersedes)+1)
	for _, ref := range observation.Supersedes {
		if ref.StoreID != a.storeID {
			return fmt.Errorf("%w: superseded observation store", ErrLateEconomicStoreMismatch)
		}
		prior, err := a.resolver.GetObservation(ctx, ref.ObservationID, ref.Revision)
		if err != nil {
			return fmt.Errorf("%w: resolve %s/%d: %w", ErrLateEconomicLineageMismatch, ref.ObservationID, ref.Revision, err)
		}
		canonicalPrior, err := prior.Canonical()
		if err != nil {
			return fmt.Errorf("%w: superseded observation invalid: %w", ErrLateEconomicLineageMismatch, err)
		}
		if canonicalPrior.Subject.StoreID != ref.StoreID {
			return fmt.Errorf("%w: superseded observation store", ErrLateEconomicStoreMismatch)
		}
		if canonicalPrior.ID != ref.ObservationID || canonicalPrior.Revision != ref.Revision {
			return fmt.Errorf("%w: superseded observation identity does not match reference", ErrLateEconomicLineageMismatch)
		}
		replayHash, err := canonicalPrior.ReplayFingerprint()
		fullHash := canonicalPrior.Fingerprint()
		if err != nil || (ref.PayloadHash != replayHash && ref.PayloadHash != fullHash) {
			return fmt.Errorf("%w: superseded observation hash", ErrLateEconomicLineageMismatch)
		}
		known = append(known, canonicalPrior)
	}
	known = append(known, observation)
	if err := sdkmetering.ValidateSupersessionGraph(known); err != nil {
		return fmt.Errorf("%w: %w", ErrLateEconomicLineageMismatch, err)
	}
	return nil
}

func firstLateNonEmpty(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func firstLateNonZero(primary, fallback uint64) uint64 {
	if primary != 0 {
		return primary
	}
	return fallback
}
