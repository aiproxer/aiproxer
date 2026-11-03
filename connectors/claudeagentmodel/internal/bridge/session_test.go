package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func toolTestLocation(t *testing.T) Location {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("testdata", "tool-sidecar.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	return Location{Node: node, Script: script, PinnedSDKVersion: "0.3.316"}
}

func toolTestRuntime(t *testing.T, mode string) Runtime {
	t.Helper()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	if mode != "" {
		env = append(env, "FAKE_MODE="+mode)
	}
	return Runtime{CLI: "/opt/claude/bin/claude", Workspace: t.TempDir(), Env: env}
}

func toolTestCatalog(t *testing.T, names ...string) *Catalog {
	t.Helper()
	if len(names) == 0 {
		names = []string{"Read"}
	}
	tools := make([]aipapi.ToolDef, 0, len(names))
	for _, name := range names {
		tools = append(tools, aipapi.ToolDef{Name: name, Parameters: objectSchema()})
	}
	catalog, _, err := BuildCatalog(tools, aipapi.ToolChoice{}, false)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func drainTurn(t *testing.T, turn *Turn) ([]aipapi.Event, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var events []aipapi.Event
	for {
		event, err := turn.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}

func firstTextPrefix(t *testing.T, call aipapi.Call) [32]byte {
	t.Helper()
	prefix, err := VisiblePrefixDigest(call)
	if err != nil {
		t.Fatal(err)
	}
	return prefix
}

func freshCall(text string) aipapi.Call {
	return aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(text)}}}}
}

func continuationCall(text, assistantText, toolID, name, args, result string) aipapi.Call {
	parts := make([]aipapi.Part, 0, 2)
	if assistantText != "" {
		parts = append(parts, aipapi.TextPart(assistantText))
	}
	parts = append(parts, aipapi.Part{Kind: aipapi.PartJSON, ToolCallID: toolID, ToolName: name, Content: json.RawMessage(args)})
	return aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(text)}},
		{Role: aipapi.RoleAssistant, Parts: parts},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: toolID, Text: result}}},
	}}
}

func eventKinds(events []aipapi.Event) []aipapi.EventKind {
	out := make([]aipapi.EventKind, 0, len(events))
	for _, event := range events {
		out = append(out, event.Kind)
	}
	return out
}

func TestToolLoop_ParksThenReleasesExactlyOnce(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	catalog := toolTestCatalog(t, "Read")
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
		RequestID:  "req-1",
		Prompt:     "read the file",
		Catalog:    catalog,
		SessionKey: "proxy-session-1",
		Prefix:     firstTextPrefix(t, freshCall("read the file")),
		PendingTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainTurn(t, turn)
	if err != nil {
		t.Fatal(err)
	}
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted, aipapi.EventMessageStarted, aipapi.EventTextDelta,
		aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished,
		aipapi.EventResponseFinished,
	}
	if got := eventKinds(events); len(got) != len(want) {
		t.Fatalf("events=%v", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("events=%v want=%v", got, want)
			}
		}
	}
	toolStarted := events[3]
	if toolStarted.ToolCallID != "toolu_01ABC" || toolStarted.ToolName != "Read" {
		t.Fatalf("tool started=%+v", toolStarted)
	}
	if got := events[4].Delta; got != `{"file_path":"x"}` {
		t.Fatalf("args delta=%q", got)
	}
	if events[6].FinishReason != "tool_use" {
		t.Fatalf("terminal=%+v", events[6])
	}
	if !turn.Parked() {
		t.Fatal("the turn must report a parked native turn")
	}

	// Closing the HTTP reader after the boundary must not retire the parked turn.
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
	if m.LiveRuntimes() != 1 {
		t.Fatalf("live=%d", m.LiveRuntimes())
	}

	session, err := m.Lookup("toolu_01ABC", "proxy-session-1")
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
	}, "req-2")
	if err != nil {
		t.Fatal(err)
	}
	resumedEvents, err := drainTurn(t, resumed)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, event := range resumedEvents {
		if event.Kind == aipapi.EventTextDelta && event.Delta == "got:contents" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the caller result did not reach the parked turn: %v", eventKinds(resumedEvents))
	}
	if resumedEvents[len(resumedEvents)-1].FinishReason != "end_turn" {
		t.Fatalf("terminal=%+v", resumedEvents[len(resumedEvents)-1])
	}
	_ = resumed.Close()
	if m.LiveRuntimes() != 0 {
		t.Fatalf("live after completion=%d", m.LiveRuntimes())
	}
	if _, err := m.Lookup("toolu_01ABC", "proxy-session-1"); err == nil {
		t.Fatal("a consumed call must not be releasable again")
	} else {
		var continuationErr *ContinuationError
		if !errors.As(err, &continuationErr) || continuationErr.Code != CodeContinuationConsumed {
			t.Fatalf("err=%v", err)
		}
	}
}

