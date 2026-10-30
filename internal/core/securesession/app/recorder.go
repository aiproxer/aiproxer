package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

const (
	auditActionClientTurnMeta      = "client_turn_transcript_meta"
	auditActionPostHookStream      = "stream_post_hook"
	transcriptEventClientInput     = "client.input"
	transcriptEventCanonicalStream = "canonical.stream"
	transcriptPayloadSchemaVersion = 1
)

// Recorder persists secure-session transcript, usage, activity, and audit via [Store].
type Recorder struct {
	store Store
}

// NewRecorder constructs a [Recorder]. store must be non-nil.
func NewRecorder(store Store) (*Recorder, error) {
	if store == nil {
		return nil, fmt.Errorf("securesession/recorder: nil store")
	}
	return &Recorder{store: store}, nil
}

var _ GateRecording = (*Recorder)(nil)

// RecordClientTurnAfterGate appends accepted client input immediately after secure-session gate success.
func (r *Recorder) RecordClientTurnAfterGate(ctx context.Context, in ClientTurnRecordInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.store == nil {
		return nil
	}
	if strings.TrimSpace(string(in.SessionID)) == "" || strings.TrimSpace(string(in.TurnID)) == "" {
		return nil
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	pol := in.Policy

	if !pol.TranscriptEnabled {
		return r.recordTranscriptDisabledMeta(ctx, in, now, pol)
	}

	for _, ln := range in.Lines {
		seq, err := r.store.NextTranscriptSeq(ctx, in.SessionID)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{
			"aip_transcript_v":   transcriptPayloadSchemaVersion,
			"trace_id":           strings.TrimSpace(in.TraceID),
			"role":               strings.TrimSpace(ln.Role),
			"ordinal":            ln.Ordinal,
			"parts":              ln.Parts,
			"transcript_enabled": true,
			"redaction_profile":  strings.TrimSpace(pol.RedactionProfile),
		})
		if err != nil {
			return err
		}
		item := domain.TranscriptItem{
			SessionID:  in.SessionID,
			TurnID:     in.TurnID,
			Seq:        seq,
			EventKind:  transcriptEventClientInput,
			PayloadRef: string(payload),
			CreatedAt:  now,
		}
		if err := r.store.AppendTranscript(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (r *Recorder) recordTranscriptDisabledMeta(ctx context.Context, in ClientTurnRecordInput, now time.Time, pol domain.PolicyMetadata) error {
	seq, err := r.store.NextAuditSeq(ctx, in.SessionID)
	if err != nil {
		return err
	}
	lines := make([]map[string]any, 0, len(in.Lines))
	for _, ln := range in.Lines {
		lines = append(lines, map[string]any{
			"role":    strings.TrimSpace(ln.Role),
			"ordinal": ln.Ordinal,
			"parts":   ln.Parts,
		})
	}
	res, err := json.Marshal(map[string]any{
		"transcript_enabled": false,
		"redaction_profile":  strings.TrimSpace(pol.RedactionProfile),
		"audit_mode":         strings.TrimSpace(pol.AuditMode),
		"lines":              lines,
		"trace_id":           strings.TrimSpace(in.TraceID),
	})
	if err != nil {
		return err
	}
	if err := r.store.AppendAudit(ctx, domain.AuditItem{
		SessionID: in.SessionID,
		TurnID:    in.TurnID,
		Seq:       seq,
		Action:    auditActionClientTurnMeta,
		Result:    string(res),
		CreatedAt: now,
	}); err != nil {
		return err
	}
	return nil
}

// RecordPostHookStreamEvent records one post-hook canonical client-facing stream slice.
func (r *Recorder) RecordPostHookStreamEvent(ctx context.Context, in StreamEventRecordInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.store == nil {
		return nil
	}
	if strings.TrimSpace(string(in.SessionID)) == "" || strings.TrimSpace(string(in.TurnID)) == "" {
		return nil
	}
	if strings.TrimSpace(in.EventKind) == "" && !in.IsUsageEvent {
		return nil
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	pol := in.Policy

	if k := strings.TrimSpace(in.EventKind); k != "" {
		if err := r.store.TouchActivity(ctx, in.SessionID, now, domain.ActivityRemoteEvent); err != nil {
			return err
		}
	}

	if in.IsUsageEvent {
		delta := domain.UsageDelta{
			SessionID:                in.SessionID,
			TurnID:                   in.TurnID,
			BLegID:                   strings.TrimSpace(in.BLegID),
			InputTokens:              in.InputTokens,
			OutputTokens:             in.OutputTokens,
			CacheReadTokens:          in.CacheReadTokens,
			CacheWriteTokens:         in.CacheWriteTokens,
			NonCachedInputTokens:     in.NonCachedInputTokens,
			ReasoningTokens:          in.ReasoningTokens,
			NonReasoningOutputTokens: in.NonReasoningOutputTokens,
			TotalTokens:              in.TotalTokens,
			CostNanoUnits:            in.CostNanoUnits,
			CostMinorUnits:           in.CostMinorUnits,
			Currency:                 strings.TrimSpace(in.Currency),
			CostSource:               strings.TrimSpace(in.CostSource),
			RawUsageJSON:             strings.TrimSpace(in.RawUsageJSON),
			BillingUnavailable:       in.BillingUnavailable,
			RequestStartedAt:         in.RequestStartedAt,
			FirstRemoteEventAt:       in.FirstRemoteEventAt,
			FirstMeaningfulTokenAt:   in.FirstMeaningfulTokenAt,
			RemoteCompletedAt:        in.RemoteCompletedAt,
			ProxyCompletedAt:         in.ProxyCompletedAt,
			TTFTMillis:               in.TTFTMillis,
			RemoteDurationMillis:     in.RemoteDurationMillis,
			CompletionDurationMillis: in.CompletionDurationMillis,
			CompletionTPSMilli:       in.CompletionTPSMilli,
		}
		if err := r.store.AddUsage(ctx, delta); err != nil {
			return err
		}
	}

	if pol.TranscriptEnabled && strings.TrimSpace(in.EventKind) != "" {
		seq, err := r.store.NextTranscriptSeq(ctx, in.SessionID)
		if err != nil {
			return err
		}
		payload := map[string]any{
			"aip_transcript_v":   transcriptPayloadSchemaVersion,
			"kind":               strings.TrimSpace(in.EventKind),
			"trace_id":           strings.TrimSpace(in.TraceID),
			"b_leg_id":           strings.TrimSpace(in.BLegID),
			"backend_id":         strings.TrimSpace(in.BackendID),
			"transcript_enabled": true,
			"redaction_profile":  strings.TrimSpace(pol.RedactionProfile),
		}
		if strings.TrimSpace(in.EventPayloadJSON) != "" {
			payload["event"] = json.RawMessage(in.EventPayloadJSON)
		}
		if strings.TrimSpace(in.ProviderCorrelationJSON) != "" {
			payload["provider_correlation"] = json.RawMessage(in.ProviderCorrelationJSON)
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if err := r.store.AppendTranscript(ctx, domain.TranscriptItem{
			SessionID:  in.SessionID,
			TurnID:     in.TurnID,
			Seq:        seq,
			EventKind:  transcriptEventCanonicalStream,
			PayloadRef: string(b),
			CreatedAt:  now,
		}); err != nil {
			return err
		}
	}

	// Operator-visible audit row for stream slice (redacted envelope).
	if err := r.appendStreamAudit(ctx, in, now, pol); err != nil {
		return err
	}
	return nil
}

func (r *Recorder) appendStreamAudit(ctx context.Context, in StreamEventRecordInput, now time.Time, pol domain.PolicyMetadata) error {
	seq, err := r.store.NextAuditSeq(ctx, in.SessionID)
	if err != nil {
		return err
	}
	env := map[string]any{
		"kind":               strings.TrimSpace(in.EventKind),
		"trace_id":           strings.TrimSpace(in.TraceID),
		"b_leg_id":           strings.TrimSpace(in.BLegID),
		"backend_id":         strings.TrimSpace(in.BackendID),
		"usage":              in.IsUsageEvent,
		"redaction_profile":  strings.TrimSpace(pol.RedactionProfile),
		"audit_mode":         strings.TrimSpace(pol.AuditMode),
		"raw_payload_denied": !RawAuditAllowed(pol),
	}
	if RawAuditAllowed(pol) && strings.TrimSpace(in.EventPayloadJSON) != "" {
		env["event"] = json.RawMessage(in.EventPayloadJSON)
	} else if strings.TrimSpace(in.EventPayloadJSON) != "" {
		env["event_digest"] = DigestJSONFields(in.EventPayloadJSON, pol)
	}
	if pc := strings.TrimSpace(in.ProviderCorrelationJSON); pc != "" {
		rc := RedactCorrelationJSON(pc, pol)
		if json.Valid([]byte(rc)) {
			env["provider_correlation"] = json.RawMessage([]byte(rc))
		} else {
			env["provider_correlation"] = rc
		}
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return r.store.AppendAudit(ctx, domain.AuditItem{
		SessionID: in.SessionID,
		TurnID:    in.TurnID,
		Seq:       seq,
		Action:    auditActionPostHookStream,
		Result:    string(b),
		CreatedAt: now,
	})
}
