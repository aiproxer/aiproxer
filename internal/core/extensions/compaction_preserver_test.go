package extensions_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

type preservingCallback struct {
	id       string
	before   func(*aipapi.Call) error
	opened   func(aipapi.Call, []compaction.Event) error
	response func(*aipapi.Event) error
}

func (p preservingCallback) ID() string { return p.id }

func (p preservingCallback) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p preservingCallback) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p preservingCallback) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type requestPreserver struct{ preservingCallback }

func (p requestPreserver) BeforeRequest(_ context.Context, call *aipapi.Call, _ compaction.RequestPreview, _ compaction.PreservationMeta, _ compaction.Services) error {
	if p.before != nil {
		return p.before(call)
	}
	return nil
}

type openedPreserver struct{ preservingCallback }

func (p openedPreserver) RequestOpened(_ context.Context, call aipapi.Call, events []compaction.Event, _ compaction.PreservationMeta, _ compaction.Services) error {
	if p.opened != nil {
		return p.opened(call, events)
	}
	return nil
}

type responsePreserver struct{ preservingCallback }

func (p responsePreserver) BeforeResponseRelease(_ context.Context, ev *aipapi.Event, _ compaction.ResponsePreview, _ compaction.PreservationMeta, _ compaction.Services) error {
	if p.response != nil {
		return p.response(ev)
	}
	return nil
}

type idCountingPreserver struct{ idCalls int }

func (p *idCountingPreserver) ID() string {
	p.idCalls++
	return "counting"
}

func (*idCountingPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (*idCountingPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (*idCountingPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func preservationCall() *aipapi.Call {
	return &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}}}
}

func TestRunCompactionPreserverBeforeRequest_orderedTransactionalFailOpen(t *testing.T) {
	t.Parallel()
	call := preservationCall()
	order := make([]string, 0, 4)
	ps := []compaction.Preserver{
		requestPreserver{preservingCallback{id: "one", before: func(c *aipapi.Call) error {
			order = append(order, "one")
			c.Messages[0].Parts[0].Text += "-one"
			return nil
		}}},
		requestPreserver{preservingCallback{id: "error", before: func(c *aipapi.Call) error {
			order = append(order, "error")
			c.Messages[0].Parts[0].Text += "-error"
			return errors.New("preserver failed")
		}}},
		requestPreserver{preservingCallback{id: "panic", before: func(c *aipapi.Call) error {
			order = append(order, "panic")
			c.Messages[0].Parts[0].Text += "-panic"
			panic("preserver panic")
		}}},
		requestPreserver{preservingCallback{id: "last", before: func(c *aipapi.Call) error {
			order = append(order, "last")
			c.Messages[0].Parts[0].Text += "-last"
			return nil
		}}},
	}
	if err := extensions.RunCompactionPreserverBeforeRequest(context.Background(), nil, nil, ps, call, compaction.RequestPreview{}, compaction.PreservationMeta{}, compaction.Services{}); err != nil {
		t.Fatal("preserver failures must be fail-open", err)
	}
	if !reflect.DeepEqual(order, []string{"one", "error", "panic", "last"}) {
		t.Fatalf("order=%v", order)
	}
	if got := call.Messages[0].Parts[0].Text; got != "hi-one-last" {
		t.Fatalf("call text=%q; failed callbacks must roll back", got)
	}
}

func TestRunCompactionPreserverBeforeRequest_invalidMutationRestoresExactCall(t *testing.T) {
	t.Parallel()
	call := preservationCall()
	prior := aipapi.CloneCall(*call)
	ps := []compaction.Preserver{
		requestPreserver{preservingCallback{id: "invalid", before: func(c *aipapi.Call) error {
			c.Messages = nil
			c.Items = []aipapi.Item{}
			return nil
		}}},
		requestPreserver{preservingCallback{id: "ok", before: func(c *aipapi.Call) error {
			c.Messages[0].Parts[0].Text += "-ok"
			return nil
		}}},
	}
	if err := extensions.RunCompactionPreserverBeforeRequest(context.Background(), nil, nil, ps, call, compaction.RequestPreview{}, compaction.PreservationMeta{}, compaction.Services{}); err != nil {
		t.Fatal(err)
	}
	prior.Messages[0].Parts[0].Text += "-ok"
	if !reflect.DeepEqual(*call, prior) {
		t.Fatalf("call=%#v want exact restored baseline then successful mutation %#v", *call, prior)
	}
}

