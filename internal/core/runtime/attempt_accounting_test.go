package runtime

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestAttemptAccountingTracker_ignoresKeepaliveAndWhitespaceForTTFT(t *testing.T) {
	t.Parallel()

	start := time.Unix(100, 0)
	tr := newAttemptAccountingTracker(start)
	tr.observeBackendEvent(start.Add(100*time.Millisecond), aipapi.Event{
		Kind: aipapi.EventWarning, WarningCode: stream.KeepaliveEventCode,
	})
	tr.observeBackendEvent(start.Add(150*time.Millisecond), aipapi.Event{Kind: aipapi.EventResponseStarted})
	tr.observeClientEvent(start.Add(200*time.Millisecond), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "   \n"})
	tr.observeClientEvent(start.Add(350*time.Millisecond), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hello"})
	tr.observeClientEvent(start.Add(850*time.Millisecond), aipapi.Event{Kind: aipapi.EventResponseFinished})

	got := tr.snapshot()
	if !got.FirstRemoteEventAt.Equal(start.Add(150 * time.Millisecond)) {
		t.Fatalf("FirstRemoteEventAt: got %v", got.FirstRemoteEventAt)
	}
	if !got.FirstMeaningfulTokenAt.Equal(start.Add(350 * time.Millisecond)) {
		t.Fatalf("FirstMeaningfulTokenAt: got %v", got.FirstMeaningfulTokenAt)
	}
	if got.TTFTMillis != 350 {
		t.Fatalf("TTFTMillis: got %d want 350", got.TTFTMillis)
	}
}

func TestAttemptAccountingTracker_completionTPSUsesOutputTokensFromMeaningfulTokenToFinish(t *testing.T) {
	t.Parallel()

	start := time.Unix(200, 0)
	tr := newAttemptAccountingTracker(start)
	tr.observeClientEvent(start.Add(time.Second), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	tr.observeUsage(aipapi.Event{Kind: aipapi.EventUsageDelta, OutputTokens: 10})
	tr.observeClientEvent(start.Add(3*time.Second), aipapi.Event{Kind: aipapi.EventResponseFinished})

	got := tr.snapshot()
	if got.CompletionTPSMilli != 5000 {
		t.Fatalf("CompletionTPSMilli: got %d want 5000", got.CompletionTPSMilli)
	}
	if got.CompletionDurationMillis != 2000 {
		t.Fatalf("CompletionDurationMillis: got %d want 2000", got.CompletionDurationMillis)
	}
}

func TestAttemptAccountingTracker_subMillisecondCompletionDoesNotPanic(t *testing.T) {
	t.Parallel()

	start := time.Unix(300, 0)
	tr := newAttemptAccountingTracker(start)
	tr.observeClientEvent(start.Add(time.Second), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"})
	tr.observeUsage(aipapi.Event{Kind: aipapi.EventUsageDelta, OutputTokens: 1})
	tr.observeClientEvent(start.Add(time.Second+500*time.Microsecond), aipapi.Event{Kind: aipapi.EventResponseFinished})

	got := tr.snapshot()
	if got.CompletionDurationMillis != 0 {
		t.Fatalf("CompletionDurationMillis: got %d want 0", got.CompletionDurationMillis)
	}
	if got.CompletionTPSMilli != 0 {
		t.Fatalf("CompletionTPSMilli: got %d want 0", got.CompletionTPSMilli)
	}
}
