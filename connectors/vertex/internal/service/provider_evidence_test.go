package service

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestVertexUsageMapsModalityAndGroundedToolEvidence(t *testing.T) {
	t.Parallel()
	u := &VertexUsageMetadata{
		PromptTokenCount: 20, CandidatesTokenCount: 8, TotalTokenCount: 32,
		CachedContentTokenCount: 4, ThoughtsTokenCount: 2, ToolUsePromptTokenCount: 1,
		PromptTokensDetails:        []VertexModalityTokenCount{{Modality: "TEXT", TokenCount: 12}, {Modality: "IMAGE", TokenCount: 8}},
		CandidatesTokensDetails:    []VertexModalityTokenCount{{Modality: "VIDEO", TokenCount: 8}},
		CacheTokensDetails:         []VertexModalityTokenCount{{Modality: "IMAGE", TokenCount: 4}},
		ToolUsePromptTokensDetails: []VertexModalityTokenCount{{Modality: "TEXT", TokenCount: 1}},
		ServiceTier:                "standard",
	}
	ev := usageEvent(u)
	if ev.Accounting.Source != aipapi.UsageSourceProviderReported || !ev.UsagePresence.CacheReadTokens || ev.CostPresent {
		t.Fatalf("Vertex usage metadata lost provider/presence semantics: %+v", ev)
	}
	draft := vertexEvidenceDraft(ev, u, "vertex.generate.v2")
	// The local parser retains six native modality/tool measures. The
	// executable bridge forwards only the representable V1 token subset and
	// does not coerce native units into text tokens.
	if len(draft.Measures) != 6 {
		t.Fatalf("Vertex native measures = %d, want 6", len(draft.Measures))
	}
	foundServiceContext := false
	for _, field := range draft.Evidence {
		if field.Path == "$.provider_schema.service_context" && field.Lexeme == "standard" {
			foundServiceContext = true
		}
	}
	if !foundServiceContext {
		t.Fatalf("Vertex service context evidence missing: %+v", draft.Evidence)
	}
	for _, measure := range draft.Measures {
		if err := measure.Validate(); err != nil {
			t.Fatalf("invalid Vertex measure %+v: %v", measure, err)
		}
	}
	if got := vertexUsageRawJSON(u); got == "" {
		t.Fatal("Vertex usage raw evidence was empty")
	}
}

func TestVertexUsageMalformedModalityIsUnavailable(t *testing.T) {
	t.Parallel()
	u := &VertexUsageMetadata{PromptTokensDetails: []VertexModalityTokenCount{{Modality: "UNKNOWN", TokenCount: 2}, {Modality: "IMAGE", TokenCount: -1}}}
	if got := vertexNativeMeasures(u); len(got) != 0 {
		t.Fatalf("malformed Vertex modality details should be omitted: %+v", got)
	}
}

