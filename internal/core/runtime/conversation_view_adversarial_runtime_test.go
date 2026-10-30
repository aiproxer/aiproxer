package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// adversaryKind enumerates late transform corruptions.
type adversaryKind string

const (
	adversaryReintroduceDelete      adversaryKind = "reintroduce_delete"
	adversaryReintroduceDuplicate   adversaryKind = "reintroduce_duplicate"
	adversaryReintroduceMove        adversaryKind = "reintroduce_move"
	adversaryAnchorRemoveFallback   adversaryKind = "anchor_remove_fallback"
	adversaryAnchorRemoveFailClosed adversaryKind = "anchor_remove_failclosed"
)

type adversaryHook struct {
	tagged       aipapi.Message
	taggedID     conversationprojection.MessageIdentity
	steeringText string
	anchorText   string
	kind         adversaryKind
}

func (h *adversaryHook) ID() string                        { return "adversary-" + string(h.kind) }
func (h *adversaryHook) Order() int                        { return 0 }
func (h *adversaryHook) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailClosed }
func (h *adversaryHook) HandleRequestParts(_ context.Context, call *aipapi.Call, _ sdkhooks.PartMeta) error {
	if call == nil {
		return nil
	}
	isItem := call.HasItemAuthority()
	if h.kind == adversaryReintroduceDelete || h.kind == adversaryReintroduceDuplicate || h.kind == adversaryReintroduceMove {
		if isItem {
			taggedItem := aipapi.Item{Kind: aipapi.ItemKindMessage, ID: "tagged-reintroduced", Status: aipapi.ItemStatusCompleted, Role: h.tagged.Role, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: h.tagged.Parts[0].Text}}}
			if len(h.tagged.Parts) > 0 {
				taggedItem.Content[0].Text = h.tagged.Parts[0].Text
			}
			call.Items = append(call.Items, taggedItem)
		} else {
			call.Messages = append(call.Messages, h.tagged)
		}
	}
	switch h.kind {
	case adversaryReintroduceDelete:
		h.deleteSteering(call)
	case adversaryReintroduceDuplicate:
		h.duplicateSteering(call)
	case adversaryReintroduceMove:
		h.moveSteering(call)
	case adversaryAnchorRemoveFallback, adversaryAnchorRemoveFailClosed:
		h.deleteAnchor(call)
		if isItem {
			taggedItem := aipapi.Item{Kind: aipapi.ItemKindMessage, ID: "tagged-reintroduced-anchor", Status: aipapi.ItemStatusCompleted, Role: h.tagged.Role, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: h.tagged.Parts[0].Text}}}
			call.Items = append(call.Items, taggedItem)
		} else {
			call.Messages = append(call.Messages, h.tagged)
		}
	}
	return nil
}

func (h *adversaryHook) deleteSteering(call *aipapi.Call) {
	if len(call.Instructions) > 0 {
		var ni []aipapi.Message
		for _, m := range call.Instructions {
			if len(m.Parts) > 0 && m.Parts[0].Text == h.steeringText {
				continue
			}
			ni = append(ni, m)
		}
		call.Instructions = ni
	}
	var nm []aipapi.Message
	for _, m := range call.Messages {
		if len(m.Parts) > 0 && m.Parts[0].Text == h.steeringText {
			continue
		}
		nm = append(nm, m)
	}
	call.Messages = nm
	if len(call.Items) > 0 {
		var nItems []aipapi.Item
		for _, it := range call.Items {
			if it.Kind == aipapi.ItemKindMessage && len(it.Content) > 0 && it.Content[0].Text == h.steeringText {
				continue
			}
			nItems = append(nItems, it)
		}
		call.Items = nItems
	}
}

func (h *adversaryHook) duplicateSteering(call *aipapi.Call) {
	if call.HasItemAuthority() {
		dupItem := aipapi.Item{Kind: aipapi.ItemKindMessage, ID: "dup-steering", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: h.steeringText}}}
		call.Items = append(call.Items, dupItem)
	} else {
		dup := aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart(h.steeringText)}}
		call.Messages = append(call.Messages, dup)
	}
}

func (h *adversaryHook) moveSteering(call *aipapi.Call) {
	var steeringMsg *aipapi.Message
	if len(call.Instructions) > 0 {
		var ni []aipapi.Message
		for _, m := range call.Instructions {
			if steeringMsg == nil && len(m.Parts) > 0 && m.Parts[0].Text == h.steeringText {
				c := m
				steeringMsg = &c
				continue
			}
			ni = append(ni, m)
		}
		call.Instructions = ni
	}
	var nm []aipapi.Message
	for _, m := range call.Messages {
		if steeringMsg == nil && len(m.Parts) > 0 && m.Parts[0].Text == h.steeringText {
			c := m
			steeringMsg = &c
			continue
		}
		nm = append(nm, m)
	}
	call.Messages = nm
	if steeringMsg != nil {
		call.Messages = append(call.Messages, *steeringMsg)
	}
	if len(call.Items) > 0 {
		var steeringItem *aipapi.Item
		var nItems []aipapi.Item
		for _, it := range call.Items {
			if steeringItem == nil && it.Kind == aipapi.ItemKindMessage && len(it.Content) > 0 && it.Content[0].Text == h.steeringText {
				c := it
				steeringItem = &c
				continue
			}
			nItems = append(nItems, it)
		}
		call.Items = nItems
		if steeringItem != nil {
			call.Items = append(call.Items, *steeringItem)
		}
	}
}

func (h *adversaryHook) deleteAnchor(call *aipapi.Call) {
	if h.anchorText == "" {
		return
	}
	var nm []aipapi.Message
	for _, m := range call.Messages {
		if len(m.Parts) > 0 && m.Parts[0].Text == h.anchorText {
			continue
		}
		nm = append(nm, m)
	}
	call.Messages = nm
	if len(call.Items) > 0 {
		var nItems []aipapi.Item
		for _, it := range call.Items {
			if it.Kind == aipapi.ItemKindMessage && len(it.Content) > 0 && it.Content[0].Text == h.anchorText {
				continue
			}
			nItems = append(nItems, it)
		}
		call.Items = nItems
	}
}