func TestRunCompactionPreserverBeforeRequest_preservesEmptyPresenceOnRollback(t *testing.T) {
	t.Parallel()
	call := preservationCall()
	call.Tools = []aipapi.ToolDef{}
	call.Extensions = map[string]json.RawMessage{}
	call.Messages[0].Parts[0].Content = json.RawMessage{}
	original := aipapi.CloneCall(*call)
	original.Messages[0].Parts[0].Content = json.RawMessage{}
	original.Tools = []aipapi.ToolDef{}
	original.Extensions = map[string]json.RawMessage{}
	if err := extensions.RunCompactionPreserverBeforeRequest(context.Background(), nil, nil, []compaction.Preserver{
		requestPreserver{preservingCallback{id: "error", before: func(c *aipapi.Call) error {
			c.Tools = nil
			c.Extensions = nil
			c.Messages[0].Parts[0].Content = nil
			return errors.New("rollback")
		}}},
	}, call, compaction.RequestPreview{}, compaction.PreservationMeta{}, compaction.Services{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*call, original) {
		t.Fatalf("rollback changed nil/empty presence: got=%#v want=%#v", *call, original)
	}
}

func TestRunCompactionPreserver_IDEvaluatedOncePerStageIteration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := &idCountingPreserver{}
	if err := extensions.RunCompactionPreserverBeforeRequest(ctx, nil, nil, []compaction.Preserver{p}, preservationCall(), compaction.RequestPreview{}, compaction.PreservationMeta{}, compaction.Services{}); err != nil {
		t.Fatal(err)
	}
	if p.idCalls != 1 {
		t.Fatalf("BeforeRequest ID calls=%d want 1", p.idCalls)
	}

	p.idCalls = 0
	if err := extensions.RunCompactionPreserverRequestOpened(ctx, nil, nil, []compaction.Preserver{p}, *preservationCall(), nil, compaction.PreservationMeta{}, compaction.Services{}); err != nil {
		t.Fatal(err)
	}
	if p.idCalls != 1 {
		t.Fatalf("RequestOpened ID calls=%d want 1", p.idCalls)
	}

	p.idCalls = 0
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"}
	if err := extensions.RunCompactionPreserverBeforeResponseRelease(ctx, nil, nil, []compaction.Preserver{p}, ev, compaction.ResponsePreview{}, compaction.PreservationMeta{}, compaction.Services{}); err != nil {
		t.Fatal(err)
	}
	if p.idCalls != 1 {
		t.Fatalf("BeforeResponseRelease ID calls=%d want 1", p.idCalls)
	}
}

func TestRunCompactionPreserverRequestOpened_isolatedAndFailOpen(t *testing.T) {
	t.Parallel()
	call := preservationCall()
	original := aipapi.CloneCall(*call)
	events := []compaction.Event{{Phase: compaction.PhaseStarted, TransactionID: "tx"}}
	seen := make([]string, 0, 2)
	ps := []compaction.Preserver{
		openedPreserver{preservingCallback{id: "mutator", opened: func(c aipapi.Call, evs []compaction.Event) error {
			seen = append(seen, c.Messages[0].Parts[0].Text+":"+evs[0].TransactionID)
			c.Messages[0].Parts[0].Text = "mutated"
			evs[0].TransactionID = "changed"
			return errors.New("ignored")
		}}},
		openedPreserver{preservingCallback{id: "next", opened: func(c aipapi.Call, evs []compaction.Event) error {
			seen = append(seen, c.Messages[0].Parts[0].Text+":"+evs[0].TransactionID)
			return nil
		}}},
	}
	if err := extensions.RunCompactionPreserverRequestOpened(context.Background(), nil, nil, ps, *call, events, compaction.PreservationMeta{}, compaction.Services{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, []string{"hi:tx", "hi:tx"}) {
		t.Fatalf("callbacks must receive isolated content copies: %v", seen)
	}
	if !reflect.DeepEqual(*call, original) || events[0].TransactionID != "tx" {
		t.Fatal("RequestOpened callback must not mutate primary content")
	}
}

func TestRunCompactionPreserverBeforeResponseRelease_transactional(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hi"}
	ps := []compaction.Preserver{
		responsePreserver{preservingCallback{id: "error", response: func(e *aipapi.Event) error {
			e.Delta += "-error"
			return errors.New("ignored")
		}}},
		responsePreserver{preservingCallback{id: "panic", response: func(e *aipapi.Event) error {
			e.Delta += "-panic"
			panic("ignored")
		}}},
		responsePreserver{preservingCallback{id: "ok", response: func(e *aipapi.Event) error {
			e.Delta += "-ok"
			return nil
		}}},
	}
	if err := extensions.RunCompactionPreserverBeforeResponseRelease(context.Background(), nil, nil, ps, ev, compaction.ResponsePreview{}, compaction.PreservationMeta{}, compaction.Services{}); err != nil {
		t.Fatal(err)
	}
	if ev.Delta != "hi-ok" {
		t.Fatalf("event delta=%q; failed callbacks must roll back", ev.Delta)
	}
}

func TestRunCompactionPreserverBeforeResponseRelease_invalidMutationRestores(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "hi"}
	ps := []compaction.Preserver{
		responsePreserver{preservingCallback{id: "invalid", response: func(e *aipapi.Event) error {
			e.Kind = aipapi.EventReasoningPart
			e.Reasoning = nil
			return nil
		}}},
		responsePreserver{preservingCallback{id: "ok", response: func(e *aipapi.Event) error {
			e.Delta += "-ok"
			return nil
		}}},
	}
	if err := extensions.RunCompactionPreserverBeforeResponseRelease(context.Background(), nil, nil, ps, ev, compaction.ResponsePreview{}, compaction.PreservationMeta{}, compaction.Services{}); err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventTextDelta || ev.Delta != "hi-ok" {
		t.Fatalf("event=%#v; invalid callback must roll back", *ev)
	}
}

