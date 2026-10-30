package compactiondetect

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

func testDetector(t *testing.T) *Detector {
	t.Helper()
	return New(Config{Now: func() time.Time { return time.Unix(100, 0).UTC() }})
}

// textCall builds a canonical request carrying the given text plus an
// optional number of declared tools.
func textCall(text string, tools int) aipapi.Call {
	call := aipapi.Call{
		ID:         "call-" + fmt.Sprintf("%d", len(text)),
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(text)}}},
	}
	for i := range tools {
		call.Tools = append(call.Tools, aipapi.ToolDef{Name: fmt.Sprintf("tool-%d", i)})
	}
	return call
}

func reqMeta(trace string) compaction.PreservationMeta {
	return compaction.PreservationMeta{TraceID: trace, ALegID: "a-leg-1", BLegID: "b-leg-1", AttemptSeq: 1, SessionID: "sess-1"}
}

func resMeta(trace string) compaction.PreservationMeta {
	return compaction.PreservationMeta{TraceID: trace, ALegID: "a-leg-1", BLegID: "b-leg-1", AttemptSeq: 1, SessionID: "sess-1"}
}

// TestRuleMatrix_positives proves every versioned rule in research.md matches
// its distinctive canonical conjunction and produces the expected rule id and
// evidence class (requirements 4.1-4.3, 8.6).
func TestRuleMatrix_positives(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		call   aipapi.Call
		op     aipapi.Operation
		ruleID string
	}{
		{
			name:   "protocol explicit compact operation",
			call:   textCall("ordinary user text", 0),
			op:     aipapi.OperationContextCompaction,
			ruleID: "protocol.context_compaction.v1",
		},
		{
			name:   "codex local checkpoint",
			call:   textCall("CONTEXT CHECKPOINT COMPACTION\nPlease compact the conversation.", 0),
			ruleID: "codex.local_checkpoint.v1",
		},
		{
			name:   "pi/openclaw context summarizer",
			call:   textCall("<conversation>\nSummarize the context into a checkpoint for continuation.", 0),
			ruleID: "pi_openclaw.compaction_summary.v1",
		},
		{
			name:   "cline agentic compaction",
			call:   textCall("Continuation-note: summarize the conversation so far.", 0),
			ruleID: "cline.agentic_compaction.v1",
		},
		{
			name:   "opencode anchored summary",
			call:   textCall("<conversation>\nProduce an anchored summary with Objective and Work State sections.", 0),
			ruleID: "opencode.anchored_summary.v1",
		},
		{
			name:   "opencode custom compaction history",
			call:   textCall("The following is the conversation history: keep it for the next turn.", 0),
			ruleID: "opencode.custom_compaction_history.v1",
		},
		{
			name:   "kilocode anchored summary template",
			call:   textCall("Objective: fix tests\nImportant Details: see below\nWork State: wip\nNext Move: run suite\nRelevant Files: a_test.go", 0),
			ruleID: "kilocode.anchored_summary.v1",
		},
		{
			name:   "claude code 2026-03 text-only compaction",
			call:   textCall("TEXT ONLY\nCompaction sections follow. Summarize this conversation compactly.", 0),
			ruleID: "claude_code_2026_03.compaction.v1",
		},
		{
			name:   "gemini cli state snapshot generation",
			call:   textCall("Generate a state snapshot of the current conversation.", 0),
			ruleID: "gemini_cli.state_snapshot.v1",
		},
		{
			name:   "roo code condense",
			call:   textCall("Condense and summarize the conversation history.", 0),
			ruleID: "roo_code.condense.v1",
		},
		{
			name:   "aider chat summary",
			call:   textCall("# USER\nWe changed the API.\n# ASSISTANT\nGot it.\nPlease summarize the programming session.", 0),
			ruleID: "aider.chat_summary.v1",
		},
		{
			name:   "crush session summary",
			call:   textCall("Session summary: preserve context across the conversation.", 0),
			ruleID: "crush.session_summary.v1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := testDetector(t)
			call := tc.call
			if tc.op != "" {
				call.Invocation.Operation = tc.op
			}
			evs := d.RequestOpened(reqMeta("tr-"+tc.name), call)
			if len(evs) != 1 {
				t.Fatalf("events=%v want exactly one started", evs)
			}
			ev := evs[0]
			if ev.Phase != compaction.PhaseStarted {
				t.Fatalf("phase=%q want started", ev.Phase)
			}
			if ev.RuleID != tc.ruleID {
				t.Fatalf("RuleID=%q want %q", ev.RuleID, tc.ruleID)
			}
			if ev.RuleID == "protocol.context_compaction.v1" {
				if ev.Evidence != compaction.EvidenceProtocolStrict {
					t.Fatalf("protocol evidence=%q want protocol_strict", ev.Evidence)
				}
			} else if ev.Evidence != compaction.EvidenceSignatureStrict {
				t.Fatalf("evidence=%q want signature_strict", ev.Evidence)
			}
			if ev.ALegID != "a-leg-1" || ev.BLegID != "b-leg-1" || ev.TraceID != "tr-"+tc.name || ev.TransactionID == "" {
				t.Fatalf("correlation metadata missing: %+v", ev)
			}
		})
	}
}