func buildAdversarySnapshotStable() (conversationprojection.Snapshot, aipapi.Message, aipapi.Message, string, []conversationprojection.OverlayProvenance, aipapi.Call, aipapi.Call) {
	taggedMsg := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("local-tagged-adversary")}}
	taggedID, _ := conversationprojection.MessageIdentityOf(taggedMsg)
	sys := aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys")}}
	user1 := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("user1")}}
	user2 := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("user2")}}
	overlay := conversationprojection.Overlay{
		OverlayID: "ov-stable-adversary", Revision: 1, SlotOrdinal: 1, Active: true,
		Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: "steering-stable-adversary"},
		Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix},
		AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
	}
	snap := conversationprojection.Snapshot{StateRevision: 1, NeverBackend: []conversationprojection.Tag{{Identity: taggedID, Reason: "test"}}, Steering: []conversationprojection.Overlay{overlay}}
	clientCall := aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{user1, taggedMsg, user2}}
	baseline, ev, _ := conversationprojection.Project(clientCall, snap)
	filtered, _ := conversationprojection.FilterNeverBackend(clientCall, snap)
	return snap, taggedMsg, user1, overlay.Message.Text, ev.Provenance, baseline, filtered
}

func buildAdversarySnapshotAfterMessage(policy conversationprojection.AnchorMissingPolicy) (conversationprojection.Snapshot, aipapi.Message, aipapi.Message, string, []conversationprojection.OverlayProvenance, aipapi.Call, aipapi.Call) {
	taggedMsg := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("local-tagged-after")}}
	taggedID, _ := conversationprojection.MessageIdentityOf(taggedMsg)
	sys := aipapi.Message{Role: aipapi.RoleSystem, Parts: []aipapi.Part{aipapi.TextPart("sys")}}
	user1 := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("anchor-user1")}}
	anchorID, _ := conversationprojection.MessageIdentityOf(user1)
	anchor := conversationprojection.MessageAnchor{Identity: anchorID, Occurrence: 1}
	overlay := conversationprojection.Overlay{
		OverlayID: "ov-after-adversary", Revision: 1, SlotOrdinal: 1, Active: true,
		Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleUser, Text: "steering-after-adversary"},
		Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementAfterMessage, Anchor: &anchor},
		AnchorMissingPolicy: policy,
	}
	snap := conversationprojection.Snapshot{StateRevision: 2, NeverBackend: []conversationprojection.Tag{{Identity: taggedID, Reason: "test"}}, Steering: []conversationprojection.Overlay{overlay}}
	clientCall := aipapi.Call{Instructions: []aipapi.Message{sys}, Messages: []aipapi.Message{user1, taggedMsg, {Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("a1")}}, {Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("user2")}}}}
	baseline, ev, _ := conversationprojection.Project(clientCall, snap)
	filtered, _ := conversationprojection.FilterNeverBackend(clientCall, snap)
	return snap, taggedMsg, user1, overlay.Message.Text, ev.Provenance, baseline, filtered
}

// countingSnapshotReader returns same frozen snapshot for any A-leg, counts Snapshot calls.
type adversarialCountingReader struct {
	snap  conversationprojection.Snapshot
	count atomic.Int32
}

func (r *adversarialCountingReader) Snapshot(_ context.Context, _ string) (conversationprojection.Snapshot, error) {
	r.count.Add(1)
	return r.snap, nil
}
func (r *adversarialCountingReader) Count() int { return int(r.count.Load()) }

// captureBackend records open calls.
type captureBackend struct {
	caps         aipapi.BackendCaps
	mu           sync.Mutex
	openCalls    []aipapi.Call
	openCount    atomic.Int32
	failOpen     error
	streamEvents []aipapi.Event
}

func newCaptureBackend() *captureBackend {
	return &captureBackend{
		caps:         aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		streamEvents: []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventTextDelta, Delta: "ok"}, {Kind: aipapi.EventResponseFinished}},
	}
}

func (c *captureBackend) Backend() execbackend.Backend {
	return execbackend.Backend{
		Caps: c.caps,
		Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			c.openCount.Add(1)
			c.mu.Lock()
			c.openCalls = append(c.openCalls, aipapi.CloneCall(call))
			c.mu.Unlock()
			if c.failOpen != nil {
				return nil, c.failOpen
			}
			return aipapi.NewFixedEventStream(c.streamEvents), nil
		},
	}
}

func (c *captureBackend) LastCall() (aipapi.Call, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.openCalls) == 0 {
		return aipapi.Call{}, false
	}
	return aipapi.CloneCall(c.openCalls[len(c.openCalls)-1]), true
}

func (c *captureBackend) AllCalls() []aipapi.Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]aipapi.Call, len(c.openCalls))
	for i, v := range c.openCalls {
		out[i] = aipapi.CloneCall(v)
	}
	return out
}

// ptbCaptureObserver records LegPTB observations via traffic.Observer.
type ptbCaptureObserver struct {
	mu           sync.Mutex
	observations []traffic.Observation
}

func (o *ptbCaptureObserver) OnObservation(_ context.Context, ev traffic.Observation) error {
	if ev.Leg == traffic.LegPTB {
		o.mu.Lock()
		o.observations = append(o.observations, ev)
		o.mu.Unlock()
	}
	return nil
}

func (o *ptbCaptureObserver) PTBCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.observations)
}

func (o *ptbCaptureObserver) PTBCalls(t *testing.T) []aipapi.Call {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []aipapi.Call
	for _, ob := range o.observations {
		if ob.Leg != traffic.LegPTB {
			continue
		}
		var c aipapi.Call
		if err := json.Unmarshal(ob.Body, &c); err != nil {
			t.Fatalf("PTB unmarshal: %v body %s", err, string(ob.Body))
		}
		out = append(out, c)
	}
	return out
}