func TestVertexUsageMapsEveryNativeModalityInInputAndOutputDirections(t *testing.T) {
	t.Parallel()
	modalities := []struct {
		provider  string
		component string
		input     string
		output    string
	}{
		{provider: "TEXT", component: sdkmetering.ComponentTextToken, input: "1", output: "11"},
		{provider: "IMAGE", component: sdkmetering.ComponentImageToken, input: "2", output: "12"},
		{provider: "AUDIO", component: sdkmetering.ComponentAudioToken, input: "3", output: "13"},
		{provider: "VIDEO", component: sdkmetering.ComponentVideoToken, input: "4", output: "14"},
		{provider: "DOCUMENT", component: sdkmetering.ComponentDocumentToken, input: "5", output: "15"},
	}
	u := &VertexUsageMetadata{}
	for i, modality := range modalities {
		u.PromptTokensDetails = append(u.PromptTokensDetails, VertexModalityTokenCount{Modality: modality.provider, TokenCount: i + 1})
		u.CandidatesTokensDetails = append(u.CandidatesTokensDetails, VertexModalityTokenCount{Modality: modality.provider, TokenCount: i + 11})
	}

	got := vertexNativeMeasures(u)
	if len(got) != len(modalities)*2 {
		t.Fatalf("Vertex native modality measures = %d, want %d: %+v", len(got), len(modalities)*2, got)
	}
	want := make(map[string]string, len(got))
	for _, modality := range modalities {
		want[string(sdkmetering.DirectionInput)+"/"+modality.component] = modality.input
		want[string(sdkmetering.DirectionOutput)+"/"+modality.component] = modality.output
	}
	for _, measure := range got {
		key := string(measure.Key.Direction) + "/" + measure.Key.Component
		if measure.Key.Unit != sdkmetering.UnitToken || measure.Key.SchemaID != "vertex.usage.v2" {
			t.Fatalf("Vertex modality measure %q lost native token schema/unit: %+v", key, measure)
		}
		if len(measure.Key.Dimensions) != 2 || measure.Key.Dimensions[0].Name != "modality" || measure.Key.Dimensions[1].Name != "evidence_plane" {
			t.Fatalf("Vertex modality measure %q lost evidence dimensions: %+v", key, measure.Key.Dimensions)
		}
		if gotValue := measure.Value.Coefficient; gotValue != want[key] {
			t.Fatalf("Vertex modality measure %q = %q, want %q", key, gotValue, want[key])
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("Vertex native modality measures missing: %v", want)
	}
}

func TestVertexEvidenceLeavesUnsupportedMediaEconomicsUnavailable(t *testing.T) {
	t.Parallel()
	u := &VertexUsageMetadata{
		PromptTokensDetails: []VertexModalityTokenCount{
			{Modality: "IMAGE", TokenCount: 2},
			{Modality: "AUDIO", TokenCount: 3},
			{Modality: "VIDEO", TokenCount: 4},
			{Modality: "DOCUMENT", TokenCount: 5},
		},
	}
	draft := vertexEvidenceDraft(usageEvent(u), u, "vertex.generate.v2")
	for _, measure := range draft.Measures {
		if measure.Key.Unit != sdkmetering.UnitToken {
			t.Fatalf("Vertex fabricated a non-token native unit: %+v", measure)
		}
		for _, dimension := range measure.Key.Dimensions {
			switch strings.ToLower(dimension.Name) {
			case "duration", "resolution", "resource", "storage", "bytes", "frames":
				t.Fatalf("Vertex fabricated unsupported media/resource qualifier: %+v", measure)
			}
		}
	}
	if len(draft.Evidence) != 0 {
		t.Fatalf("Vertex fabricated safe media/resource evidence: %+v", draft.Evidence)
	}
}

func TestVertexUsagePrefersTrafficTypeForServiceContext(t *testing.T) {
	t.Parallel()
	u := &VertexUsageMetadata{TrafficType: "ON_DEMAND", ServiceTier: "legacy-tier"}
	ev := usageEvent(u)
	if ev.Accounting.ServiceContext != "ON_DEMAND" {
		t.Fatalf("Vertex traffic type service context = %q, want %q", ev.Accounting.ServiceContext, "ON_DEMAND")
	}
	draft := vertexEvidenceDraft(ev, u, "vertex.generate.v2")
	if len(draft.Evidence) != 1 || draft.Evidence[0].Lexeme != "ON_DEMAND" {
		t.Fatalf("Vertex traffic type evidence = %+v", draft.Evidence)
	}
}

func TestVertexV1BridgeProjectsCanonicalUsageKey(t *testing.T) {
	t.Parallel()
	input := 11
	output := 8
	total := 19
	stream := newSliceStream([]aipapi.Event{{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   input,
		OutputTokens:  output,
		TotalTokens:   total,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "vertex.generate.usage:stream",
		},
	}})
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Accounting.DedupeKey != "" {
		t.Fatalf("canonical Vertex usage retained durable key %q", ev.Accounting.DedupeKey)
	}
	evidence := stream.DrainAccountingEvidence()
	if len(evidence) != 1 || evidence[0].DedupeKey != "vertex.generate.usage:stream" {
		t.Fatalf("V1 Vertex evidence = %#v", evidence)
	}
}

