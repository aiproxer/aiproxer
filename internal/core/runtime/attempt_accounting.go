package runtime

import (
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type attemptAccountingSnapshot struct {
	RequestStartedAt         time.Time
	FirstRemoteEventAt       time.Time
	FirstMeaningfulTokenAt   time.Time
	RemoteCompletedAt        time.Time
	ProxyCompletedAt         time.Time
	TTFTMillis               int64
	RemoteDurationMillis     int64
	CompletionDurationMillis int64
	// CompletionTPSMilli stores tokens-per-second with milliprecision: 5000 means 5.000 TPS.
	CompletionTPSMilli int64
	OutputTokens       int64
}

type attemptAccountingTracker struct {
	requestStartedAt       time.Time
	firstRemoteEventAt     time.Time
	firstMeaningfulTokenAt time.Time
	remoteCompletedAt      time.Time
	proxyCompletedAt       time.Time
	outputTokens           int64
	usageObserved          bool
}

func newAttemptAccountingTracker(startedAt time.Time) attemptAccountingTracker {
	return attemptAccountingTracker{requestStartedAt: startedAt}
}

func (t *attemptAccountingTracker) observeBackendEvent(at time.Time, ev aipapi.Event) {
	if t == nil {
		return
	}
	if t.firstRemoteEventAt.IsZero() && !isKeepaliveEvent(ev) {
		t.firstRemoteEventAt = at
	}
}

func (t *attemptAccountingTracker) observeClientEvent(at time.Time, ev aipapi.Event) {
	if t == nil {
		return
	}
	if t.firstMeaningfulTokenAt.IsZero() && isMeaningfulResponseToken(ev) {
		t.firstMeaningfulTokenAt = at
	}
	if ev.Kind == aipapi.EventResponseFinished {
		if t.remoteCompletedAt.IsZero() {
			t.remoteCompletedAt = at
		}
		t.proxyCompletedAt = at
	}
}

func (t *attemptAccountingTracker) observeUsage(ev aipapi.Event) {
	if t == nil || ev.Kind != aipapi.EventUsageDelta {
		return
	}
	t.usageObserved = true
	t.outputTokens += int64(ev.OutputTokens)
}

func (t *attemptAccountingTracker) snapshot() attemptAccountingSnapshot {
	if t == nil {
		return attemptAccountingSnapshot{}
	}
	out := attemptAccountingSnapshot{
		RequestStartedAt:       t.requestStartedAt,
		FirstRemoteEventAt:     t.firstRemoteEventAt,
		FirstMeaningfulTokenAt: t.firstMeaningfulTokenAt,
		RemoteCompletedAt:      t.remoteCompletedAt,
		ProxyCompletedAt:       t.proxyCompletedAt,
		OutputTokens:           t.outputTokens,
	}
	if !out.RequestStartedAt.IsZero() {
		if !out.FirstMeaningfulTokenAt.IsZero() {
			out.TTFTMillis = millis(out.FirstMeaningfulTokenAt.Sub(out.RequestStartedAt))
		}
		if !out.RemoteCompletedAt.IsZero() {
			out.RemoteDurationMillis = millis(out.RemoteCompletedAt.Sub(out.RequestStartedAt))
		}
	}
	if !out.FirstMeaningfulTokenAt.IsZero() && !out.RemoteCompletedAt.IsZero() {
		d := out.RemoteCompletedAt.Sub(out.FirstMeaningfulTokenAt)
		out.CompletionDurationMillis = millis(d)
		if out.CompletionDurationMillis > 0 && out.OutputTokens > 0 {
			// Store milli-TPS so integer accounting keeps three decimal places without floats.
			out.CompletionTPSMilli = out.OutputTokens * 1000 * 1000 / out.CompletionDurationMillis
		}
	}
	return out
}

func isKeepaliveEvent(ev aipapi.Event) bool {
	return ev.Kind == aipapi.EventWarning && ev.WarningCode == stream.KeepaliveEventCode
}

func isMeaningfulResponseToken(ev aipapi.Event) bool {
	switch ev.Kind {
	case aipapi.EventTextDelta, aipapi.EventReasoningDelta, aipapi.EventToolCallArgsDelta:
		return strings.TrimSpace(ev.Delta) != ""
	case aipapi.EventToolCallStarted, aipapi.EventAssistantImageRef, aipapi.EventAssistantFileRef:
		return true
	default:
		return false
	}
}

func millis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(d / time.Millisecond)
}
