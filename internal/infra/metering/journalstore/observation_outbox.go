package journalstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/uptrace/bun"
)

const (
	defaultObservationOutboxMaxPending = 4096
	defaultObservationOutboxBatch      = 64
	defaultObservationOutboxLease      = 30 * time.Second
	maxObservationOutboxBatch          = 500
	ObservationFaultAfterOutbox        = "after_outbox"
	// ObservationFaultBeforeClaimRelease is the failpoint of the orderly
	// shutdown release of the process-owned claims. It exists so the release
	// failure path is provable without a crashed process.
	ObservationFaultBeforeClaimRelease = "before_claim_release"
)

var (
	ErrObservationOutboxBackpressure = errors.New("metering/journalstore: observation economic outbox is full")
	ErrObservationOutboxClaimLost    = errors.New("metering/journalstore: observation economic outbox claim lost")
)

// ObservationOutboxItem is an immutable observation payload plus mutable
// relay-attempt metadata. The payload is copied from the canonical observation
// transaction and is never reconstructed from mutable billing state.
type ObservationOutboxItem struct {
	ID                     int64
	Observation            metering.Observation
	ObservationFingerprint string
	Status                 string
	AttemptCount           int
	NextAttemptAt          time.Time
	LeaseOwner             string
	LeaseUntil             time.Time
	LastError              string
}

// AppendObservationsWithOutbox appends observations and their durable relay
// entries in one local transaction. A failure after either side is written
// rolls both sides back, preserving the observation-to-work relationship.
func (s *DurableStore) AppendObservationsWithOutbox(ctx context.Context, observations []metering.Observation) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("metering/journalstore: nil store")
	}
	if ctx == nil {
		return fmt.Errorf("metering/journalstore: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(observations) == 0 {
		return nil
	}
	return s.appendObservationWithSQLiteRetryFunc(ctx, func(ctx context.Context) error {
		return s.appendObservationsWithOutboxAttempt(ctx, observations)
	})
}

func (s *DurableStore) appendObservationsWithOutboxAttempt(ctx context.Context, observations []metering.Observation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("metering/journalstore: observation outbox begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, observation := range observations {
		if err := s.AppendObservationInTx(ctx, tx, observation); err != nil {
			return err
		}
		if err := s.appendObservationOutboxInTx(ctx, tx, observation); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("metering/journalstore: observation outbox commit: %w", err)
	}
	return nil
}

