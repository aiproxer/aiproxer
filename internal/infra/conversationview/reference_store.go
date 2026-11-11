package conversationview

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
)

const DefaultMaxLegs = 100000

// ReferenceStore is an in-memory Store used to pin contract semantics.
type ReferenceStore struct {
	mu      sync.Mutex
	legs    map[string]*legView
	now     func() time.Time
	maxLegs int
}

type legView struct {
	bootstrap  map[string]BootstrapCompletion
	revision   uint64
	tags       map[MessageIdentity]Tag
	steering   map[string]*SteeringOverlay
	nextSlot   uint64
	createdAt  time.Time
	lastSeenAt time.Time
}

// NewReferenceStore creates an empty store.
func NewReferenceStore() *ReferenceStore {
	return &ReferenceStore{
		legs:    make(map[string]*legView),
		now:     time.Now,
		maxLegs: DefaultMaxLegs,
	}
}

// NewReferenceStoreWithClock creates a store with a deterministic clock (test use).
func NewReferenceStoreWithClock(now func() time.Time) *ReferenceStore {
	if now == nil {
		now = time.Now
	}
	return &ReferenceStore{
		legs:    make(map[string]*legView),
		now:     now,
		maxLegs: DefaultMaxLegs,
	}
}

// SetMaxLegs configures the maximum concurrent A-legs retained in memory.
func (s *ReferenceStore) SetMaxLegs(maxLegs int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxLegs <= 0 {
		maxLegs = DefaultMaxLegs
	}
	s.maxLegs = maxLegs
	s.evictExcessLocked()
}

func (s *ReferenceStore) evictExcessLocked() {
	if s.maxLegs <= 0 || len(s.legs) <= s.maxLegs {
		return
	}
	type legAge struct {
		id         string
		lastSeenAt time.Time
		createdAt  time.Time
	}
	ages := make([]legAge, 0, len(s.legs))
	for id, lv := range s.legs {
		// A committed bootstrap belongs to authoritative continuity lifetime.
		// Its existing retirement observer, not this independent view-cache cap,
		// releases it. Unmarked views retain the legacy bounded-eviction policy.
		if len(lv.bootstrap) > 0 {
			continue
		}
		ages = append(ages, legAge{
			id:         id,
			lastSeenAt: lv.lastSeenAt,
			createdAt:  lv.createdAt,
		})
	}
	sort.Slice(ages, func(i, j int) bool {
		if ages[i].lastSeenAt.Equal(ages[j].lastSeenAt) {
			return ages[i].createdAt.Before(ages[j].createdAt)
		}
		return ages[i].lastSeenAt.Before(ages[j].lastSeenAt)
	})
	excess := len(s.legs) - s.maxLegs
	for i := range min(excess, len(ages)) {
		delete(s.legs, ages[i].id)
	}
}

// CreateALeg registers an A-leg for conversation-view state.
func (s *ReferenceStore) CreateALeg(ctx context.Context, aLegID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateALegID(aLegID); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(aLegID)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	if lv, exists := s.legs[trimmed]; exists {
		lv.lastSeenAt = now
		return nil
	}
	s.legs[trimmed] = &legView{
		tags:       make(map[MessageIdentity]Tag),
		steering:   make(map[string]*SteeringOverlay),
		nextSlot:   1,
		createdAt:  now,
		lastSeenAt: now,
	}
	s.evictExcessLocked()
	return nil
}

// DeleteALeg removes all conversation-view state for an A-leg.
func (s *ReferenceStore) DeleteALeg(ctx context.Context, aLegID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateALegID(aLegID); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(aLegID)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.legs, trimmed)
	return nil
}

func (s *ReferenceStore) getLeg(aLegID string) (*legView, error) {
	trimmed := strings.TrimSpace(aLegID)
	if err := validateALegID(trimmed); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrALegNotFound, err)
	}
	lv, ok := s.legs[trimmed]
	if !ok {
		return nil, ErrALegNotFound
	}
	return lv, nil
}

// Snapshot returns a deep-owned coherent snapshot.
func (s *ReferenceStore) Snapshot(ctx context.Context, aLegID string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lv, err := s.getLeg(aLegID)
	if err != nil {
		return Snapshot{}, err
	}
	lv.lastSeenAt = s.now().UTC()
	// Collect tags sorted for determinism.
	tags := make([]Tag, 0, len(lv.tags))
	for _, t := range lv.tags {
		tags = append(tags, t)
	}
	sort.Slice(tags, func(i, j int) bool {
		return string(tags[i].Identity) < string(tags[j].Identity)
	})
	// Collect active overlays sorted by SlotOrdinal.
	var steering []SteeringOverlay
	for _, ov := range lv.steering {
		if ov.Active {
			steering = append(steering, ov.Clone())
		}
	}
	sort.Slice(steering, func(i, j int) bool {
		return steering[i].SlotOrdinal < steering[j].SlotOrdinal
	})
	// Deep copy tags (value copy suffices, but ensure slice ownership).
	tagsCopy := make([]Tag, len(tags))
	copy(tagsCopy, tags)
	steeringCopy := make([]conversationprojection.Overlay, len(steering))
	for i := range steering {
		steeringCopy[i] = steering[i].ToProjectionOverlay()
	}
	return Snapshot{
		StateRevision: lv.revision,
		NeverBackend:  tagsCopy,
		Steering:      steeringCopy,
	}, nil
}

