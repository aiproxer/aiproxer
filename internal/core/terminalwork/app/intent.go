package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

const intentIdentityVersion = 1

// IntentStore persists durable terminal-work intents before promotion.
type IntentStore interface {
	AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error
	PromotePending(ctx context.Context, cmd terminalwork.PromotePendingCommand) error
}

// IntentLookup reconciles AppendIntent outcomes with unambiguous
// (record, found, error) semantics. Production stores implement this; absence
// of a definitive lookup is treated as ambiguous ownership.
type IntentLookup interface {
	LookupIntent(ctx context.Context, workID string) (rec terminalwork.WorkRecord, found bool, err error)
}

// AppendIntentOutcomeStore is the definitive production append seam. Inserted
// and Replay are mutually exclusive; ambiguous transport/store errors return
// neither flag with a non-nil error.
type AppendIntentOutcomeStore interface {
	AppendIntentOutcome(ctx context.Context, rec terminalwork.WorkRecord) (terminalwork.AppendIntentOutcome, error)
}

// AmbiguousAppend describes an append whose durable outcome is not known and
// must not be parked in GenerationPinTracker without a reconciliation owner.
type AmbiguousAppend struct {
	WorkID string
	Record terminalwork.WorkRecord
	Pin    genpin.Pin
	Cause  error
}

// AmbiguousAppendHandoff is the subtask-B seam that takes candidate ownership
// for asynchronous reconciliation. Tests may supply a fake that retains the pin.
type AmbiguousAppendHandoff interface {
	Take(ctx context.Context, amb AmbiguousAppend) error
}

// ErrAppendReconcileAmbiguous means AppendIntent failed and lookup could not
// confirm absence or classify durable state for safe ownership.
var ErrAppendReconcileAmbiguous = errors.New("terminalwork: append reconcile ambiguous")

// ErrAmbiguousAppendReconcilerNotConfigured means an ambiguous append occurred
// and no AmbiguousAppendHandoff was wired. The candidate pin is released; this
// does not claim durable no-drop (subtask B wires the reconciler).
var ErrAmbiguousAppendReconcilerNotConfigured = errors.New("terminalwork: ambiguous append reconciler not configured")

// ErrIntentReplayConflict means an existing WorkID row conflicts with this accept.
var ErrIntentReplayConflict = errors.New("terminalwork: intent replay conflict")

// ExecutablePendingBinder registers WorkID-keyed executable-generation pending
// ownership without importing snapshotgen into terminalwork (task 3.6).
// Bind returns a release handle only when this call created the hold (ok=true).
// When AddPendingWork is idempotent no-op (ok=false), no release handle is
// returned so a loser candidate cannot release the winner's WorkID.
type ExecutablePendingBinder interface {
	Bind(workID string, versions terminalwork.BoundVersions) (release func(), ok bool)
}

// IntentServiceConfig configures IntentService clocks and optional runtime
// generation pin tracking for durable intents that outlive the HTTP lease.
type IntentServiceConfig struct {
	Clock func() time.Time
	// Pins retains runtime-generation pins for accepted durable work (task 3.6).
	// Nil disables pinning (legacy/test paths).
	Pins *GenerationPinTracker
	// ExecutablePending binds WorkID-keyed executable-generation pending ownership.
	ExecutablePending ExecutablePendingBinder
	// AmbiguousHandoff receives candidate ownership when append outcome is
	// ambiguous (subtask B). Nil releases the candidate and returns
	// ErrAmbiguousAppendReconcilerNotConfigured.
	AmbiguousHandoff AmbiguousAppendHandoff
}

// IntentService accepts durable settle/release intents with privacy-safe rows
// (requirements 8.3, 8.7–8.9, 12.8; design D9, D14).
type IntentService struct {
	store   IntentStore
	clock   func() time.Time
	pins    *GenerationPinTracker
	exec    ExecutablePendingBinder
	handoff AmbiguousAppendHandoff
}

// NewIntentService returns an intent accepter backed by store.
func NewIntentService(store IntentStore, cfg IntentServiceConfig) *IntentService {
	clock := cfg.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &IntentService{
		store:   store,
		clock:   clock,
		pins:    cfg.Pins,
		exec:    cfg.ExecutablePending,
		handoff: cfg.AmbiguousHandoff,
	}
}

// SetExecutablePending wires WorkID-keyed executable pending ownership after
// composition (e.g. when SnapshotPublisher becomes available).
func (s *IntentService) SetExecutablePending(b ExecutablePendingBinder) {
	if s == nil {
		return
	}
	s.exec = b
}

