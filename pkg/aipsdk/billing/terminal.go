package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

const (
	// TerminalEnvelopeVersionV1 is the first version of the terminal envelope.
	TerminalEnvelopeVersionV1 uint32 = 1
	// TerminalEnvelopeVersionV2 additionally carries the exact sealed
	// usage-evidence record behind the optional RecordCarrier sink
	// declaration. Version 1 keeps its fields, validation, and payload-hash
	// identity unchanged.
	TerminalEnvelopeVersionV2 uint32 = 2
	// MaxObservationsPerEnvelope bounds neutral evidence per terminal handoff.
	MaxObservationsPerEnvelope = 1024
	// MaxExpectedBLegIDs bounds call-closure B-leg coverage per envelope.
	MaxExpectedBLegIDs = 1024
	// MaxTerminalRecordBytes bounds one carried usage-evidence record payload.
	MaxTerminalRecordBytes = 4 << 20
)

var (
	// ErrInvalidTerminal identifies a malformed terminal handoff DTO.
	ErrInvalidTerminal = errors.New("billing: invalid terminal handoff")
	// ErrTerminalAckMismatch identifies a structurally valid acknowledgement
	// that answers a different envelope. Store and envelope identity must
	// match the sent envelope exactly; a foreign ack is never durable success.
	ErrTerminalAckMismatch = errors.New("billing: terminal acknowledgement does not match envelope")
)

// TerminalOutcome classifies the execution closure carried by an envelope.
type TerminalOutcome string

const (
	// TerminalCompleted marks successful execution closure.
	TerminalCompleted TerminalOutcome = "completed"
	// TerminalFailed marks failed execution closure; incurred usage remains.
	TerminalFailed TerminalOutcome = "failed"
	// TerminalCanceled marks canceled execution closure; incurred usage remains.
	TerminalCanceled TerminalOutcome = "canceled"
	// TerminalUnresolved is the version-2 carriage label for a record
	// disposition that establishes no execution closure: a B-leg that was
	// rejected or never started, or a record whose disposition is unknown. The
	// carried record remains authoritative and keeps the exact disposition;
	// version 1 keeps the closure-only vocabulary and rejects this value.
	TerminalUnresolved TerminalOutcome = "unresolved"
)

// IsKnown reports whether o is a documented execution-closure outcome.
func (o TerminalOutcome) IsKnown() bool {
	switch o {
	case TerminalCompleted, TerminalFailed, TerminalCanceled:
		return true
	default:
		return false
	}
}

// isCarriageKnown reports whether o is accepted by version-2 envelopes: the
// closure vocabulary plus the non-closure carriage label.
func (o TerminalOutcome) isCarriageKnown() bool {
	return o.IsKnown() || o == TerminalUnresolved
}

// TerminalWorkload carries parent/workload correlation for one terminal
// envelope. Class and role are bounded safe labels, never request content.
// A nil workload means the closure carries no workload attribution.
type TerminalWorkload struct {
	Class string `json:"class,omitempty"`
	Role  string `json:"role,omitempty"`
}

// Validate checks the bounded workload labels.
func (w TerminalWorkload) Validate() error {
	if err := validateOptionalRef("terminal workload class", w.Class, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	return validateOptionalRef("terminal workload role", w.Role, MaxIdentityBytes)
}

// IsZero reports whether the workload carries no attribution.
func (w TerminalWorkload) IsZero() bool { return w.Class == "" && w.Role == "" }

// Clone returns an independent copy of the workload.
func (w TerminalWorkload) Clone() TerminalWorkload { return w }

// TerminalUsageRecord carries one exact sealed usage-evidence record: the
// canonical persisted JSON plus the record identity used for durable replay.
// The payload schema is owned by the open-source usage-evidence contract; this
// SDK carries the bytes opaquely and never interprets them.
type TerminalUsageRecord struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	JSON        []byte `json:"json"`
}

// Clone returns an independent copy of the carried record and its bytes.
func (r TerminalUsageRecord) Clone() TerminalUsageRecord {
	out := r
	out.JSON = append([]byte(nil), r.JSON...)
	return out
}

