package ledgerstore

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// eventRow is the durable storage row shape. It holds only primitive columns;
// JSON columns are decoded into SDK DTOs by [eventFromRow]. SQL/Bun types do
// not cross into core or SDK contracts (requirement 9.5).
type eventRow struct {
	id                int64
	storeID           string
	sourceEventKey    string
	category          string
	occurredAtUnix    int64
	recordedAtUnix    int64
	traceID           string
	requestID         string
	sessionID         string
	aLegID            string
	bLegID            string
	attemptSeq        int
	frontendID        string
	backendID         string
	model             string
	parentTraceID     string
	outcome           string
	effect            string
	reasonCode        string
	visibility        string
	surfaced          string
	usagePlane        string
	usageAvailability string
	evidenceState     string
	redactionState    string
	sourceName        string
	sourceVersion     string
	summary           string
	summaryJSON       string
	scopeJSON         string
	detailJSON        string
}

func scanEventRow(rows *sql.Rows) (eventRow, error) {
	var r eventRow
	if err := rows.Scan(
		&r.id, &r.storeID, &r.sourceEventKey, &r.category,
		&r.occurredAtUnix, &r.recordedAtUnix,
		&r.traceID, &r.requestID, &r.sessionID, &r.aLegID, &r.bLegID, &r.attemptSeq,
		&r.frontendID, &r.backendID, &r.model, &r.parentTraceID,
		&r.outcome, &r.effect, &r.reasonCode, &r.visibility,
		&r.surfaced, &r.usagePlane, &r.usageAvailability,
		&r.evidenceState, &r.redactionState,
		&r.sourceName, &r.sourceVersion, &r.summary,
		&r.summaryJSON, &r.scopeJSON, &r.detailJSON,
	); err != nil {
		return eventRow{}, fmt.Errorf("ledgerstore: scan event row: %w", err)
	}
	return r, nil
}

// eventSelectColumns lists the columns selected for event projection in the
// order scanEventRow expects.
const eventSelectColumns = `id, store_id, source_event_key, category,
	occurred_at_unix, recorded_at_unix,
	trace_id, request_id, session_id, a_leg_id, b_leg_id, attempt_seq,
	frontend_id, backend_id, model, parent_trace_id,
	outcome, effect, reason_code, visibility,
	surfaced, usage_plane, usage_availability,
	evidence_state, redaction_state,
	source_name, source_version, summary,
	summary_json, scope_json, detail_json`

// eventFromRow reconstructs an SDK Event from a storage row, decoding the
// bounded scope and detail JSON. Redaction/visibility downgrade is applied by
// the caller via applyQueryVisibility when appropriate.
func eventFromRow(r eventRow) (cp.Event, error) {
	ev := cp.Event{
		ID:             cp.EventID{StoreID: r.storeID, Sequence: r.id},
		SourceEventKey: r.sourceEventKey,
		Category:       cp.Category(r.category),
		OccurredAt:     time.Unix(0, r.occurredAtUnix).UTC(),
		RecordedAt:     time.Unix(0, r.recordedAtUnix).UTC(),
		Correlation: cp.Correlation{
			TraceID:       r.traceID,
			RequestID:     r.requestID,
			SessionID:     r.sessionID,
			ALegID:        r.aLegID,
			BLegID:        r.bLegID,
			AttemptSeq:    r.attemptSeq,
			FrontendID:    r.frontendID,
			BackendID:     r.backendID,
			Model:         r.model,
			ParentTraceID: r.parentTraceID,
		},
		Source:         cp.SourceRef{Name: r.sourceName, Version: r.sourceVersion},
		Visibility:     cp.Visibility(r.visibility),
		EvidenceState:  cp.EvidenceState(r.evidenceState),
		RedactionState: cp.RedactionState(r.redactionState),
		Summary:        r.summary,
	}
	if err := json.Unmarshal([]byte(r.scopeJSON), &ev.Scope); err != nil {
		return cp.Event{}, fmt.Errorf("ledgerstore: unmarshal scope_json: %w", err)
	}
	if err := decodeDetailJSON(r.detailJSON, &ev); err != nil {
		return cp.Event{}, fmt.Errorf("ledgerstore: unmarshal detail_json: %w", err)
	}
	return ev, nil
}

// decodeDetailJSON unmarshals the single typed detail block carried by
// detail_json. The JSON shape is an object with one detail key matching the
// event category (requirement 1.7: exactly one detail block per event).
func decodeDetailJSON(raw string, ev *cp.Event) error {
	if raw == "" || raw == "{}" {
		return nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return err
	}
	if v, ok := probe["auth"]; ok {
		var d cp.AuthDetail
		if err := json.Unmarshal(v, &d); err != nil {
			return err
		}
		ev.Detail = &d
		return nil
	}
	if v, ok := probe["session"]; ok {
		var d cp.SessionDetail
		if err := json.Unmarshal(v, &d); err != nil {
			return err
		}
		ev.Detail = &d
		return nil
	}
	if v, ok := probe["attempt"]; ok {
		var d cp.AttemptDetail
		if err := json.Unmarshal(v, &d); err != nil {
			return err
		}
		ev.Detail = &d
		return nil
	}
	if v, ok := probe["usage"]; ok {
		var d cp.UsageDetail
		if err := json.Unmarshal(v, &d); err != nil {
			return err
		}
		ev.Detail = &d
		return nil
	}
	if v, ok := probe["policy"]; ok {
		var d cp.PolicyDetail
		if err := json.Unmarshal(v, &d); err != nil {
			return err
		}
		ev.Detail = &d
		return nil
	}
	if v, ok := probe["audit"]; ok {
		var d cp.AuditDetail
		if err := json.Unmarshal(v, &d); err != nil {
			return err
		}
		ev.Detail = &d
		return nil
	}
	if v, ok := probe["lifecycle"]; ok {
		var d cp.LifecycleDetail
		if err := json.Unmarshal(v, &d); err != nil {
			return err
		}
		ev.Detail = &d
		return nil
	}
	return nil
}

// encodeDetailJSON marshals the single typed detail block under its category
// key. Returns '{}' when no detail is set (which would be a validation error,
// but the encoder stays safe).
func encodeDetailJSON(ev cp.Event) (string, error) {
	var key string
	var raw any
	switch {
	case ev.Auth() != nil:
		key, raw = "auth", ev.Auth()
	case ev.Session() != nil:
		key, raw = "session", ev.Session()
	case ev.Attempt() != nil:
		key, raw = "attempt", ev.Attempt()
	case ev.Usage() != nil:
		key, raw = "usage", ev.Usage()
	case ev.Policy() != nil:
		key, raw = "policy", ev.Policy()
	case ev.Audit() != nil:
		key, raw = "audit", ev.Audit()
	case ev.Lifecycle() != nil:
		key, raw = "lifecycle", ev.Lifecycle()
	default:
		return "{}", nil
	}
	b, err := json.Marshal(map[string]any{key: raw})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// encodeScopeJSON marshals the ScopeSnapshot for result reconstruction.
func encodeScopeJSON(snap cp.ScopeSnapshot) (string, error) {
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// scopeDimCols returns the columnized (known, value) pair values for a scope
// dimension, in the order {known, value}.
func scopeDimCols(v scope.Value) (known int, value string) {
	if v.IsKnown() {
		return 1, v.Value
	}
	return 0, ""
}