// TestRuleMatrix_protocolStart proves the explicit compact operation produces a
// protocol-strict start (requirement 3.1).
func TestRuleMatrix_protocolStart(t *testing.T) {
	t.Parallel()
	call := textCall("ordinary user text", 0)
	call.Invocation.Operation = aipapi.OperationContextCompaction
	call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming

	d := testDetector(t)
	evs := d.RequestOpened(reqMeta("tr-protocol"), call)
	if len(evs) != 1 {
		t.Fatalf("events=%v", evs)
	}
	if evs[0].RuleID != "protocol.context_compaction.v1" || evs[0].Evidence != compaction.EvidenceProtocolStrict {
		t.Fatalf("protocol start wrong: %+v", evs[0])
	}
}

// TestRuleMatrix_protocolStrictPrecedence proves protocol-strict evidence wins
// over text signatures when both are present (task 1.2).
func TestRuleMatrix_protocolStrictPrecedence(t *testing.T) {
	t.Parallel()
	call := textCall("CONTEXT CHECKPOINT COMPACTION\n<conversation>", 0)
	call.Invocation.Operation = aipapi.OperationContextCompaction

	d := testDetector(t)
	evs := d.RequestOpened(reqMeta("tr-precedence"), call)
	if len(evs) != 1 {
		t.Fatalf("events=%v", evs)
	}
	if evs[0].RuleID != "protocol.context_compaction.v1" || evs[0].Evidence != compaction.EvidenceProtocolStrict {
		t.Fatalf("protocol precedence violated: %+v", evs[0])
	}
}

// TestRuleMatrix_nearMissNegatives proves ordinary summarization, no-tools
// calls, and partial marker overlap never match (requirement 4.2).
func TestRuleMatrix_nearMissNegatives(t *testing.T) {
	t.Parallel()
	negatives := []string{
		"Please summarize this conversation.",
		"Can you condense my notes?",
		"Summarize the key points for the team.",
		"<conversation>",
		"CONTEXT CHECKPOINT",   // partial codex marker
		"Objective: fix tests", // partial kilocode template
		"Work State: wip",
		"# USER\n# ASSISTANT", // aider tags without summarize
		"TEXT ONLY\nno compaction mentioned",
		"state snapshot" + " (nothing else)",
		"Session summary: nothing preserved",
	}
	for i, text := range negatives {
		t.Run(fmt.Sprintf("case_%02d", i), func(t *testing.T) {
			t.Parallel()
			d := testDetector(t)
			evs := d.RequestOpened(reqMeta(fmt.Sprintf("tr-neg-%d", i)), textCall(text, 0))
			if len(evs) != 0 {
				t.Fatalf("near-miss %q matched: %+v", text, evs)
			}
		})
	}
}

// TestRuleMatrix_noToolsCallsDoNotMatchGenericSummarize proves a no-tools call
// alone never matches the claude rule without its distinctive preamble
// (requirement 4.2).
func TestRuleMatrix_noToolsCallsDoNotMatchGenericSummarize(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	call := textCall("Please summarize the conversation for me.", 0) // no tools
	evs := d.RequestOpened(reqMeta("tr-notools"), call)
	if len(evs) != 0 {
		t.Fatalf("no-tools generic summarize matched: %+v", evs)
	}
}

