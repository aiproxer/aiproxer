package bunstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/jellydator/ttlcache/v3"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/driver/pgdriver"
	libsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func opErr(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("securesession/bunstore %s: %w", op, err)
}

// marshalJSONText marshals v as JSON text suitable for TEXT columns.
// Passing []byte to PostgreSQL binds as BYTEA and stores hex-escaped values
// that fail json.Unmarshal when read back as strings.
func marshalJSONText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Store persists secure-session state using Bun.
type Store struct {
	db   *bun.DB
	meta *sessionMetaCache
}

var (
	_ app.Store              = (*Store)(nil)
	_ app.SessionUsageRollup = (*Store)(nil)
)

// New returns a Store backed by db after applying schema. Closing the store closes the underlying sql.DB.
// ctx for migrate is [context.Background]; prefer [NewWithContext] from composition roots.
func New(db *bun.DB) (*Store, error) {
	return NewWithContext(context.Background(), db)
}

// NewWithContext returns a Store backed by db after applying schema, honoring ctx for migrate DDL.
func NewWithContext(ctx context.Context, db *bun.DB) (*Store, error) {
	return NewWithContextOptions(ctx, db, Options{})
}

// NewWithContextOptions returns a Store like [NewWithContext] with optional tuning.
func NewWithContextOptions(ctx context.Context, db *bun.DB, opts Options) (*Store, error) {
	if ctx == nil {
		return nil, fmt.Errorf("securesession/bunstore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("securesession/bunstore: nil bun db")
	}
	s := &Store{db: db}
	if err := runSecureSessionSchemaMigrate(ctx, db); err != nil {
		return nil, opErr("new", err)
	}
	if opts.SQLQueryCacheTTL > 0 {
		maxEntries := uint64(opts.SQLQueryCacheMaxEntries)
		if maxEntries == 0 {
			maxEntries = 4096
		}
		s.meta = newSessionMetaCache(opts.SQLQueryCacheTTL, maxEntries)
	}
	return s, nil
}

// Close closes the underlying database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) invalidateSessionMetaCache(id domain.SessionID) {
	if s.meta != nil {
		s.meta.invalidate(id)
	}
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) transcriptEnabledCached(ctx context.Context, q rowQuerier, id domain.SessionID) (bool, error) {
	if s.meta != nil {
		if it := s.meta.transcript.Get(id); it != nil {
			return it.Value(), nil
		}
	}
	var te int
	err := q.QueryRowContext(ctx, `SELECT transcript_enabled FROM aip_secure_sessions WHERE session_id = ?`, string(id)).Scan(&te)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, domain.ErrSessionNotFound
		}
		return false, err
	}
	en := te != 0
	if s.meta != nil {
		s.meta.transcript.Set(id, en, ttlcache.DefaultTTL)
	}
	return en, nil
}

func mapUniqueErr(err error) error {
	if err == nil {
		return nil
	}
	var pgErr pgdriver.Error
	if errors.As(err, &pgErr) && pgErr.Field('C') == "23505" {
		if uniqueErrIsResumeFingerprint(pgErr) {
			return domain.ErrDuplicateFingerprint
		}
		return domain.ErrDuplicateSessionID
	}
	return mapUniqueErrSQLite(err)
}

func uniqueErrIsResumeFingerprint(e pgdriver.Error) bool {
	var b strings.Builder
	b.Grow(128)
	b.WriteString(e.Field('n'))
	b.WriteByte(' ')
	b.WriteString(e.Field('M'))
	b.WriteByte(' ')
	b.WriteString(e.Field('D'))
	b.WriteByte(' ')
	b.WriteString(e.Field('s'))
	h := strings.ToLower(b.String())
	return strings.Contains(h, "resume_fingerprint") ||
		strings.Contains(h, "idx_aip_secure_sessions_resume_fp") ||
		strings.Contains(h, "aip_secure_sessions_resume_fingerprint")
}

func mapUniqueErrSQLite(err error) error {
	if err == nil {
		return nil
	}
	var se *libsqlite.Error
	if errors.As(err, &se) && se != nil && se.Code() == sqlite3.SQLITE_CONSTRAINT {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "unique") {
			return classifySQLiteUniqueConstraint(msg)
		}
		return err
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "unique constraint failed") && !strings.Contains(msg, "unique constraint") {
		return err
	}
	return classifySQLiteUniqueConstraint(msg)
}

func classifySQLiteUniqueConstraint(msg string) error {
	if strings.Contains(msg, "resume_fingerprint") ||
		strings.Contains(msg, "idx_aip_secure_sessions_resume_fp") ||
		strings.Contains(msg, "aip_secure_sessions.resume_fingerprint") {
		return domain.ErrDuplicateFingerprint
	}
	return domain.ErrDuplicateSessionID
}

func isFKConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	var pgErr pgdriver.Error
	if errors.As(err, &pgErr) && pgErr.Field('C') == "23503" {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "foreign key constraint failed") ||
		strings.Contains(msg, "foreign key violation") ||
		strings.Contains(msg, "23503")
}

