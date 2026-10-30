package usageevidence

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type CallUsageRecord struct {
	SchemaVersion int
	Key           string
	Fingerprint   string
	CallID        BillingCallID
	// SubmissionID is optional trusted customer scope. It is independent from
	// BillingCallID: one A-leg may resume with a new call/submission identity.
	SubmissionID       string `json:"SubmissionID,omitempty"`
	AccountID          string
	ALegID             string
	SessionID          string
	StartedAt          time.Time
	FinishedAt         time.Time
	Outcome            TurnOutcome
	CustomerPricingRef VersionRef
	ChargePolicyRef    VersionRef
	ExpectedBLegIDs    []string
	Workload           WorkloadIdentity
}
type CallLegUsageRecord struct {
	Key         string
	Fingerprint string
	CallID      BillingCallID
	// SubmissionID is optional trusted customer scope and is never a provider
	// usage meter. B-leg observations retain the same lineage separately.
	SubmissionID string `json:"SubmissionID,omitempty"`
	ALegID       string
	BLegID       string
	AttemptSeq   int
	BackendID    string
	ProviderID   string
	ModelID      string
	StartedAt    time.Time
	FinishedAt   time.Time
	Outcome      LegOutcome
	Surfaced     SurfacedState
	Evidence     FinalBillingEvidence
	// EvidenceVersion is zero for legacy V1 rows. Version 2 carries the
	// source-separated immutable observations captured for this concrete
	// B-leg; Evidence remains the explicitly labelled V1 projection.
	EvidenceVersion    int                       `json:"evidence_version,omitempty"`
	EvidenceProjection string                    `json:"evidence_projection,omitempty"`
	Observations       []metering.Observation    `json:"observations,omitempty"`
	ObservationRefs    []metering.ObservationRef `json:"observation_refs,omitempty"`
	EvidenceConflicts  []EvidenceConflict        `json:"evidence_conflicts,omitempty"`
	// EconomicEvidenceVersion and EconomicDispositions are an additive,
	// source-separated carrier for connector coverage. The observation payload
	// remains unchanged and is still the only input to meter/charge semantics.
	EconomicEvidenceVersion int                           `json:"economic_evidence_version,omitempty"`
	EconomicDispositions    []EconomicEvidenceDisposition `json:"economic_dispositions,omitempty"`
	OperatorRateRef         VersionRef
	Workload                WorkloadIdentity
}

func CallUsageKey(callID BillingCallID) (string, error) {
	if err := callID.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	return callID.String(), nil
}

func CallLegUsageKey(callID BillingCallID, bLegID string) (string, error) {
	if err := callID.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	bLegID = strings.TrimSpace(bLegID)
	if bLegID == "" {
		return "", fmt.Errorf("%w: B-leg identity is required", ErrInvalidRecord)
	}
	if strings.Contains(bLegID, ":") {
		return "", fmt.Errorf("%w: B-leg identity must not contain ':'", ErrInvalidRecord)
	}
	return callID.String() + ":" + bLegID, nil
}

func (r CallUsageRecord) Seal() (CallUsageRecord, error) {
	if err := r.validate(); err != nil {
		return CallUsageRecord{}, err
	}
	out := r
	workload, err := normalizeWorkloadIdentity(r.Workload)
	if err != nil {
		return CallUsageRecord{}, err
	}
	out.Workload = workload
	out.ExpectedBLegIDs = canonicalExpectedBLegIDs(r.ExpectedBLegIDs)
	key, err := CallUsageKey(r.CallID)
	if err != nil {
		return CallUsageRecord{}, err
	}
	out.Key = key
	fp, err := out.SemanticFingerprint()
	if err != nil {
		return CallUsageRecord{}, err
	}
	out.Fingerprint = fp
	return out, nil
}

func (r CallUsageRecord) SemanticFingerprint() (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	var c canonicalWriter
	c.string("cur")
	c.u64(uint64(r.SchemaVersion))
	c.string(r.CallID.String())
	if strings.TrimSpace(r.SubmissionID) != "" {
		c.string("submission")
		c.string(r.SubmissionID)
	}
	c.string(r.AccountID)
	c.string(r.ALegID)
	c.string(r.SessionID)
	writeWorkloadIdentity(&c, r.Workload)
	c.time(r.StartedAt)
	c.time(r.FinishedAt)
	c.string(string(r.Outcome))
	writeVersionRef(&c, r.CustomerPricingRef)
	writeVersionRef(&c, r.ChargePolicyRef)
	ids := canonicalExpectedBLegIDs(r.ExpectedBLegIDs)
	c.u64(uint64(len(ids)))
	for _, id := range ids {
		c.string(id)
	}
	return digest(c.bytes()), nil
}

func canonicalExpectedBLegIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := append([]string(nil), ids...)
	slices.Sort(out)
	return out
}

func (l CallLegUsageRecord) Seal() (CallLegUsageRecord, error) {
	prepared := l
	prepared.BLegID = strings.TrimSpace(prepared.BLegID)
	if prepared.EvidenceVersion == 0 && (len(prepared.Observations) != 0 || len(prepared.ObservationRefs) != 0 || len(prepared.EvidenceConflicts) != 0 || len(prepared.EconomicDispositions) != 0) {
		prepared.EvidenceVersion = EvidenceFormatVersionV2
	}
	if len(prepared.EconomicDispositions) != 0 && prepared.EconomicEvidenceVersion == 0 {
		prepared.EconomicEvidenceVersion = EconomicEvidenceDispositionVersionV1
	}
	if prepared.EvidenceVersion >= EvidenceFormatVersionV2 && prepared.EvidenceProjection == "" {
		prepared.EvidenceProjection = EvidenceProjectionV1
	}
	if err := prepared.validate(); err != nil {
		return CallLegUsageRecord{}, err
	}
	out := prepared
	workload, err := normalizeWorkloadIdentity(prepared.Workload)
	if err != nil {
		return CallLegUsageRecord{}, err
	}
	out.Workload = workload
	out.BLegID = strings.TrimSpace(prepared.BLegID)
	out.Observations = cloneAndCanonicalizeObservations(prepared.Observations)
	out.ObservationRefs = canonicalObservationRefs(prepared.ObservationRefs)
	out.EvidenceConflicts = canonicalEvidenceConflicts(prepared.EvidenceConflicts)
	out.EconomicDispositions = canonicalEconomicEvidenceDispositions(prepared.EconomicDispositions)
	key, err := CallLegUsageKey(out.CallID, out.BLegID)
	if err != nil {
		return CallLegUsageRecord{}, err
	}
	out.Key = key
	fp, err := out.SemanticFingerprint()
	if err != nil {
		return CallLegUsageRecord{}, err
	}
	out.Fingerprint = fp
	return out, nil
}