func (s *DurableStore) appendObservationOutboxInTx(ctx context.Context, tx bun.Tx, observation metering.Observation) error {
	canonical, payload, err := canonicalObservationForStore(s.cfg.StoreID, observation)
	if err != nil {
		return err
	}
	fingerprint := canonical.Fingerprint()
	if fingerprint == "" {
		return fmt.Errorf("metering/journalstore: observation outbox fingerprint unavailable")
	}
	var existing observationOutboxRow
	err = tx.NewRaw(`SELECT id, observation_fingerprint, payload_json, status FROM metering_observation_economic_outbox WHERE store_id = ? AND observation_id = ? AND observation_revision = ? LIMIT 1`, s.cfg.StoreID, canonical.ID, int64(canonical.Revision)).Scan(ctx, &existing)
	if err == nil {
		detail := fmt.Sprintf("observation outbox identity=%q revision=%d", canonical.ID, canonical.Revision)
		if err := validateCanonicalObservationReplay(detail, existing.PayloadJSON, existing.ObservationFingerprint, canonical); err != nil {
			return err
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("metering/journalstore: observation outbox lookup: %w", err)
	}
	maxPending := s.cfg.ObservationOutboxMaxPending
	if maxPending == 0 {
		maxPending = defaultObservationOutboxMaxPending
	}
	var pending int
	if err := tx.NewRaw(`SELECT COUNT(1) FROM metering_observation_economic_outbox WHERE store_id = ? AND status <> 'delivered'`, s.cfg.StoreID).Scan(ctx, &pending); err != nil {
		return fmt.Errorf("metering/journalstore: observation outbox capacity: %w", err)
	}
	if pending >= maxPending {
		return fmt.Errorf("%w: pending=%d limit=%d", ErrObservationOutboxBackpressure, pending, maxPending)
	}
	now := s.now().UTC().UnixNano()
	if _, err := tx.NewRaw(`
INSERT INTO metering_observation_economic_outbox(
	store_id, observation_id, observation_revision, observation_fingerprint, payload_json,
	status, attempt_count, next_attempt_at_unix, lease_owner, lease_until_unix, last_error,
	created_at_unix, updated_at_unix
) VALUES (?, ?, ?, ?, ?, 'pending', 0, 0, '', 0, '', ?, ?)
ON CONFLICT DO NOTHING`, s.cfg.StoreID, canonical.ID, int64(canonical.Revision), fingerprint, string(payload), now, now).Exec(ctx); err != nil {
		return fmt.Errorf("metering/journalstore: observation outbox insert: %w", err)
	}
	var inserted observationOutboxRow
	if err := tx.NewRaw(`SELECT id, observation_fingerprint, payload_json, status FROM metering_observation_economic_outbox WHERE store_id = ? AND observation_id = ? AND observation_revision = ? LIMIT 1`, s.cfg.StoreID, canonical.ID, int64(canonical.Revision)).Scan(ctx, &inserted); err != nil {
		return fmt.Errorf("metering/journalstore: observation outbox verify insert: %w", err)
	}
	detail := fmt.Sprintf("observation outbox identity=%q revision=%d", canonical.ID, canonical.Revision)
	if err := validateCanonicalObservationReplay(detail, inserted.PayloadJSON, inserted.ObservationFingerprint, canonical); err != nil {
		return err
	}
	if err := s.observationFault(ObservationFaultAfterOutbox); err != nil {
		return err
	}
	return nil
}

type observationOutboxRow struct {
	ID                     int64  `bun:"id"`
	StoreID                string `bun:"store_id"`
	ObservationID          string `bun:"observation_id"`
	ObservationRevision    int64  `bun:"observation_revision"`
	ObservationFingerprint string `bun:"observation_fingerprint"`
	PayloadJSON            string `bun:"payload_json"`
	Status                 string `bun:"status"`
	AttemptCount           int64  `bun:"attempt_count"`
	NextAttemptAtUnix      int64  `bun:"next_attempt_at_unix"`
	LeaseOwner             string `bun:"lease_owner"`
	LeaseUntilUnix         int64  `bun:"lease_until_unix"`
	LastError              string `bun:"last_error"`
	CreatedAtUnix          int64  `bun:"created_at_unix"`
	UpdatedAtUnix          int64  `bun:"updated_at_unix"`
}

// ListPendingObservationOutbox returns bounded active entries for diagnostics
// and tests. It does not claim or acknowledge them.
func (s *DurableStore) ListPendingObservationOutbox(ctx context.Context, limit int) ([]ObservationOutboxItem, error) {
	if err := s.validateOutboxContext(ctx); err != nil {
		return nil, err
	}
	limit, err := normalizeOutboxLimit(limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, observation_id, observation_revision, observation_fingerprint, payload_json,
       status, attempt_count, next_attempt_at_unix, lease_owner, lease_until_unix, last_error,
       created_at_unix, updated_at_unix
FROM metering_observation_economic_outbox
WHERE store_id = ? AND status <> 'delivered'
ORDER BY created_at_unix ASC, id ASC LIMIT ?`, s.cfg.StoreID, limit)
	if err != nil {
		return nil, fmt.Errorf("metering/journalstore: list observation outbox: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]ObservationOutboxItem, 0, limit)
	for rows.Next() {
		var row observationOutboxRow
		if err := rows.Scan(&row.ID, &row.ObservationID, &row.ObservationRevision, &row.ObservationFingerprint, &row.PayloadJSON, &row.Status, &row.AttemptCount, &row.NextAttemptAtUnix, &row.LeaseOwner, &row.LeaseUntilUnix, &row.LastError, &row.CreatedAtUnix, &row.UpdatedAtUnix); err != nil {
			return nil, fmt.Errorf("metering/journalstore: scan observation outbox: %w", err)
		}
		item, err := s.decodeObservationOutboxRow(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metering/journalstore: list observation outbox rows: %w", err)
	}
	return items, nil
}

// ClaimObservationOutbox leases due entries to one process-owned relay. A
// lease expiry makes a processing row restartable after a crash.
func (s *DurableStore) ClaimObservationOutbox(ctx context.Context, owner string, limit int, lease time.Duration) ([]ObservationOutboxItem, error) {
	if err := s.validateOutboxContext(ctx); err != nil {
		return nil, err
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, fmt.Errorf("metering/journalstore: observation outbox owner is required")
	}
	limit, err := normalizeOutboxLimit(limit)
	if err != nil {
		return nil, err
	}
	if lease <= 0 {
		lease = defaultObservationOutboxLease
	}
	nowTime := s.now().UTC()
	now := nowTime.UnixNano()
	leaseUntil := nowTime.Add(lease).UnixNano()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("metering/journalstore: claim observation outbox begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
SELECT id, observation_id, observation_revision, observation_fingerprint, payload_json,
       status, attempt_count, next_attempt_at_unix, lease_owner, lease_until_unix, last_error,
       created_at_unix, updated_at_unix
FROM metering_observation_economic_outbox
WHERE store_id = ? AND next_attempt_at_unix <= ?
  AND ((status = 'pending') OR (status = 'processing' AND lease_until_unix <= ?))
ORDER BY created_at_unix ASC, id ASC LIMIT ?`, s.cfg.StoreID, now, now, limit)
	if err != nil {
		return nil, fmt.Errorf("metering/journalstore: claim observation outbox query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]ObservationOutboxItem, 0, limit)
	for rows.Next() {
		var row observationOutboxRow
		if err := rows.Scan(&row.ID, &row.ObservationID, &row.ObservationRevision, &row.ObservationFingerprint, &row.PayloadJSON, &row.Status, &row.AttemptCount, &row.NextAttemptAtUnix, &row.LeaseOwner, &row.LeaseUntilUnix, &row.LastError, &row.CreatedAtUnix, &row.UpdatedAtUnix); err != nil {
			return nil, fmt.Errorf("metering/journalstore: scan claim observation outbox: %w", err)
		}
		result, err := tx.NewRaw(`
UPDATE metering_observation_economic_outbox
SET status = 'processing', attempt_count = attempt_count + 1, lease_owner = ?, lease_until_unix = ?, updated_at_unix = ?
WHERE id = ? AND store_id = ? AND next_attempt_at_unix <= ?
  AND ((status = 'pending') OR (status = 'processing' AND lease_until_unix <= ?))`, owner, leaseUntil, now, row.ID, s.cfg.StoreID, now, now).Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("metering/journalstore: claim observation outbox update: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("metering/journalstore: claim observation outbox rows affected: %w", err)
		}
		if changed != 1 {
			continue
		}
		item, err := s.decodeObservationOutboxRow(row)
		if err != nil {
			return nil, err
		}
		item.Status = "processing"
		item.AttemptCount++
		item.LeaseOwner = owner
		item.LeaseUntil = time.Unix(0, leaseUntil).UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metering/journalstore: claim observation outbox rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("metering/journalstore: claim observation outbox commit: %w", err)
	}
	return items, nil
}

// CompleteObservationOutbox acknowledges a successfully enqueued work set.
func (s *DurableStore) CompleteObservationOutbox(ctx context.Context, id int64, owner string) error {
	return s.updateObservationOutboxLease(ctx, id, owner, true, 0, "")
}

// RetryObservationOutbox releases a failed relay attempt without losing the
// durable payload. The caller may choose a bounded delay for backpressure.
func (s *DurableStore) RetryObservationOutbox(ctx context.Context, id int64, owner string, delay time.Duration, relayErr error) error {
	message := ""
	if relayErr != nil {
		message = relayErr.Error()
		if len(message) > 4096 {
			message = message[:4096]
		}
	}
	return s.updateObservationOutboxLease(ctx, id, owner, false, delay, message)
}

// ReleaseObservationOutboxClaims makes process-owned in-flight entries
// immediately retryable during orderly shutdown. A crash without this call is
// still safe because the lease expires and the next process can reclaim it.
func (s *DurableStore) ReleaseObservationOutboxClaims(ctx context.Context, owner string) error {
	if err := s.validateOutboxContext(ctx); err != nil {
		return err
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return fmt.Errorf("metering/journalstore: observation outbox owner is required")
	}
	if err := s.observationFault(ObservationFaultBeforeClaimRelease); err != nil {
		return err
	}
	now := s.now().UTC().UnixNano()
	if _, err := s.db.NewRaw(`
UPDATE metering_observation_economic_outbox
SET status = 'pending', lease_owner = '', lease_until_unix = 0, next_attempt_at_unix = ?, updated_at_unix = ?
WHERE store_id = ? AND status = 'processing' AND lease_owner = ?`, now, now, s.cfg.StoreID, owner).Exec(ctx); err != nil {
		return fmt.Errorf("metering/journalstore: release observation outbox claims: %w", err)
	}
	return nil
}

func (s *DurableStore) updateObservationOutboxLease(ctx context.Context, id int64, owner string, delivered bool, delay time.Duration, lastError string) error {
	if err := s.validateOutboxContext(ctx); err != nil {
		return err
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return fmt.Errorf("metering/journalstore: observation outbox owner is required")
	}
	nowTime := s.now().UTC()
	now := nowTime.UnixNano()
	status := "pending"
	leaseUntil := int64(0)
	nextAttempt := nowTime.Add(delay).UnixNano()
	if delivered {
		status = "delivered"
		nextAttempt = 0
	}
	result, err := s.db.NewRaw(`
UPDATE metering_observation_economic_outbox
SET status = ?, lease_owner = '', lease_until_unix = ?, next_attempt_at_unix = ?, last_error = ?, updated_at_unix = ?
WHERE id = ? AND store_id = ? AND status = 'processing' AND lease_owner = ?`, status, leaseUntil, nextAttempt, lastError, now, id, s.cfg.StoreID, owner).Exec(ctx)
	if err != nil {
		return fmt.Errorf("metering/journalstore: update observation outbox: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("metering/journalstore: update observation outbox rows affected: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("%w: id=%d owner=%q", ErrObservationOutboxClaimLost, id, owner)
	}
	return nil
}

func (s *DurableStore) validateOutboxContext(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("metering/journalstore: nil store")
	}
	if ctx == nil {
		return fmt.Errorf("metering/journalstore: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func normalizeOutboxLimit(limit int) (int, error) {
	if limit <= 0 {
		limit = defaultObservationOutboxBatch
	}
	if limit > maxObservationOutboxBatch {
		return 0, fmt.Errorf("metering/journalstore: observation outbox limit %d exceeds %d", limit, maxObservationOutboxBatch)
	}
	return limit, nil
}

func (s *DurableStore) decodeObservationOutboxRow(row observationOutboxRow) (ObservationOutboxItem, error) {
	detail := fmt.Sprintf("observation outbox identity=%q revision=%d", row.ObservationID, row.ObservationRevision)
	canonical, err := decodeValidatedStoredObservation(s.cfg.StoreID, detail, row.PayloadJSON, row.ObservationFingerprint, row.ObservationID, row.ObservationRevision)
	if err != nil {
		return ObservationOutboxItem{}, err
	}
	return ObservationOutboxItem{
		ID: row.ID, Observation: canonical, ObservationFingerprint: row.ObservationFingerprint, Status: row.Status,
		AttemptCount: int(row.AttemptCount), NextAttemptAt: time.Unix(0, row.NextAttemptAtUnix).UTC(),
		LeaseOwner: row.LeaseOwner, LeaseUntil: time.Unix(0, row.LeaseUntilUnix).UTC(), LastError: row.LastError,
	}, nil
}

// decodeValidatedStoredObservation decodes one durable observation envelope,
// runs the canonical content contract before the store-scope check so
// semantically invalid content classifies as an invalid record, then verifies
// the duplicated identity columns and the recomputed full fingerprint against
// the durable columns. Raw JSON bytes are representation and are never
// compared. It returns the canonical observation.
func decodeValidatedStoredObservation(storeID, detail, storedPayloadJSON, storedFingerprint, wantObservationID string, wantRevision int64) (metering.Observation, error) {
	var stored metering.Observation
	if err := json.Unmarshal([]byte(storedPayloadJSON), &stored); err != nil {
		return metering.Observation{}, fmt.Errorf("metering/journalstore: %s: corrupt stored observation payload: %w: %v", detail, metering.ErrInvalidObservation, err)
	}
	canonical, err := stored.Canonical()
	if err != nil {
		return metering.Observation{}, fmt.Errorf("metering/journalstore: %s: invalid stored observation: %w", detail, err)
	}
	if canonical.Subject.StoreID != storeID || canonical.Correlation.StoreID != storeID {
		return metering.Observation{}, fmt.Errorf("metering/journalstore: %s: %w: observation store mismatch", detail, ErrQueryOutOfScope)
	}
	if canonical.ID != wantObservationID || wantRevision < 0 || uint64(wantRevision) != canonical.Revision {
		return metering.Observation{}, fmt.Errorf("%w: %s: stored observation identity drift", ErrIdentityCollision, detail)
	}
	if strings.TrimSpace(storedFingerprint) == "" {
		return metering.Observation{}, fmt.Errorf("%w: %s: stored observation fingerprint is missing", ErrIdentityCollision, detail)
	}
	if recomputed := canonical.Fingerprint(); recomputed == "" {
		return metering.Observation{}, fmt.Errorf("metering/journalstore: %s: stored observation fingerprint unavailable: %w", detail, metering.ErrInvalidObservation)
	} else if recomputed != storedFingerprint {
		return metering.Observation{}, fmt.Errorf("%w: %s: stored observation fingerprint mismatch", ErrIdentityCollision, detail)
	}
	return canonical, nil
}

// validateCanonicalObservationReplay decides replay vs collision for one durable
// observation envelope using canonical domain semantics only. Raw JSON bytes are
// representation, never idempotency authority: PostgreSQL JSONB normalizes
// whitespace and key order, so byte equality is never consulted on any dialect.
// Durable identity is (store, observation ID, revision) plus the validated full
// Observation.Fingerprint recomputed from canonical content; ReplayFingerprint
// covers only the approved receipt/lineage-placement replay differences.
func validateCanonicalObservationReplay(detail, storedPayloadJSON, storedFingerprint string, incoming metering.Observation) error {
	storedCanonical, err := decodeValidatedStoredObservation(incoming.Subject.StoreID, detail, storedPayloadJSON, storedFingerprint, incoming.ID, int64(incoming.Revision))
	if err != nil {
		return err
	}
	incomingCanonical, err := incoming.Canonical()
	if err != nil {
		return fmt.Errorf("metering/journalstore: %s: invalid incoming observation: %w", detail, err)
	}
	if incomingCanonical.Fingerprint() == storedFingerprint {
		return nil
	}
	// The durable row keeps the first full envelope. Replay identity excludes
	// transport receipt metadata and approved lineage-carrier placement, so an
	// equivalent delivery remains idempotent without overwriting that history.
	storedReplay, storedErr := storedCanonical.ReplayFingerprint()
	incomingReplay, incomingErr := incomingCanonical.ReplayFingerprint()
	if storedErr == nil && incomingErr == nil && storedReplay == incomingReplay {
		// ReceivedAt and equivalent lineage-carrier placement are transport
		// details. Preserve the first durable envelope just as
		// AppendObservationInTx does for a replay-equivalent retry.
		return nil
	}
	return fmt.Errorf("%w: %s", ErrIdentityCollision, detail)
}