// SettleFailureInput describes one failed provider settle for durable recovery.
type SettleFailureInput struct {
	RequestID  string
	AttemptID  string
	TraceID    string
	ProviderID string
	Handles    []string
	Versions   terminalwork.BoundVersions
}

// ReleaseFailureInput describes one failed provider release for durable recovery.
type ReleaseFailureInput struct {
	RequestID  string
	AttemptID  string
	TraceID    string
	ProviderID string
	Handle     string
	Versions   terminalwork.BoundVersions
}

// AcceptSettleFailure appends and promotes a settle-request-provider intent.
// Raw cause text is never persisted (design D14). WorkID/SourceKey are hash-based
// and include AttemptID so repeated request actions do not collide. Payload
// handles are sorted identically to the identity material for SameIntentReplay.
func (s *IntentService) AcceptSettleFailure(ctx context.Context, in SettleFailureInput) error {
	if s == nil || s.store == nil {
		return ErrNilIntentStore
	}
	providerID := strings.TrimSpace(in.ProviderID)
	requestID := strings.TrimSpace(in.RequestID)
	if providerID == "" || requestID == "" {
		return fmt.Errorf("%w: settle intent identity", sdk.ErrInvalid)
	}
	handles := cleanHandles(in.Handles)
	attemptID := strings.TrimSpace(in.AttemptID)
	traceID := strings.TrimSpace(in.TraceID)
	workID, sourceKey := durableWorkIdentity(sdk.WorkKindSettleRequestProvider, requestID, attemptID, providerID, handles)
	payload, err := safeHandlesPayload(handles)
	if err != nil {
		return err
	}
	versions := in.Versions
	if strings.TrimSpace(versions.ProviderID) == "" {
		versions.ProviderID = providerID
	}
	rec := terminalwork.WorkRecord{
		WorkID:         workID,
		SourceKey:      sourceKey,
		PayloadVersion: 1,
		Kind:           sdk.WorkKindSettleRequestProvider,
		State:          sdk.WorkStateIntent,
		ProviderID:     providerID,
		Lifecycle: terminalwork.LifecycleCorrelation{
			RequestID: requestID,
			AttemptID: attemptID,
			TraceID:   traceID,
		},
		Versions: versions,
		Payload:  payload,
		Error: terminalwork.BoundedError{
			Code:    "outage",
			Message: "provider settle failed",
		},
	}
	return s.accept(ctx, rec)
}

// LeaseSetReleaseInput describes durable lease-set release/rollback work.
type LeaseSetReleaseInput struct {
	RequestID  string
	AttemptID  string
	TraceID    string
	LeaseSetID string
	Reason     string
	Versions   terminalwork.BoundVersions
}

// AcceptLeaseSetRelease appends and promotes a release-lease-set intent.
func (s *IntentService) AcceptLeaseSetRelease(ctx context.Context, in LeaseSetReleaseInput) error {
	if s == nil || s.store == nil {
		return ErrNilIntentStore
	}
	requestID := strings.TrimSpace(in.RequestID)
	setID := strings.TrimSpace(in.LeaseSetID)
	if requestID == "" || setID == "" {
		return fmt.Errorf("%w: lease set release identity", sdk.ErrInvalid)
	}
	attemptID := strings.TrimSpace(in.AttemptID)
	traceID := strings.TrimSpace(in.TraceID)
	workID, sourceKey := durableWorkIdentity(sdk.WorkKindReleaseLeaseSet, requestID, attemptID, "concurrency", []string{setID})
	payload, err := json.Marshal(struct {
		SetID  string `json:"set_id"`
		Reason string `json:"reason,omitempty"`
	}{SetID: setID, Reason: strings.TrimSpace(in.Reason)})
	if err != nil {
		return err
	}
	versions := in.Versions
	if strings.TrimSpace(versions.ProviderID) == "" {
		versions.ProviderID = "concurrency"
	}
	rec := terminalwork.WorkRecord{
		WorkID:         workID,
		SourceKey:      sourceKey,
		PayloadVersion: 1,
		Kind:           sdk.WorkKindReleaseLeaseSet,
		State:          sdk.WorkStateIntent,
		ProviderID:     "concurrency",
		Lifecycle: terminalwork.LifecycleCorrelation{
			RequestID: requestID,
			AttemptID: attemptID,
			TraceID:   traceID,
		},
		Versions:   versions,
		LeaseSetID: setID,
		Payload:    payload,
		Error: terminalwork.BoundedError{
			Code:    "lease_set_release",
			Message: "lease set release required",
		},
	}
	return s.accept(ctx, rec)
}