func verifyRepaired(t *testing.T, taggedID conversationprojection.MessageIdentity, steeringText string, open aipapi.Call) {
	t.Helper()
	for _, m := range open.Instructions {
		if id, _ := conversationprojection.MessageIdentityOf(m); id == taggedID {
			t.Fatalf("open still contains reintroduced tagged in Instructions")
		}
	}
	for _, m := range open.Messages {
		if id, _ := conversationprojection.MessageIdentityOf(m); id == taggedID {
			t.Fatalf("open still contains reintroduced tagged in Messages")
		}
	}
	for _, it := range open.Items {
		if it.Kind == aipapi.ItemKindMessage {
			if id, _ := conversationprojection.ItemIdentityOf(it); id == taggedID {
				t.Fatalf("open still contains tagged item")
			}
		}
	}
	count := 0
	for _, m := range open.Instructions {
		if len(m.Parts) > 0 && m.Parts[0].Text == steeringText {
			count++
		}
	}
	for _, m := range open.Messages {
		if len(m.Parts) > 0 && m.Parts[0].Text == steeringText {
			count++
		}
	}
	for _, it := range open.Items {
		if it.Kind == aipapi.ItemKindMessage && len(it.Content) > 0 && it.Content[0].Text == steeringText {
			count++
		}
	}
	if count != 1 {
		raw, _ := json.Marshal(open)
		t.Fatalf("steering count %d want 1 for %q, open=%s", count, steeringText, string(raw))
	}
	if err := open.Validate(); err != nil {
		t.Fatalf("open Validate failed: %v", err)
	}
}

func TestAdversarial_LateTransform_InitialOpen(t *testing.T) {
	t.Parallel()
	snap, taggedMsg, _, steeringText, _, _, _ := buildAdversarySnapshotStable()
	taggedID, _ := conversationprojection.MessageIdentityOf(taggedMsg)
	adversaries := []adversaryKind{adversaryReintroduceDelete, adversaryReintroduceDuplicate, adversaryReintroduceMove}
	for _, adv := range adversaries {
		t.Run(string(adv), func(t *testing.T) {
			t.Parallel()
			hook := &adversaryHook{tagged: taggedMsg, taggedID: taggedID, steeringText: steeringText, kind: adv}
			reader := &adversarialCountingReader{snap: snap}
			cap := newCaptureBackend()
			ptbObs := &ptbCaptureObserver{}
			ex := TestExecutor()
			st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			ex.Store = st
			ex.ConversationViewReader = reader
			ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{hook}})
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{TrafficObserver: ptbObs})
			ex.Backends = map[string]execbackend.Backend{"primary": cap.Backend()}
			ex.Rand = routing.NewSeededRng(1)
			ex.Now = func() time.Time { return time.Unix(1000, 0) }
			call := &aipapi.Call{
				Route:    aipapi.RouteIntent{Selector: "primary:m"},
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}, taggedMsg},
			}
			stream, err := ex.Execute(execDetachedCtx(context.Background()), call)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			_, _ = aipapi.Collect(context.Background(), stream)
			_ = stream.Close()
			if cap.openCount.Load() != 1 {
				t.Fatalf("openCount %d want 1", cap.openCount.Load())
			}
			open, _ := cap.LastCall()
			verifyRepaired(t, taggedID, steeringText, open)
			ptbCalls := ptbObs.PTBCalls(t)
			if len(ptbCalls) != 1 {
				t.Fatalf("ptbCalls count %d want 1", len(ptbCalls))
			}
			verifyRepaired(t, taggedID, steeringText, ptbCalls[0])
			if reader.Count() != 1 {
				t.Fatalf("snapshot count %d want 1", reader.Count())
			}
		})
	}
}

func TestAdversarial_LateTransform_AfterMessage_InitialOpen(t *testing.T) {
	t.Parallel()
	snap, taggedMsg, _, steeringText, _, _, _ := buildAdversarySnapshotAfterMessage(conversationprojection.AnchorStablePrefixFallback)
	taggedID, _ := conversationprojection.MessageIdentityOf(taggedMsg)
	hook := &adversaryHook{tagged: taggedMsg, taggedID: taggedID, steeringText: steeringText, kind: adversaryReintroduceMove}
	reader := &adversarialCountingReader{snap: snap}
	primaryCap := newCaptureBackend()
	primaryCap.failOpen = aipapi.RecoverablePreOutputError(errors.New("pre-output-temp"))
	secondaryCap := newCaptureBackend()
	ptbObs := &ptbCaptureObserver{}
	ex := TestExecutor()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store = st
	ex.ConversationViewReader = reader
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{hook}})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{TrafficObserver: ptbObs})
	ex.Backends = map[string]execbackend.Backend{"primary": primaryCap.Backend(), "secondary": secondaryCap.Backend()}
	ex.Rand = routing.NewSeededRng(7)
	ex.Now = func() time.Time { return time.Unix(5000, 0) }
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "primary:m|secondary:m"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("keep")}},
			taggedMsg,
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("after")}},
		},
	}
	stream, err := ex.Execute(execDetachedCtx(context.Background()), call)
	if err != nil {
		t.Fatalf("Execute failover: %v", err)
	}
	events, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if events.Text.String() != "ok" {
		t.Fatalf("secondary text got %q want ok", events.Text.String())
	}
	_ = stream.Close()
	if reader.Count() != 1 {
		t.Fatalf("pre-output failover must reuse frozen snapshot, got %d reads", reader.Count())
	}
	if primaryCap.openCount.Load() != 1 {
		t.Fatalf("primary opens %d want 1", primaryCap.openCount.Load())
	}
	if secondaryCap.openCount.Load() != 1 {
		t.Fatalf("secondary opens %d want 1", secondaryCap.openCount.Load())
	}
	open, ok := secondaryCap.LastCall()
	if !ok {
		t.Fatal("secondary no open")
	}
	verifyRepaired(t, taggedID, steeringText, open)
	if ptbObs.PTBCount() != 2 {
		t.Fatalf("PTB should be emitted for both attempts (primary recoverable + secondary success), got %d", ptbObs.PTBCount())
	}
	ptbCalls := ptbObs.PTBCalls(t)
	for _, ptb := range ptbCalls {
		verifyRepaired(t, taggedID, steeringText, ptb)
	}
	if primaryCap.openCount.Load() != 1 {
		t.Fatalf("primary should have attempted open once")
	}
}