// TestRuleMatrix_completionMarkers proves post-marker rules complete the
// matching transaction exactly once with signature-strict evidence
// (requirements 4.7, 6.4).
func TestRuleMatrix_completionMarkers(t *testing.T) {
	t.Parallel()
	completions := []struct {
		name     string
		start    func() aipapi.Call
		starts   int // expected started events from the start call
		released func() []aipapi.Event
		ruleID   string
	}{
		{
			name:   "codex handoff prefix",
			start:  func() aipapi.Call { return textCall("CONTEXT CHECKPOINT COMPACTION\ncompact", 0) },
			starts: 1,
			released: func() []aipapi.Event {
				return []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "CONTEXT CHECKPOINT COMPACTION\n<handoff>"}}
			},
			ruleID: "codex.local_checkpoint.v1",
		},
		{
			name:   "pi/openclaw summary carrier",
			start:  func() aipapi.Call { return textCall("<conversation>\nSummarize with checkpoint.", 0) },
			starts: 1,
			released: func() []aipapi.Event {
				return []aipapi.Event{assistantItem("<summary>compacted context</summary>")}
			},
			ruleID: "pi_openclaw.compaction_summary.v1",
		},
		{
			name:   "cline agentic context summary",
			start:  func() aipapi.Call { return textCall("Continuation-note: summarize.", 0) },
			starts: 1,
			released: func() []aipapi.Event {
				return []aipapi.Event{assistantItem("Context summary:\n- details")}
			},
			ruleID: "cline.agentic_compaction.v1",
		},
		{
			name:   "cline basic compaction notice",
			start:  func() aipapi.Call { return textCall("ordinary request", 0) },
			starts: 0,
			released: func() []aipapi.Event {
				return []aipapi.Event{assistantItem("<SYSTEM_NOTICE> Earlier context was compacted. Continue from here.")}
			},
			ruleID: "cline.basic_compaction_post.v1",
		},
		{
			name:   "hermes reference-only marker",
			start:  func() aipapi.Call { return textCall("ordinary request", 0) },
			starts: 0,
			released: func() []aipapi.Event {
				return []aipapi.Event{assistantItem("[CONTEXT COMPACTION \u2014 REFERENCE ONLY]")}
			},
			ruleID: "hermes.local_compaction_post.v1",
		},
		{
			name:   "hermes legacy summary",
			start:  func() aipapi.Call { return textCall("ordinary request", 0) },
			starts: 0,
			released: func() []aipapi.Event {
				return []aipapi.Event{assistantItem("[CONTEXT SUMMARY]: the prior context")}
			},
			ruleID: "hermes.legacy_compaction_post.v1",
		},
		{
			name:   "claude continuation prefix",
			start:  func() aipapi.Call { return textCall("TEXT ONLY\ncompaction of conversation", 0) },
			starts: 1,
			released: func() []aipapi.Event {
				return []aipapi.Event{assistantItem("continuation from previous conversation")}
			},
			ruleID: "claude_code_2026_03.compaction.v1",
		},
		{
			name:   "aider previous-conversation prefix",
			start:  func() aipapi.Call { return textCall("# USER\n# ASSISTANT\nsummarize", 0) },
			starts: 1,
			released: func() []aipapi.Event {
				return []aipapi.Event{assistantItem("This is a previous conversation. It continues here.")}
			},
			ruleID: "aider.chat_summary.v1",
		},
	}
	for _, tc := range completions {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := testDetector(t)
			if evs := d.RequestOpened(reqMeta("tr-c"), tc.start()); len(evs) != tc.starts {
				t.Fatalf("start events=%v want %d", evs, tc.starts)
			}
			completed := 0
			for i, ev := range tc.released() {
				got := d.ResponseReleased(resMeta("tr-c"), ev)
				if len(got) > 1 {
					t.Fatalf("release %d produced %d events: %+v", i, len(got), got)
				}
				if len(got) == 1 {
					completed++
					if got[0].Phase != compaction.PhaseCompleted {
						t.Fatalf("phase=%q want completed", got[0].Phase)
					}
					if got[0].RuleID != tc.ruleID {
						t.Fatalf("RuleID=%q want %q", got[0].RuleID, tc.ruleID)
					}
					if got[0].Evidence != compaction.EvidenceSignatureStrict {
						t.Fatalf("evidence=%q want signature_strict", got[0].Evidence)
					}
				}
			}
			if completed != 1 {
				t.Fatalf("completed events=%d want exactly 1", completed)
			}
		})
	}
}