// TerminalEnvelope is the public terminal evidence handoff. It carries format
// version, store/customer scope, call/attempt closure identity and outcome,
// the authoritative attempt sequence, frozen rate/policy references, neutral
// V2 observations, parent/workload coverage, and a payload hash. Version 2
// additionally carries the exact sealed usage-evidence record. It contains
// no SQL driver, executor pointer, request body, or mutable service map.
type TerminalEnvelope struct {
	Version         uint32                      `json:"version"`
	EnvelopeID      string                      `json:"envelope_id"`
	StoreID         string                      `json:"store_id"`
	Scope           scope.PrincipalScopeView    `json:"scope"`
	Subject         metering.SubjectRef         `json:"subject"`
	Correlation     metering.CorrelationV2      `json:"correlation"`
	Outcome         TerminalOutcome             `json:"outcome"`
	AttemptSeq      uint64                      `json:"attempt_seq"`
	AccountID       string                      `json:"account_id,omitempty"`
	ALegID          string                      `json:"a_leg_id,omitempty"`
	SubmissionID    string                      `json:"submission_id,omitempty"`
	Workload        *TerminalWorkload           `json:"workload,omitempty"`
	ExpectedBLegIDs []string                    `json:"expected_b_leg_ids,omitempty"`
	Tariff          economics.RatingSnapshotRef `json:"tariff"`
	Policy          economics.PolicySnapshotRef `json:"policy"`
	Observations    []metering.Observation      `json:"observations,omitempty"`
	ParentWorkID    string                      `json:"parent_work_id,omitempty"`
	PayloadHash     string                      `json:"payload_hash"`
	// Record carries the exact sealed usage-evidence record for version-2
	// envelopes; it is nil for version 1, whose payload hash keeps the
	// key+fingerprint identity.
	Record *TerminalUsageRecord `json:"record,omitempty"`
}