func TestVertexV1BridgeDisabledLeavesCanonicalUsageIdentity(t *testing.T) {
	t.Parallel()
	event := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   11,
		UsagePresence: aipapi.UsagePresence{InputTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "vertex.generate.usage:stream",
		},
	}
	stream := newSliceStream([]aipapi.Event{event})
	stream.SetEnabled(false)
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Accounting.DedupeKey != "vertex.generate.usage:stream" {
		t.Fatalf("disabled Vertex V1 bridge removed canonical identity %q", ev.Accounting.DedupeKey)
	}
	if got := stream.DrainAccountingEvidence(); len(got) != 0 {
		t.Fatalf("disabled Vertex V1 bridge emitted sideband evidence: %+v", got)
	}
}

func TestVertexV1BridgeRetainsTerminalAndLateCorrectionSemantics(t *testing.T) {
	t.Parallel()
	first := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   11,
		OutputTokens:  8,
		TotalTokens:   19,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "vertex.generate.usage:stream",
		},
	}
	stream := newSliceStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		first,
		{Kind: aipapi.EventResponseFinished},
	})
	for {
		if _, err := stream.Recv(context.Background()); err == nil {
			continue
		} else if err != io.EOF {
			t.Fatal(err)
		} else {
			break
		}
	}
	initial := stream.DrainAccountingEvidence()
	if len(initial) != 1 || initial[0].InputTokens == nil || *initial[0].InputTokens != 11 {
		t.Fatalf("Vertex terminal V1 evidence = %+v", initial)
	}

	late := first
	late.InputTokens = 13
	late.TotalTokens = 21
	stream.AddUsageEvent(late, "vertex.generate.usage:stream")
	correction := stream.DrainAccountingEvidence()
	if len(correction) != 1 || correction[0].InputTokens == nil || *correction[0].InputTokens != 13 || correction[0].TotalTokens == nil || *correction[0].TotalTokens != 21 {
		t.Fatalf("Vertex late correction V1 evidence = %+v", correction)
	}
	stream.AddUsageEvent(late, "vertex.generate.usage:stream")
	if got := stream.DrainAccountingEvidence(); len(got) != 0 {
		t.Fatalf("Vertex exact replay was not deduplicated: %+v", got)
	}
}

