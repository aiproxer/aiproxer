package openresponses

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDelayPlan_BeforeFirstHonored(t *testing.T) {
	t.Parallel()
	const d = 60 * time.Millisecond
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-delay", Description: "before-first delay", Mode: ModeJSON,
		Delay:    DelayPlan{BeforeFirst: d},
		Resource: NewResource("r", "m", 1, nil),
	})
	start := time.Now()
	resp, raw := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if elapsed < d {
		t.Fatalf("before-first delay not honored: %v < %v", elapsed, d)
	}
	if len(raw) == 0 {
		t.Fatal("empty body")
	}
}

func TestDelayPlan_BetweenEventsHonored(t *testing.T) {
	t.Parallel()
	const per = 30 * time.Millisecond
	// A text resource yields 8 events; between-event delays apply to 7 gaps.
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-delay-events", Description: "between events", Mode: ModeSSE,
		Delay:    DelayPlan{BetweenEvents: per},
		Resource: NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
	})
	start := time.Now()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	elapsed := time.Since(start)
	if elapsed < 7*per {
		t.Fatalf("between-event delays not honored: %v < %v", elapsed, 7*per)
	}
}

func TestDelayPlan_SlowWriteAddsLatency(t *testing.T) {
	t.Parallel()
	const slow = 20 * time.Millisecond
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-slow", Description: "slow write", Mode: ModeJSON,
		Delay:    DelayPlan{SlowWrite: slow},
		Resource: NewResource("r", "m", 1, nil),
	})
	start := time.Now()
	resp, _ := postJSON(t, ts.URL+"/responses", `{"model":"m","input":"hi"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed < slow {
		t.Fatalf("slow write not honored: %v < %v", elapsed, slow)
	}
}

func TestCancel_ObservedDuringSSE(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-cancel", Description: "cancellation observation", Mode: ModeSSE,
		Delay:    DelayPlan{BetweenEvents: 80 * time.Millisecond},
		Resource: NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Read the first event, then cancel mid-stream.
	_, _ = resp.Body.Read(make([]byte, 512))
	cancel()
	_, _ = io.Copy(io.Discard, resp.Body)

	if !eventually(t, 2*time.Second, func() bool { return srv.CancelCount() >= 1 }) {
		t.Fatalf("server did not observe cancellation (cancel=%d writeErr=%d)", srv.CancelCount(), srv.WriteErrorCount())
	}
}

func TestDisconnect_AfterNEvents(t *testing.T) {
	t.Parallel()
	_, ts := startServer(t, Options{}, &Script{
		ID: "scenario-disconnect", Description: "disconnect mid-stream", Mode: ModeSSE,
		DisconnectAfter: 2,
		Resource:        NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, rerr := io.ReadAll(resp.Body)
	// The connection is hijacked after 2 events: the client must observe an
	// abnormal end (EOF without [DONE], or a read error).
	if rerr == nil && strings.Contains(string(raw), "[DONE]") {
		t.Fatalf("stream must not complete cleanly after forced disconnect")
	}
	if rerr == nil && strings.Count(string(raw), "event:") < 2 {
		t.Fatalf("expected at least 2 events before disconnect, got %q", raw)
	}
}

func TestWriteError_ClientAbortObserved(t *testing.T) {
	t.Parallel()
	srv, ts := startServer(t, Options{}, &Script{
		ID: "scenario-abort", Description: "client abort", Mode: ModeSSE,
		Delay:    DelayPlan{BetweenEvents: 60 * time.Millisecond},
		Resource: NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/responses", strings.NewReader(`{"model":"m","input":"hi","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// Abort the connection by closing the body and cancelling immediately.
	cancel()
	_ = resp.Body.Close()

	if !eventually(t, 2*time.Second, func() bool { return srv.WriteErrorCount() >= 1 || srv.CancelCount() >= 1 }) {
		t.Fatalf("server did not observe client abort (writeErr=%d cancel=%d)", srv.WriteErrorCount(), srv.CancelCount())
	}
}

// eventually polls fn until it returns true or the deadline passes.
func eventually(t *testing.T, timeout time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fn()
}