func TestToolLoop_DuplicateReleaseRefused(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	catalog := toolTestCatalog(t, "Read")
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
		RequestID: "req-1", Prompt: "read the file", Catalog: catalog,
		Prefix: firstTextPrefix(t, freshCall("read the file")), PendingTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainTurn(t, turn); err != nil {
		t.Fatal(err)
	}
	_ = turn.Close()
	session, err := m.Lookup("toolu_01ABC", "")
	if err != nil {
		t.Fatal(err)
	}
	continuation := continuationCall("read the file", "let me look", "toolu_01ABC", "Read", `{"file_path":"x"}`, "first")
	prefix, _, err := VisiblePrefixThroughCall(continuation, "toolu_01ABC")
	if err != nil {
		t.Fatal(err)
	}
	transcript, _ := VisiblePrefixDigest(continuation)
	if _, err := session.Resume(context.Background(), ResumeSpec{
		CallID: "toolu_01ABC", CatalogDigest: catalog.Digest(), Prefix: prefix,
		Result: ToolResult{CallID: "toolu_01ABC", Text: "first"}, TranscriptPrefix: transcript,
	}, "req-2"); err != nil {
		t.Fatal(err)
	}
	// The session is running the continuation; a second release must be refused
	// before the state machine can accept it.
	if _, err := session.Resume(context.Background(), ResumeSpec{
		CallID: "toolu_01ABC", CatalogDigest: catalog.Digest(), Prefix: prefix,
		Result: ToolResult{CallID: "toolu_01ABC", Text: "duplicate"}, TranscriptPrefix: transcript,
	}, "req-3"); err == nil {
		t.Fatal("a duplicate release must be refused")
	}
	if _, err := m.Lookup("toolu_01ABC", ""); err == nil {
		t.Fatal("the consumed call must be tombstoned")
	}
}

func TestToolLoop_MismatchedResultDoesNotRelease(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	catalog := toolTestCatalog(t, "Read")
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
		RequestID: "req-1", Prompt: "read the file", Catalog: catalog,
		Prefix: firstTextPrefix(t, freshCall("read the file")), PendingTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainTurn(t, turn); err != nil {
		t.Fatal(err)
	}
	_ = turn.Close()
	session, err := m.Lookup("toolu_01ABC", "")
	if err != nil {
		t.Fatal(err)
	}
	continuation := continuationCall("read the file", "let me look", "toolu_01ABC", "Read", `{"file_path":"x"}`, "contents")
	prefix, _, err := VisiblePrefixThroughCall(continuation, "toolu_01ABC")
	if err != nil {
		t.Fatal(err)
	}
	transcript, _ := VisiblePrefixDigest(continuation)
	cases := []struct {
		name string
		spec ResumeSpec
		code string
	}{
		{"foreign call id", ResumeSpec{CallID: "toolu_foreign", CatalogDigest: catalog.Digest(), Prefix: prefix}, CodeContinuationMismatch},
		{"wrong catalogue", ResumeSpec{CallID: "toolu_01ABC", CatalogDigest: [32]byte{9}, Prefix: prefix}, CodeContinuationMismatch},
		{"wrong prefix", ResumeSpec{CallID: "toolu_01ABC", CatalogDigest: catalog.Digest(), Prefix: [32]byte{7}}, CodeContinuationMismatch},
	}
	for _, tc := range cases {
		tc.spec.Result = ToolResult{CallID: "toolu_01ABC", Text: "nope"}
		tc.spec.TranscriptPrefix = transcript
		_, err := session.Resume(context.Background(), tc.spec, "req-2")
		var continuationErr *ContinuationError
		if err == nil || !errors.As(err, &continuationErr) || continuationErr.Code != tc.code {
			t.Fatalf("%s err=%v", tc.name, err)
		}
	}
	// Nothing was released: the same session still accepts the valid result.
	resumed, err := session.Resume(context.Background(), ResumeSpec{
		CallID: "toolu_01ABC", CatalogDigest: catalog.Digest(), Prefix: prefix,
		Result: ToolResult{CallID: "toolu_01ABC", Text: "contents"}, TranscriptPrefix: transcript,
	}, "req-3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainTurn(t, resumed); err != nil {
		t.Fatal(err)
	}
}