// AcceptReleaseFailure appends and promotes a release-request-provider intent.
func (s *IntentService) AcceptReleaseFailure(ctx context.Context, in ReleaseFailureInput) error {
	if s == nil || s.store == nil {
		return ErrNilIntentStore
	}
	providerID := strings.TrimSpace(in.ProviderID)
	requestID := strings.TrimSpace(in.RequestID)
	if providerID == "" || requestID == "" {
		return fmt.Errorf("%w: release intent identity", sdk.ErrInvalid)
	}
	handles := cleanHandles([]string{in.Handle})
	attemptID := strings.TrimSpace(in.AttemptID)
	traceID := strings.TrimSpace(in.TraceID)
	workID, sourceKey := durableWorkIdentity(sdk.WorkKindReleaseRequestProvider, requestID, attemptID, providerID, handles)
	payload, err := safeHandlesPayload(handles)
	if err != nil {
		return err
	}
	versions := in.Versions
	if strings.TrimSpace(versions.ProviderID) == "" {
		versions.ProviderID = providerID
	}
	rec := terminalwork.WorkRecord{
		WorkID:         workID,
		SourceKey:      sourceKey,
		PayloadVersion: 1,
		Kind:           sdk.WorkKindReleaseRequestProvider,
		State:          sdk.WorkStateIntent,
		ProviderID:     providerID,
		Lifecycle: terminalwork.LifecycleCorrelation{
			RequestID: requestID,
			AttemptID: attemptID,
			TraceID:   traceID,
		},
		Versions: versions,
		Payload:  payload,
		Error: terminalwork.BoundedError{
			Code:    "outage",
			Message: "provider release failed",
		},
	}
	return s.accept(ctx, rec)
}

func (s *IntentService) accept(ctx context.Context, rec terminalwork.WorkRecord) error {
	if ctx == nil {
		ctx = context.Background()
	}
	now := s.clock().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now

	pin, err := retainProviderPin(ctx, &rec.Versions)
	if err != nil {
		return err
	}

	token := s.beginAdoption(rec.WorkID)
	defer token.End()

	if outcomeStore, ok := s.store.(AppendIntentOutcomeStore); ok {
		outcome, aerr := outcomeStore.AppendIntentOutcome(ctx, rec)
		if aerr != nil {
			return s.afterAppendOutcomeError(ctx, rec, pin, token, aerr)
		}
		switch {
		case outcome.Inserted:
			return s.promoteInserted(ctx, rec, pin, token, now)
		case outcome.Replay:
			return s.promoteReplay(ctx, rec, pin, token, now)
		default:
			return s.handoffAmbiguous(ctx, AmbiguousAppend{
				WorkID: rec.WorkID,
				Record: rec,
				Pin:    pin,
				Cause:  ErrAppendReconcileAmbiguous,
			})
		}
	}

	// Compatibility fakes without AppendIntentOutcomeStore.
	if err := s.store.AppendIntent(ctx, rec); err != nil {
		return s.reconcileAfterAppendError(ctx, rec, pin, token, err)
	}
	return s.promoteAfterCompatPersist(ctx, rec, pin, token, now)
}

func (s *IntentService) beginAdoption(workID string) *AdoptionToken {
	if s == nil || s.pins == nil {
		return &AdoptionToken{}
	}
	return s.pins.BeginAdoption(workID)
}

func isDefinitiveTerminal(state sdk.WorkState) bool {
	return state.IsTerminal()
}

// promoteInserted adopts under reservation for a definitively new Intent row.
func (s *IntentService) promoteInserted(ctx context.Context, rec terminalwork.WorkRecord, pin genpin.Pin, token *AdoptionToken, now time.Time) error {
	s.publishOwnership(rec, pin, token)
	return s.promoteAndReconcile(ctx, rec, now)
}

// promoteReplay classifies durable state before publishing replacement ownership.
func (s *IntentService) promoteReplay(ctx context.Context, rec terminalwork.WorkRecord, pin genpin.Pin, token *AdoptionToken, now time.Time) error {
	lookup, ok := s.store.(IntentLookup)
	if !ok {
		return s.handoffAmbiguous(ctx, AmbiguousAppend{
			WorkID: rec.WorkID,
			Record: rec,
			Pin:    pin,
			Cause:  ErrAppendReconcileAmbiguous,
		})
	}
	existing, found, lerr := lookup.LookupIntent(ctx, rec.WorkID)
	if lerr != nil || !found {
		return s.replayLookupAmbiguous(ctx, rec, pin, lerr)
	}
	if isDefinitiveTerminal(existing.State) {
		if pin != nil {
			pin.Release()
		}
		return nil
	}
	s.publishOwnership(rec, pin, token)
	return s.promoteAndReconcile(ctx, rec, now)
}

