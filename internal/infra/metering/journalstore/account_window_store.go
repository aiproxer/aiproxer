package journalstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/uptrace/bun"
)

const (
	accountWindowCursorPrefix  = "aw1."
	accountWindowCursorVersion = 1
	// Current/as-of projection is rebuilt from immutable history. Keep the
	// rebuild bounded so one account cannot turn a request into an unbounded
	// journal scan. History remains available through normal pagination.
	accountWindowProjectionMaxRows = 10_000
)

// These aliases expose the domain query contract at the journal adapter
// boundary without duplicating DTOs in infrastructure.
type (
	AccountWindowQuery           = coremetering.AccountWindowQuery
	AccountWindowObservationPage = coremetering.AccountWindowObservationPage
	AccountWindowProjection      = coremetering.AccountWindowProjection
	AccountWindowProjectionPage  = coremetering.AccountWindowProjectionPage
)

// AppendAccountWindowObservation validates and appends one provider allowance
// snapshot. The canonical observation journal remains the sole authority;
// this spelling only prevents callers from accidentally treating a non-gauge
// or B-leg observation as an account-window record.
func (s *DurableStore) AppendAccountWindowObservation(ctx context.Context, observation aipsdkmetering.Observation) error {
	if observation.Subject.Kind != aipsdkmetering.SubjectAccountWindow {
		return fmt.Errorf("metering/journalstore: account-window observation requires account_window subject")
	}
	if err := validateProviderAllowanceObservation(observation); err != nil {
		return err
	}
	return s.AppendObservation(ctx, observation)
}

// AppendAccountWindowObservationInTx is the transaction-composing form of
// AppendAccountWindowObservation. It never commits or rolls back tx.
func (s *DurableStore) AppendAccountWindowObservationInTx(ctx context.Context, tx bun.Tx, observation aipsdkmetering.Observation) error {
	if observation.Subject.Kind != aipsdkmetering.SubjectAccountWindow {
		return fmt.Errorf("metering/journalstore: account-window observation requires account_window subject")
	}
	if err := validateProviderAllowanceObservation(observation); err != nil {
		return err
	}
	return s.AppendObservationInTx(ctx, tx, observation)
}

// validateProviderAllowanceObservation keeps the provider allowance API
// narrower than the generic account-window observation journal. Local and
// statement account-window observations are valid generic evidence, but they
// must never be admitted to or returned from this provider allowance plane.
func validateProviderAllowanceObservation(observation aipsdkmetering.Observation) error {
	if observation.Subject.Kind != aipsdkmetering.SubjectAccountWindow {
		return fmt.Errorf("%w: provider allowance requires account_window subject", aipsdkmetering.ErrInvalidObservation)
	}
	if observation.Origin != aipsdkmetering.OriginProvider {
		return fmt.Errorf("%w: provider allowance requires provider origin", aipsdkmetering.ErrInvalidObservation)
	}
	if observation.Authority != aipsdkmetering.AuthorityObservedClaim {
		return fmt.Errorf("%w: provider allowance requires observed provider authority", aipsdkmetering.ErrInvalidObservation)
	}
	return nil
}

// ListAccountWindowObservations returns immutable account-window history in
// effective observed-at order. Provider account is mandatory; pool, window,
// reset and AsOf are selective refinements.
func (s *DurableStore) ListAccountWindowObservations(ctx context.Context, query AccountWindowQuery) (AccountWindowObservationPage, error) {
	if s == nil || s.db == nil {
		return AccountWindowObservationPage{}, fmt.Errorf("metering/journalstore: nil store")
	}
	if ctx == nil {
		return AccountWindowObservationPage{}, fmt.Errorf("metering/journalstore: nil context")
	}
	storeID, err := normalizeAccountWindowQuery(query, s.cfg.StoreID)
	if err != nil {
		return AccountWindowObservationPage{}, err
	}
	limit, err := normalizeV2Limit(query.Limit, s.defaultPageSize)
	if err != nil {
		return AccountWindowObservationPage{}, err
	}
	filterHash := accountWindowFilterHash(storeID, query)
	position, err := decodeAccountWindowCursor(query.Cursor, "history", storeID, filterHash)
	if err != nil {
		return AccountWindowObservationPage{}, err
	}
	rows, err := s.queryAccountWindowRows(ctx, query, storeID, position, limit+1)
	if err != nil {
		return AccountWindowObservationPage{}, err
	}
	page := AccountWindowObservationPage{Observations: make([]aipsdkmetering.Observation, 0, minInt(len(rows), limit))}
	if len(rows) > limit {
		last := rows[limit-1]
		page.NextCursor = encodeAccountWindowCursor(accountWindowCursor{
			Version: accountWindowCursorVersion, Kind: "history", StoreID: storeID, FilterHash: filterHash,
			ObservedAt: last.observedAt, ReceivedAt: last.receivedAt, StreamID: last.streamID,
			Sequence: last.sequence, ObservationID: last.observationID, Revision: last.revision, RowID: last.rowID,
		})
		rows = rows[:limit]
	}
	for _, row := range rows {
		observation, err := decodeAccountWindowRow(row)
		if err != nil {
			return AccountWindowObservationPage{}, err
		}
		page.Observations = append(page.Observations, observation)
	}
	return page, nil
}