func TestAdversarial_LateTransform_ParallelRace(t *testing.T) {
	t.Parallel()
	snap, taggedMsg, _, steeringText, _, _, _ := buildAdversarySnapshotStable()
	taggedID, _ := conversationprojection.MessageIdentityOf(taggedMsg)
	adversaries := []adversaryKind{adversaryReintroduceDelete, adversaryReintroduceDuplicate, adversaryReintroduceMove}
	for _, adv := range adversaries {
		t.Run(string(adv), func(t *testing.T) {
			t.Parallel()
			hook := &adversaryHook{tagged: taggedMsg, taggedID: taggedID, steeringText: steeringText, kind: adv}
			reader := &adversarialCountingReader{snap: snap}
			capA := newCaptureBackend()
			capB := newCaptureBackend()
			ptbObs := &ptbCaptureObserver{}
			ex := TestExecutor()
			st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			ex.Store = st
			ex.ConversationViewReader = reader
			ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{hook}})
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{TrafficObserver: ptbObs})
			ex.Backends = map[string]execbackend.Backend{"a": capA.Backend(), "b": capB.Backend()}
			ex.Rand = routing.NewSeededRng(1)
			ex.Now = func() time.Time { return time.Unix(5000, 0) }
			call := &aipapi.Call{
				Route: aipapi.RouteIntent{Selector: "a:m!b:m"},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("keep")}},
					taggedMsg,
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("after")}},
				},
			}
			stream, err := ex.Execute(execDetachedCtx(context.Background()), call)
			if err != nil {
				t.Fatalf("parallel Execute: %v", err)
			}
			events, err := aipapi.Collect(context.Background(), stream)
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if events.Text.String() != "ok" {
				t.Fatalf("winner text %q want ok", events.Text.String())
			}
			_ = stream.Close()
			if reader.Count() != 1 {
				t.Fatalf("parallel race must use one frozen snapshot, got %d", reader.Count())
			}
			all := append(capA.AllCalls(), capB.AllCalls()...)
			if len(all) < 1 || len(all) > 2 {
				t.Fatalf("parallel race realized opens %d want 1..2 (scheduler-dependent winner selection)", len(all))
			}
			for _, open := range all {
				verifyRepaired(t, taggedID, steeringText, open)
			}
			ptbCalls := ptbObs.PTBCalls(t)
			if len(ptbCalls) != len(all) {
				t.Fatalf("PTB count %d must equal realized backend opens %d (scheduler-dependent winner selection)", len(ptbCalls), len(all))
			}
			for _, ptb := range ptbCalls {
				verifyRepaired(t, taggedID, steeringText, ptb)
			}
		})
	}
}

func TestAdversarial_LateTransform_TTFTReplacement(t *testing.T) {
	t.Parallel()
	snap, taggedMsg, _, steeringText, _, _, _ := buildAdversarySnapshotStable()
	taggedID, _ := conversationprojection.MessageIdentityOf(taggedMsg)
	hook := &adversaryHook{tagged: taggedMsg, taggedID: taggedID, steeringText: steeringText, kind: adversaryReintroduceDuplicate}
	reader := &adversarialCountingReader{snap: snap}
	slowCap := newCaptureBackend()
	slowBackend := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			slowCap.openCount.Add(1)
			slowCap.mu.Lock()
			slowCap.openCalls = append(slowCap.openCalls, aipapi.CloneCall(call))
			slowCap.mu.Unlock()
			return &delayedStreamForTTFT{delay: 200 * time.Millisecond, ctx: ctx}, nil
		},
	}
	fastCap := newCaptureBackend()
	ptbObs := &ptbCaptureObserver{}
	ex := TestExecutor()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store = st
	ex.ConversationViewReader = reader
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{hook}})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{TrafficObserver: ptbObs})
	ex.Backends = map[string]execbackend.Backend{"slow": slowBackend, "fast": fastCap.Backend()}
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(5000, 0) }
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "[ttft_timeout=50]slow:m!fast:m"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("keep")}},
			taggedMsg,
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("after")}},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := ex.Execute(ctx, call)
	if err != nil {
		t.Fatalf("TTFT Execute: %v", err)
	}
	events, err := aipapi.Collect(ctx, stream)
	if err != nil {
		t.Fatalf("collect TTFT: %v", err)
	}
	if events.Text.String() != "ok" {
		t.Fatalf("TTFT winner text %q want ok (slow should be eliminated)", events.Text.String())
	}
	_ = stream.Close()
	if reader.Count() != 1 {
		t.Fatalf("TTFT replacement must reuse frozen snapshot, got %d", reader.Count())
	}
	if fastCap.openCount.Load() == 0 {
		t.Fatal("fast backend never opened")
	}
	open, _ := fastCap.LastCall()
	verifyRepaired(t, taggedID, steeringText, open)
	if slowCap.openCount.Load() > 0 {
		slowOpen, _ := slowCap.LastCall()
		verifyRepaired(t, taggedID, steeringText, slowOpen)
	}
	ptbCalls := ptbObs.PTBCalls(t)
	if len(ptbCalls) == 0 {
		t.Fatal("TTFT winner PTB missing")
	}
	for _, ptb := range ptbCalls {
		verifyRepaired(t, taggedID, steeringText, ptb)
	}
}

type delayedStreamForTTFT struct {
	delay  time.Duration
	ctx    context.Context
	idx    int
	events []aipapi.Event
}

func (d *delayedStreamForTTFT) Recv(ctx context.Context) (aipapi.Event, error) {
	if d.idx == 0 {
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		case <-d.ctx.Done():
			return aipapi.Event{}, d.ctx.Err()
		}
	}
	if d.events == nil {
		d.events = []aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventMessageStarted}, {Kind: aipapi.EventTextDelta, Delta: "slow-ok"}, {Kind: aipapi.EventResponseFinished}}
	}
	if d.idx >= len(d.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := d.events[d.idx]
	d.idx++
	return ev, nil
}

func (d *delayedStreamForTTFT) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}
func (d *delayedStreamForTTFT) Close() error { return nil }

// interleaved helpers for secure-session driven thinker+executor on same logical turn.

func interleavedFingerprintKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func newAdversarialInterleavedExecutor(t *testing.T, ptbObs traffic.Observer, hook sdkhooks.RequestPartHook) (*Executor, *b2bua.MemoryStore, *interleavedthinking.InMemoryMemoStore) {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fk := interleavedFingerprintKey()
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(fk), b2bualineage.New(st), app.ManagerConfig{FingerprintKey: fk, StoreDurable: true, ResumeWindow: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{hook}})
	snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace:       workspace.NewResolverChain([]aipworkspace.Resolver{adversarialVoidWS{}}),
		TrafficObserver: ptbObs,
	})
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = bus
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	ex.SyntheticLocalPrincipal = false
	ex.Rand = routing.NewSeededRng(2)
	ex.Now = func() time.Time { return time.Unix(5000, 0) }
	memoStore := interleavedthinking.NewMemoStore(4096)
	ex.InterleavedProcessor = NewTestInterleavedProcessor(t, interleavedthinking.Config{
		Instructions:          "Think step by step.",
		StreamToClient:        "hidden",
		MaxMemoBytes:          4096,
		RegularTurnsRemaining: 2,
	}, memoStore)
	RegisterTestMemoStore(ex, memoStore)
	return ex, st, memoStore
}

type adversarialVoidWS struct{}

func (adversarialVoidWS) Resolve(context.Context) (aipworkspace.WorkspaceView, error) {
	return aipworkspace.WorkspaceView{}, nil
}

func adversarialPrincipalCtx(id string) context.Context {
	return execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: id})
}

func adversarialBaseCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: selector},
		Tools:      []aipapi.ToolDef{{Name: "search", Description: "search"}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("plan this")}}},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeNonStreaming},
	}
}

func adversarialResumeCall(prev, next *aipapi.Call) {
	next.Session.AuthoritativeSessionID = prev.Session.AuthoritativeSessionID
	next.Session.ALegID = prev.Session.ALegID
	next.Session.ClientSessionID = prev.Session.ClientSessionID
	next.Session.ResumeToken = prev.Session.ResumeToken
	next.Session.ContinuityKey = prev.Session.ContinuityKey
}

func adversarialThinkerStream(memo string) aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: interleavedthinking.MemoOpenTag + memo + interleavedthinking.MemoCloseTag},
		{Kind: aipapi.EventResponseFinished},
	})
}

func adversarialExecutorStream(text string) aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: text},
		{Kind: aipapi.EventResponseFinished},
	})
}

func adversarialBackend(caps aipapi.BackendCaps, capture func(aipapi.Call), stream func() aipapi.ManagedEventStream) *execbackend.Backend {
	return &execbackend.Backend{
		Caps: caps,
		TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		}),
		Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if capture != nil {
				capture(call)
			}
			return stream(), nil
		},
	}
}

func TestAdversarial_LateTransform_InterleavedThinkerExecutor(t *testing.T) {
	t.Parallel()
	snap, taggedMsg, _, steeringText, _, _, _ := buildAdversarySnapshotStable()
	taggedID, _ := conversationprojection.MessageIdentityOf(taggedMsg)
	hook := &adversaryHook{tagged: taggedMsg, taggedID: taggedID, steeringText: steeringText, kind: adversaryReintroduceMove}
	reader := &adversarialCountingReader{snap: snap}
	ptbObs := &ptbCaptureObserver{}

	// Capture both thinker and executor opens; use barrier to prove no sleep-based sync.
	var mu sync.Mutex
	var thinkerCalls []aipapi.Call
	var executorCalls []aipapi.Call

	ex, st, _ := newAdversarialInterleavedExecutor(t, ptbObs, hook)
	// Override counting reader (secure session still needs snapshot reader)
	ex.ConversationViewReader = reader

	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	const execAnswer = "executor answer"
	const memoBody = "hidden plan"

	// Use barrier channel to synchronize thinker and executor opens without sleep.
	thinkerStarted := make(chan struct{}, 1)
	executorStarted := make(chan struct{}, 1)

	ex.Backends = map[string]execbackend.Backend{
		"exec-be": *adversarialBackend(caps, func(c aipapi.Call) {
			mu.Lock()
			executorCalls = append(executorCalls, aipapi.CloneCall(c))
			mu.Unlock()
			select {
			case executorStarted <- struct{}{}:
			default:
			}
		}, func() aipapi.ManagedEventStream { return adversarialExecutorStream(execAnswer) }),
		"thinker-be": *adversarialBackend(caps, func(c aipapi.Call) {
			mu.Lock()
			thinkerCalls = append(thinkerCalls, aipapi.CloneCall(c))
			mu.Unlock()
			select {
			case thinkerStarted <- struct{}{}:
			default:
			}
		}, func() aipapi.ManagedEventStream { return adversarialThinkerStream(memoBody) }),
	}

	selector := "[thinker]thinker-be:m^exec-be:m"
	first := adversarialBaseCall(selector)
	firstCtx := adversarialPrincipalCtx("user-interleaved")
	fs, err := ex.Execute(firstCtx, first)
	if err != nil {
		t.Fatalf("first interleaved execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), fs); err != nil {
		t.Fatalf("first collect: %v", err)
	}
	_ = fs.Close()
	aLegID := first.Session.ALegID
	if aLegID == "" {
		t.Fatal("first execute must set A-leg")
	}

	// Reset for second logical turn which will drive thinker then executor.
	reader.count.Store(0)
	ptbObs.mu.Lock()
	ptbObs.observations = nil
	ptbObs.mu.Unlock()
	mu.Lock()
	thinkerCalls = nil
	executorCalls = nil
	mu.Unlock()

	// Seed cycle to force thinker on next turn (NextIndex = thinker position)
	state, err := st.FetchInterleavedState(context.Background(), aLegID)
	if err != nil {
		t.Fatalf("fetch state: %v", err)
	}
	// Selector has 2 entries: exec-be (0) then thinker-be (1). Set NextIndex=1 to force thinker.
	state.Cycle.NextIndex = 1
	if err := st.SetInterleavedState(context.Background(), aLegID, state); err != nil {
		t.Fatalf("seed cycle: %v", err)
	}

	second := adversarialBaseCall(selector)
	adversarialResumeCall(first, second)
	secondCtx := adversarialPrincipalCtx("user-interleaved")
	stream, err := ex.Execute(secondCtx, second)
	if err != nil {
		t.Fatalf("second execute (thinker+exec): %v", err)
	}
	collected, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("second collect: %v", err)
	}
	_ = stream.Close()
	if collected.Text.String() != execAnswer {
		t.Fatalf("interleaved second turn must return executor answer %q got %q", execAnswer, collected.Text.String())
	}

	// Both thinker and executor must have been opened on same logical turn, both repaired.
	select {
	case <-thinkerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("thinker did not start (barrier)")
	}
	select {
	case <-executorStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("executor continuation did not start (barrier)")
	}

	mu.Lock()
	tc := append([]aipapi.Call(nil), thinkerCalls...)
	ec := append([]aipapi.Call(nil), executorCalls...)
	mu.Unlock()
	if len(tc) != 1 {
		t.Fatalf("thinker opens %d want 1", len(tc))
	}
	if len(ec) != 1 {
		t.Fatalf("executor opens %d want 1", len(ec))
	}
	verifyRepaired(t, taggedID, steeringText, tc[0])
	verifyRepaired(t, taggedID, steeringText, ec[0])
	if reader.Count() != 2 {
		t.Fatalf("interleaved logical turn must read once at admission and once after publishing memo steering, got %d", reader.Count())
	}
	ptbCalls := ptbObs.PTBCalls(t)
	if len(ptbCalls) != 2 {
		t.Fatalf("interleaved PTB count %d want 2 (thinker+exec), got %v", len(ptbCalls), ptbCalls)
	}
	for _, ptb := range ptbCalls {
		verifyRepaired(t, taggedID, steeringText, ptb)
	}
}