// promoteAfterCompatPersist handles AppendIntent nil (insert or replay unknown).
func (s *IntentService) promoteAfterCompatPersist(ctx context.Context, rec terminalwork.WorkRecord, pin genpin.Pin, token *AdoptionToken, now time.Time) error {
	lookup, ok := s.store.(IntentLookup)
	if !ok {
		// No lookup: treat as inserted-like under reservation (legacy fakes).
		s.publishOwnership(rec, pin, token)
		return s.promoteAndReconcile(ctx, rec, now)
	}
	existing, found, lerr := lookup.LookupIntent(ctx, rec.WorkID)
	if lerr != nil || !found {
		return s.replayLookupAmbiguous(ctx, rec, pin, lerr)
	}
	if isDefinitiveTerminal(existing.State) {
		if pin != nil {
			pin.Release()
		}
		return nil
	}
	s.publishOwnership(rec, pin, token)
	return s.promoteAndReconcile(ctx, rec, now)
}

func (s *IntentService) promoteAndReconcile(ctx context.Context, rec terminalwork.WorkRecord, now time.Time) error {
	if err := s.store.PromotePending(ctx, terminalwork.PromotePendingCommand{
		WorkID: rec.WorkID,
		Now:    now,
	}); err != nil {
		return s.reconcilePromoteFailure(ctx, rec, err)
	}
	return nil
}

// publishOwnership publishes combined ownership via PublishBound so executable
// Bind runs only for the tracker winner, serialized against MarkTerminal.
func (s *IntentService) publishOwnership(rec terminalwork.WorkRecord, pin genpin.Pin, token *AdoptionToken) {
	if token == nil {
		unwindCandidate(pin, nil)
		return
	}
	_ = token.PublishBound(pin, func() (func(), bool) {
		if s == nil || s.exec == nil {
			return nil, false
		}
		return s.exec.Bind(rec.WorkID, rec.Versions)
	})
}

// reconcilePromoteFailure releases tracker ownership only when the durable row
// is already terminal (replay/callback race). Nonterminal or ambiguous rows
// retain ownership under the durable Intent.
func (s *IntentService) reconcilePromoteFailure(ctx context.Context, rec terminalwork.WorkRecord, promoteErr error) error {
	lookup, ok := s.store.(IntentLookup)
	if !ok {
		return promoteErr
	}
	existing, found, lerr := lookup.LookupIntent(ctx, rec.WorkID)
	if lerr != nil || !found {
		return promoteErr
	}
	if !isDefinitiveTerminal(existing.State) {
		return promoteErr
	}
	if s.pins != nil {
		s.pins.MarkTerminal(rec.WorkID)
	}
	return nil
}

func (s *IntentService) afterAppendOutcomeError(ctx context.Context, rec terminalwork.WorkRecord, pin genpin.Pin, token *AdoptionToken, appendErr error) error {
	// A negative LookupIntent is never proof that an ambiguous append did not
	// commit (PostgreSQL commit races). Every zero-outcome append error hands
	// off unless lookup definitively classifies same-intent or conflict.
	lookup, ok := s.store.(IntentLookup)
	if ok {
		existing, found, lerr := lookup.LookupIntent(ctx, rec.WorkID)
		if lerr == nil && found {
			if !terminalwork.SameIntentReplay(existing, rec) {
				if pin != nil {
					pin.Release()
				}
				return errors.Join(appendErr, ErrIntentReplayConflict)
			}
			return s.promoteReplay(ctx, rec, pin, token, s.clock().UTC())
		}
		if lerr != nil {
			appendErr = errors.Join(appendErr, lerr)
		}
	}
	return s.handoffAmbiguous(ctx, AmbiguousAppend{
		WorkID: rec.WorkID,
		Record: rec,
		Pin:    pin,
		Cause:  appendErr,
	})
}

