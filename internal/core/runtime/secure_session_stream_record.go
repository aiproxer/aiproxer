package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type responseRecordingOutcome uint8

const (
	responseRecordingSkipped responseRecordingOutcome = iota
	responseRecordingRecorded
	responseRecordingBestEffortFailure
	responseRecordingMandatoryPreOutputFailure
	responseRecordingMandatoryPostCommitFailure
)

// responseRecordingResult is deliberately separate from commitment and
// terminal state. The caller decides whether a mandatory failure can recover;
// the response owner only reports the recorder's typed result.
type responseRecordingResult struct {
	outcome responseRecordingOutcome
	err     error
}

func (r responseRecordingResult) failed() bool {
	return r.outcome == responseRecordingBestEffortFailure ||
		r.outcome == responseRecordingMandatoryPreOutputFailure ||
		r.outcome == responseRecordingMandatoryPostCommitFailure
}

func (r responseRecordingResult) mandatory() bool {
	return r.outcome == responseRecordingMandatoryPreOutputFailure ||
		r.outcome == responseRecordingMandatoryPostCommitFailure
}

// recordClientFacing records one post-hook canonical event before it is
// emitted to the client. Commitment is supplied as a snapshot by the caller;
// this owner never mutates terminal authority or stores a second commitment
// flag.
func (p *responsePipeline) recordClientFacing(
	ctx context.Context,
	facts recvTurnFacts,
	attempt *attemptSession,
	ev aipapi.Event,
	committed bool,
) responseRecordingResult {
	return p.recordClientFacingEvidence(ctx, facts.responseEvidence(), attempt, ev, committed)
}

func (p *responsePipeline) recordClientFacingTerminal(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, ev aipapi.Event, committed bool) responseRecordingResult {
	return p.recordClientFacingEvidence(ctx, request.responseEvidence(), attempt, ev, committed)
}

func (p *responsePipeline) recordClientFacingEvidence(ctx context.Context, evidence responseRequestEvidence, attempt *attemptSession, ev aipapi.Event, committed bool) responseRecordingResult {
	if p == nil || p.secureSessionRecorder == nil || !evidence.secureTurnOK {
		return responseRecordingResult{outcome: responseRecordingSkipped}
	}
	if ev.Kind == aipapi.EventWarning && ev.WarningCode == stream.KeepaliveEventCode {
		return responseRecordingResult{outcome: responseRecordingSkipped}
	}
	in := buildStreamEventRecordInputEvidence(evidence, attempt, p.nowTime(), ev)
	err := p.secureSessionRecorder.RecordPostHookStreamEvent(ctx, in)
	if err != nil {
		committed = committed || aipapi.OutputCommitted(ev)
		if p.secureSessionMetrics != nil {
			p.secureSessionMetrics.ObserveRecorderStreamEventFailed(committed, p.secureRecordingMandatory)
		}
		if p.secureRecordingMandatory {
			outcome := responseRecordingMandatoryPreOutputFailure
			if committed {
				outcome = responseRecordingMandatoryPostCommitFailure
			}
			p.setRecordingOutcome(outcome)
			return responseRecordingResult{outcome: outcome, err: err}
		}
		p.setRecordingOutcome(responseRecordingBestEffortFailure)
		return responseRecordingResult{outcome: responseRecordingBestEffortFailure, err: err}
	}
	p.setRecordingOutcome(responseRecordingRecorded)
	return responseRecordingResult{outcome: responseRecordingRecorded}
}

func buildStreamEventRecordInput(facts recvTurnFacts, attempt *attemptSession, now time.Time, ev aipapi.Event) app.StreamEventRecordInput {
	return buildStreamEventRecordInputEvidence(responseRequestEvidence{traceID: facts.traceID, secureTurn: facts.secureTurn, secureTurnOK: facts.secureTurnOK}, attempt, now, ev)
}