// TestRuleMatrix_completionOnlyNeverStarts proves completion-only rules emit
// completed without any start (requirements 1.5, 6.1).
func TestRuleMatrix_completionOnlyNeverStarts(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	evs := d.RequestOpened(reqMeta("tr-co"), textCall("[CONTEXT SUMMARY]: installed", 0))
	if len(evs) != 0 {
		t.Fatalf("completion-only request must not start: %+v", evs)
	}
	got := d.ResponseReleased(resMeta("tr-co"), assistantItem("[CONTEXT SUMMARY]: compacted"))
	if len(got) != 1 || got[0].Phase != compaction.PhaseCompleted || got[0].RuleID != "hermes.legacy_compaction_post.v1" {
		t.Fatalf("completion-only completed event wrong: %+v", got)
	}
	// A second identical post marker within the same request emits once.
	if got2 := d.ResponseReleased(resMeta("tr-co"), assistantItem("[CONTEXT SUMMARY]: again")); len(got2) != 0 {
		t.Fatalf("repeat marker duplicated completion: %+v", got2)
	}
}

// TestRuleMatrix_protocolItemCompletion proves a released compaction item
// completes with protocol-strict evidence and deduplicates (requirements 3.3,
// 6.4).
func TestRuleMatrix_protocolItemCompletion(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	evs := d.RequestOpened(reqMeta("tr-pi"), textCall("compact now", 0))
	if len(evs) != 0 {
		t.Fatalf("ordinary request must not start: %+v", evs)
	}
	itemEv := aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
		Kind: aipapi.ItemKindCompaction, Status: aipapi.ItemStatusCompleted,
		Compaction: &aipapi.CompactionItem{EncapsulatedID: "enc-1"},
	}}
	got := d.ResponseReleased(resMeta("tr-pi"), itemEv)
	if len(got) != 1 || got[0].Phase != compaction.PhaseCompleted ||
		got[0].RuleID != "protocol.context_compaction.v1" || got[0].Evidence != compaction.EvidenceProtocolStrict {
		t.Fatalf("compaction item completion wrong: %+v", got)
	}
	if got2 := d.ResponseReleased(resMeta("tr-pi"), itemEv); len(got2) != 0 {
		t.Fatalf("repeat compaction item duplicated completion: %+v", got2)
	}
}

// TestRuleMatrix_protocolTerminalCompletion proves a successful terminal of an
// explicit compact operation completes once when no compaction item did
// (requirement 3.4).
func TestRuleMatrix_protocolTerminalCompletion(t *testing.T) {
	t.Parallel()
	call := textCall("compact", 0)
	call.Invocation.Operation = aipapi.OperationContextCompaction
	d := testDetector(t)
	if evs := d.RequestOpened(reqMeta("tr-term"), call); len(evs) != 1 {
		t.Fatalf("start events=%v", evs)
	}
	finish := aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "stop"}
	got := d.ResponseReleased(resMeta("tr-term"), finish)
	if len(got) != 1 || got[0].Phase != compaction.PhaseCompleted || got[0].Evidence != compaction.EvidenceProtocolStrict {
		t.Fatalf("terminal completion wrong: %+v", got)
	}
	if got2 := d.ResponseReleased(resMeta("tr-term"), finish); len(got2) != 0 {
		t.Fatalf("repeat terminal duplicated completion: %+v", got2)
	}
}