// TagNeverBackend atomically tags a batch of identities.
func (s *ReferenceStore) TagNeverBackend(ctx context.Context, aLegID string, tags []TagRequest) (TagResult, error) {
	if err := ctx.Err(); err != nil {
		return TagResult{}, err
	}
	if err := validateALegID(aLegID); err != nil {
		return TagResult{}, fmt.Errorf("%w: %w", ErrALegNotFound, err)
	}
	// Validate batch first (no mutation).
	seenInBatch := make(map[MessageIdentity]struct{}, len(tags))
	newIdentities := make(map[MessageIdentity]TagRequest)
	for i, req := range tags {
		if err := req.Validate(); err != nil {
			return TagResult{}, fmt.Errorf("%w: index %d: %w", ErrInvalidTagRequest, i, err)
		}
		if _, dup := seenInBatch[req.Identity]; dup {
			// Duplicate within batch - treat as idempotent de-dupe, not error; keep first.
			continue
		}
		seenInBatch[req.Identity] = struct{}{}
		// Will check existing later under lock, but track for cap calculation.
		newIdentities[req.Identity] = req
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lv, err := s.getLeg(aLegID)
	if err != nil {
		return TagResult{}, err
	}
	now := s.now().UTC()
	lv.lastSeenAt = now
	// Determine truly new identities not already stored.
	trulyNew := 0
	for id := range newIdentities {
		if _, exists := lv.tags[id]; !exists {
			trulyNew++
		}
	}
	if len(lv.tags)+trulyNew > MaxNeverBackendTags {
		return TagResult{}, ErrTagLimitExceeded
	}
	// Check if batch is semantic no-op (all identities already present).
	isNoOp := trulyNew == 0
	if !isNoOp {
		for id, req := range newIdentities {
			if _, exists := lv.tags[id]; exists {
				continue
			}
			lv.tags[id] = Tag{
				Identity:  req.Identity,
				Reason:    req.Reason,
				CreatedAt: now,
			}
		}
		if lv.revision == 1<<64-1 {
			return TagResult{}, ErrRevisionExhausted
		}
		lv.revision++
	}
	// Build result tags sorted.
	outTags := make([]Tag, 0, len(lv.tags))
	for _, t := range lv.tags {
		outTags = append(outTags, t)
	}
	sort.Slice(outTags, func(i, j int) bool {
		return string(outTags[i].Identity) < string(outTags[j].Identity)
	})
	return TagResult{
		StateRevision: lv.revision,
		Tags:          outTags,
	}, nil
}

// PutSteering creates or replaces a steering overlay.
func (s *ReferenceStore) PutSteering(ctx context.Context, aLegID string, req PutSteeringRequest) (SteeringState, error) {
	if err := ctx.Err(); err != nil {
		return SteeringState{}, err
	}
	if err := req.Validate(); err != nil {
		return SteeringState{}, err
	}
	if err := validateALegID(aLegID); err != nil {
		return SteeringState{}, fmt.Errorf("%w: %w", ErrALegNotFound, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lv, err := s.getLeg(aLegID)
	if err != nil {
		return SteeringState{}, err
	}
	now := s.now().UTC()
	lv.lastSeenAt = now
	// Check caps before mutation.
	existing, exists := lv.steering[req.OverlayID]

	if exists {
		// Replacement: check semantic no-op.
		candidate := SteeringOverlay{
			OverlayID:           req.OverlayID,
			Revision:            existing.Revision,
			SlotOrdinal:         existing.SlotOrdinal,
			Active:              true,
			Message:             req.Message,
			Placement:           req.Placement,
			AnchorMissingPolicy: req.AnchorMissingPolicy,
			Reason:              req.Reason,
			CreatedAt:           existing.CreatedAt,
			UpdatedAt:           now,
		}
		if overlaysEqual(*existing, candidate) {
			// No-op: do not bump revision or StateRevision.
			return SteeringState{
				OverlayID:                   existing.OverlayID,
				Revision:                    existing.Revision,
				SlotOrdinal:                 existing.SlotOrdinal,
				Active:                      true,
				StateRevision:               lv.revision,
				CacheDiscontinuityKind:      CacheDiscontinuityNone,
				CacheDiscontinuityPlacement: "",
			}, nil
		}
		// Determine if placement change.
		placementChanged := existing.Placement.Kind != req.Placement.Kind
		if !placementChanged && req.Placement.Kind == PlacementAfterMessage {
			if (existing.Placement.Anchor == nil) != (req.Placement.Anchor == nil) {
				placementChanged = true
			} else if existing.Placement.Anchor != nil && *existing.Placement.Anchor != *req.Placement.Anchor {
				placementChanged = true
			}
		}
		// Registration invariant (Req 9.7): a newly bound after_message anchor must not be
		// never_backend at this atomic persistence point (prevents resolve/tag/persist TOCTOU).
		if RegistersNewAfterMessageAnchor(req, true, placementChanged) {
			if _, excluded := lv.tags[req.Placement.Anchor.Identity]; excluded {
				return SteeringState{}, ErrSteeringAnchorExcluded
			}
		}
		// Compute active count/bytes after replacement.
		activeCount := 0
		totalBytes := 0
		for _, ov := range lv.steering {
			if ov.Active && ov.OverlayID != req.OverlayID {
				activeCount++
				totalBytes += len(ov.Message.Text)
			}
		}
		// The replacement will be active.
		activeCount++
		totalBytes += len(req.Message.Text)
		if activeCount > MaxActiveOverlays {
			return SteeringState{}, ErrSteeringLimitExceeded
		}
		if totalBytes > MaxTotalSteeringBytes {
			return SteeringState{}, ErrSteeringLimitExceeded
		}
		if len(req.Message.Text) > MaxSteeringTextBytes {
			return SteeringState{}, ErrSteeringLimitExceeded
		}
		// Apply.
		newSlot := existing.SlotOrdinal
		if placementChanged {
			if lv.nextSlot == 1<<64-1 {
				return SteeringState{}, ErrRevisionExhausted
			}
			newSlot = lv.nextSlot
			lv.nextSlot++
		}
		if existing.Revision == 1<<64-1 || lv.revision == 1<<64-1 {
			return SteeringState{}, ErrRevisionExhausted
		}
		updated := &SteeringOverlay{
			OverlayID:           req.OverlayID,
			Revision:            existing.Revision + 1,
			SlotOrdinal:         newSlot,
			Active:              true,
			Message:             req.Message,
			Placement:           req.Placement,
			AnchorMissingPolicy: req.AnchorMissingPolicy,
			Reason:              req.Reason,
			CreatedAt:           existing.CreatedAt,
			UpdatedAt:           now,
		}
		if updated.Placement.Anchor != nil {
			cp := *updated.Placement.Anchor
			updated.Placement.Anchor = &cp
		}
		lv.steering[req.OverlayID] = updated
		lv.revision++
		kind := CacheDiscontinuityReplace
		if placementChanged {
			kind = CacheDiscontinuityMove
		}
		return SteeringState{
			OverlayID:                   updated.OverlayID,
			Revision:                    updated.Revision,
			SlotOrdinal:                 updated.SlotOrdinal,
			Active:                      true,
			StateRevision:               lv.revision,
			CacheDiscontinuityKind:      kind,
			CacheDiscontinuityPlacement: req.Placement.Kind,
		}, nil
	}
	// New overlay creation.
	// Registration invariant (Req 9.7): see exists-branch comment above.
	if RegistersNewAfterMessageAnchor(req, false, true) {
		if _, excluded := lv.tags[req.Placement.Anchor.Identity]; excluded {
			return SteeringState{}, ErrSteeringAnchorExcluded
		}
	}
	activeCount := 0
	totalBytes := 0
	for _, ov := range lv.steering {
		if ov.Active {
			activeCount++
			totalBytes += len(ov.Message.Text)
		}
	}
	activeCount++
	totalBytes += len(req.Message.Text)
	if activeCount > MaxActiveOverlays {
		return SteeringState{}, ErrSteeringLimitExceeded
	}
	if totalBytes > MaxTotalSteeringBytes {
		return SteeringState{}, ErrSteeringLimitExceeded
	}
	if len(req.Message.Text) > MaxSteeringTextBytes {
		return SteeringState{}, ErrSteeringLimitExceeded
	}
	if lv.nextSlot == 1<<64-1 || lv.revision == 1<<64-1 {
		return SteeringState{}, ErrRevisionExhausted
	}
	slot := lv.nextSlot
	lv.nextSlot++
	ov := &SteeringOverlay{
		OverlayID:           req.OverlayID,
		Revision:            1,
		SlotOrdinal:         slot,
		Active:              true,
		Message:             req.Message,
		Placement:           req.Placement,
		AnchorMissingPolicy: req.AnchorMissingPolicy,
		Reason:              req.Reason,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if ov.Placement.Anchor != nil {
		cp := *ov.Placement.Anchor
		ov.Placement.Anchor = &cp
	}
	lv.steering[req.OverlayID] = ov
	lv.revision++
	return SteeringState{
		OverlayID:                   ov.OverlayID,
		Revision:                    ov.Revision,
		SlotOrdinal:                 ov.SlotOrdinal,
		Active:                      true,
		StateRevision:               lv.revision,
		CacheDiscontinuityKind:      CacheDiscontinuityCreate,
		CacheDiscontinuityPlacement: req.Placement.Kind,
	}, nil
}

// DeactivateSteering marks an overlay inactive.
func (s *ReferenceStore) DeactivateSteering(ctx context.Context, aLegID string, overlayID string) (SteeringState, error) {
	if err := ctx.Err(); err != nil {
		return SteeringState{}, err
	}
	if err := ValidateOverlayID(overlayID); err != nil {
		return SteeringState{}, fmt.Errorf("%w: %w", ErrInvalidSteeringRequest, err)
	}
	if err := validateALegID(aLegID); err != nil {
		return SteeringState{}, fmt.Errorf("%w: %w", ErrALegNotFound, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lv, err := s.getLeg(aLegID)
	if err != nil {
		return SteeringState{}, err
	}
	now := s.now().UTC()
	lv.lastSeenAt = now
	existing, exists := lv.steering[overlayID]
	if !exists {
		return SteeringState{}, ErrOverlayNotFound
	}
	if !existing.Active {
		// No-op deactivation: stable revision.
		return SteeringState{
			OverlayID:                   existing.OverlayID,
			Revision:                    existing.Revision,
			SlotOrdinal:                 existing.SlotOrdinal,
			Active:                      false,
			StateRevision:               lv.revision,
			CacheDiscontinuityKind:      CacheDiscontinuityNone,
			CacheDiscontinuityPlacement: "",
		}, nil
	}
	if existing.Revision == 1<<64-1 || lv.revision == 1<<64-1 {
		return SteeringState{}, ErrRevisionExhausted
	}
	updated := &SteeringOverlay{
		OverlayID:           existing.OverlayID,
		Revision:            existing.Revision + 1,
		SlotOrdinal:         existing.SlotOrdinal,
		Active:              false,
		Message:             existing.Message,
		Placement:           existing.Placement,
		AnchorMissingPolicy: existing.AnchorMissingPolicy,
		Reason:              existing.Reason,
		CreatedAt:           existing.CreatedAt,
		UpdatedAt:           now,
	}
	if updated.Placement.Anchor != nil {
		cp := *updated.Placement.Anchor
		updated.Placement.Anchor = &cp
	}
	lv.steering[overlayID] = updated
	lv.revision++
	return SteeringState{
		OverlayID:                   updated.OverlayID,
		Revision:                    updated.Revision,
		SlotOrdinal:                 updated.SlotOrdinal,
		Active:                      false,
		StateRevision:               lv.revision,
		CacheDiscontinuityKind:      CacheDiscontinuityDeactivate,
		CacheDiscontinuityPlacement: existing.Placement.Kind,
	}, nil
}

// GetOverlay is a test/debugging helper to inspect an overlay regardless of active state.
func (s *ReferenceStore) GetOverlay(ctx context.Context, aLegID string, overlayID string) (SteeringOverlay, error) {
	if err := ctx.Err(); err != nil {
		return SteeringOverlay{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lv, err := s.getLeg(aLegID)
	if err != nil {
		return SteeringOverlay{}, err
	}
	lv.lastSeenAt = s.now().UTC()
	ov, ok := lv.steering[overlayID]
	if !ok {
		return SteeringOverlay{}, ErrOverlayNotFound
	}
	return ov.Clone(), nil
}

func overlaysEqual(a, b SteeringOverlay) bool {
	if a.OverlayID != b.OverlayID || a.Active != b.Active || a.SlotOrdinal != b.SlotOrdinal {
		return false
	}
	if !a.Message.Equal(b.Message) {
		return false
	}
	if a.Placement.Kind != b.Placement.Kind {
		return false
	}
	if a.Placement.Kind == PlacementAfterMessage {
		if (a.Placement.Anchor == nil) != (b.Placement.Anchor == nil) {
			return false
		}
		if a.Placement.Anchor != nil && *a.Placement.Anchor != *b.Placement.Anchor {
			return false
		}
	}
	if a.AnchorMissingPolicy != b.AnchorMissingPolicy {
		return false
	}
	if a.Reason != b.Reason {
		return false
	}
	return true
}

var (
	_ Store         = (*ReferenceStore)(nil)
	_ Reader        = (*ReferenceStore)(nil)
	_ Tagger        = (*ReferenceStore)(nil)
	_ SteeringStore = (*ReferenceStore)(nil)
)