func buildStreamEventRecordInputEvidence(evidence responseRequestEvidence, attempt *attemptSession, now time.Time, ev aipapi.Event) app.StreamEventRecordInput {
	st := evidence.secureTurn
	in := app.StreamEventRecordInput{
		Now:       now,
		TraceID:   strings.TrimSpace(evidence.traceID),
		SessionID: st.SessionID,
		TurnID:    st.TurnID,
		BLegID:    strings.TrimSpace(attempt.bleg.BLegID),
		BackendID: strings.TrimSpace(attempt.cand.Primary.Backend),
		Policy:    st.Policy,
		EventKind: string(ev.Kind),
	}
	if b, err := json.Marshal(streamEventWire(ev)); err == nil {
		in.EventPayloadJSON = string(b)
	}
	if ev.Kind == aipapi.EventUsageDelta || ev.Kind == aipapi.EventResponseFinished {
		in.IsUsageEvent = true
	}
	if ev.Kind == aipapi.EventUsageDelta {
		in.InputTokens = int64(ev.InputTokens)
		in.OutputTokens = int64(ev.OutputTokens)
		in.CacheReadTokens = int64(ev.CacheReadTokens)
		in.CacheWriteTokens = int64(ev.CacheWriteTokens)
		in.NonCachedInputTokens = int64(max(ev.InputTokens-ev.CacheReadTokens-ev.CacheWriteTokens, 0))
		in.ReasoningTokens = int64(ev.ReasoningTokens)
		in.NonReasoningOutputTokens = int64(max(ev.OutputTokens-ev.ReasoningTokens, 0))
		in.TotalTokens = int64(ev.TotalTokens)
		in.CostNanoUnits = ev.CostNanoUnits
		in.Currency = strings.TrimSpace(ev.Currency)
		in.CostSource = strings.TrimSpace(ev.CostSource)
		in.RawUsageJSON = boundRawUsageJSON(strings.TrimSpace(ev.RawUsageJSON))
		if in.InputTokens == 0 && in.OutputTokens == 0 && in.TotalTokens == 0 {
			in.BillingUnavailable = true
		}
	}
	if ev.Kind == aipapi.EventResponseFinished {
		acct := attempt.accountingSnapshot()
		in.RequestStartedAt = acct.RequestStartedAt
		in.FirstRemoteEventAt = acct.FirstRemoteEventAt
		in.FirstMeaningfulTokenAt = acct.FirstMeaningfulTokenAt
		in.RemoteCompletedAt = acct.RemoteCompletedAt
		in.ProxyCompletedAt = acct.ProxyCompletedAt
		in.TTFTMillis = acct.TTFTMillis
		in.RemoteDurationMillis = acct.RemoteDurationMillis
		in.CompletionDurationMillis = acct.CompletionDurationMillis
		in.CompletionTPSMilli = acct.CompletionTPSMilli
	}
	if pc := providerCorrelationJSON(ev); pc != "" {
		in.ProviderCorrelationJSON = pc
	}
	return in
}

const maxRawUsageJSONBytes = 16 << 10

func boundRawUsageJSON(s string) string {
	if len(s) <= maxRawUsageJSONBytes {
		return s
	}
	return s[:maxRawUsageJSONBytes] + `...{"truncated":true}`
}

func streamEventWire(ev aipapi.Event) map[string]any {
	w := map[string]any{
		"kind":          ev.Kind,
		"message_index": ev.MessageIndex,
	}
	if ev.ToolCallID != "" {
		w["tool_call_id"] = ev.ToolCallID
	}
	if ev.ToolName != "" {
		w["tool_name"] = ev.ToolName
	}
	if ev.Delta != "" {
		// Redact large bodies at the wire snapshot layer (digest only when very long).
		if len(ev.Delta) > 4096 {
			w["delta_digest"] = map[string]any{"len": len(ev.Delta)}
		} else {
			w["delta"] = ev.Delta
		}
	}
	if ev.WarningCode != "" {
		w["warning_code"] = ev.WarningCode
	}
	if ev.ErrorCode != "" {
		w["error_code"] = ev.ErrorCode
	}
	if ev.FinishReason != "" {
		w["finish_reason"] = ev.FinishReason
	}
	return w
}

// providerCorrelationJSON extracts non-authoritative provider correlation hints from canonical events.
func providerCorrelationJSON(ev aipapi.Event) string {
	// Wire snapshot only; never treat as session authority (see secure-session design 1.10–1.11).
	if ev.Kind != aipapi.EventResponseStarted {
		return ""
	}
	if strings.TrimSpace(ev.Delta) == "" {
		return ""
	}
	b, err := json.Marshal(map[string]any{"response_started_hint": strings.TrimSpace(ev.Delta)})
	if err != nil {
		return ""
	}
	return string(b)
}