// TestRuleMatrix_protocolTerminalRequiresSuccess proves only a successful
// terminal of an explicit compact operation completes the protocol transaction
// (requirement 3.4): an authoritative incomplete status, legacy truncation,
// cancellation, a proxy recovery artifact, or any unknown/empty finish reason
// never fabricate completion (the terminal fails closed).
func TestRuleMatrix_protocolTerminalRequiresSuccess(t *testing.T) {
	t.Parallel()
	start := func(t *testing.T, d *Detector) { // opens a protocol transaction
		t.Helper()
		call := textCall("compact", 0)
		call.Invocation.Operation = aipapi.OperationContextCompaction
		if evs := d.RequestOpened(reqMeta("tr-fin"), call); len(evs) != 1 || evs[0].RuleID != "protocol.context_compaction.v1" {
			t.Fatalf("start events=%v", evs)
		}
	}

	t.Run("incomplete status blocked", func(t *testing.T) {
		t.Parallel()
		d := testDetector(t)
		start(t, d)
		fin := aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "incomplete", FinishReason: "length"}
		if got := d.ResponseReleased(resMeta("tr-fin"), fin); len(got) != 0 {
			t.Fatalf("incomplete terminal emitted completion: %+v", got)
		}
	})

	t.Run("legacy truncation finish blocked", func(t *testing.T) {
		t.Parallel()
		d := testDetector(t)
		start(t, d)
		for _, reason := range []string{"length", "max_tokens"} {
			fin := aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: reason}
			if got := d.ResponseReleased(resMeta("tr-fin"), fin); len(got) != 0 {
				t.Fatalf("truncation finish %q emitted completion: %+v", reason, got)
			}
		}
	})

	t.Run("cancellation finish blocked", func(t *testing.T) {
		t.Parallel()
		d := testDetector(t)
		start(t, d)
		fin := aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "cancelled"}
		if got := d.ResponseReleased(resMeta("tr-fin"), fin); len(got) != 0 {
			t.Fatalf("cancelled terminal emitted completion: %+v", got)
		}
	})

	t.Run("proxy recovery finish blocked", func(t *testing.T) {
		t.Parallel()
		d := testDetector(t)
		start(t, d)
		fin := aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "proxy_stream_recovered"}
		if got := d.ResponseReleased(resMeta("tr-fin"), fin); len(got) != 0 {
			t.Fatalf("proxy recovery terminal emitted completion: %+v", got)
		}
	})

	t.Run("authoritative completed status wins over ambiguous reason", func(t *testing.T) {
		t.Parallel()
		d := testDetector(t)
		start(t, d)
		fin := aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed", FinishReason: "content_filter"}
		got := d.ResponseReleased(resMeta("tr-fin"), fin)
		if len(got) != 1 || got[0].Phase != compaction.PhaseCompleted || got[0].Evidence != compaction.EvidenceProtocolStrict {
			t.Fatalf("authoritative completed terminal wrong: %+v", got)
		}
	})

	t.Run("unknown finish reason blocked", func(t *testing.T) {
		t.Parallel()
		d := testDetector(t)
		start(t, d)
		// Legacy inference fails closed: without an authoritative ResponseStatus,
		// ambiguous, unknown, and empty finish reasons never fabricate a
		// completion.
		for _, reason := range []string{"content_filter", "tool_calls", "stop_marker", ""} {
			fin := aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: reason}
			if got := d.ResponseReleased(resMeta("tr-fin"), fin); len(got) != 0 {
				t.Fatalf("unknown finish reason %q emitted completion: %+v", reason, got)
			}
		}
	})

	t.Run("canonical end_turn finish completes", func(t *testing.T) {
		t.Parallel()
		d := testDetector(t)
		start(t, d)
		fin := aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "end_turn"}
		got := d.ResponseReleased(resMeta("tr-fin"), fin)
		if len(got) != 1 || got[0].Phase != compaction.PhaseCompleted || got[0].Evidence != compaction.EvidenceProtocolStrict {
			t.Fatalf("end_turn terminal wrong: %+v", got)
		}
	})

	t.Run("blocked terminal does not suppress later compaction", func(t *testing.T) {
		t.Parallel()
		d := testDetector(t)
		start(t, d)
		// The uncompleted transaction survives the blocked terminal; an ordinary
		// request closes it silently, and the next explicit compact starts fresh.
		if evs := d.RequestOpened(reqMeta("tr-fin2"), textCall("ordinary turn", 0)); len(evs) != 0 {
			t.Fatalf("ordinary request emitted: %+v", evs)
		}
		call := textCall("compact", 0)
		call.Invocation.Operation = aipapi.OperationContextCompaction
		evs := d.RequestOpened(reqMeta("tr-fin3"), call)
		if len(evs) != 1 || evs[0].Phase != compaction.PhaseStarted {
			t.Fatalf("fresh compact start after blocked terminal missing: %+v", evs)
		}
	})
}

