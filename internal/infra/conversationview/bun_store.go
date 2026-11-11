package conversationview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Compile-time assertions keep the optional capability discoverable via AsStore.
var (
	_ Store         = (*BunStore)(nil)
	_ Reader        = (*BunStore)(nil)
	_ Tagger        = (*BunStore)(nil)
	_ SteeringStore = (*BunStore)(nil)
)

// BunStore is the Bun-backed persistence adapter for conversation view.
// All operations run under the A-leg row lock so snapshot and mutations
// are linearizable per A-leg and follow A-leg deletion atomically.
type BunStore struct {
	db *bun.DB
}

// NewBunStore creates a Bun-backed conversation view store.
func NewBunStore(db *bun.DB) *BunStore {
	return &BunStore{db: db}
}

// DB returns the underlying Bun DB handle.
func (m *BunStore) DB() *bun.DB {
	return m.db
}

// CreateALeg creates an A-leg row if not already present (primarily for tests).
func (m *BunStore) CreateALeg(ctx context.Context, aLegID string) error {
	_, err := m.db.NewRaw(`INSERT INTO a_legs(a_leg_id, continuity_key, created_at_unix, last_seen_at_unix, weighted_first_consumed, next_b_seq) VALUES(?,?,?,?,0,0)`, aLegID, "", int64(0), int64(0)).Exec(ctx)
	if err != nil {
		var count int
		if err2 := m.db.NewRaw(`SELECT count(*) FROM a_legs WHERE a_leg_id = ?`, aLegID).Scan(ctx, &count); err2 == nil && count == 1 {
			return nil
		}
	}
	return err
}

// DeleteALeg deletes an A-leg row (cascades conversation view data).
func (m *BunStore) DeleteALeg(ctx context.Context, aLegID string) error {
	_, err := m.db.NewRaw(`DELETE FROM a_legs WHERE a_leg_id = ?`, aLegID).Exec(ctx)
	return err
}

// Snapshot returns a deep-owned coherent snapshot for the A-leg. Legacy legs
// with no state row read as empty revision 0.
func (m *BunStore) Snapshot(ctx context.Context, aLegID string) (conversationprojection.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return conversationprojection.Snapshot{}, err
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return conversationprojection.Snapshot{}, ErrALegNotFound
	}
	var out conversationprojection.Snapshot
	err := m.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := m.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		snap, err := m.loadSnapshotTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		out = snap
		return nil
	})
	if err != nil {
		return conversationprojection.Snapshot{}, err
	}
	return out, nil
}

// TagNeverBackend atomically tags a batch of identities.
func (m *BunStore) TagNeverBackend(ctx context.Context, aLegID string, tags []TagRequest) (TagResult, error) {
	if err := ctx.Err(); err != nil {
		return TagResult{}, err
	}
	// Validate and de-duplicate batch before taking lock.
	seenInBatch := make(map[conversationprojection.MessageIdentity]struct{}, len(tags))
	newIdentities := make(map[conversationprojection.MessageIdentity]TagRequest)
	for i, req := range tags {
		if err := req.Validate(); err != nil {
			return TagResult{}, fmt.Errorf("%w: index %d: %w", ErrInvalidTagRequest, i, err)
		}
		if _, dup := seenInBatch[req.Identity]; dup {
			continue
		}
		seenInBatch[req.Identity] = struct{}{}
		newIdentities[req.Identity] = req
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return TagResult{}, ErrALegNotFound
	}
	var result TagResult
	err := m.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := m.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		rev, nextSlot, err := m.loadStateTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		existingSet, err := m.loadTagSetTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		trulyNew := 0
		for id := range newIdentities {
			if _, exists := existingSet[id]; !exists {
				trulyNew++
			}
		}
		if len(existingSet)+trulyNew > MaxNeverBackendTags {
			return ErrTagLimitExceeded
		}
		isNoOp := trulyNew == 0
		if !isNoOp {
			if rev == math.MaxInt64 {
				return ErrRevisionExhausted
			}
			rev++
			now := time.Now().UTC().UnixNano()
			for id, req := range newIdentities {
				if _, exists := existingSet[id]; exists {
					continue
				}
				version, digest, err := splitIdentity(id)
				if err != nil {
					return err
				}
				if _, err := tx.NewRaw(`
					INSERT INTO a_leg_never_backend_messages(a_leg_id, identity_version, identity_digest, reason, created_at_unix)
					VALUES(?,?,?,?,?)
				`, aLegID, version, digest, string(req.Reason), now).Exec(ctx); err != nil {
					return opErr("insert never backend tag", err)
				}
			}
			if err := m.upsertStateTx(ctx, tx, aLegID, rev, nextSlot); err != nil {
				return err
			}
		}
		if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		// Build result tags sorted by identity.
		tagsSlice, err := m.loadTagsTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		result = TagResult{
			StateRevision: uint64(rev),
			Tags:          tagsSlice,
		}
		return nil
	})
	if err != nil {
		return TagResult{}, err
	}
	return result, nil
}