// ProjectAccountWindows rebuilds one current/as-of projection per matching
// account/pool/window/reset identity from immutable observations. It never
// performs SQL arithmetic: percentages, limits and remaining values remain
// independent gauge measures.
func (s *DurableStore) ProjectAccountWindows(ctx context.Context, query AccountWindowQuery) (AccountWindowProjectionPage, error) {
	if s == nil || s.db == nil {
		return AccountWindowProjectionPage{}, fmt.Errorf("metering/journalstore: nil store")
	}
	if ctx == nil {
		return AccountWindowProjectionPage{}, fmt.Errorf("metering/journalstore: nil context")
	}
	storeID, err := normalizeAccountWindowQuery(query, s.cfg.StoreID)
	if err != nil {
		return AccountWindowProjectionPage{}, err
	}
	limit, err := normalizeV2Limit(query.Limit, s.defaultPageSize)
	if err != nil {
		return AccountWindowProjectionPage{}, err
	}
	filterHash := accountWindowFilterHash(storeID, query)
	position, err := decodeAccountWindowCursor(query.Cursor, "projections", storeID, filterHash)
	if err != nil {
		return AccountWindowProjectionPage{}, err
	}
	// Projection pagination is applied after reduction by reset-scoped identity;
	// the history cursor must not discard observations needed for a later field.
	loadQuery := query
	loadQuery.Cursor = ""
	rows, err := s.queryAccountWindowRows(ctx, loadQuery, storeID, accountWindowCursor{}, accountWindowProjectionMaxRows+1)
	if err != nil {
		return AccountWindowProjectionPage{}, err
	}
	if len(rows) > accountWindowProjectionMaxRows {
		return AccountWindowProjectionPage{}, fmt.Errorf("%w: account-window projection history exceeds %d rows", ErrPageSizeExceeded, accountWindowProjectionMaxRows)
	}
	observations := make([]aipsdkmetering.Observation, 0, len(rows))
	for _, row := range rows {
		observation, decodeErr := decodeAccountWindowRow(row)
		if decodeErr != nil {
			return AccountWindowProjectionPage{}, decodeErr
		}
		observations = append(observations, observation)
	}
	projections, err := coremetering.ProjectAccountWindows(observations, query.AsOf)
	if err != nil {
		return AccountWindowProjectionPage{}, fmt.Errorf("metering/journalstore: project account windows: %w", err)
	}
	start := 0
	if query.Cursor != "" {
		for start < len(projections) && projections[start].IdentityKey() <= position.Identity {
			start++
		}
	}
	page := AccountWindowProjectionPage{}
	if start >= len(projections) {
		return page, nil
	}
	end := minInt(start+limit, len(projections))
	page.Projections = append(page.Projections, projections[start:end]...)
	if end < len(projections) {
		last := page.Projections[len(page.Projections)-1]
		page.NextCursor = encodeAccountWindowCursor(accountWindowCursor{
			Version: accountWindowCursorVersion, Kind: "projections", StoreID: storeID, FilterHash: filterHash,
			Identity: last.IdentityKey(),
		})
	}
	return page, nil
}

// QueryAccountWindowObservations is a compatibility spelling for callers
// that use query-oriented naming.
func (s *DurableStore) QueryAccountWindowObservations(ctx context.Context, query AccountWindowQuery) (AccountWindowObservationPage, error) {
	return s.ListAccountWindowObservations(ctx, query)
}

type accountWindowCursor struct {
	Version       int    `json:"version"`
	Kind          string `json:"kind"`
	StoreID       string `json:"store_id"`
	FilterHash    string `json:"filter_hash"`
	ObservedAt    int64  `json:"observed_at,omitempty"`
	ReceivedAt    int64  `json:"received_at,omitempty"`
	StreamID      string `json:"stream_id,omitempty"`
	Sequence      int64  `json:"sequence,omitempty"`
	ObservationID string `json:"observation_id,omitempty"`
	Revision      int64  `json:"revision,omitempty"`
	RowID         int64  `json:"row_id,omitempty"`
	Identity      string `json:"identity,omitempty"`
}