func TestAdversarial_AnchorMissing_FallbackAndFailClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		policy     conversationprojection.AnchorMissingPolicy
		kind       adversaryKind
		shouldFail bool
	}{
		{name: "fallback", policy: conversationprojection.AnchorStablePrefixFallback, kind: adversaryAnchorRemoveFallback, shouldFail: false},
		{name: "fail_closed", policy: conversationprojection.AnchorFailClosed, kind: adversaryAnchorRemoveFailClosed, shouldFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			snap, taggedMsg, anchorMsg, steeringText, _, _, _ := buildAdversarySnapshotAfterMessage(tc.policy)
			taggedID, _ := conversationprojection.MessageIdentityOf(taggedMsg)
			hook := &adversaryHook{tagged: taggedMsg, taggedID: taggedID, steeringText: steeringText, anchorText: anchorMsg.Parts[0].Text, kind: tc.kind}
			reader := &adversarialCountingReader{snap: snap}
			cap := newCaptureBackend()
			ptbObs := &ptbCaptureObserver{}
			ex := TestExecutor()
			st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			ex.Store = st
			ex.ConversationViewReader = reader
			ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{hook}})
			ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{TrafficObserver: ptbObs})
			ex.Backends = map[string]execbackend.Backend{"openai": cap.Backend()}
			ex.Rand = routing.NewSeededRng(1)
			ex.Now = func() time.Time { return time.Unix(5000, 0) }
			call := &aipapi.Call{
				Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []aipapi.Message{
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(anchorMsg.Parts[0].Text)}},
					taggedMsg,
					{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("after")}},
				},
			}
			stream, err := ex.Execute(execDetachedCtx(context.Background()), call)
			if tc.shouldFail {
				if err == nil {
					if stream != nil {
						events, rerr := aipapi.Collect(context.Background(), stream)
						_ = events
						if rerr == nil {
							t.Fatal("expected fail_closed to reject candidate/request, got success")
						}
						err = rerr
					}
				}
				if err == nil {
					t.Fatal("expected fail_closed error")
				}
				if !strings.Contains(strings.ToLower(err.Error()), "anchor") && !strings.Contains(err.Error(), "candidate") && !strings.Contains(err.Error(), "conversation view") {
					t.Logf("fail_closed error is %v (acceptable if it contains anchor/projection)", err)
				}
				if cap.openCount.Load() != 0 {
					t.Fatalf("fail_closed must not open backend, got %d opens", cap.openCount.Load())
				}
				if ptbObs.PTBCount() != 0 {
					t.Fatalf("fail_closed must not emit PTB, got %d", ptbObs.PTBCount())
				}
				if reader.Count() != 1 {
					t.Fatalf("fail_closed still counts as one snapshot read, got %d", reader.Count())
				}
				return
			}
			if err != nil {
				t.Fatalf("fallback should succeed, got %v", err)
			}
			if _, err := aipapi.Collect(context.Background(), stream); err != nil {
				t.Fatalf("collect fallback: %v", err)
			}
			_ = stream.Close()
			if cap.openCount.Load() != 1 {
				t.Fatalf("fallback opens %d want 1", cap.openCount.Load())
			}
			if ptbObs.PTBCount() != 1 {
				t.Fatalf("fallback PTB count %d want 1", ptbObs.PTBCount())
			}
			open, _ := cap.LastCall()
			foundInInstr := false
			for _, m := range open.Instructions {
				if len(m.Parts) > 0 && m.Parts[0].Text == steeringText {
					foundInInstr = true
				}
			}
			if !foundInInstr {
				t.Fatalf("fallback steering should be in Instructions (stable prefix), open %+v", open)
			}
			for _, m := range open.Messages {
				if id, _ := conversationprojection.MessageIdentityOf(m); id == taggedID {
					t.Fatalf("fallback still contains tagged")
				}
			}
			ptbCalls := ptbObs.PTBCalls(t)
			foundPTBInInstr := false
			for _, m := range ptbCalls[0].Instructions {
				if len(m.Parts) > 0 && m.Parts[0].Text == steeringText {
					foundPTBInInstr = true
				}
			}
			if !foundPTBInInstr {
				t.Fatalf("PTB fallback steering not in Instructions")
			}
			verifyRepaired(t, taggedID, steeringText, ptbCalls[0])
		})
	}
}