func TestToolLoop_ExpiredPendingIsTyped(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 80*time.Millisecond, time.Minute, 5*time.Second, time.Now)
	catalog := toolTestCatalog(t, "Read")
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
		RequestID: "req-1", Prompt: "read the file", Catalog: catalog,
		Prefix: firstTextPrefix(t, freshCall("read the file")), PendingTTL: 80 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainTurn(t, turn); err != nil {
		t.Fatal(err)
	}
	_ = turn.Close()
	session, err := m.Lookup("toolu_01ABC", "")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	continuation := continuationCall("read the file", "let me look", "toolu_01ABC", "Read", `{"file_path":"x"}`, "late")
	prefix, _, err := VisiblePrefixThroughCall(continuation, "toolu_01ABC")
	if err != nil {
		t.Fatal(err)
	}
	_ = session
	_ = prefix
	deadline := time.Now().Add(2 * time.Second)
	var err2 error
	for {
		// The engine always resolves the conversation through the manager, so an
		// expired pending call surfaces as a typed continuation error.
		_, err2 = m.Lookup("toolu_01ABC", "")
		if err2 != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the expired pending call was not retired")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var continuationErr *ContinuationError
	if !errors.As(err2, &continuationErr) || continuationErr.Code != CodeContinuationExpired {
		t.Fatalf("err=%v", err2)
	}
	if m.LiveRuntimes() != 0 {
		t.Fatalf("live=%d", m.LiveRuntimes())
	}
}

