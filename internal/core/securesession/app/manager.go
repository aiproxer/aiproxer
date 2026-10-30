package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

const auditActionTurnOutcome = "turn_outcome"

const turnIDRandBytes = 16

// Manager enforces secure-session policy for BeginTurn and records turn outcomes.
type Manager struct {
	store   Store
	gen     Generator
	lineage LineageStore
	cfg     ManagerConfig
}

// NewManager constructs a Manager. store, gen, and lineage must be non-nil; cfg.FingerprintKey must be non-empty.
func NewManager(store Store, gen Generator, lineage LineageStore, cfg ManagerConfig) (*Manager, error) {
	if store == nil || gen == nil || lineage == nil {
		return nil, fmt.Errorf("securesession/manager: nil dependency")
	}
	if len(cfg.FingerprintKey) == 0 {
		return nil, fmt.Errorf("securesession/manager: empty fingerprint key")
	}
	return &Manager{store: store, gen: gen, lineage: lineage, cfg: cfg}, nil
}

// BeginTurn authorizes a new or resumed session turn and returns validated proxy-owned state.
func (m *Manager) BeginTurn(ctx context.Context, in BeginInput) (BeginResult, error) {
	var zero BeginResult
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !domain.PrincipalIDPresent(in.Principal) {
		return zero, domain.ErrMissingPrincipal
	}

	if m.isResume(in.Session) {
		return m.beginResume(ctx, in)
	}
	return m.beginNew(ctx, in)
}

func (m *Manager) isResume(s SessionWire) bool {
	return strings.TrimSpace(s.ResumeToken) != ""
}

func (m *Manager) entropyMaterial(in BeginInput) EntropyMaterial {
	agent := strings.TrimSpace(in.ClientHints.AgentIdentityDigest)
	if agent == "" {
		agent = strings.TrimSpace(in.Session.ClientSessionID)
	}
	return EntropyMaterial{
		PrincipalID:        strings.TrimSpace(in.Principal.ID),
		AgentDigest:        agent,
		FirstMessageDigest: strings.TrimSpace(in.FirstMessageDigest),
	}
}

func (m *Manager) materialForResumeFingerprint(in BeginInput) EntropyMaterial {
	mat := m.entropyMaterial(in)
	if m.cfg.ResumeFingerprintPrincipalOnly {
		return EntropyMaterial{PrincipalID: mat.PrincipalID}
	}
	return mat
}

func (m *Manager) clientHints(in BeginInput) domain.ClientHints {
	h := in.ClientHints
	if strings.TrimSpace(h.ClientSessionID) == "" {
		h.ClientSessionID = strings.TrimSpace(in.Session.ClientSessionID)
	}
	return h
}

func (m *Manager) runMandatoryPreBackendChecklist(ctx context.Context, pol domain.PolicyMetadata) error {
	if m.cfg.RequireDurableStore && !m.cfg.StoreDurable {
		return domain.ErrStorageUnavailable
	}
	return m.store.CheckReadiness(ctx, pol)
}

func neutralPolicyBaseline() domain.PolicyMetadata {
	return domain.PolicyMetadata{
		PolicyVersion:      "baseline",
		TranscriptEnabled:  true,
		EffectiveTreatment: "relaxed",
		RedactionProfile:   "standard",
		AuditMode:          "best_effort",
	}
}

// DefaultGlobalPolicy returns the baseline global policy merged during [Manager.BeginTurn]
// when the executor has no operator-specific policy wiring.
func DefaultGlobalPolicy() domain.PolicyMetadata {
	return neutralPolicyBaseline()
}

func mergePolicies(global, session domain.PolicyMetadata) domain.PolicyMetadata {
	out := session
	if strings.TrimSpace(global.PolicyVersion) != "" {
		out.PolicyVersion = strings.TrimSpace(global.PolicyVersion)
	}
	out.TranscriptEnabled = global.TranscriptEnabled && session.TranscriptEnabled
	out.EffectiveTreatment = stricterEffectiveTreatment(global.EffectiveTreatment, session.EffectiveTreatment)
	out.RouteHint = firstNonEmpty(strings.TrimSpace(session.RouteHint), strings.TrimSpace(global.RouteHint))
	out.RedactionProfile = stricterRedaction(global.RedactionProfile, session.RedactionProfile)
	out.AuditMode = stricterAuditModeString(global.AuditMode, session.AuditMode)
	if strings.TrimSpace(global.StricterPolicyResolution) != "" {
		out.StricterPolicyResolution = strings.TrimSpace(global.StricterPolicyResolution)
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return strings.TrimSpace(b)
}

func rankTreatment(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "strict":
		return 2
	case "standard":
		return 1
	case "relaxed", "":
		return 0
	default:
		return 1
	}
}