// SemanticFingerprint computes the immutable evidence hash for a call-leg
// record.
//
// Fingerprint versions:
//   - v1 (legacy): AttemptSeq == 0. The sequence is unknown (pre-fix durable
//     rows have attempt_seq NULL). The byte stream is byte-for-byte identical
//     to the pre-sequence contract so historical fingerprints stay valid.
//   - v2 (sequence-aware): AttemptSeq > 0. The exact b2bua.BLegRecord.Seq is a
//     financial fact and participates in replay identity, so a same-key replay
//     with a different sequence fingerprints differently and conflicts.
//
// A zero AttemptSeq never represents a known sequence; the runtime append seam
// requires a positive sequence for every new record.
func (l CallLegUsageRecord) SemanticFingerprint() (string, error) {
	prepared := l
	if prepared.EvidenceVersion == 0 && (len(prepared.Observations) != 0 || len(prepared.ObservationRefs) != 0 || len(prepared.EvidenceConflicts) != 0 || len(prepared.EconomicDispositions) != 0) {
		prepared.EvidenceVersion = EvidenceFormatVersionV2
	}
	if len(prepared.EconomicDispositions) != 0 && prepared.EconomicEvidenceVersion == 0 {
		prepared.EconomicEvidenceVersion = EconomicEvidenceDispositionVersionV1
	}
	if prepared.EvidenceVersion >= EvidenceFormatVersionV2 && prepared.EvidenceProjection == "" {
		prepared.EvidenceProjection = EvidenceProjectionV1
	}
	if err := prepared.validate(); err != nil {
		return "", err
	}
	l = prepared
	var c canonicalWriter
	c.string("clur")
	c.string(l.CallID.String())
	if strings.TrimSpace(l.SubmissionID) != "" {
		c.string("submission")
		c.string(l.SubmissionID)
	}
	c.string(l.ALegID)
	c.string(strings.TrimSpace(l.BLegID))
	c.string(l.BackendID)
	c.string(l.ProviderID)
	c.string(l.ModelID)
	c.time(l.StartedAt)
	c.time(l.FinishedAt)
	c.string(string(l.Outcome))
	c.string(string(l.Surfaced))
	writeQuantity(&c, l.Evidence.InputTokens)
	writeQuantity(&c, l.Evidence.OutputTokens)
	writeQuantity(&c, l.Evidence.CacheReadTokens)
	writeQuantity(&c, l.Evidence.CacheWriteTokens)
	writeQuantity(&c, l.Evidence.ReasoningTokens)
	writeQuantity(&c, l.Evidence.TotalTokens)
	c.i64(l.Evidence.Cost.NanoUnits)
	c.boolean(l.Evidence.Cost.Present)
	c.string(l.Evidence.Cost.Currency)
	c.string(string(l.Evidence.Source))
	c.string(string(l.Evidence.Authority))
	c.string(l.Evidence.DedupeKey)
	writeVersionRef(&c, l.OperatorRateRef)
	writeWorkloadIdentity(&c, l.Workload)
	if l.EvidenceVersion >= EvidenceFormatVersionV2 {
		c.u64(uint64(l.EvidenceVersion))
		c.string(l.EvidenceProjection)
		observations := canonicalObservations(l.Observations)
		c.u64(uint64(len(observations)))
		for _, observation := range observations {
			// Receipt time is transport metadata. Keep record replay stable when
			// one immutable source revision is delivered through a later store
			// boundary, matching the shared metering replay contract.
			c.string(evidenceReplayFingerprint(observation))
			c.string(observation.IdentityKey())
		}
		refs := canonicalObservationRefs(l.ObservationRefs)
		c.u64(uint64(len(refs)))
		for _, ref := range refs {
			c.string(ref.StoreID)
			c.string(ref.ObservationID)
			c.u64(ref.Revision)
			c.string(ref.PayloadHash)
		}
		conflicts := canonicalEvidenceConflicts(l.EvidenceConflicts)
		c.u64(uint64(len(conflicts)))
		for _, conflict := range conflicts {
			c.string(conflict.Identity)
			c.string(conflict.ExistingHash)
			c.string(conflict.IncomingHash)
			if conflict.HasCoverageMetadata() {
				c.string("coverage-disposition")
				c.string(string(conflict.ExistingCoverage))
				c.string(conflict.ExistingCoverageReason)
				c.string(string(conflict.IncomingCoverage))
				c.string(conflict.IncomingCoverageReason)
			}
		}
		dispositions := canonicalEconomicEvidenceDispositions(l.EconomicDispositions)
		if len(dispositions) != 0 {
			c.u64(uint64(l.EconomicEvidenceVersion))
			c.u64(uint64(len(dispositions)))
			for _, disposition := range dispositions {
				c.string(disposition.ObservationIdentity)
				c.string(disposition.ObservationHash)
				c.string(string(disposition.Coverage))
				c.string(disposition.CoverageReason)
			}
		}
	}
	if l.AttemptSeq > 0 {
		c.u64(uint64(l.AttemptSeq))
	}
	return digest(c.bytes()), nil
}

func CheckCallUsageReplay(existing, incoming CallUsageRecord) error {
	if err := validateSealedCallUsage(existing); err != nil {
		return err
	}
	if err := validateSealedCallUsage(incoming); err != nil {
		return err
	}
	if existing.Key != incoming.Key {
		return ErrReplayKeyMismatch
	}
	if existing.Fingerprint != incoming.Fingerprint {
		return ErrReplayConflict
	}
	return nil
}

func CheckCallLegUsageReplay(existing, incoming CallLegUsageRecord) error {
	if err := validateSealedCallLegUsage(existing); err != nil {
		return err
	}
	if err := validateSealedCallLegUsage(incoming); err != nil {
		return err
	}
	if existing.Key != incoming.Key {
		return ErrReplayKeyMismatch
	}
	if existing.Fingerprint != incoming.Fingerprint {
		return ErrReplayConflict
	}
	return nil
}