func TestToolLoop_ParallelAndNativeCallsFailClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode string
		code string
	}{
		{"parallel", CodeUnsupportedParallelToolCalls},
		{"native_tool", "native_tool_use"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
			catalog := toolTestCatalog(t, "Read")
			turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, tc.mode), TurnRequest{
				RequestID: "req-1", Prompt: "read the file", Catalog: catalog,
				Prefix: firstTextPrefix(t, freshCall("read the file")), PendingTTL: 5 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = drainTurn(t, turn)
			if err == nil {
				t.Fatal("expected a typed failure")
			}
			if continuationErr, ok := errors.AsType[*ContinuationError](err); ok {
				if continuationErr.Code != tc.code {
					t.Fatalf("code=%s want=%s", continuationErr.Code, tc.code)
				}
				return
			}
			var sidecarErr *SidecarError
			if !errors.As(err, &sidecarErr) || sidecarErr.Code != tc.code {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestToolLoop_CancelRetiresParkedAndRunningTurns(t *testing.T) {
	t.Parallel()
	t.Run("parked", func(t *testing.T) {
		t.Parallel()
		m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
		catalog := toolTestCatalog(t, "Read")
		turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
			RequestID: "req-1", Prompt: "read the file", Catalog: catalog,
			Prefix: firstTextPrefix(t, freshCall("read the file")), PendingTTL: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := drainTurn(t, turn); err != nil {
			t.Fatal(err)
		}
		result := turn.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		if result.Mode != aipapi.CancelModeTransport {
			t.Fatalf("result=%+v", result)
		}
		if m.LiveRuntimes() != 0 {
			t.Fatalf("live=%d", m.LiveRuntimes())
		}
		if _, err := m.Lookup("toolu_01ABC", ""); err == nil {
			t.Fatal("a canceled parked call must not be releasable")
		}
	})
	t.Run("running", func(t *testing.T) {
		t.Parallel()
		m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
		catalog := toolTestCatalog(t, "Read")
		turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, "slow"), TurnRequest{
			RequestID: "req-1", Prompt: "read the file", Catalog: catalog,
			Prefix: firstTextPrefix(t, freshCall("read the file")), PendingTTL: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		// Read the first events so the turn is actively running.
		if _, err := turn.Recv(context.Background()); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = turn.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("cancel did not join")
		}
		if m.LiveRuntimes() != 0 {
			t.Fatalf("live=%d", m.LiveRuntimes())
		}
	})
}

func TestToolLoop_SecondCallInTheSameNativeTurn(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	catalog := toolTestCatalog(t, "Read", "Bash")
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, "second_tool"), TurnRequest{
		RequestID: "req-1", Prompt: "read then run", Catalog: catalog,
		Prefix: firstTextPrefix(t, freshCall("read then run")), PendingTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainTurn(t, turn); err != nil {
		t.Fatal(err)
	}
	_ = turn.Close()

	first := continuationCall("read then run", "let me look", "toolu_01ABC", "Read", `{"file_path":"x"}`, "contents")
	prefix, _, err := VisiblePrefixThroughCall(first, "toolu_01ABC")
	if err != nil {
		t.Fatal(err)
	}
	transcript, _ := VisiblePrefixDigest(first)
	session, err := m.Lookup("toolu_01ABC", "")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := session.Resume(context.Background(), ResumeSpec{
		CallID: "toolu_01ABC", CatalogDigest: catalog.Digest(), Prefix: prefix,
		Result: ToolResult{CallID: "toolu_01ABC", Text: "contents"}, TranscriptPrefix: transcript,
	}, "req-2")
	if err != nil {
		t.Fatal(err)
	}
	events, err := drainTurn(t, resumed)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Parked() {
		t.Fatalf("expected a second park: %v", eventKinds(events))
	}
	if !containsKind(events, aipapi.EventToolCallStarted) {
		t.Fatalf("missing second tool call: %v", eventKinds(events))
	}
	_ = resumed.Close()

	second := aipapi.Call{Messages: append(append([]aipapi.Message(nil), first.Messages...),
		aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
			aipapi.TextPart("got:contents"),
			{Kind: aipapi.PartJSON, ToolCallID: "toolu_02SECOND", ToolName: "Bash", Content: json.RawMessage(`{"file_path":"z"}`)},
		}},
		aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{Kind: aipapi.PartToolResult, ToolCallID: "toolu_02SECOND", Text: "ok"}}},
	)}
	secondPrefix, found, err := VisiblePrefixThroughCall(second, "toolu_02SECOND")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	secondTranscript, _ := VisiblePrefixDigest(second)
	session2, err := m.Lookup("toolu_02SECOND", "")
	if err != nil {
		t.Fatal(err)
	}
	resumed2, err := session2.Resume(context.Background(), ResumeSpec{
		CallID: "toolu_02SECOND", CatalogDigest: catalog.Digest(), Prefix: secondPrefix,
		Result: ToolResult{CallID: "toolu_02SECOND", Text: "ok"}, TranscriptPrefix: secondTranscript,
	}, "req-3")
	if err != nil {
		t.Fatal(err)
	}
	events2, err := drainTurn(t, resumed2)
	if err != nil {
		t.Fatal(err)
	}
	if resumed2.Parked() {
		t.Fatalf("the second continuation should complete: %v", eventKinds(events2))
	}
	if m.LiveRuntimes() != 0 {
		t.Fatalf("live=%d", m.LiveRuntimes())
	}
}

func TestToolLoop_ConcurrentSessionsNeverCrossResume(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	catalog := toolTestCatalog(t, "Read")
	start := func(key, prompt string) *Turn {
		turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
			RequestID: "req-" + key, Prompt: prompt, Catalog: catalog, SessionKey: key,
			Prefix: firstTextPrefix(t, freshCall(prompt)), PendingTTL: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := drainTurn(t, turn); err != nil {
			t.Fatal(err)
		}
		_ = turn.Close()
		return turn
	}
	start("session-a", "read a")
	start("session-b", "read b")
	sessionB, err := m.Lookup("toolu_01ABC", "session-b")
	if err != nil {
		t.Fatal(err)
	}
	foreign := continuationCall("read a", "let me look", "toolu_01ABC", "Read", `{"file_path":"x"}`, "contents")
	foreignPrefix, _, err := VisiblePrefixThroughCall(foreign, "toolu_01ABC")
	if err != nil {
		t.Fatal(err)
	}
	foreignTranscript, _ := VisiblePrefixDigest(foreign)
	// The tool id exists, but its prefix belongs to the other conversation.
	if _, err := sessionB.Resume(context.Background(), ResumeSpec{
		CallID: "toolu_01ABC", CatalogDigest: catalog.Digest(), Prefix: foreignPrefix,
		Result: ToolResult{CallID: "toolu_01ABC", Text: "contents"}, TranscriptPrefix: foreignTranscript,
	}, "req-b2"); err == nil {
		t.Fatal("a foreign conversation prefix must not release another session")
	}
	// Session A is still parked and releases exactly once.
	sessionA, err := m.Lookup("toolu_01ABC", "session-a")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := sessionA.Resume(context.Background(), ResumeSpec{
		CallID: "toolu_01ABC", CatalogDigest: catalog.Digest(), Prefix: foreignPrefix,
		Result: ToolResult{CallID: "toolu_01ABC", Text: "contents"}, TranscriptPrefix: foreignTranscript,
	}, "req-a2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainTurn(t, resumed); err != nil {
		t.Fatal(err)
	}
}

