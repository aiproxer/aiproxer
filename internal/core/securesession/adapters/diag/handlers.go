package diag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// Handler serves secure-session operator diagnostics under a URL prefix (e.g. /debug/sessions).
type Handler struct {
	prefix           string
	store            Store
	redactionDefault string
	authz            SessionDiagnosticsAuthorizer
	log              *slog.Logger
	// health is an optional borrowed advisory reader over process-owned
	// prompt-cache collapse state. Nil keeps the pre-health response shape.
	health CacheHealthReader
}

// NewHandler returns an [http.Handler] for GET list, detail, transcript, audit, and by-A-leg routes.
// prefix is the base path without a trailing slash (e.g. "/debug/sessions"). store must be non-nil.
// authz may be nil to use [NewScopedOwnerAuthorizer].
// log, when non-nil, is used for error and debug lines (JSON/handler options from the process); when nil, [slog.Default] is used.
func NewHandler(prefix string, store Store, redactionDefault string, authz SessionDiagnosticsAuthorizer, log *slog.Logger) (http.Handler, error) {
	return NewHandlerWithCacheHealth(prefix, store, redactionDefault, authz, log, nil)
}

// NewHandlerWithCacheHealth is [NewHandler] plus an advisory per-session
// prompt-cache health reader. Health is attached only to already-authorized
// session list/detail responses behind the existing diagnostics protection and
// owner scoping; it introduces no new identifiers and no routing or billing
// authority. A nil health reader preserves the [NewHandler] response shape.
func NewHandlerWithCacheHealth(prefix string, store Store, redactionDefault string, authz SessionDiagnosticsAuthorizer, log *slog.Logger, health CacheHealthReader) (http.Handler, error) {
	if store == nil {
		return nil, fmt.Errorf("diag: nil store")
	}
	if authz == nil {
		authz = NewScopedOwnerAuthorizer()
	}
	p := strings.TrimSuffix(strings.TrimSpace(prefix), "/")
	return &Handler{
		prefix:           p,
		store:            store,
		redactionDefault: strings.TrimSpace(redactionDefault),
		authz:            authz,
		log:              log,
		health:           health,
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	base := h.prefix
	if !strings.HasPrefix(r.URL.Path, base) {
		h.writeNotFound(ctx, w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path[len(base):], "/")
	segs := strings.Split(rest, "/")
	for len(segs) > 0 && segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	switch {
	case rest == "":
		h.serveList(ctx, w, r)
	case len(segs) == 2 && segs[0] == "by-a-leg":
		h.serveByALeg(ctx, w, r, segs[1])
	case len(segs) == 2 && segs[1] == "transcript":
		h.serveTranscript(ctx, w, r, domain.SessionID(segs[0]))
	case len(segs) == 2 && segs[1] == "audit":
		h.serveAudit(ctx, w, r, domain.SessionID(segs[0]))
	case len(segs) == 1 && segs[0] != "":
		h.serveDetail(ctx, w, r, domain.SessionID(segs[0]))
	default:
		h.writeNotFound(ctx, w, r)
	}
}

func (h *Handler) serveList(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	owner, workspace, deny := h.authz.ListFilters(r, h.redactionDefault)
	if deny {
		h.writeJSON(ctx, w, r, http.StatusOK, map[string]any{"sessions": []any{}})
		return
	}
	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := h.store.Summary(ctx, domain.SummaryQuery{
		OwnerID: owner, WorkspaceID: workspace, Limit: limit,
	})
	if err != nil {
		h.writeServerError(ctx, w, r, err)
		return
	}
	out := make([]sessionSummaryDTO, 0, len(rows))
	for _, s := range rows {
		out = append(out, sessionSummaryDTO{
			SessionID:      string(s.SessionID),
			OwnerID:        s.OwnerID,
			WorkspaceID:    s.WorkspaceID,
			LastActivityAt: s.LastActivityAt.UTC().Format(time.RFC3339Nano),
			TurnCount:      s.TurnCount,
			AttemptCount:   s.AttemptCount,

			ResumeEligible:    s.ResumeEligible,
			ALegID:            s.ALegID,
			PolicyVersion:     s.PolicyVersion,
			TranscriptEnabled: s.TranscriptEnabled,
			RedactionProfile:  s.RedactionProfile,
			AuditMode:         s.AuditMode,
			UsageInputTokens:  s.UsageInputTokens,
			UsageOutputTokens: s.UsageOutputTokens,
			CacheHealth:       newCacheHealthDTO(h.health, string(s.SessionID)),
		})
	}
	h.writeJSON(ctx, w, r, http.StatusOK, map[string]any{"sessions": out})
}

func (h *Handler) serveDetail(ctx context.Context, w http.ResponseWriter, r *http.Request, id domain.SessionID) {
	rec, err := h.store.LoadByID(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			h.writeNotFound(ctx, w, r)
			return
		}
		h.writeServerError(ctx, w, r, err)
		return
	}
	dec, err := h.authz.AuthorizeSession(r, rec, OpSessionDetail, h.redactionDefault)
	if err != nil {
		h.writeServerError(ctx, w, r, err)
		return
	}
	if !dec.Allow || dec.DenyAsNotFound {
		h.writeNotFound(ctx, w, r)
		return
	}
	inTok, outTok := h.usageTotals(ctx, id)
	attempts := h.listAttemptsForDetail(ctx, id, dec)
	session := mapRecord(rec, inTok, outTok)
	useLatestAttemptFromHistory(&session, attempts)
	session.CacheHealth = newCacheHealthDTO(h.health, string(id))
	h.writeJSON(ctx, w, r, http.StatusOK, sessionDetailDTO{
		Session:  session,
		Attempts: attempts,
		PolicyEffective: map[string]any{
			"redaction_default": h.redactionDefault,
		},
	})
}

func (h *Handler) serveByALeg(ctx context.Context, w http.ResponseWriter, r *http.Request, aLegID string) {
	rec, err := h.store.LoadByALegID(ctx, aLegID)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			h.writeNotFound(ctx, w, r)
			return
		}
		h.writeServerError(ctx, w, r, err)
		return
	}
	dec, err := h.authz.AuthorizeSession(r, rec, OpSessionDetail, h.redactionDefault)
	if err != nil {
		h.writeServerError(ctx, w, r, err)
		return
	}
	if !dec.Allow || dec.DenyAsNotFound {
		h.writeNotFound(ctx, w, r)
		return
	}
	inTok, outTok := h.usageTotals(ctx, rec.SessionID)
	attempts := h.listAttemptsForDetail(ctx, rec.SessionID, dec)
	session := mapRecord(rec, inTok, outTok)
	useLatestAttemptFromHistory(&session, attempts)
	session.CacheHealth = newCacheHealthDTO(h.health, string(rec.SessionID))
	h.writeJSON(ctx, w, r, http.StatusOK, sessionDetailDTO{
		Session:  session,
		Attempts: attempts,
		PolicyEffective: map[string]any{
			"redaction_default": h.redactionDefault,
		},
	})
}