func validateSealedCallUsage(r CallUsageRecord) error {
	if strings.TrimSpace(r.Key) == "" || strings.TrimSpace(r.Fingerprint) == "" {
		return fmt.Errorf("%w: replay requires sealed key and fingerprint", ErrInvalidRecord)
	}
	key, err := CallUsageKey(r.CallID)
	if err != nil || key != r.Key {
		return fmt.Errorf("%w: call usage key does not match immutable identity", ErrInvalidRecord)
	}
	fp, err := r.SemanticFingerprint()
	if err != nil {
		return err
	}
	if fp != r.Fingerprint {
		return fmt.Errorf("%w: call usage fingerprint does not match immutable content", ErrInvalidRecord)
	}
	return nil
}

func validateSealedCallLegUsage(l CallLegUsageRecord) error {
	if strings.TrimSpace(l.Key) == "" || strings.TrimSpace(l.Fingerprint) == "" {
		return fmt.Errorf("%w: replay requires sealed key and fingerprint", ErrInvalidRecord)
	}
	key, err := CallLegUsageKey(l.CallID, l.BLegID)
	if err != nil || key != l.Key {
		return fmt.Errorf("%w: call-leg usage key does not match immutable identity", ErrInvalidRecord)
	}
	fp, err := l.SemanticFingerprint()
	if err != nil {
		return err
	}
	if fp != l.Fingerprint {
		return fmt.Errorf("%w: call-leg usage fingerprint does not match immutable content", ErrInvalidRecord)
	}
	return nil
}