type countingAdmission struct {
	mu    sync.Mutex
	limit int
	live  int
}

func (a *countingAdmission) Acquire(context.Context, time.Duration) (Release, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.live >= a.limit {
		return nil, errors.New("claudeagentmodel: capacity exhausted")
	}
	a.live++
	return releaseFunc(func() {
		a.mu.Lock()
		if a.live > 0 {
			a.live--
		}
		a.mu.Unlock()
	}), nil
}

type releaseFunc func()

func (f releaseFunc) Release() { f() }

func TestManager_AdmissionIsHeldUntilRetirement(t *testing.T) {
	t.Parallel()
	admission := &countingAdmission{limit: 1}
	m := NewManager(admission, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	first, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, "slow"), TurnRequest{
		RequestID: "req-1", Prompt: "hi", Prefix: firstTextPrefix(t, freshCall("hi")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
		RequestID: "req-2", Prompt: "hi", Prefix: firstTextPrefix(t, freshCall("hi")),
	}); err == nil {
		t.Fatal("a second sidecar was admitted beyond the bound")
	}
	_ = first.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if m.LiveRuntimes() != 0 {
		t.Fatalf("live=%d", m.LiveRuntimes())
	}
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
		RequestID: "req-3", Prompt: "hi", Prefix: firstTextPrefix(t, freshCall("hi")),
	})
	if err != nil {
		t.Fatalf("admission slot was not released after retirement: %v", err)
	}
	if _, err := drainTurn(t, turn); err != nil {
		t.Fatal(err)
	}
}

func containsKind(events []aipapi.Event, kind aipapi.EventKind) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func TestToolLoop_ProcessDeathAndShutdownRetireTheParkedTurn(t *testing.T) {
	t.Parallel()
	park := func(t *testing.T, mode string) (*Manager, *Turn) {
		t.Helper()
		m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
		turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, mode), TurnRequest{
			RequestID: "req-1", Prompt: "read the file", Catalog: toolTestCatalog(t, "Read"),
			Prefix: firstTextPrefix(t, freshCall("read the file")), PendingTTL: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := drainTurn(t, turn); err != nil {
			t.Fatal(err)
		}
		_ = turn.Close()
		return m, turn
	}

	t.Run("process death", func(t *testing.T) {
		t.Parallel()
		m, _ := park(t, "die_after_park")
		time.Sleep(80 * time.Millisecond)
		session, err := m.Lookup("toolu_01ABC", "")
		if err != nil {
			t.Fatal(err)
		}
		continuation := continuationCall("read the file", "let me look", "toolu_01ABC", "Read", `{"file_path":"x"}`, "contents")
		prefix, _, err := VisiblePrefixThroughCall(continuation, "toolu_01ABC")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := session.Resume(context.Background(), ResumeSpec{
			CallID: "toolu_01ABC", CatalogDigest: toolTestCatalog(t, "Read").Digest(), Prefix: prefix,
			Result: ToolResult{CallID: "toolu_01ABC", Text: "contents"},
		}, "req-2"); err == nil {
			t.Fatal("a dead sidecar must not accept a release")
		}
		if m.LiveRuntimes() != 0 {
			t.Fatalf("live=%d", m.LiveRuntimes())
		}
	})

	t.Run("shutdown", func(t *testing.T) {
		t.Parallel()
		m, _ := park(t, "")
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
		if m.LiveRuntimes() != 0 {
			t.Fatalf("live=%d", m.LiveRuntimes())
		}
		if _, err := m.Lookup("toolu_01ABC", ""); err == nil {
			t.Fatal("shutdown must retire the parked call")
		}
	})
}