func TestVertexV1BridgeLiftsTrustedBLegProviderIdentity(t *testing.T) {
	t.Parallel()
	event := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		InputTokens:   11,
		OutputTokens:  8,
		TotalTokens:   19,
		UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
		Accounting: aipapi.UsageAccountingMetadata{
			Plane: aipapi.UsagePlaneProviderBillable, Source: aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative, DedupeKey: "vertex.generate.usage:stream",
		},
	}
	stream := newSliceStream([]aipapi.Event{event})
	evidence := stream.DrainAccountingEvidence()
	if len(evidence) != 1 {
		t.Fatalf("Vertex V1 evidence = %+v", evidence)
	}
	if evidence[0].Source != backendplugin.AccountingSourceProviderReported || evidence[0].Authority != backendplugin.AccountingAuthorityAuthoritative || evidence[0].Plane != backendplugin.AccountingPlaneProviderBillable {
		t.Fatalf("Vertex provider evidence provenance = %+v", evidence[0])
	}
	const storeID = "vertex-meter-store"
	const providerAccount = "gcp-project:test-project"
	subject := sdkmetering.SubjectRef{
		Kind: sdkmetering.SubjectBLeg, StoreID: storeID, ALegID: "a-leg", RequestID: "request",
		BillingCallID: "billing-call", BLegID: "b-leg", AttemptID: "attempt", AttemptSeq: 1,
		ProviderAccountKey: providerAccount, ProviderRequestID: "vertex-request",
	}
	correlation := sdkmetering.CorrelationV2{
		StoreID: storeID, RequestID: "request", BillingCallID: "billing-call", ALegID: "a-leg",
		BLegID: "b-leg", AttemptID: "attempt", AttemptSeq: 1,
		ProviderAccountKey: providerAccount, ProviderRequestID: "vertex-request",
	}
	lifted, err := backendplugin.LiftAccountingEvidenceV1(evidence[0], backendplugin.V1EvidenceIdentity{
		StoreID: storeID, ObservationID: "vertex-observation", SourceEventKey: evidence[0].DedupeKey,
		Revision: 1, StreamID: "b-leg", Sequence: 1, Subject: subject, Correlation: correlation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if lifted.Coverage != backendplugin.EvidenceCoveragePartial || lifted.CoverageReason != "legacy V1 token-only evidence" {
		t.Fatalf("Vertex V1 lift coverage = %q/%q", lifted.Coverage, lifted.CoverageReason)
	}
	obs := lifted.Observation
	if obs.Subject.Kind != sdkmetering.SubjectBLeg || obs.Subject.BLegID != "b-leg" || obs.Subject.ProviderAccountKey != providerAccount {
		t.Fatalf("Vertex lifted B-leg/provider identity = %+v", obs.Subject)
	}
	if obs.Correlation.BLegID != "b-leg" || obs.Correlation.ProviderAccountKey != providerAccount || obs.Correlation.BillingCallID != "billing-call" {
		t.Fatalf("Vertex lifted correlation identity = %+v", obs.Correlation)
	}
	if obs.Origin != sdkmetering.OriginProvider || obs.Acquisition != sdkmetering.AcquisitionProviderResponse || obs.Authority != sdkmetering.AuthorityObservedClaim || obs.Boundary != sdkmetering.BoundaryBackendIngress || obs.Lifecycle != sdkmetering.LifecycleBackendAttempt || obs.Semantics != sdkmetering.SemanticsDelta {
		t.Fatalf("Vertex lifted provenance/semantics = origin=%q acquisition=%q authority=%q boundary=%q lifecycle=%q semantics=%q", obs.Origin, obs.Acquisition, obs.Authority, obs.Boundary, obs.Lifecycle, obs.Semantics)
	}
}

func TestVertexUsagePreservesWireZeroVersusAbsent(t *testing.T) {
	t.Parallel()
	var usage VertexUsageMetadata
	if err := json.Unmarshal([]byte(`{"promptTokenCount":0,"totalTokenCount":0}`), &usage); err != nil {
		t.Fatal(err)
	}
	ev := usageEvent(&usage)
	if !ev.UsagePresence.InputTokens || !ev.UsagePresence.TotalTokens {
		t.Fatalf("explicit wire zero lost: %+v", ev.UsagePresence)
	}
	if ev.UsagePresence.OutputTokens || ev.UsagePresence.CacheReadTokens || ev.UsagePresence.ReasoningTokens {
		t.Fatalf("omitted Vertex counters became present: %+v", ev.UsagePresence)
	}
}

func TestVertexNegativeUsageFieldRemainsUnavailable(t *testing.T) {
	t.Parallel()
	negative := -1
	ev := usageEvent(&VertexUsageMetadata{PromptTokenCount: negative, CandidatesTokenCount: 2, TotalTokenCount: 2})
	if ev.UsagePresence.InputTokens || ev.InputTokens != 0 {
		t.Fatalf("negative Vertex input became provider evidence: %+v", ev)
	}
}

func TestVertexNativeMeasuresDropsOverflowingModalityAggregate(t *testing.T) {
	t.Parallel()
	maxInt := int(^uint(0) >> 1)
	u := &VertexUsageMetadata{
		PromptTokensDetails: []VertexModalityTokenCount{
			{Modality: "TEXT", TokenCount: maxInt},
			{Modality: "TEXT", TokenCount: maxInt},
		},
	}
	for _, measure := range vertexNativeMeasures(u) {
		if measure.Value != nil && strings.HasPrefix(measure.Value.Coefficient, "-") {
			t.Fatalf("overflowing modality aggregate became negative: %+v", measure)
		}
	}
}