func (r CallUsageRecord) validate() error {
	if r.SchemaVersion != CurrentRecordSchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidRecord, r.SchemaVersion)
	}
	if err := r.CallID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	if err := validateOptionalSubmissionID(r.SubmissionID); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"account": r.AccountID, "A-leg": r.ALegID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s identity is required", ErrInvalidRecord, name)
		}
	}
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
		return fmt.Errorf("%w: invalid call usage timestamps", ErrInvalidRecord)
	}
	if !validTurnOutcome(r.Outcome) {
		return fmt.Errorf("%w: unknown call outcome %q", ErrInvalidRecord, r.Outcome)
	}
	if _, err := normalizeWorkloadIdentity(r.Workload); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(r.ExpectedBLegIDs))
	for _, id := range r.ExpectedBLegIDs {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id {
			return fmt.Errorf("%w: expected B-leg identity is required", ErrInvalidRecord)
		}
		if strings.Contains(id, ":") {
			return fmt.Errorf("%w: B-leg identity must not contain ':'", ErrInvalidRecord)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: duplicate expected B-leg %q", ErrInvalidRecord, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (l CallLegUsageRecord) validate() error {
	if err := l.CallID.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRecord, err)
	}
	if err := validateOptionalSubmissionID(l.SubmissionID); err != nil {
		return err
	}
	if strings.TrimSpace(l.ALegID) == "" || strings.TrimSpace(l.BLegID) == "" {
		return fmt.Errorf("%w: call-leg lineage is required", ErrInvalidRecord)
	}
	if strings.Contains(l.BLegID, ":") {
		return fmt.Errorf("%w: B-leg identity must not contain ':'", ErrInvalidRecord)
	}
	if strings.TrimSpace(l.BackendID) == "" || strings.TrimSpace(l.ProviderID) == "" || strings.TrimSpace(l.ModelID) == "" {
		return fmt.Errorf("%w: call-leg identity/provider/model fields are required", ErrInvalidRecord)
	}
	if l.StartedAt.IsZero() || l.FinishedAt.IsZero() || l.FinishedAt.Before(l.StartedAt) {
		return fmt.Errorf("%w: invalid call-leg timestamps", ErrInvalidRecord)
	}
	if !validLegOutcome(l.Outcome) || !validSurfacedState(l.Surfaced) {
		return fmt.Errorf("%w: invalid call-leg outcome/surfaced state", ErrInvalidRecord)
	}
	if _, err := normalizeWorkloadIdentity(l.Workload); err != nil {
		return err
	}
	// AttemptSeq == 0 means the sequence is absent (legacy v1 row); a positive
	// value is the exact B2BUA attempt sequence. Negative values are nonsense.
	if l.AttemptSeq < 0 {
		return fmt.Errorf("%w: call-leg attempt sequence cannot be negative", ErrInvalidRecord)
	}
	if err := validateEvidence(l.Evidence); err != nil {
		return err
	}
	if l.EvidenceVersion != 0 && l.EvidenceVersion != EvidenceFormatVersionV2 {
		return fmt.Errorf("%w: unsupported evidence format version %d", ErrInvalidRecord, l.EvidenceVersion)
	}
	if l.EvidenceVersion == 0 && (len(l.Observations) != 0 || len(l.ObservationRefs) != 0 || len(l.EvidenceConflicts) != 0 || len(l.EconomicDispositions) != 0) {
		return fmt.Errorf("%w: V2 evidence requires format version %d", ErrInvalidRecord, EvidenceFormatVersionV2)
	}
	if l.EvidenceVersion == 0 && l.EvidenceProjection != "" {
		return fmt.Errorf("%w: V1 evidence cannot carry a V2 projection label", ErrInvalidRecord)
	}
	if l.EvidenceVersion >= EvidenceFormatVersionV2 && l.EvidenceProjection != EvidenceProjectionV1 {
		return fmt.Errorf("%w: V2 evidence requires projection label %q", ErrInvalidRecord, EvidenceProjectionV1)
	}
	if l.EconomicEvidenceVersion != 0 && l.EconomicEvidenceVersion != EconomicEvidenceDispositionVersionV1 {
		return fmt.Errorf("%w: unsupported economic evidence disposition version %d", ErrInvalidRecord, l.EconomicEvidenceVersion)
	}
	if l.EconomicEvidenceVersion == 0 && len(l.EconomicDispositions) != 0 {
		return fmt.Errorf("%w: economic dispositions require format version %d", ErrInvalidRecord, EconomicEvidenceDispositionVersionV1)
	}
	if l.EconomicEvidenceVersion == EconomicEvidenceDispositionVersionV1 && len(l.EconomicDispositions) == 0 {
		return fmt.Errorf("%w: economic evidence disposition version requires dispositions", ErrInvalidRecord)
	}
	if len(l.EconomicDispositions) > MaxCallLegEvidenceObservations {
		return fmt.Errorf("%w: economic evidence disposition bound exceeded", ErrInvalidRecord)
	}
	if len(l.Observations) > MaxCallLegEvidenceObservations {
		return fmt.Errorf("%w: call-leg observation bound exceeded", ErrInvalidRecord)
	}
	if len(l.ObservationRefs) > MaxCallLegEvidenceRefs {
		return fmt.Errorf("%w: call-leg observation reference bound exceeded", ErrInvalidRecord)
	}
	if len(l.EvidenceConflicts) > MaxCallLegEvidenceConflicts {
		return fmt.Errorf("%w: call-leg evidence conflict bound exceeded", ErrInvalidRecord)
	}
	if err := validateCallLegObservations(l); err != nil {
		return err
	}
	if err := validateEconomicEvidenceDispositions(l); err != nil {
		return err
	}
	seenRefs := make(map[string]struct{}, len(l.ObservationRefs))
	for i, ref := range l.ObservationRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: observation ref %d: %w", ErrInvalidRecord, i, err)
		}
		key := ref.StoreID + "\x00" + ref.ObservationID + "\x00" + strconv.FormatUint(ref.Revision, 10)
		if _, exists := seenRefs[key]; exists {
			return fmt.Errorf("%w: duplicate observation ref %q", ErrInvalidRecord, key)
		}
		seenRefs[key] = struct{}{}
	}
	for i, conflict := range l.EvidenceConflicts {
		if !conflict.valid() {
			return fmt.Errorf("%w: invalid evidence conflict %d", ErrInvalidRecord, i)
		}
	}
	return nil
}

func validateOptionalSubmissionID(id string) error {
	if id == "" {
		return nil
	}
	if id != strings.TrimSpace(id) || len(id) > 512 || !utf8.ValidString(id) {
		return fmt.Errorf("%w: invalid submission identity", ErrInvalidRecord)
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f || !unicode.IsPrint(r) {
			return fmt.Errorf("%w: invalid submission identity", ErrInvalidRecord)
		}
	}
	return nil
}