func TestToolLoop_CloseAtTerminalStillFinalizesThePark(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, "flood_park"), TurnRequest{
		RequestID: "req-1", Prompt: "read the file", Catalog: toolTestCatalog(t, "Read"),
		Prefix: firstTextPrefix(t, freshCall("read the file")), PendingTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The response queue is exactly full when the parked terminal arrives, so its
	// emission blocks. Close then fails that emission; terminal ownership must
	// still be finalized.
	deadline := time.Now().Add(5 * time.Second)
	for !turn.Parked() {
		if time.Now().After(deadline) {
			t.Fatal("the parked terminal was never observed")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
	// The terminal frame is still read and finalized: the conversation is parked,
	// not stranded in RUNNING, and the per-turn slot is free.
	deadline = time.Now().Add(5 * time.Second)
	for {
		session, err := m.Lookup("toolu_01ABC", "")
		if err != nil {
			t.Fatalf("parked conversation lost: %v", err)
		}
		if session.PendingCallID() == "toolu_01ABC" && session.isWaiting() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal ownership was not finalized")
		}
		time.Sleep(5 * time.Millisecond)
	}
	continuation := continuationCall("read the file", "let me look", "toolu_01ABC", "Read", `{"file_path":"x"}`, "contents")
	prefix, found, err := VisiblePrefixThroughCall(continuation, "toolu_01ABC")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	session, err := m.Lookup("toolu_01ABC", "")
	if err != nil {
		t.Fatal(err)
	}
	// A new reader can acquire the slot: the old reader released it during
	// finalization even though its own emission failed.
	resumed, err := session.Resume(context.Background(), ResumeSpec{
		CallID: "toolu_01ABC", CatalogDigest: toolTestCatalog(t, "Read").Digest(), Prefix: prefix,
		Result: ToolResult{CallID: "toolu_01ABC", Text: "contents"},
	}, "req-2")
	if err != nil {
		t.Fatalf("slot was stranded: %v", err)
	}
	_ = resumed.Close()
}

func TestToolLoop_OldReaderDetachKeepsTheNewTimer(t *testing.T) {
	t.Parallel()
	m := NewManager(nil, 5*time.Second, time.Minute, 5*time.Second, time.Now)
	turn, err := m.Start(context.Background(), toolTestLocation(t), toolTestRuntime(t, ""), TurnRequest{
		RequestID: "req-1", Prompt: "read the file", Catalog: toolTestCatalog(t, "Read"),
		Prefix: firstTextPrefix(t, freshCall("read the file")), PendingTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainTurn(t, turn); err != nil {
		t.Fatal(err)
	}
	_ = turn.Close()
	session, err := m.Lookup("toolu_01ABC", "")
	if err != nil {
		t.Fatal(err)
	}
	continuation := continuationCall("read the file", "let me look", "toolu_01ABC", "Read", `{"file_path":"x"}`, "contents")
	prefix, _, err := VisiblePrefixThroughCall(continuation, "toolu_01ABC")
	if err != nil {
		t.Fatal(err)
	}
	transcript, _ := VisiblePrefixDigest(continuation)
	resumed, err := session.Resume(context.Background(), ResumeSpec{
		CallID: "toolu_01ABC", CatalogDigest: toolTestCatalog(t, "Read").Digest(), Prefix: prefix,
		Result: ToolResult{CallID: "toolu_01ABC", Text: "contents"}, TranscriptPrefix: transcript,
	}, "req-2")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resumed.Close() }()
	// The old reader's detach must not clear the new reader's state or timer.
	session.mu.Lock()
	owned := session.active == resumed
	hasIdle := session.idle != nil
	session.mu.Unlock()
	if !owned {
		t.Fatal("the new reader lost session ownership")
	}
	if !hasIdle {
		t.Fatal("the new reader's idle timer was cleared by the old detach")
	}
	session.detach(turn)
	session.mu.Lock()
	stillOwned := session.active == resumed
	stillIdle := session.idle != nil
	session.mu.Unlock()
	if !stillOwned || !stillIdle {
		t.Fatal("an old reader detached the new reader's timer")
	}
}
