package sessionclassification

import (
	"context"
	"database/sql"
	"errors"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

var (
	ErrInvalidBunDB          = errors.New("session classification: invalid Bun database")
	ErrUnsupportedBunDialect = errors.New("session classification: unsupported Bun dialect")
	ErrBunSchema             = errors.New("session classification: schema operation failed")
	ErrBunLoad               = errors.New("session classification: durable load failed")
	ErrBunPromote            = errors.New("session classification: durable promotion failed")
	ErrBunRemoteClaim        = errors.New("session classification: durable remote claim failed")
	ErrBunRemoteComplete     = errors.New("session classification: durable remote completion failed")
	ErrInvalidBunRecord      = errors.New("session classification: invalid durable record")
	ErrBunRecordMissing      = errors.New("session classification: durable winner disappeared")
)

const (
	promoteSQL = `INSERT INTO session_classification (
		scope_kind, scope_id, kind, source, confidence, evidence_code,
		classification_revision, remote_attempts, remote_lease_id,
		remote_lease_until, remote_next_eligible_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', NULL, NULL, ?)
	ON CONFLICT (scope_kind, scope_id) DO UPDATE SET
		kind = excluded.kind,
		source = excluded.source,
		confidence = excluded.confidence,
		evidence_code = excluded.evidence_code,
		classification_revision = excluded.classification_revision,
		remote_lease_id = '',
		remote_lease_until = NULL,
		remote_next_eligible_at = NULL,
		updated_at = excluded.updated_at
	WHERE session_classification.kind = ''
	RETURNING *`
	claimRemoteSQL = `INSERT INTO session_classification (
		scope_kind, scope_id, kind, source, confidence, evidence_code,
		classification_revision, remote_attempts, remote_lease_id,
		remote_lease_until, remote_next_eligible_at, updated_at
	) VALUES (?, ?, '', '', '', '', 0, 1, ?, ?, NULL, ?)
	ON CONFLICT (scope_kind, scope_id) DO UPDATE SET
		remote_attempts = session_classification.remote_attempts + 1,
		remote_lease_id = excluded.remote_lease_id,
		remote_lease_until = excluded.remote_lease_until,
		updated_at = excluded.updated_at
	WHERE session_classification.kind = ''
		AND session_classification.remote_attempts >= 0
		AND session_classification.remote_attempts < ?
		AND (
			(session_classification.remote_lease_id = '' AND session_classification.remote_lease_until IS NULL)
			OR (session_classification.remote_lease_id <> '' AND session_classification.remote_lease_until <= ?)
		)
		AND (
			session_classification.remote_next_eligible_at IS NULL
			OR session_classification.remote_next_eligible_at <= ?
		)
	RETURNING *`
	completeRemoteNeutralSQL = `UPDATE session_classification SET
		remote_lease_id = '',
		remote_lease_until = NULL,
		remote_next_eligible_at = ?,
		updated_at = ?
	WHERE scope_kind = ? AND scope_id = ?
		AND remote_lease_id = ? AND remote_attempts = ?
		AND kind = '' AND remote_lease_until > ?
	RETURNING *`
	completeRemotePositiveSQL = `UPDATE session_classification SET
		kind = ?,
		source = ?,
		confidence = ?,
		evidence_code = ?,
		classification_revision = 1,
		remote_lease_id = '',
		remote_lease_until = NULL,
		remote_next_eligible_at = NULL,
		updated_at = ?
	WHERE scope_kind = ? AND scope_id = ?
		AND remote_lease_id = ? AND remote_attempts = ?
		AND kind = '' AND remote_lease_until > ?
	RETURNING *`
)

// BunStore persists bounded session-classification state in the feature-owned
// logical table. The Bun DB is borrowed from featurehost and remains owned by
// its host.
type BunStore struct {
	db *bun.DB
}

var _ featurestate.Store = (*BunStore)(nil)

type bunClassificationRow struct {
	bun.BaseModel `bun:"table:session_classification"`

	ScopeKind              featurestate.ScopeKind       `bun:"scope_kind,pk,notnull"`
	ScopeID                string                       `bun:"scope_id,pk,notnull"`
	Kind                   session.Kind                 `bun:"kind,notnull"`
	Source                 session.ClassificationSource `bun:"source,notnull"`
	Confidence             session.ConfidenceBand       `bun:"confidence,notnull"`
	EvidenceCode           session.EvidenceCode         `bun:"evidence_code,notnull"`
	ClassificationRevision int64                        `bun:"classification_revision,type:BIGINT,notnull"`
	RemoteAttempts         int64                        `bun:"remote_attempts,type:INTEGER,notnull"`
	RemoteLeaseID          string                       `bun:"remote_lease_id,notnull"`
	RemoteLeaseUntil       *time.Time                   `bun:"remote_lease_until"`
	RemoteNextEligibleAt   *time.Time                   `bun:"remote_next_eligible_at"`
	UpdatedAt              time.Time                    `bun:"updated_at,notnull"`
}

// NewBunStore validates and retains a borrowed Bun DB without performing I/O.
func NewBunStore(db *bun.DB) (*BunStore, error) {
	if db == nil || db.Dialect() == nil {
		return nil, ErrInvalidBunDB
	}
	switch db.Dialect().Name() {
	case dialect.SQLite, dialect.PG:
		return &BunStore{db: db}, nil
	default:
		return nil, ErrUnsupportedBunDialect
	}
}

// EnsureSchema creates the classification table when the owning lifecycle is
// ready to initialize durable feature state. It never drops or resets rows.
func (s *BunStore) EnsureSchema(ctx context.Context) error {
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return ErrInvalidBunDB
	}
	if err := runSessionClassificationSchemaMigrations(ctx, s.db); err != nil {
		return ErrBunSchema
	}
	return nil
}