// PutSteering creates or replaces a steering overlay.
func (m *BunStore) PutSteering(ctx context.Context, aLegID string, req PutSteeringRequest) (SteeringState, error) {
	if err := ctx.Err(); err != nil {
		return SteeringState{}, err
	}
	if err := req.Validate(); err != nil {
		return SteeringState{}, err
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return SteeringState{}, ErrALegNotFound
	}
	var out SteeringState
	err := m.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := m.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		rev, nextSlot, err := m.loadStateTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		existing, exists, err := m.loadOverlayTx(ctx, tx, aLegID, req.OverlayID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		nowUnix := now.UnixNano()

		if exists {
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
			if overlaysEqualForBun(existing, candidate) {
				if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
					return err
				}
				out = SteeringState{
					OverlayID:                   existing.OverlayID,
					Revision:                    existing.Revision,
					SlotOrdinal:                 existing.SlotOrdinal,
					Active:                      true,
					StateRevision:               uint64(rev),
					CacheDiscontinuityKind:      CacheDiscontinuityNone,
					CacheDiscontinuityPlacement: "",
				}
				return nil
			}
			placementChanged := existing.Placement.Kind != req.Placement.Kind
			if !placementChanged && req.Placement.Kind == PlacementAfterMessage {
				if (existing.Placement.Anchor == nil) != (req.Placement.Anchor == nil) {
					placementChanged = true
				} else if existing.Placement.Anchor != nil && *existing.Placement.Anchor != *req.Placement.Anchor {
					placementChanged = true
				}
			}
			// Registration invariant: newly bound after_message anchor must not be never_backend.
			if RegistersNewAfterMessageAnchor(req, true, placementChanged) {
				tagSet, terr := m.loadTagSetTx(ctx, tx, aLegID)
				if terr != nil {
					return terr
				}
				if _, excluded := tagSet[req.Placement.Anchor.Identity]; excluded {
					return ErrSteeringAnchorExcluded
				}
			}
			activeCount, totalBytes, err := m.loadActiveStatsTx(ctx, tx, aLegID)
			if err != nil {
				return err
			}
			if existing.Active {
				activeCount--
				totalBytes -= len(existing.Message.Text)
			}
			activeCount++
			totalBytes += len(req.Message.Text)
			if activeCount > MaxActiveOverlays {
				return ErrSteeringLimitExceeded
			}
			if totalBytes > MaxTotalSteeringBytes {
				return ErrSteeringLimitExceeded
			}
			if len(req.Message.Text) > MaxSteeringTextBytes {
				return ErrSteeringLimitExceeded
			}
			newSlot := existing.SlotOrdinal
			if placementChanged {
				if nextSlot == math.MaxInt64 {
					return ErrRevisionExhausted
				}
				newSlot = uint64(nextSlot)
				nextSlot++
			}
			if existing.Revision == math.MaxInt64 || rev == math.MaxInt64 {
				return ErrRevisionExhausted
			}
			newRev := existing.Revision + 1
			rev++
			if err := m.upsertOverlayTx(ctx, tx, aLegID, SteeringOverlay{
				OverlayID:           req.OverlayID,
				Revision:            newRev,
				SlotOrdinal:         newSlot,
				Active:              true,
				Message:             req.Message,
				Placement:           req.Placement,
				AnchorMissingPolicy: req.AnchorMissingPolicy,
				Reason:              req.Reason,
				CreatedAt:           existing.CreatedAt,
				UpdatedAt:           now,
			}); err != nil {
				return err
			}
			if err := m.upsertStateTx(ctx, tx, aLegID, rev, nextSlot); err != nil {
				return err
			}
			if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
				return err
			}
			kind := CacheDiscontinuityReplace
			if placementChanged {
				kind = CacheDiscontinuityMove
			}
			out = SteeringState{
				OverlayID:                   req.OverlayID,
				Revision:                    newRev,
				SlotOrdinal:                 newSlot,
				Active:                      true,
				StateRevision:               uint64(rev),
				CacheDiscontinuityKind:      kind,
				CacheDiscontinuityPlacement: req.Placement.Kind,
			}
			_ = nowUnix
			return nil
		}
		// New overlay creation.
		if RegistersNewAfterMessageAnchor(req, false, true) {
			tagSet, terr := m.loadTagSetTx(ctx, tx, aLegID)
			if terr != nil {
				return terr
			}
			if _, excluded := tagSet[req.Placement.Anchor.Identity]; excluded {
				return ErrSteeringAnchorExcluded
			}
		}
		activeCount, totalBytes, err := m.loadActiveStatsTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		activeCount++
		totalBytes += len(req.Message.Text)
		if activeCount > MaxActiveOverlays {
			return ErrSteeringLimitExceeded
		}
		if totalBytes > MaxTotalSteeringBytes {
			return ErrSteeringLimitExceeded
		}
		if len(req.Message.Text) > MaxSteeringTextBytes {
			return ErrSteeringLimitExceeded
		}
		if nextSlot == math.MaxInt64 || rev == math.MaxInt64 {
			return ErrRevisionExhausted
		}
		slot := uint64(nextSlot)
		nextSlot++
		rev++
		ov := SteeringOverlay{
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
		if err := m.upsertOverlayTx(ctx, tx, aLegID, ov); err != nil {
			return err
		}
		if err := m.upsertStateTx(ctx, tx, aLegID, rev, nextSlot); err != nil {
			return err
		}
		if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		out = SteeringState{
			OverlayID:                   ov.OverlayID,
			Revision:                    ov.Revision,
			SlotOrdinal:                 ov.SlotOrdinal,
			Active:                      true,
			StateRevision:               uint64(rev),
			CacheDiscontinuityKind:      CacheDiscontinuityCreate,
			CacheDiscontinuityPlacement: req.Placement.Kind,
		}
		_ = nowUnix
		return nil
	})
	if err != nil {
		return SteeringState{}, err
	}
	return out, nil
}

