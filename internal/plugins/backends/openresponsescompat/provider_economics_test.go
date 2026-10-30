package openresponsescompat

import (
	"math"
	"testing"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestDecodeUsageFieldsCapacityHintDoesNotOverflow(t *testing.T) {
	tests := []struct {
		name  string
		base  int
		extra int
		want  int
	}{
		{name: "normal", base: 3, extra: 8, want: 11},
		{name: "zero base", base: 0, extra: 8, want: 8},
		{name: "exact boundary", base: math.MaxInt - 8, extra: 8, want: math.MaxInt},
		{name: "just past boundary stays safe", base: math.MaxInt - 7, extra: 8, want: math.MaxInt - 7},
		{name: "maximum base stays safe", base: math.MaxInt, extra: 8, want: math.MaxInt},
		{name: "no extra", base: 5, extra: 0, want: 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapCapacityHint(tc.base, tc.extra)
			if got != tc.want {
				t.Fatalf("mapCapacityHint(%d, %d) = %d, want %d", tc.base, tc.extra, got, tc.want)
			}
			if got < 0 {
				t.Fatalf("mapCapacityHint(%d, %d) overflowed to %d", tc.base, tc.extra, got)
			}
		})
	}
}

func TestParseResource_NativeMediaOnlyUsageIsEmitted(t *testing.T) {
	t.Parallel()
	const raw = `{
  "id": "resp_media_only",
  "object": "response",
  "created_at": 1719900000,
  "status": "completed",
  "model": "media-model",
  "output": [],
  "usage": {
    "input_image_tokens": 23,
    "output_audio_seconds": 1.5,
    "input_video_frames": 12,
    "output_document_pages": 2
  }
}`

	events, _, err := parseResource("compatible", []byte(raw), defaultResponseTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	var usage *aipapi.Event
	for i := range events {
		if events[i].Kind == aipapi.EventUsageDelta {
			usage = &events[i]
			break
		}
	}
	if usage == nil {
		t.Fatalf("native-only usage was dropped: %+v", events)
	}
	if usage.RawUsageJSON == "" {
		t.Fatal("native-only usage lost raw provider payload")
	}
}

func TestStreamMapper_NativeMediaOnlyUsageIsEmitted(t *testing.T) {
	t.Parallel()
	m := newStreamMapper("compatible", defaultResponseTestLimits())
	if _, err := m.mapRecord(created(`{"type":"response.created","sequence_number":0}`)); err != nil {
		t.Fatal(err)
	}
	events, err := m.mapRecord(rec("response.completed", `{"type":"response.completed","sequence_number":1,"response":{"id":"resp_media_only","status":"completed","model":"media-model","output":[],"usage":{"output_audio_seconds":1.5}}}`))
	if err != nil {
		t.Fatal(err)
	}
	var usage *aipapi.Event
	for i := range events {
		if events[i].Kind == aipapi.EventUsageDelta {
			usage = &events[i]
			break
		}
	}
	if usage == nil {
		t.Fatalf("native-only streaming usage was dropped: %+v", events)
	}
	if usage.Accounting.ProviderRequestID != "resp_media_only" || usage.RawUsageJSON == "" {
		t.Fatalf("streaming native usage context = %+v", usage)
	}
}

func TestProviderEvidence_CompatibleNativeMediaPreservesDirectionAndNativeUnits(t *testing.T) {
	t.Parallel()
	const raw = `{
  "input_image_tokens": 23,
  "output_image_count": 2,
  "input_audio_seconds": 1.5,
  "output_audio_tokens": 7,
  "input_video_frames": 12,
  "output_video_seconds": 2.5,
  "input_document_pages": 3,
  "output_file_pages": 4,
  "input_bytes": 4096,
  "output_document_tokens": 9
}`
	event := aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		RawUsageJSON: raw,
		Accounting: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	draft := providerEvidenceDraft(event, "openresponses.compat.v2", "compatible:media")
	want := map[string]string{
		"input/image_token/token":     "23/0",
		"output/image/image":          "2/0",
		"input/audio/second":          "15/1",
		"output/audio_token/token":    "7/0",
		"input/video/frame":           "12/0",
		"output/video/second":         "25/1",
		"input/document/page":         "3/0",
		"output/file/page":            "4/0",
		"input/file/byte":             "4096/0",
		"output/document_token/token": "9/0",
	}
	if len(draft.Measures) != len(want) {
		t.Fatalf("native measures = %d, want %d: %+v", len(draft.Measures), len(want), draft.Measures)
	}
	for _, measure := range draft.Measures {
		key := string(measure.Key.Direction) + "/" + measure.Key.Component + "/" + measure.Key.Unit
		value, ok := want[key]
		if !ok {
			t.Errorf("unexpected native measure key %q", key)
			continue
		}
		if measure.Value == nil || measure.Value.CanonicalString() != value {
			t.Errorf("native measure %q = %+v, want %s", key, measure.Value, value)
		}
		if measure.Key.SchemaID != "openresponses.usage.v2" || measure.MethodRef != "openresponses.usage.native.v2" {
			t.Errorf("native measure provenance = %+v", measure)
		}
	}
	for _, measure := range draft.Measures {
		if measure.Key.Component == sdkmetering.ComponentTextToken {
			t.Fatalf("native media was converted to text tokens: %+v", measure)
		}
	}
	if len(draft.Evidence) != len(want) {
		t.Fatalf("native evidence = %d, want %d", len(draft.Evidence), len(want))
	}
}

func TestProviderEvidence_CompatibleNativeEvidenceRetainsActualProviderPaths(t *testing.T) {
	t.Parallel()
	const raw = `{
  "prompt_audio_seconds": 1.5,
  "input_tokens_details": {"images": 2}
}`
	event := aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		RawUsageJSON: raw,
		Accounting: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	draft := providerEvidenceDraft(event, "openresponses.compat.v2", "compatible:paths")
	want := map[string]string{
		"$.usage.prompt_audio_seconds":        "1.5",
		"$.usage.input_tokens_details.images": "2",
	}
	for _, field := range draft.Evidence {
		if value, ok := want[field.Path]; ok {
			if !field.Present || field.Lexeme != value {
				t.Errorf("evidence %q = %+v, want present lexeme %q", field.Path, field, value)
			}
			delete(want, field.Path)
		}
	}
	if len(want) != 0 {
		t.Fatalf("native provider paths missing: %v (evidence=%+v)", want, draft.Evidence)
	}
}

func TestProviderEvidence_CompatibleStreamBindsV2AndRetainsLateCorrection(t *testing.T) {
	t.Parallel()
	first := aipapi.Event{
		Kind:         aipapi.EventUsageDelta,
		InputTokens:  4,
		OutputTokens: 2,
		TotalTokens:  6,
		UsagePresence: aipapi.UsagePresence{
			InputTokens: true, OutputTokens: true, TotalTokens: true,
		},
		RawUsageJSON: `{"input_tokens":4,"output_tokens":2,"total_tokens":6,"input_image_count":1,"output_audio_seconds":1.5}`,
		Accounting: aipapi.UsageAccountingMetadata{
			Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
			ProviderRequestID: "compat-resp-1", DedupeKey: "compat:compat-resp-1",
		},
	}
	rawStream := newProviderEventStream([]aipapi.Event{first}, "openresponses.compat.v2")
	stream, ok := rawStream.(*providerEventStream)
	if !ok {
		t.Fatalf("provider event stream is %T, want *providerEventStream", rawStream)
	}
	if got := stream.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("unbound compatible evidence escaped: %d", len(got))
	}
	stream.BindEconomicEvidence(coremetering.ObservationIdentity{
		StoreID: "store", RequestID: "request", CallID: "call", BillingCallID: "billing",
		ALegID: "a-leg", BLegID: "b-leg", AttemptID: "attempt",
	})
	observations := stream.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("initial compatible observations = %d, want 1", len(observations))
	}
	initial := observations[0]
	if initial.Version != sdkmetering.ObservationVersionV2 || initial.Authority != sdkmetering.AuthorityObservedClaim || initial.Acquisition != sdkmetering.AcquisitionProviderResponse {
		t.Fatalf("provider authority/provenance = version=%d authority=%q acquisition=%q", initial.Version, initial.Authority, initial.Acquisition)
	}
	if initial.Subject.Kind != sdkmetering.SubjectBLeg || initial.Subject.BLegID != "b-leg" || initial.Subject.ProviderRequestID != "compat-resp-1" {
		t.Fatalf("provider observation lineage = %+v", initial.Subject)
	}
	if initial.Semantics != sdkmetering.SemanticsCumulative {
		t.Fatalf("initial semantics = %q, want cumulative", initial.Semantics)
	}
	if _, err := sdkmetering.ProjectObservationToFact(initial); err == nil {
		t.Fatal("fractional native media measure must not be coerced through the V1 bridge")
	}

	correction := first
	correction.InputTokens = 5
	correction.TotalTokens = 7
	correction.RawUsageJSON = `{"input_tokens":5,"output_tokens":2,"total_tokens":7,"input_image_count":1,"output_audio_seconds":2.5}`
	stream.Add(providerEvidenceDraft(correction, "openresponses.compat.v2", "compat:compat-resp-1"))
	corrected := stream.DrainEconomicObservations()
	if len(corrected) != 1 {
		t.Fatalf("late compatible correction observations = %d, want 1", len(corrected))
	}
	if corrected[0].Revision != 2 || corrected[0].Semantics != sdkmetering.SemanticsReplacement {
		t.Fatalf("late correction revision/semantics = %d/%q", corrected[0].Revision, corrected[0].Semantics)
	}
	if len(corrected[0].Supersedes) != 1 || corrected[0].Supersedes[0].ObservationID != initial.ID {
		t.Fatalf("late correction supersession = %+v, want %q", corrected[0].Supersedes, initial.ID)
	}
}