func TestRunCompactionPreserverBeforeResponseRelease_nestedItemRollbackPreservesCarriers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		build  func() aipapi.Event
		mutate func(*aipapi.Event)
	}{
		{
			name: "compaction opaque bytes",
			build: func() aipapi.Event {
				return aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
					Kind:       aipapi.ItemKindCompaction,
					Compaction: &aipapi.CompactionItem{Opaque: json.RawMessage(`"opaque-bytes"`)},
				}}
			},
			mutate: func(ev *aipapi.Event) {
				ev.Item.Compaction.Opaque[0] = 'x'
				ev.Item.Compaction.Opaque = nil
			},
		},
		{
			name: "message content presence and bytes",
			build: func() aipapi.Event {
				return aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
					Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser,
					Content: []aipapi.ContentPart{{
						Kind:       aipapi.ContentPartAnnotation,
						Annotation: &aipapi.AnnotationPart{Type: "note", Data: json.RawMessage{}},
					}},
				}}
			},
			mutate: func(ev *aipapi.Event) {
				ev.Item.Content[0].Annotation.Data = json.RawMessage(`{"changed":true}`)
				ev.Item.Content = nil
			},
		},
		{
			name: "tool result empty parts and output bytes",
			build: func() aipapi.Event {
				return aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
					Kind:       aipapi.ItemKindToolResult,
					ToolResult: &aipapi.ToolResultItem{CallID: "call-1", Name: "tool", Output: "result-bytes", Parts: []aipapi.ContentPart{}},
				}}
			},
			mutate: func(ev *aipapi.Event) {
				ev.Item.ToolResult.Output = "changed"
				ev.Item.ToolResult.Parts = nil
			},
		},
		{
			name: "tool call arguments bytes and presence",
			build: func() aipapi.Event {
				return aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
					Kind:     aipapi.ItemKindToolCall,
					ToolCall: &aipapi.ToolCallItem{CallID: "call-1", Name: "tool", Arguments: json.RawMessage{}},
				}}
			},
			mutate: func(ev *aipapi.Event) {
				ev.Item.ToolCall.Arguments = json.RawMessage(`{"changed":true}`)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.build()
			want := tt.build()
			p := responsePreserver{preservingCallback{id: "nested", response: func(ev *aipapi.Event) error {
				tt.mutate(ev)
				return errors.New("rollback")
			}}}
			if err := extensions.RunCompactionPreserverBeforeResponseRelease(context.Background(), nil, nil, []compaction.Preserver{p}, &got, compaction.ResponsePreview{}, compaction.PreservationMeta{}, compaction.Services{}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("rollback changed nested carrier: got=%#v want=%#v", got, want)
			}
		})
	}
}