// DeactivateSteering marks an overlay inactive.
func (m *BunStore) DeactivateSteering(ctx context.Context, aLegID string, overlayID string) (SteeringState, error) {
	if err := ctx.Err(); err != nil {
		return SteeringState{}, err
	}
	tmpReq := PutSteeringRequest{
		OverlayID:           overlayID,
		Message:             StoredMessageV1{Role: "user", Text: "x"},
		Placement:           StoredPlacement{Kind: PlacementStablePrefix},
		AnchorMissingPolicy: AnchorStablePrefixFallback,
		Reason:              "r",
	}
	if err := tmpReq.Validate(); err != nil {
		return SteeringState{}, fmt.Errorf("%w: %w", ErrInvalidSteeringRequest, err)
	}
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" {
		return SteeringState{}, ErrALegNotFound
	}
	var out SteeringState
	err := m.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := m.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		rev, nextSlot, err := m.loadStateTx(ctx, tx, aLegID)
		if err != nil {
			return err
		}
		existing, exists, err := m.loadOverlayTx(ctx, tx, aLegID, overlayID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrOverlayNotFound
		}
		if !existing.Active {
			if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
				return err
			}
			out = SteeringState{
				OverlayID:                   existing.OverlayID,
				Revision:                    existing.Revision,
				SlotOrdinal:                 existing.SlotOrdinal,
				Active:                      false,
				StateRevision:               uint64(rev),
				CacheDiscontinuityKind:      CacheDiscontinuityNone,
				CacheDiscontinuityPlacement: "",
			}
			return nil
		}
		if existing.Revision == math.MaxInt64 || rev == math.MaxInt64 {
			return ErrRevisionExhausted
		}
		now := time.Now().UTC()
		newRev := existing.Revision + 1
		rev++
		updated := SteeringOverlay{
			OverlayID:           existing.OverlayID,
			Revision:            newRev,
			SlotOrdinal:         existing.SlotOrdinal,
			Active:              false,
			Message:             existing.Message,
			Placement:           existing.Placement,
			AnchorMissingPolicy: existing.AnchorMissingPolicy,
			Reason:              existing.Reason,
			CreatedAt:           existing.CreatedAt,
			UpdatedAt:           now,
		}
		if err := m.upsertOverlayTx(ctx, tx, aLegID, updated); err != nil {
			return err
		}
		if err := m.upsertStateTx(ctx, tx, aLegID, rev, nextSlot); err != nil {
			return err
		}
		if err := m.touchALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		out = SteeringState{
			OverlayID:                   existing.OverlayID,
			Revision:                    newRev,
			SlotOrdinal:                 existing.SlotOrdinal,
			Active:                      false,
			StateRevision:               uint64(rev),
			CacheDiscontinuityKind:      CacheDiscontinuityDeactivate,
			CacheDiscontinuityPlacement: "",
		}
		return nil
	})
	if err != nil {
		return SteeringState{}, err
	}
	return out, nil
}