// Load returns the record for a proxy-authority key without creating missing
// rows.
func (s *BunStore) Load(ctx context.Context, key featurestate.Key) (featurestate.Record, bool, error) {
	if err := featurestate.ValidateKey(key); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.Record{}, false, err
	}
	if s == nil || s.db == nil {
		return featurestate.Record{}, false, ErrInvalidBunDB
	}

	var row bunClassificationRow
	err := s.db.NewSelect().Model(&row).
		Where("scope_kind = ?", key.Kind).
		Where("scope_id = ?", key.ID).
		Limit(1).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return featurestate.Record{}, false, nil
	}
	if err != nil {
		return featurestate.Record{}, false, ErrBunLoad
	}
	record, err := row.record()
	if err != nil {
		return featurestate.Record{}, false, err
	}
	return record, true, nil
}

// Promote atomically records the first valid positive classification. The
// single dialect-neutral UPSERT installs a positive row or updates an unknown
// row while preserving its remote-attempt history.
func (s *BunStore) Promote(ctx context.Context, key featurestate.Key, proposal session.Classification, now time.Time) (featurestate.Record, bool, error) {
	if err := featurestate.ValidateKey(key); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreTime(now); err != nil {
		return featurestate.Record{}, false, err
	}
	if err := featurestate.ValidatePositiveProposal(proposal); err != nil {
		return featurestate.Record{}, false, err
	}
	if s == nil || s.db == nil {
		return featurestate.Record{}, false, ErrInvalidBunDB
	}

	proposal.Revision = 1
	var row bunClassificationRow
	err := s.db.NewRaw(promoteSQL,
		key.Kind,
		key.ID,
		proposal.Kind,
		proposal.Source,
		proposal.Confidence,
		proposal.Evidence,
		int64(proposal.Revision),
		now.UTC(),
	).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		record, found, loadErr := s.Load(ctx, key)
		if loadErr != nil {
			return featurestate.Record{}, false, loadErr
		}
		if !found {
			return featurestate.Record{}, false, ErrBunRecordMissing
		}
		return record, false, nil
	}
	if err != nil {
		return featurestate.Record{}, false, ErrBunPromote
	}
	record, err := row.record()
	if err != nil {
		return featurestate.Record{}, false, err
	}
	return record, true, nil
}