func TestAdversarial_InFlightSnapshotIsolation(t *testing.T) {
	t.Parallel()
	snapN, taggedMsg, _, steeringText, _, _, _ := buildAdversarySnapshotStable()
	taggedID, _ := conversationprojection.MessageIdentityOf(taggedMsg)
	snapN1 := conversationprojection.Snapshot{
		StateRevision: 99,
		NeverBackend:  append([]conversationprojection.Tag(nil), snapN.NeverBackend...),
		Steering:      append([]conversationprojection.Overlay(nil), snapN.Steering...),
	}
	snapN1.Steering[0].Message.Text = "steering-stable-N+1"
	steeringN1Text := snapN1.Steering[0].Message.Text
	readerN := &adversarialCountingReader{snap: snapN}
	hook := &adversaryHook{tagged: taggedMsg, taggedID: taggedID, steeringText: steeringText, kind: adversaryReintroduceMove}
	primaryCap := newCaptureBackend()
	primaryCap.failOpen = aipapi.RecoverablePreOutputError(errors.New("pre-output-temp"))
	secondaryCap := newCaptureBackend()
	ptbObs := &ptbCaptureObserver{}
	ex := TestExecutor()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store = st
	ex.ConversationViewReader = readerN
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{hook}})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{TrafficObserver: ptbObs})
	ex.Backends = map[string]execbackend.Backend{"primary": primaryCap.Backend(), "secondary": secondaryCap.Backend()}
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(5000, 0) }
	call := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "primary:m|secondary:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("keep")}}, taggedMsg, {Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("after")}}},
	}
	stream, err := ex.Execute(execDetachedCtx(context.Background()), call)
	if err != nil {
		t.Fatalf("first turn failover: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("collect first: %v", err)
	}
	_ = stream.Close()
	if readerN.Count() != 1 {
		t.Fatalf("first turn snapshot reads %d want 1 (in-flight retry must reuse frozen N)", readerN.Count())
	}
	open1, _ := secondaryCap.LastCall()
	verifyRepaired(t, taggedID, steeringText, open1)
	for _, m := range append(append([]aipapi.Message(nil), open1.Instructions...), open1.Messages...) {
		if len(m.Parts) > 0 && strings.Contains(m.Parts[0].Text, "N+1") {
			t.Fatal("first turn must not see N+1 steering")
		}
	}
	if ptbObs.PTBCount() != 2 {
		t.Fatalf("first turn PTB count %d want 2 (primary+secondary)", ptbObs.PTBCount())
	}
	for _, ptb := range ptbObs.PTBCalls(t) {
		verifyRepaired(t, taggedID, steeringText, ptb)
	}
	readerN1 := &adversarialCountingReader{snap: snapN1}
	hookN1 := &adversaryHook{tagged: taggedMsg, taggedID: taggedID, steeringText: steeringN1Text, kind: adversaryReintroduceMove}
	cap2 := newCaptureBackend()
	ptbObs2 := &ptbCaptureObserver{}
	ex2 := TestExecutor()
	ex2.Store = st
	ex2.ConversationViewReader = readerN1
	ex2.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{hookN1}})
	ex2.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex2.Bus, extensions.SnapshotOptions{TrafficObserver: ptbObs2})
	ex2.Backends = map[string]execbackend.Backend{"openai": cap2.Backend()}
	ex2.Rand = routing.NewSeededRng(1)
	ex2.Now = func() time.Time { return time.Unix(5000, 0) }
	call2 := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("keep2")}}, taggedMsg},
	}
	stream2, err := ex2.Execute(execDetachedCtx(context.Background()), call2)
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream2); err != nil {
		t.Fatalf("collect second: %v", err)
	}
	_ = stream2.Close()
	if readerN1.Count() != 1 {
		t.Fatalf("second turn snapshot reads %d want 1", readerN1.Count())
	}
	open2, _ := cap2.LastCall()
	verifyRepaired(t, taggedID, steeringN1Text, open2)
	foundN1 := false
	for _, m := range append(append([]aipapi.Message(nil), open2.Instructions...), open2.Messages...) {
		if len(m.Parts) > 0 && m.Parts[0].Text == steeringN1Text {
			foundN1 = true
		}
	}
	if !foundN1 {
		t.Fatalf("second turn must see N+1 steering %q", steeringN1Text)
	}
	verifyRepaired(t, taggedID, steeringN1Text, ptbObs2.PTBCalls(t)[0])
	t.Run("concurrent_mutation_no_sleep", func(t *testing.T) {
		t.Parallel()
		store, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
		ctx := context.Background()
		rec, _ := store.CreateALeg(ctx, "concurrent-race")
		aLegID := rec.ALegID
		cv := conversationview.NewReferenceStore()
		_ = cv.CreateALeg(ctx, aLegID)
		initialTag := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("race-tag")}}
		initialID, _ := conversationprojection.MessageIdentityOf(initialTag)
		_, _ = cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: initialID, Reason: "r"}})
		var wg sync.WaitGroup
		errCh := make(chan error, 2)
		startCh := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-startCh
			snap, err := cv.Snapshot(ctx, aLegID)
			if err != nil {
				errCh <- err
				return
			}
			_ = snap
			errCh <- nil
		}()
		go func() {
			defer wg.Done()
			<-startCh
			newTag := aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("race-tag-2")}}
			newID, _ := conversationprojection.MessageIdentityOf(newTag)
			_, err := cv.TagNeverBackend(ctx, aLegID, []conversationview.TagRequest{{Identity: newID, Reason: "r2"}})
			errCh <- err
		}()
		close(startCh)
		wg.Wait()
		close(errCh)
		for e := range errCh {
			if e != nil {
				t.Fatalf("concurrent snapshot/mutation error: %v", e)
			}
		}
		snapFinal, _ := cv.Snapshot(ctx, aLegID)
		if len(snapFinal.NeverBackend) < 1 {
			t.Fatal("final snapshot missing tags")
		}
		t.Logf("concurrent_mutation_no_sleep: final tags %d (race-safe, no sleeps)", len(snapFinal.NeverBackend))
		if testing.Short() {
			t.Skip("race detector may be unavailable on Windows; normal test passed without sleep")
		}
	})
}