// GetOverlay returns a stored overlay (used in tests and inspection).
func (m *BunStore) GetOverlay(ctx context.Context, aLegID, overlayID string) (SteeringOverlay, error) {
	if err := ctx.Err(); err != nil {
		return SteeringOverlay{}, err
	}
	var out SteeringOverlay
	err := m.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := m.lockALegTx(ctx, tx, aLegID); err != nil {
			return err
		}
		ov, exists, err := m.loadOverlayTx(ctx, tx, aLegID, overlayID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrOverlayNotFound
		}
		out = ov
		return nil
	})
	if err != nil {
		return SteeringOverlay{}, err
	}
	return out, nil
}

func (m *BunStore) lockALegTx(ctx context.Context, tx bun.Tx, aLegID string) error {
	q := `SELECT a_leg_id FROM a_legs WHERE a_leg_id = ?`
	if m.db.Dialect().Name() == dialect.PG {
		q = `SELECT a_leg_id FROM a_legs WHERE a_leg_id = ? FOR UPDATE`
	}
	var id string
	err := tx.NewRaw(q, aLegID).Scan(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrALegNotFound
	}
	if err != nil {
		return opErr("lock a leg for conversation view", err)
	}
	return nil
}

func (m *BunStore) touchALegTx(ctx context.Context, tx bun.Tx, aLegID string) error {
	_, err := tx.NewRaw(`UPDATE a_legs SET last_seen_at_unix = ? WHERE a_leg_id = ?`, time.Now().UnixNano(), aLegID).Exec(ctx)
	if err != nil {
		return opErr("touch a leg last seen", err)
	}
	return nil
}

func (m *BunStore) loadStateTx(ctx context.Context, tx bun.Tx, aLegID string) (rev int64, nextSlot int64, err error) {
	err = tx.NewRaw(`SELECT state_revision, next_slot_ordinal FROM a_leg_conversation_view_state WHERE a_leg_id = ?`, aLegID).Scan(ctx, &rev, &nextSlot)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 1, nil
	}
	if err != nil {
		return 0, 0, opErr("select conversation view state", err)
	}
	return rev, nextSlot, nil
}

func (m *BunStore) upsertStateTx(ctx context.Context, tx bun.Tx, aLegID string, rev, nextSlot int64) error {
	_, err := tx.NewRaw(`
		INSERT INTO a_leg_conversation_view_state(a_leg_id, state_revision, next_slot_ordinal)
		VALUES(?,?,?)
		ON CONFLICT(a_leg_id) DO UPDATE SET
			state_revision=excluded.state_revision,
			next_slot_ordinal=excluded.next_slot_ordinal
	`, aLegID, rev, nextSlot).Exec(ctx)
	if err != nil {
		return opErr("upsert conversation view state", err)
	}
	return nil
}

func (m *BunStore) loadSnapshotTx(ctx context.Context, tx bun.Tx, aLegID string) (conversationprojection.Snapshot, error) {
	rev, _, err := m.loadStateTx(ctx, tx, aLegID)
	if err != nil {
		return conversationprojection.Snapshot{}, err
	}
	tags, err := m.loadTagsTx(ctx, tx, aLegID)
	if err != nil {
		return conversationprojection.Snapshot{}, err
	}
	steering, err := m.loadActiveOverlaysTx(ctx, tx, aLegID)
	if err != nil {
		return conversationprojection.Snapshot{}, err
	}
	projSteering := make([]conversationprojection.Overlay, len(steering))
	for i, s := range steering {
		projSteering[i] = s.ToProjectionOverlay()
	}
	return conversationprojection.Snapshot{
		StateRevision: uint64(rev),
		NeverBackend:  tags,
		Steering:      projSteering,
	}, nil
}

