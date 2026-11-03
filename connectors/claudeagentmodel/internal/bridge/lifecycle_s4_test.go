package bridge

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestLifecycle_PendingTTLUsesInjectedClock proves the pending deadline is a
// function of the manager clock, not wall time, and that expiry is explicit and
// retained long enough to answer a late continuation.
func TestLifecycle_PendingTTLUsesInjectedClock(t *testing.T) {
	t.Parallel()
	base := time.Date(2031, time.January, 2, 3, 4, 5, 0, time.UTC)
	pendingTTL := time.Hour
	m := NewManager(nil, pendingTTL, time.Hour, 5*time.Second, func() time.Time { return base })
	catalog := toolTestCatalog(t, "Read")
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
		RequestID:  "req-ttl",
		Prompt:     "read the file",
		Catalog:    catalog,
		SessionKey: "proxy-session-ttl",
		Prefix:     firstTextPrefix(t, freshCall("read the file")),
		PendingTTL: pendingTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainTurn(t, turn); err != nil {
		t.Fatal(err)
	}
	_ = turn.Close()
	session, err := m.Lookup("toolu_01ABC", "proxy-session-ttl")
	if err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	pending := session.pending
	session.mu.Unlock()
	if pending == nil {
		t.Fatal("the committed call was not parked")
	}
	if !pending.ExpiresAt.Equal(base.Add(pendingTTL)) {
		t.Fatalf("expires_at=%s want=%s", pending.ExpiresAt, base.Add(pendingTTL))
	}

	base = base.Add(2 * pendingTTL)
	session.expire(pending)
	_, err = m.Lookup("toolu_01ABC", "proxy-session-ttl")
	var expired *ContinuationError
	if !errors.As(err, &expired) || expired.Code != CodeContinuationExpired {
		t.Fatalf("expired lookup=%v", err)
	}
	if m.LiveRuntimes() != 0 {
		t.Fatalf("expired runtime still live: %d", m.LiveRuntimes())
	}

	// Tombstones are pruned after twice the pending TTL, so a much later request
	// is an honest no-pending result rather than an unrelated replay marker.
	base = base.Add(3 * pendingTTL)
	if _, err := m.Lookup("toolu_01ABC", "proxy-session-ttl"); !errors.Is(err, ErrNoPending) {
		t.Fatalf("pruned tombstone lookup=%v", err)
	}
}

// TestLifecycle_IdleRetirementIsOwnershipConditional proves a released
// continuation that never reaches a terminal is retired by its idle timer, and
// that a stale timer callback cannot close a turn it does not own.
func TestLifecycle_IdleRetirementIsOwnershipConditional(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, time.Hour, time.Hour, 5*time.Second, time.Now)
	catalog := toolTestCatalog(t, "Read")
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, "stall_after_result"), TurnRequest{
		RequestID:  "req-idle",
		Prompt:     "read the file",
		Catalog:    catalog,
		SessionKey: "proxy-session-idle",
		Prefix:     firstTextPrefix(t, freshCall("read the file")),
		PendingTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainTurn(t, turn); err != nil {
		t.Fatal(err)
	}
	_ = turn.Close()
	session, err := m.Lookup("toolu_01ABC", "proxy-session-idle")
	if err != nil {
		t.Fatal(err)
	}
	continuation := continuationCall("read the file", "let me look", "toolu_01ABC", "Read", `{"file_path":"x"}`, "contents")
	prefix, found, err := VisiblePrefixThroughCall(continuation, "toolu_01ABC")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	transcript, err := VisiblePrefixDigest(continuation)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := session.Resume(context.Background(), ResumeSpec{
		CallID:           "toolu_01ABC",
		CatalogDigest:    catalog.Digest(),
		Prefix:           prefix,
		Result:           ToolResult{CallID: "toolu_01ABC", Text: "contents"},
		TranscriptPrefix: transcript,
	}, "req-idle-2")
	if err != nil {
		t.Fatal(err)
	}

	// A callback from an older reader must not retire the current turn.
	session.retireIdle(&Turn{})
	if session.closed {
		t.Fatal("a stale idle callback closed the live conversation")
	}
	session.retireIdle(resumed)
	if !session.closed {
		t.Fatal("the owning idle callback did not retire the conversation")
	}
	if m.LiveRuntimes() != 0 {
		t.Fatalf("idle runtime still live: %d", m.LiveRuntimes())
	}
}