// ClaimRemote atomically consumes one finite attempt and records its lease
// before the caller performs remote I/O. A single conditional upsert arbitrates
// claimers across store instances without a process-wide lock or open transaction.
func (s *BunStore) ClaimRemote(ctx context.Context, key featurestate.Key, now time.Time, maxAttempts uint32, leaseTTL time.Duration, retryBackoff time.Duration) (featurestate.RemoteClaim, featurestate.Record, bool, error) {
	if err := featurestate.ValidateKey(key); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}
	if err := featurestate.ValidateStoreTime(now); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}
	if maxAttempts == 0 || maxAttempts > featurestate.MaxRemoteAttemptsPerSession || leaseTTL <= 0 || leaseTTL > featurestate.MaxRemoteLeaseTTL || retryBackoff < 0 || retryBackoff > featurestate.MaxRemoteRetryBackoff {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, featurestate.ErrInvalidRemoteOptions
	}
	if s == nil || s.db == nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, ErrInvalidBunDB
	}

	// The cryptographic nonce is independent of this BunStore instance, so
	// separate replicas do not rely on a process-local sequence for ownership.
	leaseID, err := randomLeaseNonce()
	if err != nil || !validLeaseID(leaseID) {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, featurestate.ErrLeaseNonce
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}

	leaseUntil := featurestate.RoundRemoteDeadlineUpToMicrosecond(now.Add(leaseTTL))
	// Lease/backoff deadlines are microsecond-aligned. Flooring now preserves
	// exact before/at/after comparisons on both nanosecond and microsecond DBs.
	eligibilityTime := now.Truncate(time.Microsecond)
	var row bunClassificationRow
	err = s.db.NewRaw(claimRemoteSQL,
		key.Kind, key.ID, leaseID, leaseUntil.UTC(), now.UTC(),
		int64(maxAttempts), eligibilityTime.UTC(), eligibilityTime.UTC(),
	).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		record, found, loadErr := s.Load(ctx, key)
		if loadErr != nil {
			return featurestate.RemoteClaim{}, featurestate.Record{}, false, loadErr
		}
		if !found {
			return featurestate.RemoteClaim{}, featurestate.Record{}, false, nil
		}
		return featurestate.RemoteClaim{}, record, false, nil
	}
	if err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, ErrBunRemoteClaim
	}
	record, err := row.record()
	if err != nil {
		return featurestate.RemoteClaim{}, featurestate.Record{}, false, err
	}
	claim := featurestate.RemoteClaim{Key: key, LeaseID: leaseID, Attempt: record.RemoteAttempts, RetryBackoff: retryBackoff}
	return claim, record, true, nil
}