func (m *BunStore) loadTagsTx(ctx context.Context, tx bun.Tx, aLegID string) ([]conversationprojection.Tag, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT identity_version, identity_digest, reason, created_at_unix
		FROM a_leg_never_backend_messages
		WHERE a_leg_id = ?
		ORDER BY identity_digest ASC, identity_version ASC
	`, aLegID)
	if err != nil {
		return nil, opErr("select never backend tags", err)
	}
	defer func() { _ = rows.Close() }()
	var out []conversationprojection.Tag
	for rows.Next() {
		var version, digest, reason string
		var createdAt int64
		if err := rows.Scan(&version, &digest, &reason, &createdAt); err != nil {
			return nil, opErr("scan never backend tag", err)
		}
		id := conversationprojection.MessageIdentity(version + ":" + digest)
		if err := id.Validate(); err != nil {
			return nil, opErr("stored tag identity invalid", err)
		}
		out = append(out, conversationprojection.Tag{
			Identity:  id,
			Reason:    conversationprojection.ReasonCode(reason),
			CreatedAt: time.Unix(0, createdAt).UTC(),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, opErr("iterate never backend tags", err)
	}
	if out == nil {
		out = []conversationprojection.Tag{}
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].Identity) < string(out[j].Identity)
	})
	return out, nil
}

func (m *BunStore) loadTagSetTx(ctx context.Context, tx bun.Tx, aLegID string) (map[conversationprojection.MessageIdentity]struct{}, error) {
	tags, err := m.loadTagsTx(ctx, tx, aLegID)
	if err != nil {
		return nil, err
	}
	set := make(map[conversationprojection.MessageIdentity]struct{}, len(tags))
	for _, t := range tags {
		set[t.Identity] = struct{}{}
	}
	return set, nil
}

func (m *BunStore) loadActiveOverlaysTx(ctx context.Context, tx bun.Tx, aLegID string) ([]SteeringOverlay, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT overlay_id, overlay_revision, slot_ordinal, active, message_version, message_role, message_text,
		       placement_kind, anchor_identity_version, anchor_identity_digest, anchor_occurrence, anchor_missing_policy, reason, created_at_unix, updated_at_unix
		FROM a_leg_steering_overlays
		WHERE a_leg_id = ? AND active = 1
		ORDER BY slot_ordinal ASC
	`, aLegID)
	if err != nil {
		return nil, opErr("select active overlays", err)
	}
	defer func() { _ = rows.Close() }()
	var out []SteeringOverlay
	for rows.Next() {
		var overlayID string
		var overlayRevision, slotOrdinal int64
		var active int
		var messageVersion, messageRole, messageText, placementKind, anchorVersion, anchorDigest, anchorMissingPolicy, reason string
		var anchorOccurrence int64
		var createdAt, updatedAt int64
		if err := rows.Scan(&overlayID, &overlayRevision, &slotOrdinal, &active, &messageVersion, &messageRole, &messageText, &placementKind, &anchorVersion, &anchorDigest, &anchorOccurrence, &anchorMissingPolicy, &reason, &createdAt, &updatedAt); err != nil {
			return nil, opErr("scan overlay", err)
		}
		ov, err := rowToOverlay(overlayID, overlayRevision, slotOrdinal, active, messageRole, messageText, placementKind, anchorVersion, anchorDigest, anchorOccurrence, anchorMissingPolicy, reason, createdAt, updatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, ov)
	}
	if err := rows.Err(); err != nil {
		return nil, opErr("iterate overlays", err)
	}
	if out == nil {
		out = []SteeringOverlay{}
	}
	return out, nil
}