func stricterEffectiveTreatment(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if rankTreatment(a) > rankTreatment(b) {
		return a
	}
	if rankTreatment(b) > rankTreatment(a) {
		return b
	}
	if a != "" {
		return a
	}
	return b
}

func stricterRedaction(a, b string) string {
	rank := func(s string) int {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "strict":
			return 2
		case "standard", "":
			return 1
		default:
			return 1
		}
	}
	if rank(a) >= rank(b) {
		return pickRedaction(a, b)
	}
	return pickRedaction(b, a)
}

func pickRedaction(a, b string) string {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a != "" {
		return a
	}
	return b
}

func stricterAuditModeString(a, b string) string {
	rank := func(s string) int {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "mandatory":
			return 2
		case "best_effort", "":
			return 0
		default:
			return 1
		}
	}
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if rank(a) > rank(b) {
		return a
	}
	if rank(b) > rank(a) {
		return b
	}
	if a != "" {
		return a
	}
	return b
}

func (m *Manager) beginNew(ctx context.Context, in BeginInput) (BeginResult, error) {
	var zero BeginResult
	if in.WorkspaceMatchRequired && strings.TrimSpace(in.Workspace.ID) == "" {
		return zero, domain.ErrPolicyUnavailable
	}

	effective := mergePolicies(in.GlobalPolicy, neutralPolicyBaseline())
	if err := m.runMandatoryPreBackendChecklist(ctx, effective); err != nil {
		return zero, err
	}

	al, err := m.lineage.CreateALeg(ctx, "")
	if err != nil {
		return zero, err
	}

	mat := m.entropyMaterial(in)
	sid, err := m.gen.NewSessionID(ctx, mat)
	if err != nil {
		return zero, err
	}
	tok, fp, err := m.gen.NewResumeToken(ctx, m.materialForResumeFingerprint(in))
	if err != nil {
		return zero, err
	}

	cr := domain.CreateRecord{
		SessionID:         sid,
		ResumeFingerprint: fp,
		Owner:             in.Principal,
		Workspace:         in.Workspace,
		ClientHints:       m.clientHints(in),
		Policy:            effective,
		ALegID:            al.ALegID,
		ResumeEligible:    true,
		CreatedAt:         in.Now,
	}
	rec, err := m.store.Create(ctx, cr)
	if err != nil {
		return zero, err
	}

	t0 := time.Now()
	if err := m.store.TouchActivity(ctx, sid, in.Now, domain.ActivityClientRequest); err != nil {
		return zero, err
	}
	if o := m.cfg.ObserveActivityTouch; o != nil {
		o(time.Since(t0).Seconds())
	}

	tid, err := newTurnID()
	if err != nil {
		return zero, err
	}

	rec = withEffectivePolicy(rec, effective)
	return BeginResult{
		Record:          rec,
		TurnID:          tid,
		IsNew:           true,
		EffectivePolicy: effective,
		Response: ResponseMetadata{
			SessionID:   string(sid),
			ResumeToken: tok,
		},
	}, nil
}

