package backendplugin_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	fakebp "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestFake_ResolveProfile_advertisesDialectSupport(t *testing.T) {
	t.Parallel()

	svc := &fakebp.FakeService{Mode: fakebp.ModeValid}
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "inst", FactoryKind: "fake",
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
		Negotiation:   backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Capabilities.OrderedItems || len(profile.DialectSupport.CompactionDialects) == 0 {
		t.Fatalf("profile=%#v", profile)
	}
}

func TestFake_orderedItemWire_conformanceRoundTrip(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindItemReference, ID: "ref-1", Status: aipapi.ItemStatusCompleted, Reference: &aipapi.ItemReference{ID: "prev"}},
			{Kind: aipapi.ItemKindReasoning, ID: "rs-1", Status: aipapi.ItemStatusCompleted, Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "chain",
			}}},
			{Kind: aipapi.ItemKindCompaction, ID: "cmp-1", Status: aipapi.ItemStatusCompleted, Compaction: &aipapi.CompactionItem{
				Dialect: "compact.v1", Opaque: json.RawMessage(`{"ok":true}`),
			}},
			{Kind: aipapi.ItemKindExtension, ID: "ext-1", Status: aipapi.ItemStatusCompleted, Extension: &aipapi.OpaqueExtension{
				Namespace: "ns", Type: "beta", Data: json.RawMessage(`{"k":1}`),
			}},
		},
	}
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("x")}}}},
	}
	neg := backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
	}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, neg); err != nil {
		t.Fatal(err)
	}
	p, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.InvocationFromProto(p)
	if err != nil {
		t.Fatal(err)
	}
	if !back.ItemAuthority || len(back.Items) != 4 {
		t.Fatalf("back=%#v", back)
	}
}

func TestFake_orderedItemWire_rejectsOldMinor(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{Items: []aipapi.Item{{
		Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
	}}}
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("x")}}}},
	}
	err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: 0,
	})
	if err == nil {
		t.Fatal("expected ABI rejection")
	}
}

func TestFake_exactOpenResponsesConformanceRoundTrip(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		PromptCacheKey: "conv-1",
		Items: []aipapi.Item{
			{
				Kind: aipapi.ItemKindMessage, ID: "file-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{
					Kind: aipapi.ContentPartFileRef, FileRef: "https://x/f", FileData: "aGVsbG8=", FileName: "minimal.pdf",
				}},
			},
			{
				Kind: aipapi.ItemKindMessage, ID: "ext-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{
					Kind:      aipapi.ContentPartExtension,
					Extension: &aipapi.ExtensionContentPart{Type: "acme:input_file", Data: json.RawMessage(`{"type":"acme:input_file"}`)},
				}},
			},
			{
				Kind: aipapi.ItemKindReasoning, ID: "rs-1", Status: aipapi.ItemStatusCompleted,
				Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{
					Dialect:                 aipapi.ReasoningDialectOpenAIResponsesItemV1,
					Summary:                 json.RawMessage(`[]`),
					SummaryPresent:          true,
					EncryptedContent:        json.RawMessage("null"),
					EncryptedContentPresent: true,
				}},
			},
			{
				Kind: aipapi.ItemKindCompaction, ID: "cmp-1", Status: aipapi.ItemStatusCompleted,
				Compaction: &aipapi.CompactionItem{Dialect: "compact.v1", EncryptedContent: "gAAAAABpayload"},
			},
		},
	}
	inv := backendplugin.Invocation{
		RequestID: "req", AttemptID: "att", ALegID: "a", BLegID: "b", CanonicalModelID: "m",
	}
	neg := backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorExactOpenResponsesFields,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems, backendplugin.FeatureExactOpenResponsesFields},
	}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, neg); err != nil {
		t.Fatal(err)
	}
	p, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.InvocationFromProto(p)
	if err != nil {
		t.Fatal(err)
	}

	svc := &fakebp.FakeService{Mode: fakebp.ModeValid}
	inst, err := svc.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "inst", FactoryKind: "fake",
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
		Negotiation:   neg,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	stream := &memExecuteStream{ctx: ctx, start: back}
	if err := inst.Execute(stream); err != nil {
		t.Fatal(err)
	}
	if svc.LastStartInvocation == nil {
		t.Fatal("fake did not capture start invocation")
	}
	got := svc.LastStartInvocation
	if got.PromptCacheKey != "conv-1" {
		t.Fatalf("prompt_cache_key=%q", got.PromptCacheKey)
	}
	if got.Items[0].Content[0].FileData == nil || *got.Items[0].Content[0].FileData != "aGVsbG8=" {
		t.Fatalf("file_data=%#v", got.Items[0].Content[0])
	}
	if got.Items[1].Content[0].ExtensionType == nil || *got.Items[1].Content[0].ExtensionType != "acme:input_file" {
		t.Fatalf("extension=%#v", got.Items[1].Content[0])
	}
	if got.Items[2].Reasoning == nil || got.Items[2].Reasoning.EncryptedContent.State() != backendplugin.RawJSONNull {
		t.Fatalf("reasoning=%#v", got.Items[2].Reasoning)
	}
	if got.Items[3].Compaction == nil || got.Items[3].Compaction.EncryptedContent != "gAAAAABpayload" {
		t.Fatalf("compaction=%#v", got.Items[3].Compaction)
	}
	if svc.LastStartCall == nil {
		t.Fatal("fake did not capture start call")
	}
	gotCall := svc.LastStartCall
	if gotCall.PromptCacheKey != "conv-1" || gotCall.Items[0].Content[0].FileData != "aGVsbG8=" {
		t.Fatalf("start call=%#v", gotCall)
	}
	if gotCall.Items[2].Reasoning.Reasoning.EncryptedContentPresent == false {
		t.Fatalf("reasoning presence lost in start call: %#v", gotCall.Items[2].Reasoning)
	}
}

type memExecuteStream struct {
	ctx    context.Context
	start  backendplugin.Invocation
	frames []backendplugin.ServerFrame
	sent   bool
}

func (m *memExecuteStream) Context() context.Context { return m.ctx }
func (m *memExecuteStream) Recv() (backendplugin.ClientFrame, error) {
	if !m.sent {
		m.sent = true
		return backendplugin.ClientFrame{Kind: backendplugin.ClientFrameStart, InstanceID: "inst", Invocation: &m.start}, nil
	}
	return backendplugin.ClientFrame{}, io.EOF
}

func (m *memExecuteStream) Send(f backendplugin.ServerFrame) error {
	m.frames = append(m.frames, f)
	return nil
}