// CompleteRemote accepts only the currently held, unexpired lease. The guarded
// update lets a concurrent local promotion win without rewriting its positive.
func (s *BunStore) CompleteRemote(ctx context.Context, claim featurestate.RemoteClaim, result featurestate.RemoteCompletion, now time.Time) (featurestate.Record, error) {
	if err := validateBunRemoteClaim(claim); err != nil {
		return featurestate.Record{}, err
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.Record{}, err
	}
	if err := featurestate.ValidateStoreTime(now); err != nil {
		return featurestate.Record{}, err
	}
	proposal := result.Proposal
	if proposal != (session.Classification{}) {
		if err := featurestate.ValidatePositiveProposal(proposal); err != nil || proposal.Source != session.SourceRemote {
			return featurestate.Record{}, featurestate.ErrInvalidProposal
		}
		proposal.Revision = 1
	}
	if s == nil || s.db == nil {
		return featurestate.Record{}, ErrInvalidBunDB
	}
	if err := featurestate.ValidateStoreContext(ctx); err != nil {
		return featurestate.Record{}, err
	}

	eligibilityTime := now.Truncate(time.Microsecond).UTC()
	var row bunClassificationRow
	var err error
	if proposal != (session.Classification{}) {
		err = s.db.NewRaw(completeRemotePositiveSQL,
			proposal.Kind, proposal.Source, proposal.Confidence, proposal.Evidence, now.UTC(),
			claim.Key.Kind, claim.Key.ID, claim.LeaseID, int64(claim.Attempt), eligibilityTime,
		).Scan(ctx, &row)
	} else {
		var nextEligibleAt *time.Time
		if claim.RetryBackoff > 0 {
			deadline := featurestate.RoundRemoteDeadlineUpToMicrosecond(now.Add(claim.RetryBackoff)).UTC()
			nextEligibleAt = &deadline
		}
		err = s.db.NewRaw(completeRemoteNeutralSQL,
			nextEligibleAt, now.UTC(), claim.Key.Kind, claim.Key.ID,
			claim.LeaseID, int64(claim.Attempt), eligibilityTime,
		).Scan(ctx, &row)
	}
	if errors.Is(err, sql.ErrNoRows) {
		current, found, loadErr := s.Load(ctx, claim.Key)
		if loadErr != nil {
			return featurestate.Record{}, loadErr
		}
		if !found {
			return featurestate.Record{}, featurestate.ErrStaleRemoteClaim
		}
		return current, featurestate.ErrStaleRemoteClaim
	}
	if err != nil {
		return featurestate.Record{}, ErrBunRemoteComplete
	}
	return row.record()
}

func validateBunRemoteClaim(claim featurestate.RemoteClaim) error {
	if err := featurestate.ValidateKey(claim.Key); err != nil || !validLeaseID(claim.LeaseID) || claim.Attempt == 0 || claim.Attempt > featurestate.MaxRemoteAttemptsPerSession || claim.RetryBackoff < 0 || claim.RetryBackoff > featurestate.MaxRemoteRetryBackoff {
		return featurestate.ErrInvalidRemoteClaim
	}
	return nil
}

func (row bunClassificationRow) record() (featurestate.Record, error) {
	if row.ClassificationRevision < 0 || row.RemoteAttempts < 0 || uint64(row.RemoteAttempts) > uint64(featurestate.MaxRemoteAttemptsPerSession) {
		return featurestate.Record{}, ErrInvalidBunRecord
	}
	classification := session.Classification{
		Kind:       row.Kind,
		Source:     row.Source,
		Confidence: row.Confidence,
		Evidence:   row.EvidenceCode,
		Revision:   uint64(row.ClassificationRevision),
	}
	if classification.Validate() != nil {
		return featurestate.Record{}, ErrInvalidBunRecord
	}
	if row.RemoteLeaseID != "" && (!validLeaseID(row.RemoteLeaseID) || row.RemoteLeaseUntil == nil) {
		return featurestate.Record{}, ErrInvalidBunRecord
	}
	if row.RemoteLeaseID == "" && row.RemoteLeaseUntil != nil {
		return featurestate.Record{}, ErrInvalidBunRecord
	}

	record := featurestate.Record{
		Key:            featurestate.Key{Kind: row.ScopeKind, ID: row.ScopeID},
		Classification: classification,
		RemoteAttempts: uint32(row.RemoteAttempts),
		RemoteLeaseID:  row.RemoteLeaseID,
		UpdatedAt:      row.UpdatedAt,
	}
	if row.RemoteLeaseUntil != nil {
		record.RemoteLeaseUntil = *row.RemoteLeaseUntil
	}
	if row.RemoteNextEligibleAt != nil {
		record.RemoteNextEligibleAt = *row.RemoteNextEligibleAt
	}
	if featurestate.ValidateKey(record.Key) != nil || row.UpdatedAt.IsZero() {
		return featurestate.Record{}, ErrInvalidBunRecord
	}
	return record, nil
}