func (s *IntentService) reconcileAfterAppendError(ctx context.Context, rec terminalwork.WorkRecord, pin genpin.Pin, token *AdoptionToken, appendErr error) error {
	// Compatibility AppendIntent path: same invariant as afterAppendOutcomeError —
	// never release on one immediate not-found after an ambiguous append error.
	lookup, ok := s.store.(IntentLookup)
	if ok {
		existing, found, lerr := lookup.LookupIntent(ctx, rec.WorkID)
		if lerr == nil && found {
			if !terminalwork.SameIntentReplay(existing, rec) {
				if pin != nil {
					pin.Release()
				}
				return errors.Join(appendErr, ErrIntentReplayConflict)
			}
			return s.promoteReplay(ctx, rec, pin, token, s.clock().UTC())
		}
		cause := errors.Join(appendErr, ErrAppendReconcileAmbiguous)
		if lerr != nil {
			cause = errors.Join(appendErr, lerr, ErrAppendReconcileAmbiguous)
		}
		return s.handoffAmbiguous(ctx, AmbiguousAppend{
			WorkID: rec.WorkID,
			Record: rec,
			Pin:    pin,
			Cause:  cause,
		})
	}
	return s.handoffAmbiguous(ctx, AmbiguousAppend{
		WorkID: rec.WorkID,
		Record: rec,
		Pin:    pin,
		Cause:  errors.Join(appendErr, ErrAppendReconcileAmbiguous),
	})
}

func (s *IntentService) replayLookupAmbiguous(ctx context.Context, rec terminalwork.WorkRecord, pin genpin.Pin, lookupErr error) error {
	if s.pins != nil && s.pins.OwnershipSafe(rec.WorkID) {
		if pin != nil {
			pin.Release()
		}
		if lookupErr != nil {
			return errors.Join(lookupErr, ErrAppendReconcileAmbiguous)
		}
		return ErrAppendReconcileAmbiguous
	}
	cause := ErrAppendReconcileAmbiguous
	if lookupErr != nil {
		cause = errors.Join(lookupErr, ErrAppendReconcileAmbiguous)
	}
	return s.handoffAmbiguous(ctx, AmbiguousAppend{
		WorkID: rec.WorkID,
		Record: rec,
		Pin:    pin,
		Cause:  cause,
	})
}

func (s *IntentService) handoffAmbiguous(ctx context.Context, amb AmbiguousAppend) error {
	if s != nil && s.handoff != nil {
		// Take ignores request cancellation during queue-capacity wait so a
		// possibly-committed ambiguous row cannot lose its only generation pin.
		// On error (not-running / validation / conflict) Take releases the
		// candidate because ownership was not transferred. Success means the
		// reconciler owns the pin; return ErrDurablePending as today.
		if err := s.handoff.Take(ctx, amb); err != nil {
			return err
		}
		// Ownership transferred: durable acceptance / reconciliation pending.
		if amb.Cause != nil {
			return errors.Join(amb.Cause, ErrDurablePending)
		}
		return ErrDurablePending
	}
	if amb.Pin != nil {
		amb.Pin.Release()
	}
	if amb.Cause != nil {
		return errors.Join(amb.Cause, ErrAmbiguousAppendReconcilerNotConfigured)
	}
	return ErrAmbiguousAppendReconcilerNotConfigured
}

func durableWorkIdentity(kind sdk.WorkKind, requestID, attemptID, providerID string, handles []string) (string, terminalwork.SourceKey) {
	var b strings.Builder
	writeLenPrefixed(&b, string(kind))
	writeLenPrefixed(&b, requestID)
	writeLenPrefixed(&b, attemptID)
	writeLenPrefixed(&b, providerID)
	sorted := append([]string(nil), handles...)
	sort.Strings(sorted)
	for _, h := range sorted {
		writeLenPrefixed(&b, h)
	}
	sum := sha256.Sum256([]byte(b.String()))
	digest := hex.EncodeToString(sum[:16])
	return "tw_" + digest, terminalwork.SourceKey{
		IdentityVersion: intentIdentityVersion,
		Key:             "sk_" + digest,
	}
}

func writeLenPrefixed(b *strings.Builder, s string) {
	s = strings.TrimSpace(s)
	fmt.Fprintf(b, "%d:%s|", len(s), s)
}

func cleanHandles(handles []string) []string {
	clean := make([]string, 0, len(handles))
	seen := make(map[string]struct{}, len(handles))
	for _, h := range handles {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		clean = append(clean, h)
	}
	sort.Strings(clean)
	return clean
}

func safeHandlesPayload(handles []string) ([]byte, error) {
	// handles are already sorted by cleanHandles; keep payload identical to identity.
	return json.Marshal(struct {
		Handles []string `json:"handles,omitempty"`
	}{Handles: handles})
}