func (m *Manager) beginResume(ctx context.Context, in BeginInput) (BeginResult, error) {
	var zero BeginResult
	fp := FingerprintResumeToken(m.cfg.FingerprintKey, domain.ResumeToken(strings.TrimSpace(in.Session.ResumeToken)), m.materialForResumeFingerprint(in))

	rec, err := m.store.LoadByResumeFingerprint(ctx, fp)
	if err != nil {
		return zero, err
	}

	if hint := strings.TrimSpace(in.Session.SessionID); hint != "" && hint != string(rec.SessionID) {
		return zero, domain.ErrInvalidResumeToken
	}

	if !ownersMatch(rec.Owner, in.Principal) {
		return zero, domain.ErrOwnerMismatch
	}

	if hint := strings.TrimSpace(in.Session.ALegID); hint != "" && hint != rec.ALegID {
		return zero, domain.ErrSessionNotFound
	}

	if in.WorkspaceMatchRequired && strings.TrimSpace(in.Workspace.ID) == "" {
		return zero, domain.ErrPolicyUnavailable
	}
	if strings.TrimSpace(rec.Workspace.ID) != "" && strings.TrimSpace(in.Workspace.ID) != strings.TrimSpace(rec.Workspace.ID) {
		return zero, domain.ErrWorkspaceDenied
	}

	if rec.Status.IsQuarantined() {
		return zero, domain.ErrSessionQuarantined
	}

	if !rec.ResumeEligible {
		return zero, domain.ErrResumeExpired
	}
	if m.cfg.ResumeWindow > 0 && in.Now.Sub(rec.LastActivityAt) > m.cfg.ResumeWindow {
		return zero, domain.ErrResumeExpired
	}

	effective := mergePolicies(in.GlobalPolicy, rec.Policy)
	if err := m.runMandatoryPreBackendChecklist(ctx, effective); err != nil {
		return zero, err
	}

	t1 := time.Now()
	if err := m.store.TouchActivity(ctx, rec.SessionID, in.Now, domain.ActivityClientRequest); err != nil {
		return zero, err
	}
	if o := m.cfg.ObserveActivityTouch; o != nil {
		o(time.Since(t1).Seconds())
	}

	tid, err := newTurnID()
	if err != nil {
		return zero, err
	}

	rec2 := withEffectivePolicy(rec, effective)
	return BeginResult{
		Record:          rec2,
		TurnID:          tid,
		IsNew:           false,
		EffectivePolicy: effective,
		Response:        ResponseMetadata{},
	}, nil
}

func withEffectivePolicy(rec domain.Record, pol domain.PolicyMetadata) domain.Record {
	rec.Policy = pol
	return rec
}

func newTurnID() (domain.TurnID, error) {
	var buf [turnIDRandBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("securesession/manager: turn id entropy: %w", err)
	}
	return domain.TurnID("t_" + base64.RawURLEncoding.EncodeToString(buf[:])), nil
}

// FinishTurn records terminal turn outcome evidence (audit row).
func (m *Manager) FinishTurn(ctx context.Context, sessionID domain.SessionID, turnID domain.TurnID, outcome TurnOutcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := m.store.LoadByID(ctx, sessionID); err != nil {
		return err
	}
	seq, err := m.store.NextAuditSeq(ctx, sessionID)
	if err != nil {
		return err
	}
	return m.store.AppendAudit(ctx, domain.AuditItem{
		SessionID: sessionID,
		TurnID:    turnID,
		Seq:       seq,
		Action:    auditActionTurnOutcome,
		Result:    turnOutcomeResult(outcome.Kind),
		CreatedAt: time.Now(),
	})
}

func ownersMatch(stored, want domain.PrincipalRef) bool {
	return strings.TrimSpace(stored.ID) == strings.TrimSpace(want.ID) &&
		strings.TrimSpace(stored.Issuer) == strings.TrimSpace(want.Issuer) &&
		strings.TrimSpace(stored.Tenant) == strings.TrimSpace(want.Tenant)
}

// PreBackendMandatoryGates runs the same readiness checks as [Manager.BeginTurn] before opening a backend attempt.
func (m *Manager) PreBackendMandatoryGates(ctx context.Context, pol domain.PolicyMetadata) error {
	return m.runMandatoryPreBackendChecklist(ctx, pol)
}

// LoadByALegID returns the secure-session row owning a proxy A-leg.
func (m *Manager) LoadByALegID(ctx context.Context, aLegID string) (domain.Record, error) {
	if err := ctx.Err(); err != nil {
		return domain.Record{}, err
	}
	return m.store.LoadByALegID(ctx, strings.TrimSpace(aLegID))
}

// RecordAttemptOpened persists immutable B-leg open metadata for secure-session diagnostics.
func (m *Manager) RecordAttemptOpened(ctx context.Context, trace domain.AttemptTrace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.store.AppendAttemptTrace(ctx, trace)
}

// RecordAttemptOutcome updates the latest terminal attempt outcome for secure-session diagnostics.
func (m *Manager) RecordAttemptOutcome(ctx context.Context, outcome domain.AttemptOutcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.store.UpdateAttemptOutcome(ctx, outcome)
}

func turnOutcomeResult(k TurnOutcomeKind) string {
	switch k {
	case TurnOutcomeSuccess:
		return "success"
	case TurnOutcomePreOutputDenied:
		return "pre_output_denied"
	case TurnOutcomeSurfacedFailure:
		return "surfaced_failure"
	case TurnOutcomePostOutputRecorderFailure:
		return "post_output_recorder_failure"
	default:
		return "unknown"
	}
}