type accountWindowRow struct {
	payload            string
	fingerprint        string
	tenantID           string
	providerAccountKey string
	poolID             string
	windowID           string
	resetAt            int64
	observedAt         int64
	receivedAt         int64
	streamID           string
	sequence           int64
	observationID      string
	revision           int64
	rowID              int64
}

func normalizeAccountWindowQuery(query AccountWindowQuery, openedStore string) (string, error) {
	storeID, err := normalizeV2StoreID(query.StoreID, openedStore)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(query.ProviderAccountKey) == "" {
		return "", ErrQueryTooBroad
	}
	if query.ResetAt != nil && query.ResetAt.IsZero() {
		return "", fmt.Errorf("%w: zero reset_at", ErrQueryOutOfScope)
	}
	for field, value := range map[string]string{
		"tenant_id": query.TenantID, "provider_account_key": query.ProviderAccountKey,
		"pool_id": query.PoolID, "window_id": query.WindowID,
	} {
		if len(value) > aipsdkmetering.MaxSchemaIDBytes {
			return "", fmt.Errorf("%w: %s exceeds %d bytes", ErrQueryOutOfScope, field, aipsdkmetering.MaxSchemaIDBytes)
		}
	}
	return storeID, nil
}

func accountWindowFilterHash(storeID string, query AccountWindowQuery) string {
	reset := int64(0)
	resetAtSet := query.ResetAt != nil
	if query.ResetAt != nil {
		reset = query.ResetAt.UTC().UnixNano()
	}
	// Presence must be bound separately: the explicit Unix epoch has the same
	// Unix-nanosecond value as an omitted reset filter, but a different predicate.
	return filterHash(struct {
		StoreID, TenantID, ProviderAccountKey, PoolID, WindowID string
		ResetAtSet                                              bool
		ResetAt, AsOf                                           int64
	}{storeID, strings.TrimSpace(query.TenantID), strings.TrimSpace(query.ProviderAccountKey), strings.TrimSpace(query.PoolID), strings.TrimSpace(query.WindowID), resetAtSet, reset, query.AsOf.UTC().UnixNano()})
}