func (s *Store) insertTurnIgnore(ctx context.Context, tx bun.Tx, sessionID, turnID string) error {
	switch s.db.Dialect().Name() {
	case dialect.SQLite:
		_, err := tx.ExecContext(
			ctx,
			`INSERT OR IGNORE INTO aip_secure_turns(session_id, turn_id) VALUES(?,?)`,
			sessionID,
			turnID,
		)
		return err
	case dialect.PG:
		_, err := tx.ExecContext(ctx,
			`INSERT INTO aip_secure_turns(session_id, turn_id) VALUES(?,?) ON CONFLICT DO NOTHING`,
			sessionID, turnID)
		return err
	default:
		return fmt.Errorf("securesession/bunstore: unsupported dialect for insert turn")
	}
}

const selectSession = `SELECT
		session_id, resume_fingerprint,
		owner_id, owner_issuer, owner_tenant,
		workspace_id, client_session_id, agent_digest,
		policy_version, transcript_enabled, effective_treatment, stricter_policy_resolution,
		route_hint, redaction_profile, audit_mode,
		a_leg_id, resume_eligible,
		status, quarantined_at_unix, quarantine_reason_code, quarantine_event_id,
		last_activity_unix, last_activity_source, created_at_unix,
		usage_in, usage_out, attempt_count,
		latest_attempt_trace_json, latest_attempt_outcome_json, latest_attempt_accounting_json
	FROM aip_secure_sessions WHERE `

func (s *Store) Create(ctx context.Context, rec domain.CreateRecord) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	fp := rec.ResumeFingerprint[:]
	te := 0
	if rec.Policy.TranscriptEnabled {
		te = 1
	}
	re := 0
	if rec.ResumeEligible {
		re = 1
	}
	_, err := s.db.ExecContext(
		ctx, `INSERT INTO aip_secure_sessions(
		session_id, resume_fingerprint,
		owner_id, owner_issuer, owner_tenant,
		workspace_id, client_session_id, agent_digest,
		policy_version, transcript_enabled, effective_treatment, stricter_policy_resolution,
		route_hint, redaction_profile, audit_mode,
		a_leg_id, resume_eligible,
		status, quarantined_at_unix, quarantine_reason_code, quarantine_event_id,
		last_activity_unix, last_activity_source, created_at_unix,
		usage_in, usage_out, attempt_count,
		latest_attempt_trace_json, latest_attempt_outcome_json, latest_attempt_accounting_json
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'',0,'','',?,?,?,0,0,0,'{}','{}','{}')`,
		string(rec.SessionID), fp,
		rec.Owner.ID, rec.Owner.Issuer, rec.Owner.Tenant,
		rec.Workspace.ID, rec.ClientHints.ClientSessionID, rec.ClientHints.AgentIdentityDigest,
		rec.Policy.PolicyVersion, te, rec.Policy.EffectiveTreatment, rec.Policy.StricterPolicyResolution,
		rec.Policy.RouteHint, rec.Policy.RedactionProfile, rec.Policy.AuditMode,
		rec.ALegID, re,
		rec.CreatedAt.UnixNano(), string(domain.ActivitySystem), rec.CreatedAt.UnixNano(),
	)
	if err != nil {
		return domain.Record{}, mapUniqueErr(err)
	}
	if s.meta != nil {
		s.meta.seedAfterCreate(rec.SessionID, rec.Policy.TranscriptEnabled)
	}
	return s.LoadByID(ctx, rec.SessionID)
}

func (s *Store) LoadByID(ctx context.Context, id domain.SessionID) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	row := s.db.QueryRowContext(ctx, selectSession+`session_id = ?`, string(id))
	return scanRecord(row)
}

func (s *Store) LoadByResumeFingerprint(ctx context.Context, fp domain.TokenFingerprint) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	row := s.db.QueryRowContext(ctx, selectSession+`resume_fingerprint = ?`, fp[:])
	return scanRecord(row)
}

func (s *Store) LoadByALegID(ctx context.Context, aLegID string) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	row := s.db.QueryRowContext(ctx, selectSession+`a_leg_id = ?`, aLegID)
	return scanRecord(row)
}

func (s *Store) TouchActivity(ctx context.Context, id domain.SessionID, at time.Time, source domain.ActivitySource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	nano := at.UnixNano()
	res, err := s.db.ExecContext(ctx, `UPDATE aip_secure_sessions SET
		last_activity_unix = CASE WHEN ? > last_activity_unix THEN ? ELSE last_activity_unix END,
		last_activity_source = CASE WHEN ? > last_activity_unix THEN ? ELSE last_activity_source END
		WHERE session_id = ?`, nano, nano, nano, string(source), string(id))
	if err != nil {
		return opErr("touch", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return opErr("touch rows affected", err)
	}
	if n == 0 {
		s.invalidateSessionMetaCache(id)
		return domain.ErrSessionNotFound
	}
	return nil
}
