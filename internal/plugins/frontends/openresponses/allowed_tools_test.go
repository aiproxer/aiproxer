package openresponses

import (
	"context"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestAllowedToolsStream_suppressesForbiddenToolCallLifecycle(t *testing.T) {
	call := &aipapi.Call{
		Tools: []aipapi.ToolDef{{Name: "forbidden_fn"}, {Name: "allowed_fn"}},
		ToolChoice: aipapi.ToolChoice{
			Mode:         aipapi.ToolChoiceAuto,
			AllowedTools: []string{"allowed_fn"},
		},
	}
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_bad", ToolName: "forbidden_fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_bad", Delta: `{"q":"leak"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_bad"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_good", ToolName: "allowed_fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_good", Delta: `{"q":"ok"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_good"},
		{Kind: aipapi.EventResponseFinished},
	}
	stream := newAllowedToolsStream(call, aipapi.NewFixedEventStream(events))

	var got []aipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv failed: %v", err)
		}
		got = append(got, ev)
	}

	for _, ev := range got {
		switch ev.Kind {
		case aipapi.EventToolCallStarted:
			if ev.ToolName == "forbidden_fn" {
				t.Fatalf("forbidden tool call started leaked: %+v", ev)
			}
		case aipapi.EventToolCallArgsDelta:
			if ev.ToolCallID == "call_bad" {
				t.Fatalf("forbidden tool call args leaked: %+v", ev)
			}
		case aipapi.EventToolCallFinished:
			if ev.ToolCallID == "call_bad" {
				t.Fatalf("forbidden tool call finished leaked: %+v", ev)
			}
		}
	}

	sawAllowed := false
	for _, ev := range got {
		if ev.Kind == aipapi.EventToolCallStarted && ev.ToolName == "allowed_fn" {
			sawAllowed = true
		}
	}
	if !sawAllowed {
		t.Fatalf("allowed tool call was dropped: %+v", got)
	}
	if len(got) != 6 {
		t.Fatalf("expected 6 events after suppression (start, msg, allowed started/delta/finished, finished), got %d: %+v", len(got), got)
	}
}

func TestAllowedToolsStream_clearsSuppressedCallOnFinish(t *testing.T) {
	call := &aipapi.Call{
		Tools: []aipapi.ToolDef{{Name: "a"}, {Name: "b"}},
		ToolChoice: aipapi.ToolChoice{
			Mode:         aipapi.ToolChoiceAuto,
			AllowedTools: []string{"a"},
		},
	}
	// Two parallel forbidden calls plus an allowed call; the suppression state
	// must be keyed per call ID so one finished forbidden call does not leak
	// into the other.
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "bad1", ToolName: "b"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "bad1", Delta: "1"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "bad1"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "bad2", ToolName: "b"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "bad2", Delta: "2"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "bad2"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "good", ToolName: "a"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "good", Delta: "3"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "good"},
		{Kind: aipapi.EventResponseFinished},
	}
	stream := newAllowedToolsStream(call, aipapi.NewFixedEventStream(events))

	seen := map[string]bool{}
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Recv failed: %v", err)
		}
		if ev.Kind == aipapi.EventToolCallStarted {
			seen[ev.ToolCallID] = true
		}
		if ev.Kind == aipapi.EventToolCallArgsDelta && ev.ToolCallID != "good" {
			t.Fatalf("forbidden args leaked: %+v", ev)
		}
	}
	if seen["bad1"] || seen["bad2"] {
		t.Fatalf("forbidden starts leaked: %v", seen)
	}
	if !seen["good"] {
		t.Fatalf("allowed start missing: %v", seen)
	}
}

func TestAllowedToolsStream_withoutSubsetPassesThrough(t *testing.T) {
	call := &aipapi.Call{Tools: []aipapi.ToolDef{{Name: "any_fn"}}, ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto}}
	raw := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "c", ToolName: "any_fn"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "c"},
	})
	stream := newAllowedToolsStream(call, raw)
	if stream != raw {
		t.Fatalf("expected identity passthrough when no subset is configured")
	}
}

func TestAllowedToolsStream_modeNoneSuppressesAllToolCalls(t *testing.T) {
	// allowed_tools mode none is legal (the subset is vacuous) but the model
	// must never call a tool: even subset members must be fully suppressed.
	call := &aipapi.Call{
		Tools: []aipapi.ToolDef{{Name: "forbidden_fn"}, {Name: "allowed_fn"}},
		ToolChoice: aipapi.ToolChoice{
			Mode:         aipapi.ToolChoiceNone,
			AllowedTools: []string{"allowed_fn"},
		},
	}
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_bad", ToolName: "forbidden_fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_bad", Delta: `{"q":"leak"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_bad"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_allowed", ToolName: "allowed_fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_allowed", Delta: `{"q":"also-leak"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_allowed"},
		{Kind: aipapi.EventResponseFinished},
	}
	stream := newAllowedToolsStream(call, aipapi.NewFixedEventStream(events))
	got := drainAllowedToolsStream(t, stream)
	for _, ev := range got {
		switch ev.Kind {
		case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
			t.Fatalf("mode none leaked tool call lifecycle event: %+v", ev)
		}
	}
	if len(got) != 3 {
		t.Fatalf("expected only response/message/finish envelope (3 events), got %d: %+v", len(got), got)
	}
}