func (m *BunStore) loadActiveStatsTx(ctx context.Context, tx bun.Tx, aLegID string) (count int, totalBytes int, err error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT message_text FROM a_leg_steering_overlays WHERE a_leg_id = ? AND active = 1
	`, aLegID)
	if err != nil {
		return 0, 0, opErr("select active stats", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return 0, 0, opErr("scan active stats", err)
		}
		count++
		totalBytes += len(text)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, opErr("iterate active stats", err)
	}
	return count, totalBytes, nil
}

func (m *BunStore) loadOverlayTx(ctx context.Context, tx bun.Tx, aLegID, overlayID string) (SteeringOverlay, bool, error) {
	var overlayRevision, slotOrdinal int64
	var active int
	var messageVersion, messageRole, messageText, placementKind, anchorVersion, anchorDigest, anchorMissingPolicy, reason string
	var anchorOccurrence int64
	var createdAt, updatedAt int64
	err := tx.NewRaw(`
		SELECT overlay_id, overlay_revision, slot_ordinal, active, message_version, message_role, message_text,
		       placement_kind, anchor_identity_version, anchor_identity_digest, anchor_occurrence, anchor_missing_policy, reason, created_at_unix, updated_at_unix
		FROM a_leg_steering_overlays WHERE a_leg_id = ? AND overlay_id = ?
	`, aLegID, overlayID).Scan(ctx, &overlayID, &overlayRevision, &slotOrdinal, &active, &messageVersion, &messageRole, &messageText, &placementKind, &anchorVersion, &anchorDigest, &anchorOccurrence, &anchorMissingPolicy, &reason, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SteeringOverlay{}, false, nil
	}
	if err != nil {
		return SteeringOverlay{}, false, opErr("select overlay", err)
	}
	ov, err := rowToOverlay(overlayID, overlayRevision, slotOrdinal, active, messageRole, messageText, placementKind, anchorVersion, anchorDigest, anchorOccurrence, anchorMissingPolicy, reason, createdAt, updatedAt)
	if err != nil {
		return SteeringOverlay{}, false, err
	}
	return ov, true, nil
}

func (m *BunStore) upsertOverlayTx(ctx context.Context, tx bun.Tx, aLegID string, ov SteeringOverlay) error {
	active := 0
	if ov.Active {
		active = 1
	}
	anchorVersion, anchorDigest, anchorOcc := "", "", int64(0)
	if ov.Placement.Kind == PlacementAfterMessage && ov.Placement.Anchor != nil {
		anchorVersion = ov.Placement.Anchor.Identity.Version()
		anchorDigest = ov.Placement.Anchor.Identity.Digest()
		anchorOcc = int64(ov.Placement.Anchor.Occurrence)
	}
	_, err := tx.NewRaw(`
		INSERT INTO a_leg_steering_overlays(
			a_leg_id, overlay_id, overlay_revision, slot_ordinal, active, message_version, message_role, message_text,
			placement_kind, anchor_identity_version, anchor_identity_digest, anchor_occurrence, anchor_missing_policy, reason, created_at_unix, updated_at_unix
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(a_leg_id, overlay_id) DO UPDATE SET
			overlay_revision=excluded.overlay_revision,
			slot_ordinal=excluded.slot_ordinal,
			active=excluded.active,
			message_version=excluded.message_version,
			message_role=excluded.message_role,
			message_text=excluded.message_text,
			placement_kind=excluded.placement_kind,
			anchor_identity_version=excluded.anchor_identity_version,
			anchor_identity_digest=excluded.anchor_identity_digest,
			anchor_occurrence=excluded.anchor_occurrence,
			anchor_missing_policy=excluded.anchor_missing_policy,
			reason=excluded.reason,
			created_at_unix=excluded.created_at_unix,
			updated_at_unix=excluded.updated_at_unix
	`, aLegID, ov.OverlayID, int64(ov.Revision), int64(ov.SlotOrdinal), active, "v1", string(ov.Message.Role), ov.Message.Text, string(ov.Placement.Kind), anchorVersion, anchorDigest, anchorOcc, string(ov.AnchorMissingPolicy), string(ov.Reason), ov.CreatedAt.UnixNano(), ov.UpdatedAt.UnixNano()).Exec(ctx)
	if err != nil {
		return opErr("upsert overlay", err)
	}
	return nil
}

func rowToOverlay(overlayID string, overlayRevision, slotOrdinal int64, active int, messageRole, messageText, placementKind, anchorVersion, anchorDigest string, anchorOccurrence int64, anchorMissingPolicy, reason string, createdAt, updatedAt int64) (SteeringOverlay, error) {
	placement := StoredPlacement{Kind: PlacementKind(placementKind)}
	if placement.Kind == PlacementAfterMessage {
		if anchorVersion == "" || anchorDigest == "" {
			return SteeringOverlay{}, fmt.Errorf("bunstore: stored overlay %q has after_message placement without anchor", overlayID)
		}
		id := conversationprojection.MessageIdentity(anchorVersion + ":" + anchorDigest)
		if err := id.Validate(); err != nil {
			return SteeringOverlay{}, opErr("stored anchor identity invalid", err)
		}
		placement.Anchor = &conversationprojection.MessageAnchor{
			Identity:   id,
			Occurrence: uint32(anchorOccurrence),
		}
	}
	ov := SteeringOverlay{
		OverlayID:           overlayID,
		Revision:            uint64(overlayRevision),
		SlotOrdinal:         uint64(slotOrdinal),
		Active:              active != 0,
		Message:             StoredMessageV1{Role: aipapi.Role(messageRole), Text: messageText},
		Placement:           placement,
		AnchorMissingPolicy: AnchorMissingPolicy(anchorMissingPolicy),
		Reason:              ReasonCode(reason),
		CreatedAt:           time.Unix(0, createdAt).UTC(),
		UpdatedAt:           time.Unix(0, updatedAt).UTC(),
	}
	if err := ov.Validate(); err != nil {
		return SteeringOverlay{}, fmt.Errorf("bunstore: stored overlay invalid: %w", err)
	}
	return ov, nil
}

func splitIdentity(id conversationprojection.MessageIdentity) (version, digest string, err error) {
	s := string(id)
	version, digest, found := strings.Cut(s, ":")
	if !found {
		return "", "", fmt.Errorf("%w: missing colon in identity %q", ErrInvalidMessageIdentity, s)
	}
	return version, digest, nil
}

func overlaysEqualForBun(a, b SteeringOverlay) bool {
	if a.OverlayID != b.OverlayID || a.Active != b.Active || a.SlotOrdinal != b.SlotOrdinal {
		return false
	}
	if a.Message.Role != b.Message.Role || a.Message.Text != b.Message.Text {
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

func opErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("conversationview bun: %s: %w", op, err)
}

// EnsureSchema creates the continuity tables and conversation view tables if not present.
func EnsureSchema(ctx context.Context, db *bun.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS a_legs (
			a_leg_id TEXT NOT NULL PRIMARY KEY,
			continuity_key TEXT NOT NULL,
			created_at_unix INTEGER NOT NULL,
			last_seen_at_unix INTEGER NOT NULL,
			weighted_first_consumed INTEGER NOT NULL DEFAULT 0,
			next_b_seq INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS a_leg_conversation_view_state (
			a_leg_id TEXT NOT NULL PRIMARY KEY,
			state_revision INTEGER NOT NULL,
			next_slot_ordinal INTEGER NOT NULL,
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS a_leg_never_backend_messages (
			a_leg_id TEXT NOT NULL,
			identity_version TEXT NOT NULL,
			identity_digest TEXT NOT NULL,
			reason TEXT NOT NULL,
			created_at_unix INTEGER NOT NULL,
			PRIMARY KEY(a_leg_id, identity_version, identity_digest),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS a_leg_steering_overlays (
			a_leg_id TEXT NOT NULL,
			overlay_id TEXT NOT NULL,
			overlay_revision INTEGER NOT NULL,
			slot_ordinal INTEGER NOT NULL,
			active INTEGER NOT NULL,
			message_version TEXT NOT NULL,
			message_role TEXT NOT NULL,
			message_text TEXT NOT NULL,
			placement_kind TEXT NOT NULL,
			anchor_identity_version TEXT NOT NULL,
			anchor_identity_digest TEXT NOT NULL,
			anchor_occurrence INTEGER NOT NULL,
			anchor_missing_policy TEXT NOT NULL,
			reason TEXT NOT NULL,
			created_at_unix INTEGER NOT NULL,
			updated_at_unix INTEGER NOT NULL,
			PRIMARY KEY(a_leg_id, overlay_id),
			FOREIGN KEY(a_leg_id) REFERENCES a_legs(a_leg_id) ON DELETE CASCADE
		)`,
	}
	stmts = append(stmts, BootstrapDDL)
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("conversationview bun ensure schema: %w", err)
		}
	}
	return nil
}