// TestRuleMatrix_completionMarkerSplitAcrossDeltas proves signature post
// markers are matched against the accumulated released text, not single
// deltas (F1; requirement 4.7): a marker split across streamed deltas still
// completes the transaction exactly once.
func TestRuleMatrix_completionMarkerSplitAcrossDeltas(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	start := d.RequestOpened(reqMeta("tr-split"), textCall("CONTEXT CHECKPOINT COMPACTION\ncompact", 0))
	if len(start) != 1 {
		t.Fatalf("start events=%v", start)
	}
	deltas := []string{"CONTEXT CHECK", "POINT COMPACT", "ION\n<handoff>"}
	completed := 0
	for i, delta := range deltas {
		got := d.ResponseReleased(resMeta("tr-split"), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: delta})
		if len(got) > 1 {
			t.Fatalf("delta %d produced %d events: %+v", i, len(got), got)
		}
		if len(got) == 1 {
			completed++
			if got[0].Phase != compaction.PhaseCompleted || got[0].RuleID != "codex.local_checkpoint.v1" {
				t.Fatalf("delta %d completion wrong: %+v", i, got[0])
			}
		}
	}
	if completed != 1 {
		t.Fatalf("completed events=%d want exactly 1", completed)
	}
}

// TestRuleMatrix_completionOnlySplitAcrossDeltas proves a completion-only
// post marker streamed as multiple deltas completes without any start and
// exactly once.
func TestRuleMatrix_completionOnlySplitAcrossDeltas(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	if evs := d.RequestOpened(reqMeta("tr-co-split"), textCall("ordinary turn", 0)); len(evs) != 0 {
		t.Fatalf("ordinary request must not start: %+v", evs)
	}
	if got := d.ResponseReleased(resMeta("tr-co-split"), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "[CONTEXT SUMM"}); len(got) != 0 {
		t.Fatalf("partial marker completed early: %+v", got)
	}
	got := d.ResponseReleased(resMeta("tr-co-split"), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ARY]: compacted"})
	if len(got) != 1 || got[0].Phase != compaction.PhaseCompleted || got[0].RuleID != "hermes.legacy_compaction_post.v1" {
		t.Fatalf("split completion-only marker wrong: %+v", got)
	}
	if got2 := d.ResponseReleased(resMeta("tr-co-split"), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: " more text"}); len(got2) != 0 {
		t.Fatalf("repeat marker duplicated completion: %+v", got2)
	}
}

// TestRuleMatrix_markerAtStartOfLargeDelta proves matching runs against the
// full new chunk plus the retained window before trimming, so a marker at the
// start of a large delta is still recognized, and the retained window is
// discarded once the transaction completes (requirement 7.3).
func TestRuleMatrix_markerAtStartOfLargeDelta(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	start := d.RequestOpened(reqMeta("tr-big"), textCall("CONTEXT CHECKPOINT COMPACTION\ncompact", 0))
	if len(start) != 1 {
		t.Fatalf("start events=%v", start)
	}
	big := "CONTEXT CHECKPOINT COMPACTION\n" + strings.Repeat("x", 4096)
	got := d.ResponseReleased(resMeta("tr-big"), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: big})
	if len(got) != 1 || got[0].Phase != compaction.PhaseCompleted {
		t.Fatalf("marker at start of large delta missed: %+v", got)
	}
	d.mu.Lock()
	windowLen := d.legs["a-leg-1"].releaseText.Len()
	d.mu.Unlock()
	if windowLen != 0 {
		t.Fatalf("release text retained after completion: %d chars", windowLen)
	}
}

// TestRuleMatrix_releaseWindowBounded proves the retained release-text window
// never exceeds the cap when no completion fires (requirement 7.3).
func TestRuleMatrix_releaseWindowBounded(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	for i := range 20 {
		got := d.ResponseReleased(resMeta("tr-w"), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: strings.Repeat(fmt.Sprintf("delta-%d-", i), 100)})
		if len(got) != 0 {
			t.Fatalf("release %d emitted: %+v", i, got)
		}
	}
	d.mu.Lock()
	windowLen := d.legs["a-leg-1"].releaseText.Len()
	d.mu.Unlock()
	if windowLen == 0 || windowLen > releaseTextWindow {
		t.Fatalf("release window size=%d want 0 < n <= %d", windowLen, releaseTextWindow)
	}
}

func assistantItem(text string) aipapi.Event {
	return aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Role:    aipapi.RoleAssistant,
		Status:  aipapi.ItemStatusCompleted,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: text}},
	}}
}