func TestProviderEvidence_CompatibleAssistantMediaDoesNotInventProviderEconomics(t *testing.T) {
	t.Parallel()
	rawStream := newProviderEventStream([]aipapi.Event{
		{Kind: aipapi.EventAssistantImageRef, AssistantRef: "https://cdn.example/image.png"},
		{Kind: aipapi.EventAssistantFileRef, AssistantRef: "file-output-1"},
		{
			Kind: aipapi.EventUsageDelta, InputTokens: 4, OutputTokens: 2, TotalTokens: 6,
			UsagePresence: aipapi.UsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true},
			RawUsageJSON:  `{"input_tokens":4,"output_tokens":2,"total_tokens":6}`,
			Accounting: aipapi.UsageAccountingMetadata{
				Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
				DedupeKey: "compat:media-absent",
			},
		},
	}, "openresponses.compat.v2")
	stream, ok := rawStream.(*providerEventStream)
	if !ok {
		t.Fatalf("provider event stream is %T, want *providerEventStream", rawStream)
	}
	stream.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BLegID: "b-leg"})
	observations := stream.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("assistant-media observation count = %d, want one usage observation", len(observations))
	}
	for _, measure := range observations[0].Measures {
		switch measure.Key.Component {
		case sdkmetering.ComponentImage, sdkmetering.ComponentImageToken,
			sdkmetering.ComponentAudio, sdkmetering.ComponentAudioToken,
			sdkmetering.ComponentVideo, sdkmetering.ComponentVideoToken,
			sdkmetering.ComponentDocument, sdkmetering.ComponentDocumentToken,
			sdkmetering.ComponentFile:
			t.Fatalf("assistant media reference invented provider measure: %+v", measure)
		}
	}
	if len(observations[0].Charges) != 0 {
		t.Fatalf("assistant media reference invented provider charge: %+v", observations[0].Charges)
	}
}

// TestProviderEventStreamDelegatesToEmbeddedBuffer is a termination
// regression: BindEconomicEvidence and DrainEconomicObservations must delegate
// to the embedded buffer, never recurse into themselves (a qualifying
// embedded selector must stay explicit where the outer method shadows it).
func TestProviderEventStreamDelegatesToEmbeddedBuffer(t *testing.T) {
	t.Parallel()
	stream := &providerEventStream{
		events:                 nil,
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
	}
	stream.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BillingCallID: "call", BLegID: "b-1"})
	if got := stream.DrainEconomicObservations(); len(got) != 0 {
		t.Fatalf("empty buffer drained %d observations, want 0", len(got))
	}
}