func (s *DurableStore) queryAccountWindowRows(ctx context.Context, query AccountWindowQuery, storeID string, position accountWindowCursor, limit int) ([]accountWindowRow, error) {
	where := []string{
		"f.store_id = ?", "f.payload_kind = 'observation'", "f.observation_subject_kind = ?", "f.observation_provider_account_key = ?",
	}
	args := []any{storeID, string(aipsdkmetering.SubjectAccountWindow), strings.TrimSpace(query.ProviderAccountKey)}
	if strings.TrimSpace(query.TenantID) != "" {
		where = append(where, "f.observation_tenant_id = ?")
		args = append(args, strings.TrimSpace(query.TenantID))
	}
	if strings.TrimSpace(query.PoolID) != "" {
		where = append(where, "f.observation_pool_id = ?")
		args = append(args, strings.TrimSpace(query.PoolID))
	}
	if strings.TrimSpace(query.WindowID) != "" {
		where = append(where, "f.observation_window_id = ?")
		args = append(args, strings.TrimSpace(query.WindowID))
	}
	if query.ResetAt != nil {
		where = append(where, "f.observation_reset_at_unix = ?")
		args = append(args, query.ResetAt.UTC().UnixNano())
	}
	if !query.AsOf.IsZero() {
		where = append(where, "f.observation_observed_at_unix <= ?")
		args = append(args, query.AsOf.UTC().UnixNano())
	}
	if position.Kind == "history" {
		where = append(where, `(
			f.observation_observed_at_unix > ? OR
			(f.observation_observed_at_unix = ? AND (
				f.observation_received_at_unix > ? OR
				(f.observation_received_at_unix = ? AND (
					f.stream_id > ? OR
					(f.stream_id = ? AND (
						f.sequence > ? OR
						(f.sequence = ? AND (
							f.observation_id > ? OR
							(f.observation_id = ? AND (
								f.observation_revision > ? OR
								(f.observation_revision = ? AND f.id > ?)
							))
						))
					))
				))
			))
		)`)
		args = append(args, position.ObservedAt, position.ObservedAt, position.ReceivedAt, position.ReceivedAt, position.StreamID, position.StreamID, position.Sequence, position.Sequence, position.ObservationID, position.ObservationID, position.Revision, position.Revision, position.RowID)
	}
	querySQL := `SELECT f.payload_json, f.observation_fingerprint, f.observation_tenant_id, f.observation_provider_account_key, f.observation_pool_id, f.observation_window_id, f.observation_reset_at_unix, f.observation_observed_at_unix, f.observation_received_at_unix, f.stream_id, f.sequence, f.observation_id, f.observation_revision, f.id FROM metering_facts f WHERE ` + strings.Join(where, " AND ") + ` ORDER BY f.observation_observed_at_unix ASC, f.observation_received_at_unix ASC, f.stream_id ASC, f.sequence ASC, f.observation_id ASC, f.observation_revision ASC, f.id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("metering/journalstore: list account-window observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]accountWindowRow, 0, limit)
	for rows.Next() {
		var row accountWindowRow
		if err := rows.Scan(&row.payload, &row.fingerprint, &row.tenantID, &row.providerAccountKey, &row.poolID, &row.windowID, &row.resetAt, &row.observedAt, &row.receivedAt, &row.streamID, &row.sequence, &row.observationID, &row.revision, &row.rowID); err != nil {
			return nil, fmt.Errorf("metering/journalstore: scan account-window observation: %w", err)
		}
		if row.sequence < 0 || row.revision <= 0 || row.rowID <= 0 {
			return nil, fmt.Errorf("metering/journalstore: invalid account-window observation search projection")
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("metering/journalstore: account-window observation rows: %w", err)
	}
	return result, nil
}

func decodeAccountWindowRow(row accountWindowRow) (aipsdkmetering.Observation, error) {
	var observation aipsdkmetering.Observation
	if err := json.Unmarshal([]byte(row.payload), &observation); err != nil {
		return aipsdkmetering.Observation{}, fmt.Errorf("metering/journalstore: decode account-window observation: %w", err)
	}
	if err := observation.Validate(); err != nil {
		return aipsdkmetering.Observation{}, fmt.Errorf("metering/journalstore: invalid stored account-window observation: %w", err)
	}
	if err := validateProviderAllowanceObservation(observation); err != nil {
		return aipsdkmetering.Observation{}, fmt.Errorf("metering/journalstore: invalid stored provider allowance observation: %w", err)
	}
	effectiveTenant := observationTenant(observation)
	effectiveProviderAccount := observationProviderAccount(observation)
	if observation.Subject.Kind != aipsdkmetering.SubjectAccountWindow || observation.Subject.ResetAt.IsZero() ||
		(row.fingerprint != "" && observation.Fingerprint() != row.fingerprint) ||
		effectiveTenant != row.tenantID || effectiveProviderAccount != row.providerAccountKey ||
		observation.Subject.PoolID != row.poolID || observation.Subject.WindowID != row.windowID ||
		observation.Subject.ResetAt.UTC().UnixNano() != row.resetAt ||
		observation.ObservedAt.UTC().UnixNano() != row.observedAt || observation.ReceivedAt.UTC().UnixNano() != row.receivedAt ||
		observation.StreamID != row.streamID ||
		observation.Sequence > math.MaxInt64 || int64(observation.Sequence) != row.sequence || observation.Revision > math.MaxInt64 || int64(observation.Revision) != row.revision ||
		observation.ID != row.observationID {
		return aipsdkmetering.Observation{}, fmt.Errorf("%w: account-window search projection does not match canonical payload", ErrIdentityCollision)
	}
	return observation, nil
}

func encodeAccountWindowCursor(cursor accountWindowCursor) string {
	b, _ := json.Marshal(cursor)
	return accountWindowCursorPrefix + base64.RawURLEncoding.EncodeToString(b)
}

func decodeAccountWindowCursor(raw, kind, storeID, hash string) (accountWindowCursor, error) {
	if raw == "" {
		return accountWindowCursor{}, nil
	}
	if !strings.HasPrefix(raw, accountWindowCursorPrefix) {
		return accountWindowCursor{}, ErrInvalidCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, accountWindowCursorPrefix))
	if err != nil {
		return accountWindowCursor{}, fmt.Errorf("%w: base64", ErrInvalidCursor)
	}
	var cursor accountWindowCursor
	if err := json.Unmarshal(b, &cursor); err != nil {
		return accountWindowCursor{}, fmt.Errorf("%w: JSON", ErrInvalidCursor)
	}
	if cursor.Version != accountWindowCursorVersion || cursor.Kind != kind || cursor.StoreID != storeID || cursor.FilterHash != hash {
		return accountWindowCursor{}, ErrInvalidCursor
	}
	switch kind {
	case "history":
		if cursor.StreamID == "" || cursor.Sequence < 0 || cursor.ObservationID == "" || cursor.Revision <= 0 || cursor.RowID <= 0 {
			return accountWindowCursor{}, ErrInvalidCursor
		}
	case "projections":
		if cursor.Identity == "" {
			return accountWindowCursor{}, ErrInvalidCursor
		}
	default:
		return accountWindowCursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

var _ coremetering.AccountWindowStore = (*DurableStore)(nil)