func (h *Handler) serveTranscript(ctx context.Context, w http.ResponseWriter, r *http.Request, id domain.SessionID) {
	rec, err := h.store.LoadByID(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			h.writeNotFound(ctx, w, r)
			return
		}
		h.writeServerError(ctx, w, r, err)
		return
	}
	dec, err := h.authz.AuthorizeSession(r, rec, OpTranscript, h.redactionDefault)
	if err != nil {
		h.writeServerError(ctx, w, r, err)
		return
	}
	if !dec.Allow || dec.DenyAsNotFound {
		h.writeNotFound(ctx, w, r)
		return
	}
	opts := readOptionsFromQuery(r)
	items, err := h.store.Transcript(ctx, id, opts)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			h.writeNotFound(ctx, w, r)
			return
		}
		h.writeServerError(ctx, w, r, err)
		return
	}
	out := make([]transcriptItemDTO, 0, len(items))
	for _, it := range items {
		payload := shapeTranscriptPayload(it.PayloadRef, dec.EffectivePolicy)
		out = append(out, transcriptItemDTO{
			Seq:        it.Seq,
			TurnID:     string(it.TurnID),
			EventKind:  it.EventKind,
			PayloadRef: payload,
			CreatedAt:  it.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	h.writeJSON(ctx, w, r, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) serveAudit(ctx context.Context, w http.ResponseWriter, r *http.Request, id domain.SessionID) {
	rec, err := h.store.LoadByID(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			h.writeNotFound(ctx, w, r)
			return
		}
		h.writeServerError(ctx, w, r, err)
		return
	}
	dec, err := h.authz.AuthorizeSession(r, rec, OpAudit, h.redactionDefault)
	if err != nil {
		h.writeServerError(ctx, w, r, err)
		return
	}
	if !dec.Allow || dec.DenyAsNotFound {
		h.writeNotFound(ctx, w, r)
		return
	}
	opts := readOptionsFromQuery(r)
	items, err := h.store.Audit(ctx, id, opts)
	if err != nil {
		if errors.Is(err, domain.ErrSessionNotFound) {
			h.writeNotFound(ctx, w, r)
			return
		}
		h.writeServerError(ctx, w, r, err)
		return
	}
	out := make([]auditItemDTO, 0, len(items))
	for _, it := range items {
		res := redactAuditResultJSON(it.Result, dec.EffectivePolicy, dec.RawAuditAllowed)
		out = append(out, auditItemDTO{
			Seq:       it.Seq,
			TurnID:    string(it.TurnID),
			Action:    it.Action,
			Result:    json.RawMessage([]byte(res)),
			CreatedAt: it.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	h.writeJSON(ctx, w, r, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) listAttemptsForDetail(ctx context.Context, id domain.SessionID, dec AuthDecision) []attemptDTO {
	evidence, err := h.store.ListAttemptEvidence(ctx, id, domain.ReadOptions{Limit: 100})
	if err != nil {
		h.logOrDefault().DebugContext(ctx, "secure_session_diagnostics: attempt history unavailable", "err", err)
		return []attemptDTO{}
	}
	out := make([]attemptDTO, 0, len(evidence))
	for _, ev := range evidence {
		out = append(out, mapAttemptEvidence(ev, dec.EffectivePolicy, dec.RawAuditAllowed))
	}
	return out
}

func (h *Handler) usageTotals(ctx context.Context, id domain.SessionID) (int64, int64) {
	u, ok := h.store.(SessionUsageRollup)
	if !ok {
		return 0, 0
	}
	inTok, outTok, err := u.UsageTokenTotals(ctx, id)
	if err != nil {
		h.logOrDefault().DebugContext(ctx, "secure_session_diagnostics: usage token totals unavailable", "err", err)
		return 0, 0
	}
	return inTok, outTok
}

func readOptionsFromQuery(r *http.Request) domain.ReadOptions {
	var o domain.ReadOptions
	if v := strings.TrimSpace(r.URL.Query().Get("after_seq")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			o.AfterSeq = n
		}
	}
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
			o.Limit = n
		}
	}
	return o
}

type sessionSummaryDTO struct {
	SessionID      string `json:"session_id"`
	OwnerID        string `json:"owner_id"`
	WorkspaceID    string `json:"workspace_id"`
	LastActivityAt string `json:"last_activity_at"`
	TurnCount      int    `json:"turn_count"`
	AttemptCount   int    `json:"attempt_count"`

	ResumeEligible    bool   `json:"resume_eligible"`
	ALegID            string `json:"a_leg_id,omitempty"`
	PolicyVersion     string `json:"policy_version,omitempty"`
	TranscriptEnabled bool   `json:"transcript_enabled"`
	RedactionProfile  string `json:"redaction_profile,omitempty"`
	AuditMode         string `json:"audit_mode,omitempty"`
	UsageInputTokens  int64  `json:"usage_input_tokens"`
	UsageOutputTokens int64  `json:"usage_output_tokens"`
	// CacheHealth is advisory prompt-cache state borrowed from the
	// process-owned collapse diagnostic. Omitted when no reader is wired.
	CacheHealth *cacheHealthDTO `json:"cache_health,omitempty"`
}

type sessionDetailDTO struct {
	Session         sessionDTO     `json:"session"`
	Attempts        []attemptDTO   `json:"attempts"`
	PolicyEffective map[string]any `json:"policy_effective"`
}

type sessionDTO struct {
	SessionID          string      `json:"session_id"`
	OwnerID            string      `json:"owner_id"`
	OwnerIssuer        string      `json:"owner_issuer"`
	OwnerTenant        string      `json:"owner_tenant"`
	WorkspaceID        string      `json:"workspace_id"`
	ALegID             string      `json:"a_leg_id"`
	LastActivityAt     string      `json:"last_activity_at"`
	LastActivitySource string      `json:"last_activity_source"`
	ResumeEligible     bool        `json:"resume_eligible"`
	TranscriptEnabled  bool        `json:"transcript_enabled"`
	PolicyVersion      string      `json:"policy_version"`
	RedactionProfile   string      `json:"redaction_profile"`
	AuditMode          string      `json:"audit_mode"`
	UsageInputTokens   int64       `json:"usage_input_tokens"`
	UsageOutputTokens  int64       `json:"usage_output_tokens"`
	LatestAttempt      *attemptDTO `json:"latest_attempt,omitempty"`
	// CacheHealth is advisory prompt-cache state borrowed from the
	// process-owned collapse diagnostic. Omitted when no reader is wired.
	CacheHealth *cacheHealthDTO `json:"cache_health,omitempty"`
}

type attemptDTO struct {
	TurnID             string         `json:"turn_id,omitempty"`
	AttemptSeq         int            `json:"attempt_seq,omitempty"`
	BLegID             string         `json:"b_leg_id,omitempty"`
	RequestedModel     string         `json:"requested_model,omitempty"`
	RequestedAlias     string         `json:"requested_alias,omitempty"`
	ResolvedBackend    string         `json:"resolved_backend,omitempty"`
	ResolvedModel      string         `json:"resolved_model,omitempty"`
	RouteSource        string         `json:"route_source,omitempty"`
	OpenFailed         bool           `json:"open_failed,omitempty"`
	Success            bool           `json:"success"`
	SurfaceState       string         `json:"surface_state,omitempty"`
	ErrorCode          string         `json:"error_code,omitempty"`
	HTTPStatus         int            `json:"http_status,omitempty"`
	ProviderStatus     string         `json:"provider_status,omitempty"`
	TimeoutClass       string         `json:"timeout_class,omitempty"`
	StartedAt          string         `json:"started_at,omitempty"`
	EndedAt            string         `json:"ended_at,omitempty"`
	InputTokens        int64          `json:"input_tokens"`
	OutputTokens       int64          `json:"output_tokens"`
	CacheReadTokens    int64          `json:"cache_read_tokens"`
	CacheWriteTokens   int64          `json:"cache_write_tokens"`
	ReasoningTokens    int64          `json:"reasoning_tokens,omitempty"`
	TotalTokens        int64          `json:"total_tokens,omitempty"`
	CostNanoUnits      int64          `json:"cost_nano_units,omitempty"`
	CostMinorUnits     int64          `json:"cost_minor_units,omitempty"`
	Currency           string         `json:"currency,omitempty"`
	CostSource         string         `json:"cost_source,omitempty"`
	BillingUnavailable bool           `json:"billing_unavailable"`
	SettingsSummary    map[string]any `json:"settings_summary,omitempty"`
	DebugReason        string         `json:"debug_reason,omitempty"`
}

type transcriptItemDTO struct {
	Seq        int64  `json:"seq"`
	TurnID     string `json:"turn_id"`
	EventKind  string `json:"event_kind"`
	PayloadRef string `json:"payload_ref"`
	CreatedAt  string `json:"created_at"`
}

type auditItemDTO struct {
	Seq       int64           `json:"seq"`
	TurnID    string          `json:"turn_id"`
	Action    string          `json:"action"`
	Result    json.RawMessage `json:"result"`
	CreatedAt string          `json:"created_at"`
}

func mapAttemptEvidence(ev domain.AttemptEvidence, pol domain.PolicyMetadata, rawAudit bool) attemptDTO {
	tr := ev.Trace
	ac := ev.Accounting
	out := ev.Outcome
	dto := attemptDTO{
		TurnID:             string(tr.TurnID),
		AttemptSeq:         tr.AttemptSeq,
		BLegID:             tr.BLegID,
		RequestedModel:     tr.RequestedModel,
		RequestedAlias:     tr.RequestedAlias,
		ResolvedBackend:    tr.ResolvedBackend,
		ResolvedModel:      tr.ResolvedModel,
		RouteSource:        tr.RouteSource,
		OpenFailed:         tr.OpenFailed,
		Success:            out.Success,
		SurfaceState:       string(out.SurfaceState),
		ErrorCode:          out.ErrorCode,
		HTTPStatus:         out.HTTPStatus,
		ProviderStatus:     out.ProviderStatus,
		TimeoutClass:       out.TimeoutClass,
		InputTokens:        ac.InputTokens,
		OutputTokens:       ac.OutputTokens,
		CacheReadTokens:    ac.CacheReadTokens,
		CacheWriteTokens:   ac.CacheWriteTokens,
		ReasoningTokens:    ac.ReasoningTokens,
		TotalTokens:        ac.TotalTokens,
		CostNanoUnits:      ac.CostNanoUnits,
		CostMinorUnits:     ac.CostMinorUnits,
		Currency:           ac.Currency,
		CostSource:         ac.CostSource,
		BillingUnavailable: ac.BillingUnavailable,
		SettingsSummary:    settingsSummaryMap(tr.Settings),
	}
	if !tr.StartedAt.IsZero() {
		dto.StartedAt = tr.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !out.EndedAt.IsZero() {
		dto.EndedAt = out.EndedAt.UTC().Format(time.RFC3339Nano)
	}
	if rawAudit && strings.TrimSpace(out.DebugReason) != "" {
		dto.DebugReason = out.DebugReason
	} else if strings.EqualFold(strings.TrimSpace(pol.RedactionProfile), "strict") {
		// strict diagnostics: omit debug_reason
	} else if strings.TrimSpace(out.DebugReason) != "" {
		dto.DebugReason = "[redacted]"
	}
	return dto
}

func settingsSummaryMap(s domain.AttemptSettings) map[string]any {
	if s.MaxTokens == nil && s.Temperature == nil && s.Timeout == 0 && strings.TrimSpace(s.ReasoningEffort) == "" && strings.TrimSpace(s.Verbosity) == "" &&
		!s.Streaming && len(s.ToolSummary) == 0 && strings.TrimSpace(s.BackendOptionsDigest) == "" {
		return nil
	}
	m := map[string]any{}
	if s.Temperature != nil {
		m["temperature"] = *s.Temperature
	}
	if s.MaxTokens != nil {
		m["max_tokens"] = *s.MaxTokens
	}
	if s.Timeout > 0 {
		m["timeout_ms"] = s.Timeout.Milliseconds()
	}
	if strings.TrimSpace(s.ReasoningEffort) != "" {
		m["reasoning_effort"] = s.ReasoningEffort
	}
	if strings.TrimSpace(s.Verbosity) != "" {
		m["verbosity"] = s.Verbosity
	}
	m["streaming"] = s.Streaming
	if n := len(s.ToolSummary); n > 0 {
		m["tool_count"] = n
	}
	if strings.TrimSpace(s.BackendOptionsDigest) != "" {
		m["backend_options_digest"] = s.BackendOptionsDigest
	}
	return m
}

func mapRecord(rec domain.Record, usageIn, usageOut int64) sessionDTO {
	s := sessionDTO{
		SessionID:          string(rec.SessionID),
		OwnerID:            rec.Owner.ID,
		OwnerIssuer:        rec.Owner.Issuer,
		OwnerTenant:        rec.Owner.Tenant,
		WorkspaceID:        rec.Workspace.ID,
		ALegID:             rec.ALegID,
		LastActivityAt:     rec.LastActivityAt.UTC().Format(time.RFC3339Nano),
		LastActivitySource: string(rec.LastActivitySource),
		ResumeEligible:     rec.ResumeEligible,
		TranscriptEnabled:  rec.Policy.TranscriptEnabled,
		PolicyVersion:      rec.Policy.PolicyVersion,
		RedactionProfile:   rec.Policy.RedactionProfile,
		AuditMode:          rec.Policy.AuditMode,
		UsageInputTokens:   usageIn,
		UsageOutputTokens:  usageOut,
	}
	tr := rec.LatestAttemptTrace
	if strings.TrimSpace(tr.BLegID) != "" || strings.TrimSpace(tr.ResolvedModel) != "" {
		// The trace, outcome, and accounting snapshots are maintained
		// independently, so overlapping attempts can leave them describing
		// different B-legs. Only a snapshot of the same B-leg may be joined.
		ev := domain.AttemptEvidence{Trace: tr}
		if rec.LatestAttemptOutcome.BLegID == tr.BLegID {
			ev.Outcome = rec.LatestAttemptOutcome
		}
		if rec.LatestAttemptAccounting.BLegID == tr.BLegID {
			ev.Accounting = rec.LatestAttemptAccounting
		}
		a := mapAttemptEvidence(ev, rec.Policy, false)
		s.LatestAttempt = &a
	}
	return s
}

// useLatestAttemptFromHistory serves the latest attempt from the attempt
// history when it is available. Each history row joins one B-leg's trace,
// outcome, and accounting, so the snapshot stays coherent even when attempts
// finish out of order; the record's independently maintained snapshots do not.
func useLatestAttemptFromHistory(s *sessionDTO, attempts []attemptDTO) {
	if len(attempts) == 0 {
		return
	}
	latest := attempts[len(attempts)-1]
	s.LatestAttempt = &latest
}

func shapeTranscriptPayload(payload string, pol domain.PolicyMetadata) string {
	return app.RedactCorrelationJSON(payload, pol)
}

func redactAuditResultJSON(raw string, pol domain.PolicyMetadata, allowRaw bool) string {
	if allowRaw {
		return raw
	}

	fallbackDigest := func(input string) string {
		dig := app.DigestJSONFields(input, pol)
		b, mErr := json.Marshal(map[string]any{"event_digest": json.RawMessage([]byte(dig))})
		if mErr != nil {
			return `{"event_digest":{"digest":"invalid_json"}}`
		}
		return string(b)
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		// security: invalid JSON must not bypass digest-only audit exposure (stored rows are untrusted bytes).
		return fallbackDigest(raw)
	}
	ev, ok := m["event"]
	if !ok {
		return fallbackDigest(raw)
	}
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return fallbackDigest(raw)
	}
	dig := app.DigestJSONFields(string(evJSON), pol)
	m["event_digest"] = json.RawMessage([]byte(dig))
	delete(m, "event")
	b, err := json.Marshal(m)
	if err != nil {
		return fallbackDigest(raw)
	}
	return string(b)
}

func (h *Handler) logOrDefault() *slog.Logger {
	if h != nil && h.log != nil {
		return h.log
	}
	return slog.Default()
}

func (h *Handler) writeJSON(ctx context.Context, w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(v); err != nil {
		if r != nil {
			h.logOrDefault().ErrorContext(ctx, "secure_session_diagnostics: json encode failed",
				"method", r.Method, "path", r.URL.Path, "err", err)
		} else {
			h.logOrDefault().ErrorContext(ctx, "secure_session_diagnostics: json encode failed", "err", err)
		}
	}
}

func (h *Handler) writeNotFound(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	h.writeJSON(ctx, w, r, http.StatusNotFound, map[string]any{"error": "not_found"})
}

func (h *Handler) writeServerError(ctx context.Context, w http.ResponseWriter, r *http.Request, err error) {
	if r == nil {
		h.logOrDefault().ErrorContext(ctx, "secure_session_diagnostics: request failed", "err", err)
	} else {
		h.logOrDefault().ErrorContext(ctx, "secure_session_diagnostics: request failed",
			"method", r.Method, "path", r.URL.Path, "err", err)
	}
	h.writeJSON(ctx, w, r, http.StatusInternalServerError, map[string]any{"error": "internal"})
}