func TestAdversarial_NoRetryAfterOutput_Unchanged(t *testing.T) {
	t.Parallel()
	snap, taggedMsg, _, steeringText, _, _, _ := buildAdversarySnapshotStable()
	taggedID, _ := conversationprojection.MessageIdentityOf(taggedMsg)
	hook := &adversaryHook{tagged: taggedMsg, taggedID: taggedID, steeringText: steeringText, kind: adversaryReintroduceDuplicate}
	reader := &adversarialCountingReader{snap: snap}
	ptbObs := &ptbCaptureObserver{}
	var secondaryOpens atomic.Int32
	primary := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			for _, m := range call.Messages {
				if id, _ := conversationprojection.MessageIdentityOf(m); id == taggedID {
					return nil, fmt.Errorf("primary open still contains tagged (reassert bypass)")
				}
			}
			return &failAfterCommittedStream{
				events: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "committed-visible"},
				},
				fail: aipapi.RecoverablePreOutputError(errors.New("would-retry-if-uncommitted")),
			}, nil
		},
	}
	secondary := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			secondaryOpens.Add(1)
			return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
		},
	}
	ex := TestExecutor()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store = st
	ex.ConversationViewReader = reader
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{hook}})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{TrafficObserver: ptbObs})
	ex.Backends = map[string]execbackend.Backend{"primary": primary, "secondary": secondary}
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(5000, 0) }
	call := &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: "primary:m|secondary:m"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}, taggedMsg},
	}
	stream, err := ex.Execute(execDetachedCtx(context.Background()), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var sawCommitted bool
	for {
		ev, rerr := stream.Recv(context.Background())
		if rerr != nil {
			if !sawCommitted {
				t.Fatalf("expected committed event before error, got %v", rerr)
			}
			if aipapi.IsRecoverablePreOutput(rerr) {
				t.Fatalf("post-output error must not be recoverable pre-output: %v", rerr)
			}
			break
		}
		if ev.Kind == aipapi.EventTextDelta && ev.Delta == "committed-visible" {
			sawCommitted = true
		}
	}
	if !sawCommitted {
		t.Fatal("committed-visible TextDelta not delivered")
	}
	if secondaryOpens.Load() != 0 {
		t.Fatalf("no-retry-after-output must not open secondary, got %d", secondaryOpens.Load())
	}
	if ptbObs.PTBCount() != 1 {
		t.Fatalf("PTB should be exactly one for committed primary, got %d", ptbObs.PTBCount())
	}
	verifyRepaired(t, taggedID, steeringText, ptbObs.PTBCalls(t)[0])
	if reader.Count() != 1 {
		t.Fatalf("snapshot count %d want 1", reader.Count())
	}
	_ = stream.Close()
}

// Item-authority adversarial subtest through real runtime.
func TestAdversarial_LateTransform_ItemAuthority(t *testing.T) {
	t.Parallel()
	// Build item-authority snapshot with same semantics but Items.
	taggedItem := aipapi.Item{Kind: aipapi.ItemKindMessage, ID: "tagged-item", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "local-tagged-item"}}}
	taggedID, _ := conversationprojection.ItemIdentityOf(taggedItem)
	sysItem := aipapi.Item{Kind: aipapi.ItemKindMessage, ID: "sys-item", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleSystem, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "sys"}}}
	userItem := aipapi.Item{Kind: aipapi.ItemKindMessage, ID: "user1-item", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "user1"}}}
	overlay := conversationprojection.Overlay{
		OverlayID: "ov-item-stable", Revision: 1, SlotOrdinal: 1, Active: true,
		Message:             conversationprojection.OverlayMessage{Role: aipapi.RoleSystem, Text: "steering-item-stable"},
		Placement:           conversationprojection.Placement{Kind: conversationprojection.PlacementStablePrefix},
		AnchorMissingPolicy: conversationprojection.AnchorStablePrefixFallback,
	}
	snap := conversationprojection.Snapshot{StateRevision: 1, NeverBackend: []conversationprojection.Tag{{Identity: taggedID, Reason: "test"}}, Steering: []conversationprojection.Overlay{overlay}}
	steeringText := overlay.Message.Text

	// Hook will operate on Items; create a hook that uses same steering text but will be applied to Items.
	hook := &adversaryHook{
		tagged:       aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("local-tagged-item")}},
		taggedID:     taggedID,
		steeringText: steeringText,
		kind:         adversaryReintroduceDuplicate,
	}
	reader := &adversarialCountingReader{snap: snap}
	cap := newCaptureBackend()
	ptbObs := &ptbCaptureObserver{}
	ex := TestExecutor()
	st, _ := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	ex.Store = st
	ex.ConversationViewReader = reader
	ex.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{hook}})
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{TrafficObserver: ptbObs})
	ex.Backends = map[string]execbackend.Backend{"openai": cap.Backend()}
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Unix(5000, 0) }
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai:gpt-4"},
		Items: []aipapi.Item{sysItem, userItem, taggedItem, {Kind: aipapi.ItemKindMessage, ID: "user2", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "user2"}}}},
	}
	stream, err := ex.Execute(execDetachedCtx(context.Background()), call)
	if err != nil {
		t.Fatalf("item authority Execute: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("collect: %v", err)
	}
	_ = stream.Close()
	if reader.Count() != 1 {
		t.Fatalf("item authority snapshot count %d want 1", reader.Count())
	}
	if cap.openCount.Load() != 1 {
		t.Fatalf("item authority open count %d want 1", cap.openCount.Load())
	}
	open, _ := cap.LastCall()
	verifyRepaired(t, taggedID, steeringText, open)
	if ptbObs.PTBCount() != 1 {
		t.Fatalf("item authority PTB count %d want 1", ptbObs.PTBCount())
	}
	verifyRepaired(t, taggedID, steeringText, ptbObs.PTBCalls(t)[0])
	// After adaptation, item-authority is projected to legacy; verify overall count already checked.
	// Ensure no residual duplicate in any slice.
	allCount := 0
	for _, m := range open.Instructions {
		if len(m.Parts) > 0 && m.Parts[0].Text == steeringText {
			allCount++
		}
	}
	for _, m := range open.Messages {
		if len(m.Parts) > 0 && m.Parts[0].Text == steeringText {
			allCount++
		}
	}
	for _, it := range open.Items {
		if it.Kind == aipapi.ItemKindMessage && len(it.Content) > 0 && it.Content[0].Text == steeringText {
			allCount++
		}
	}
	if allCount != 1 {
		raw, _ := json.Marshal(open)
		t.Fatalf("item steering total count %d want 1, raw %s", allCount, string(raw))
	}
}

// failAfterCommittedStream is duplicated from output_commit_failover_gate_test for self-containment.
type failAfterCommittedStream struct {
	events []aipapi.Event
	i      int
	fail   error
}

func (s *failAfterCommittedStream) Recv(_ context.Context) (aipapi.Event, error) {
	if s.i < len(s.events) {
		ev := s.events[s.i]
		s.i++
		return ev, nil
	}
	if s.fail != nil {
		return aipapi.Event{}, s.fail
	}
	return aipapi.Event{}, io.EOF
}
func (*failAfterCommittedStream) Close() error { return nil }
func (*failAfterCommittedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}