// Validate checks envelope identity, call/leg lineage, frozen references,
// neutral evidence, closure coverage, and payload integrity. Version 2
// additionally requires the exact sealed record payload and a payload hash
// over its bytes; version 1 keeps every existing rule unchanged.
//
// Lineage rules: B-leg closures require A-leg identity and a positive
// authoritative attempt sequence; call closures require the customer account
// and A-leg identity but carry no leg sequence. Submission and workload are
// optional attribution present only when the closure carries them.
func (e TerminalEnvelope) Validate() error {
	if e.Version != TerminalEnvelopeVersionV1 && e.Version != TerminalEnvelopeVersionV2 {
		return fmt.Errorf("%w: unsupported envelope version %d", ErrInvalidTerminal, e.Version)
	}
	if err := validateRef("terminal envelope_id", e.EnvelopeID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if err := validateRef("terminal store_id", e.StoreID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if err := e.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %v", ErrInvalidTerminal, err)
	}
	if e.Subject.StoreID != e.StoreID {
		return fmt.Errorf("%w: subject store %q differs from envelope store %q", ErrInvalidTerminal, e.Subject.StoreID, e.StoreID)
	}
	if err := e.Correlation.Validate(); err != nil {
		return fmt.Errorf("%w: correlation: %v", ErrInvalidTerminal, err)
	}
	if e.Correlation.StoreID != e.StoreID {
		return fmt.Errorf("%w: correlation store %q differs from envelope store %q", ErrInvalidTerminal, e.Correlation.StoreID, e.StoreID)
	}
	// Monetary closures must carry the frozen trusted customer scope. The
	// runtime freezes request scope onto every terminal handoff context, so
	// a scope-free envelope is unattributable, never merely degraded.
	if e.Subject.Kind == metering.SubjectBLeg || e.Subject.Kind == metering.SubjectBillingCall {
		if !e.Scope.PrincipalID.IsKnown() {
			return fmt.Errorf("%w: monetary closure requires trusted customer scope with known principal", ErrInvalidTerminal)
		}
	}
	// The execution-closure vocabulary is exact for version 1. Version 2 adds
	// the coarse non-closure carriage label; the sealed record it carries
	// stays the exact disposition authority.
	knownOutcome := e.Outcome.IsKnown()
	if e.Version == TerminalEnvelopeVersionV2 {
		knownOutcome = e.Outcome.isCarriageKnown()
	}
	if !knownOutcome {
		return fmt.Errorf("%w: unknown outcome %q", ErrInvalidTerminal, e.Outcome)
	}
	switch e.Subject.Kind {
	case metering.SubjectBLeg:
		if strings.TrimSpace(e.ALegID) == "" {
			return fmt.Errorf("%w: b-leg closure requires a-leg identity", ErrInvalidTerminal)
		}
		if e.AttemptSeq == 0 {
			return fmt.Errorf("%w: b-leg closure requires a positive authoritative attempt sequence", ErrInvalidTerminal)
		}
	case metering.SubjectBillingCall:
		if strings.TrimSpace(e.AccountID) == "" {
			return fmt.Errorf("%w: call closure requires customer account identity", ErrInvalidTerminal)
		}
		if strings.TrimSpace(e.ALegID) == "" {
			return fmt.Errorf("%w: call closure requires a-leg identity", ErrInvalidTerminal)
		}
	}
	if err := validateOptionalRef("terminal account_id", e.AccountID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if err := validateOptionalRef("terminal a_leg_id", e.ALegID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if err := validateOptionalRef("terminal submission_id", e.SubmissionID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if err := checkLineageAgreement(e); err != nil {
		return err
	}
	if e.Workload != nil {
		if err := e.Workload.Validate(); err != nil {
			return fmt.Errorf("%w: workload: %v", ErrInvalidTerminal, err)
		}
	}
	if err := validateExpectedBLegIDs(e.ExpectedBLegIDs); err != nil {
		return err
	}
	if err := validateSnapshotRef("terminal tariff", e.Tariff.ID, e.Tariff.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if err := validateSnapshotRef("terminal policy", e.Policy.ID, e.Policy.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if err := validateRef("terminal policy_id", e.Policy.PolicyID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if len(e.Observations) > MaxObservationsPerEnvelope {
		return fmt.Errorf("%w: observation bound exceeded", ErrInvalidTerminal)
	}
	for i, observation := range e.Observations {
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("%w: observation %d: %v", ErrInvalidTerminal, i, err)
		}
		if observation.Subject.StoreID != e.StoreID {
			return fmt.Errorf("%w: observation %d store %q differs from envelope store %q", ErrInvalidTerminal, i, observation.Subject.StoreID, e.StoreID)
		}
		if err := validateObservationLineage(e, i, observation); err != nil {
			return err
		}
	}
	if err := validateOptionalRef("terminal parent_work_id", e.ParentWorkID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if err := validatePayloadHash("terminal payload_hash", e.PayloadHash); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if err := e.validateRecordCarriage(); err != nil {
		return err
	}
	return nil
}

// validateRecordCarriage enforces the versioned record-carriage contract:
// version 1 carries no record, and version 2 requires one bounded, valid-JSON
// payload whose SHA-256 the envelope payload hash must cover exactly.
func (e TerminalEnvelope) validateRecordCarriage() error {
	switch e.Version {
	case TerminalEnvelopeVersionV1:
		if e.Record != nil {
			return fmt.Errorf("%w: version %d must not carry a record payload", ErrInvalidTerminal, e.Version)
		}
		return nil
	case TerminalEnvelopeVersionV2:
		if e.Record == nil {
			return fmt.Errorf("%w: version %d requires a record payload", ErrInvalidTerminal, e.Version)
		}
		record := *e.Record
		if err := validateRef("terminal record key", record.Key, MaxIdentityBytes); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
		}
		if record.Key != e.EnvelopeID {
			return fmt.Errorf("%w: record key %q differs from envelope identity %q", ErrInvalidTerminal, record.Key, e.EnvelopeID)
		}
		if err := validateRef("terminal record fingerprint", record.Fingerprint, MaxIdentityBytes); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
		}
		if len(record.JSON) == 0 {
			return fmt.Errorf("%w: record payload is required", ErrInvalidTerminal)
		}
		if len(record.JSON) > MaxTerminalRecordBytes {
			return fmt.Errorf("%w: record payload exceeds %d bytes", ErrInvalidTerminal, MaxTerminalRecordBytes)
		}
		if !json.Valid(record.JSON) {
			return fmt.Errorf("%w: record payload must be valid JSON", ErrInvalidTerminal)
		}
		sum := sha256.Sum256(record.JSON)
		if e.PayloadHash != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("%w: payload hash does not cover the carried record bytes", ErrInvalidTerminal)
		}
		return nil
	default:
		return nil
	}
}

// checkLineageAgreement enforces that envelope-level lineage agrees with
// the subject carrier when both state a value. Empty on either side is not
// a conflict: subject and envelope are alternate placement details. For a
// B-leg envelope, a present subject attempt sequence must equal the
// top-level authoritative sequence and the correlation sequence.
func checkLineageAgreement(e TerminalEnvelope) error {
	pairs := [][3]string{
		{"a_leg_id", e.ALegID, e.Subject.ALegID},
		{"submission_id", e.SubmissionID, e.Subject.SubmissionID},
		{"account_id", e.AccountID, e.Subject.AccountID},
	}
	for _, pair := range pairs {
		if pair[1] != "" && pair[2] != "" && pair[1] != pair[2] {
			return fmt.Errorf("%w: envelope/subject %s mismatch", ErrInvalidTerminal, pair[0])
		}
	}
	if e.Subject.Kind == metering.SubjectBLeg && e.Subject.AttemptSeq != 0 {
		if e.Subject.AttemptSeq != e.AttemptSeq {
			return fmt.Errorf("%w: envelope/subject attempt sequence mismatch", ErrInvalidTerminal)
		}
		if e.Correlation.AttemptSeq != 0 && e.Subject.AttemptSeq != e.Correlation.AttemptSeq {
			return fmt.Errorf("%w: subject/correlation attempt sequence mismatch", ErrInvalidTerminal)
		}
	}
	return validateCorrelationLineage(e)
}

// validateCorrelationLineage binds the envelope's own correlation carrier
// to the frozen top-level/subject lineage for every present applicable
// field. It runs independently of observations: a foreign correlation call
// fails even on an observation-less envelope. Fields without an envelope
// counterpart (protocol/request IDs, provider and resource references,
// window/statement lineage) are the correlation's own scope and are not
// compared here.
func validateCorrelationLineage(e TerminalEnvelope) error {
	mismatch := func(field, got, want string) error {
		return fmt.Errorf("%w: envelope correlation %s %q differs from envelope %q", ErrInvalidTerminal, field, got, want)
	}
	call, leg := e.Subject.BillingCallID, e.Subject.BLegID
	aleg := firstPresentLineage(e.ALegID, e.Subject.ALegID)
	submission := firstPresentLineage(e.SubmissionID, e.Subject.SubmissionID)
	cor := e.Correlation
	if cor.BillingCallID != "" && call != "" && cor.BillingCallID != call {
		return mismatch("call", cor.BillingCallID, call)
	}
	if cor.BLegID != "" && leg != "" && cor.BLegID != leg {
		return mismatch("b-leg", cor.BLegID, leg)
	}
	if cor.ALegID != "" && aleg != "" && cor.ALegID != aleg {
		return mismatch("a-leg", cor.ALegID, aleg)
	}
	if cor.SubmissionID != "" && submission != "" && cor.SubmissionID != submission {
		return mismatch("submission", cor.SubmissionID, submission)
	}
	if cor.AttemptSeq != 0 && cor.AttemptSeq != e.AttemptSeq {
		return mismatch("attempt sequence", formatSeq(cor.AttemptSeq), formatSeq(e.AttemptSeq))
	}
	if cor.ParentWorkID != "" && e.ParentWorkID != "" && cor.ParentWorkID != e.ParentWorkID {
		return mismatch("parent work", cor.ParentWorkID, e.ParentWorkID)
	}
	return nil
}

// isAllowanceSubject reports whether the kind carries non-request
// economics: account-window gauges, resource intervals, and statement lines
// are never request-attributed. The gate is the typed kind, so a
// request-scoped observation cannot evade lineage checks by omitting
// identity fields.
func isAllowanceSubject(kind metering.SubjectKind) bool {
	switch kind {
	case metering.SubjectAccountWindow, metering.SubjectResource, metering.SubjectStatementLine:
		return true
	default:
		return false
	}
}

// validateObservationLineage enforces cross-field consistency between the
// envelope lineage and each carried observation.
//
// Request-attributed observations (A-leg, request, call, B-leg, submission,
// provider charge/debit) compare every present applicable field — account,
// call, A-leg, submission, B-leg, authoritative sequence, parent work, and
// trusted scope identity — against the frozen envelope lineage. Absent
// fields are not conflicts, but the metering contract already requires
// kind-mandatory identity, so omission cannot dodge the gate.
//
// Allowance observations (account-window, resource, statement-line) keep the
// documented store-only relationship, but only when they are explicitly
// un-attributed: any present request attribution (call, leg, A-leg,
// submission, sequence, or parent reference) routes them through the full
// request checks instead. The metering per-kind lineage prohibitions make
// that routing unreachable for well-formed allowance evidence; the gate
// exists so the exemption can never silently widen.
func validateObservationLineage(e TerminalEnvelope, i int, observation metering.Observation) error {
	if isAllowanceSubject(observation.Subject.Kind) && !observationCarriesRequestAttribution(observation) {
		return validateScopeAgreement(e, i, observation.Scope)
	}
	mismatch := func(field, got, want string) error {
		return fmt.Errorf("%w: observation %d %s %q differs from envelope %q", ErrInvalidTerminal, i, field, got, want)
	}
	call, leg := e.Subject.BillingCallID, e.Subject.BLegID
	aleg := firstPresentLineage(e.ALegID, e.Subject.ALegID)
	submission := firstPresentLineage(e.SubmissionID, e.Subject.SubmissionID)
	account := firstPresentLineage(e.AccountID, e.Subject.AccountID)
	parent := e.ParentWorkID
	sub := observation.Subject
	if sub.BillingCallID != "" && call != "" && sub.BillingCallID != call {
		return mismatch("call", sub.BillingCallID, call)
	}
	if sub.BLegID != "" && leg != "" && sub.BLegID != leg {
		return mismatch("b-leg", sub.BLegID, leg)
	}
	if sub.ALegID != "" && aleg != "" && sub.ALegID != aleg {
		return mismatch("a-leg", sub.ALegID, aleg)
	}
	if sub.SubmissionID != "" && submission != "" && sub.SubmissionID != submission {
		return mismatch("submission", sub.SubmissionID, submission)
	}
	if sub.AttemptSeq != 0 && e.Subject.Kind == metering.SubjectBLeg && e.AttemptSeq != 0 && sub.AttemptSeq != e.AttemptSeq {
		return mismatch("attempt sequence", formatSeq(sub.AttemptSeq), formatSeq(e.AttemptSeq))
	}
	if sub.AccountID != "" && account != "" && sub.AccountID != account {
		return mismatch("account", sub.AccountID, account)
	}
	cor := observation.Correlation
	if cor.BillingCallID != "" && call != "" && cor.BillingCallID != call {
		return mismatch("correlation call", cor.BillingCallID, call)
	}
	if cor.BLegID != "" && leg != "" && cor.BLegID != leg {
		return mismatch("correlation b-leg", cor.BLegID, leg)
	}
	if cor.ALegID != "" && aleg != "" && cor.ALegID != aleg {
		return mismatch("correlation a-leg", cor.ALegID, aleg)
	}
	if cor.SubmissionID != "" && submission != "" && cor.SubmissionID != submission {
		return mismatch("correlation submission", cor.SubmissionID, submission)
	}
	if cor.AttemptSeq != 0 && e.Subject.Kind == metering.SubjectBLeg && e.AttemptSeq != 0 && cor.AttemptSeq != e.AttemptSeq {
		return mismatch("correlation attempt sequence", formatSeq(cor.AttemptSeq), formatSeq(e.AttemptSeq))
	}
	if cor.ParentWorkID != "" && parent != "" && cor.ParentWorkID != parent {
		return mismatch("correlation parent work", cor.ParentWorkID, parent)
	}
	return validateScopeAgreement(e, i, observation.Scope)
}

// observationCarriesRequestAttribution reports whether an observation names
// any request lineage: call, leg, A-leg, submission, attempt sequence, or a
// parent work reference, on either the subject or the correlation carrier.
func observationCarriesRequestAttribution(observation metering.Observation) bool {
	sub := observation.Subject
	if sub.BillingCallID != "" || sub.BLegID != "" || sub.ALegID != "" ||
		sub.SubmissionID != "" || sub.AttemptSeq != 0 {
		return true
	}
	cor := observation.Correlation
	return cor.BillingCallID != "" || cor.BLegID != "" || cor.ALegID != "" ||
		cor.SubmissionID != "" || cor.AttemptSeq != 0 || cor.ParentWorkID != ""
}

// validateScopeAgreement compares every applicable trusted scope identity
// field with an envelope counterpart: principal plus tenant, organization,
// workspace, project, department, and cost-center IDs. Each axis applies
// only when both sides state a known value; descriptive and operational
// scope fields (display name, auth method, roles, claims, origin) are not
// monetary identity and are never compared.
func validateScopeAgreement(e TerminalEnvelope, i int, observed scope.PrincipalScopeView) error {
	type idField struct {
		name string
		a, b scope.Value
	}
	for _, field := range []idField{
		{"principal", observed.PrincipalID, e.Scope.PrincipalID},
		{"tenant", observed.TenantID, e.Scope.TenantID},
		{"organization", observed.OrganizationID, e.Scope.OrganizationID},
		{"workspace", observed.WorkspaceID, e.Scope.WorkspaceID},
		{"project", observed.ProjectID, e.Scope.ProjectID},
		{"department", observed.DepartmentID, e.Scope.DepartmentID},
		{"cost center", observed.CostCenterID, e.Scope.CostCenterID},
	} {
		if field.a.IsKnown() && field.b.IsKnown() && !field.a.Equal(field.b) {
			return fmt.Errorf("%w: observation %d scope %s differs from envelope scope", ErrInvalidTerminal, i, field.name)
		}
	}
	return nil
}

// firstPresentLineage prefers the envelope-level lineage value, falling back
// to the subject carrier. Envelope/subject agreement itself is enforced by
// checkLineageAgreement, so either placement identifies the same lineage.
func firstPresentLineage(top, subject string) string {
	if top != "" {
		return top
	}
	return subject
}

// formatSeq renders an attempt sequence for mismatch diagnostics.
func formatSeq(seq uint64) string {
	return strconv.FormatUint(seq, 10)
}

// validateExpectedBLegIDs enforces deterministic call-closure coverage:
// bounded unique well-formed B-leg IDs with no key separator.
func validateExpectedBLegIDs(ids []string) error {
	if len(ids) > MaxExpectedBLegIDs {
		return fmt.Errorf("%w: expected B-leg coverage bound exceeded", ErrInvalidTerminal)
	}
	seen := make(map[string]struct{}, len(ids))
	for i, id := range ids {
		if err := validateRef("terminal expected b-leg id", id, MaxIdentityBytes); err != nil {
			return fmt.Errorf("%w: coverage %d: %v", ErrInvalidTerminal, i, err)
		}
		if strings.Contains(id, ":") {
			return fmt.Errorf("%w: coverage %d B-leg identity must not contain ':'", ErrInvalidTerminal, i)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: duplicate coverage B-leg %q", ErrInvalidTerminal, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// Clone returns an independent copy of the envelope. Coverage order and the
// authoritative sequence are preserved as carried; only the slices, the
// workload pointer, and the version-2 record payload are deep-copied, never
// reordered.
func (e TerminalEnvelope) Clone() TerminalEnvelope {
	out := e
	out.Scope = e.Scope.Clone()
	out.Subject = e.Subject.Clone()
	if e.Observations != nil {
		out.Observations = make([]metering.Observation, len(e.Observations))
		for i, observation := range e.Observations {
			out.Observations[i] = observation.Clone()
		}
	}
	if e.Workload != nil {
		workload := e.Workload.Clone()
		out.Workload = &workload
	}
	out.ExpectedBLegIDs = append([]string(nil), e.ExpectedBLegIDs...)
	if e.Record != nil {
		record := e.Record.Clone()
		out.Record = &record
	}
	return out
}

// TerminalAck is the idempotent durable acknowledgement for one envelope.
type TerminalAck struct {
	StoreID    string `json:"store_id"`
	EnvelopeID string `json:"envelope_id"`
	Revision   uint64 `json:"revision"`
}

// Validate requires the acknowledged envelope identity and a nonzero durable
// revision.
func (a TerminalAck) Validate() error {
	if err := validateRef("terminal ack store_id", a.StoreID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if err := validateRef("terminal ack envelope_id", a.EnvelopeID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTerminal, err)
	}
	if a.Revision == 0 {
		return fmt.Errorf("%w: durable revision required", ErrInvalidTerminal)
	}
	return nil
}

// Clone returns an independent copy of the acknowledgement.
func (a TerminalAck) Clone() TerminalAck { return a }

// MatchesEnvelope reports whether the acknowledgement answers exactly the
// sent envelope, identified by store and envelope ID.
func (a TerminalAck) MatchesEnvelope(env TerminalEnvelope) bool {
	return a.StoreID == env.StoreID && a.EnvelopeID == env.EnvelopeID
}