func TestAllowedToolsStream_modeNoneWithEmptySubsetSuppressesAllToolCalls(t *testing.T) {
	call := &aipapi.Call{
		Tools: []aipapi.ToolDef{{Name: "any_fn"}},
		ToolChoice: aipapi.ToolChoice{
			Mode: aipapi.ToolChoiceNone,
		},
	}
	stream := newAllowedToolsStream(call, aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_any", ToolName: "any_fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_any", Delta: "{}"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_any"},
		{Kind: aipapi.EventResponseFinished},
	}))
	got := drainAllowedToolsStream(t, stream)
	for _, ev := range got {
		switch ev.Kind {
		case aipapi.EventToolCallStarted, aipapi.EventToolCallArgsDelta, aipapi.EventToolCallFinished:
			t.Fatalf("empty-subset mode none leaked tool lifecycle event: %+v", ev)
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected response start/finish only, got %d: %+v", len(got), got)
	}
}

func TestAllowedToolsStream_emptyIDEventsFailClosedWhenInterleaved(t *testing.T) {
	// When allowed and suppressed calls interleave, an empty-ID args/finish
	// event could belong to a suppressed call. It must be dropped (fail
	// closed), and bookkeeping must stay consistent so later empty-ID events
	// are handled once no suppressed call is open.
	call := &aipapi.Call{
		Tools: []aipapi.ToolDef{{Name: "a"}, {Name: "b"}},
		ToolChoice: aipapi.ToolChoice{
			Mode:         aipapi.ToolChoiceAuto,
			AllowedTools: []string{"a"},
		},
	}
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "bad", ToolName: "b"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "good", ToolName: "a"},
		{Kind: aipapi.EventToolCallArgsDelta, Delta: "ambig"},
		{Kind: aipapi.EventToolCallFinished},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "good"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "bad"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "good2", ToolName: "a"},
		{Kind: aipapi.EventToolCallArgsDelta, Delta: "ok"},
		{Kind: aipapi.EventToolCallFinished},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "good2"},
		{Kind: aipapi.EventResponseFinished},
	}
	stream := newAllowedToolsStream(call, aipapi.NewFixedEventStream(events))
	got := drainAllowedToolsStream(t, stream)
	for _, ev := range got {
		if ev.Kind == aipapi.EventToolCallArgsDelta && ev.Delta == "ambig" {
			t.Fatalf("empty-ID args delta leaked while a suppressed call was open: %+v", got)
		}
	}
	sawOK := false
	sawGood := false
	for _, ev := range got {
		switch ev.Kind {
		case aipapi.EventToolCallArgsDelta:
			if ev.Delta == "ok" {
				sawOK = true
			}
		case aipapi.EventToolCallStarted:
			if ev.ToolCallID == "good" || ev.ToolCallID == "good2" {
				sawGood = true
			}
		}
	}
	if !sawOK {
		t.Fatalf("empty-ID args delta after suppressed call closed should pass: %+v", got)
	}
	if !sawGood {
		t.Fatalf("allowed calls were dropped: %+v", got)
	}
}

func TestAllowedToolsStream_modeRequiredForbiddenCallsStaySuppressed(t *testing.T) {
	// allowed_tools mode required (canonical any) must still keep forbidden
	// calls fully suppressed; no terminal enforcement is added beyond what the
	// filter already guarantees.
	call := &aipapi.Call{
		Tools: []aipapi.ToolDef{{Name: "allowed_fn"}, {Name: "forbidden_fn"}},
		ToolChoice: aipapi.ToolChoice{
			Mode:         aipapi.ToolChoiceAny,
			AllowedTools: []string{"allowed_fn"},
		},
	}
	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_forbidden", ToolName: "forbidden_fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_forbidden", Delta: `{"q":"leak"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_forbidden"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_allowed", ToolName: "allowed_fn"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_allowed", Delta: `{"q":"ok"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_allowed"},
		{Kind: aipapi.EventResponseFinished},
	}
	stream := newAllowedToolsStream(call, aipapi.NewFixedEventStream(events))
	got := drainAllowedToolsStream(t, stream)
	for _, ev := range got {
		switch ev.Kind {
		case aipapi.EventToolCallStarted:
			if ev.ToolName == "forbidden_fn" {
				t.Fatalf("required-mode forbidden tool call leaked: %+v", ev)
			}
		case aipapi.EventToolCallArgsDelta:
			if ev.ToolCallID == "call_forbidden" {
				t.Fatalf("required-mode forbidden args leaked: %+v", ev)
			}
		case aipapi.EventToolCallFinished:
			if ev.ToolCallID == "call_forbidden" {
				t.Fatalf("required-mode forbidden finish leaked: %+v", ev)
			}
		}
	}
	sawAllowed := false
	for _, ev := range got {
		if ev.Kind == aipapi.EventToolCallStarted && ev.ToolName == "allowed_fn" {
			sawAllowed = true
		}
	}
	if !sawAllowed {
		t.Fatalf("required-mode allowed call was dropped: %+v", got)
	}
}

func drainAllowedToolsStream(t *testing.T, stream aipapi.EventStream) []aipapi.Event {
	t.Helper()
	var got []aipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatalf("Recv failed: %v", err)
		}
		got = append(got, ev)
	}
}
