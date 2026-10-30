package openaiusage_test

// R7-C1 (PR #659 adversarial repair): provider-family usage schema binding and
// intact wire proof — open-source half.
//
// These vectors bind the frozen OpenAI Chat/Responses inclusion schema declared
// in the openaiusage package and drive the production adapter -> provider
// evidence path. They prove that:
//
//   - the schema relates the host-neutral aggregate input/output token keys to
//     the native directional text/audio child keys the adapter actually emits,
//     across the two schema identities;
//   - the frozen provider schema is material tariff identity, and the returned
//     schema slice is a fresh deep copy each call;
//   - a conflicting flat audio alias never overrides the native nested detail;
//   - present-but-malformed or non-canonical nested image shapes stay explicit
//     unavailable measures and never leak the raw lexeme into evidence;
//   - a present nested image token is emitted as a native input image token
//     bearing the provider's true wire field identity.
//
// The real-rater monetary assertions of the original file (schema-free
// additive 123/0; frozen both-priced typed overlap; child-only complete 74/0
// with per-direction amounts; detail-shape completes/partials at 59/0 and 65/0;
// nested-image partial with payable 16/15/15/28; malformed/non-canonical image
// fail-closed partial) are preserved by task 3.5 in the Enterprise tree:
// enterprise/internal/preservation/openai_inclusion_test.go. The open-source
// half below keeps the schema/wire/measure/native-presence assertions.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaiusage"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

const wireResponsesSuccess = `{"id":"resp-audio","object":"response","created_at":1715620000,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":9,"total_tokens":20,"input_tokens_details":{"text_tokens":8,"audio_tokens":3,"images":0},"output_tokens_details":{"text_tokens":5,"audio_tokens":4,"images":0}}}`

// TestNativeUsageInclusionSchemaDeclaresDirectionalTextAudioPartition pins the
// frozen provider-family schema shape: explicit partition edges for the
// directional text/audio children, plus OPTIONAL partition edges for the
// native image token members (input and output), and the bounded subset edges
// for the included cached (input), reasoning (output) and cache-write (input)
// subsets. The cache-write subset has two edges because the standard native
// member and the compatible-provider extension fallback are emitted under
// different schema identities; the adapter never lets both bind the same
// observation. The optional partition kind is deliberate: image tokens are a
// disjoint member of the prompt/completion partition (so text+audio+image rates
// completely, and an aggregate+image tariff must fail closed), but the member
// is reported only when the request/response carried image media, so an omitted
// image member keeps the text/audio partition complete without inventing a
// provider zero. The cached/reasoning/cache-write subsets are partial
// containments, so an aggregate-only tariff over them stays complete and a
// child-only tariff over one can never prove the parent's complete coverage.
// The output image token edge only ever binds the recognized compatible-provider
// flat alias vocabulary the adapter emits; the native OpenAI output detail has
// no image token member. The cached/reasoning subset children share the default
// inclusion schema identity with their aggregates because the adapter emits them
// there (providerTokenMeasures), while the native text/audio/image/cache-write
// children live under the native detail schema. It validates the published
// schema set exactly as BuildTariffSnapshotWithSchemas will.
func TestNativeUsageInclusionSchemaDeclaresDirectionalTextAudioPartition(t *testing.T) {
	t.Parallel()
	schemas := openaiusage.NativeUsageInclusionSchemas()
	if err := sdkmetering.ValidateComponentSchemas(schemas); err != nil {
		t.Fatalf("frozen inclusion schema must validate: %v", err)
	}
	if len(schemas) != 1 {
		t.Fatalf("schema count = %d, want 1", len(schemas))
	}
	schema := schemas[0]
	if schema.ID != openaiusage.NativeUsageInclusionSchemaID {
		t.Fatalf("schema id = %q, want %q", schema.ID, openaiusage.NativeUsageInclusionSchemaID)
	}
	if len(schema.Relationships) != 10 {
		t.Fatalf("relationship count = %d, want 10: %+v", len(schema.Relationships), schema.Relationships)
	}
	type edge struct {
		kind        sdkmetering.RelationshipKind
		direction   string
		parent      string
		child       string
		childSchema string
		optional    bool
	}
	seen := make(map[edge]struct{})
	partitionEdges, subsetEdges := 0, 0
	for _, relationship := range schema.Relationships {
		switch relationship.Kind {
		case sdkmetering.RelationshipPartition:
			partitionEdges++
		case sdkmetering.RelationshipSubset:
			subsetEdges++
		default:
			t.Fatalf("relationship kind = %q, want partition or subset", relationship.Kind)
		}
		if relationship.Optional && relationship.Kind != sdkmetering.RelationshipPartition {
			t.Fatalf("optional %q relationship is not a partition member: %+v", relationship.Kind, relationship)
		}
		if relationship.Parent.SchemaID != sdkmetering.DefaultInclusionSchemaID {
			t.Fatalf("parent schema = %q, want %q", relationship.Parent.SchemaID, sdkmetering.DefaultInclusionSchemaID)
		}
		switch relationship.Child.SchemaID {
		case openaiusage.NativeUsageSchemaID, sdkmetering.DefaultInclusionSchemaID:
		default:
			t.Fatalf("child schema = %q, want %q or %q", relationship.Child.SchemaID,
				openaiusage.NativeUsageSchemaID, sdkmetering.DefaultInclusionSchemaID)
		}
		if relationship.Parent.Unit != sdkmetering.UnitToken || relationship.Child.Unit != sdkmetering.UnitToken {
			t.Fatalf("edge units = %q/%q, want token", relationship.Parent.Unit, relationship.Child.Unit)
		}
		if relationship.Parent.Direction != relationship.Child.Direction {
			t.Fatalf("edge direction mismatch: %q -> %q", relationship.Parent.Direction, relationship.Child.Direction)
		}
		seen[edge{relationship.Kind, string(relationship.Parent.Direction), relationship.Parent.Component, relationship.Child.Component, relationship.Child.SchemaID, relationship.Optional}] = struct{}{}
	}
	for _, want := range []edge{
		{sdkmetering.RelationshipPartition, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentTextToken, openaiusage.NativeUsageSchemaID, false},
		{sdkmetering.RelationshipPartition, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentAudioToken, openaiusage.NativeUsageSchemaID, false},
		{sdkmetering.RelationshipPartition, "output", sdkmetering.ComponentOutputToken, sdkmetering.ComponentTextToken, openaiusage.NativeUsageSchemaID, false},
		{sdkmetering.RelationshipPartition, "output", sdkmetering.ComponentOutputToken, sdkmetering.ComponentAudioToken, openaiusage.NativeUsageSchemaID, false},
		{sdkmetering.RelationshipPartition, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentImageToken, openaiusage.NativeUsageSchemaID, true},
		{sdkmetering.RelationshipPartition, "output", sdkmetering.ComponentOutputToken, sdkmetering.ComponentImageToken, openaiusage.NativeUsageSchemaID, true},
		{sdkmetering.RelationshipSubset, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentCacheReadInputToken, sdkmetering.DefaultInclusionSchemaID, false},
		{sdkmetering.RelationshipSubset, "output", sdkmetering.ComponentOutputToken, sdkmetering.ComponentReasoningOutputToken, sdkmetering.DefaultInclusionSchemaID, false},
		{sdkmetering.RelationshipSubset, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentCacheWriteInputToken, openaiusage.NativeUsageSchemaID, false},
		{sdkmetering.RelationshipSubset, "input", sdkmetering.ComponentInputToken, sdkmetering.ComponentCacheWriteInputToken, sdkmetering.DefaultInclusionSchemaID, false},
	} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("schema missing edge %+v: %+v", want, seen)
		}
	}
	if partitionEdges != 6 {
		t.Fatalf("partition edge count = %d, want 4 text/audio + 2 optional image partition edges", partitionEdges)
	}
	if subsetEdges != 4 {
		t.Fatalf("subset edge count = %d, want 4 included native/default subset edges", subsetEdges)
	}
}

// TestNativeUsageInclusionSchemaBindsAdapterWireKeys proves the schema is bound
// to the adapter's real wire-family vocabulary rather than a hand-written
// assumption. Every declared child key must be a key the adapter actually emits
// for native/detail measures, and every declared parent key must be a key the
// adapter actually emits for the ordinary aggregate totals. A drift on either
// side fails this test instead of silently mis-binding the partition. The
// cached/reasoning subset children are emitted by providerTokenMeasures under
// the default inclusion schema (not the native detail schema), so they are bound
// from the provider evidence draft alongside the aggregate parents.
func TestNativeUsageInclusionSchemaBindsAdapterWireKeys(t *testing.T) {
	t.Parallel()
	schema := openaiusage.NativeUsageInclusionSchema()

	raw := `{"prompt_tokens":13,"completion_tokens":9,"total_tokens":22,"output_image_tokens":1,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"image_tokens":2,"cached_tokens":4,"cache_write_tokens":5},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4,"reasoning_tokens":3}}`
	nativeKeys := make(map[string]struct{})
	for _, measure := range openaiusage.NativeUsageMeasures(raw) {
		switch measure.Key.Component {
		case sdkmetering.ComponentTextToken, sdkmetering.ComponentAudioToken, sdkmetering.ComponentImageToken, sdkmetering.ComponentCacheWriteInputToken:
			key, err := measure.Key.Normalize()
			if err != nil {
				t.Fatalf("native key normalize: %v", err)
			}
			nativeKeys[key.CanonicalKey()] = struct{}{}
		}
	}
	if len(nativeKeys) != 7 {
		t.Fatalf("adapter native text/audio/image/cache-write keys = %d, want 7: %+v", len(nativeKeys), nativeKeys)
	}

	event := aipapi.Event{
		Kind:             aipapi.EventUsageDelta,
		InputTokens:      13,
		OutputTokens:     9,
		CacheReadTokens:  4,
		CacheWriteTokens: 5,
		ReasoningTokens:  3,
		TotalTokens:      22,
		UsagePresence: aipapi.UsagePresence{
			InputTokens: true, OutputTokens: true,
			CacheReadTokens: true, CacheWriteTokens: true, ReasoningTokens: true, TotalTokens: true,
		},
		RawUsageJSON: raw,
	}
	aggregateKeys := make(map[string]struct{})
	defaultDetailKeys := make(map[string]struct{})
	for _, measure := range openaiusage.ProviderEvidenceDraft(event, "openai.chat.v2", "bind-src").Measures {
		key, err := measure.Key.Normalize()
		if err != nil {
			t.Fatalf("provider measure key normalize: %v", err)
		}
		switch measure.Key.Component {
		case sdkmetering.ComponentInputToken, sdkmetering.ComponentOutputToken:
			aggregateKeys[key.CanonicalKey()] = struct{}{}
		case sdkmetering.ComponentCacheReadInputToken, sdkmetering.ComponentReasoningOutputToken:
			defaultDetailKeys[key.CanonicalKey()] = struct{}{}
		case sdkmetering.ComponentCacheWriteInputToken:
			// The standard member is emitted under the native detail schema; the
			// extension fallback is emitted under the default inclusion schema.
			// Both edges must be bound, so only the default-inclusion spelling is
			// collected here (the native spelling is bound from NativeUsageMeasures).
			if measure.Key.SchemaID == sdkmetering.DefaultInclusionSchemaID {
				defaultDetailKeys[key.CanonicalKey()] = struct{}{}
			}
		}
	}
	if len(aggregateKeys) != 2 {
		t.Fatalf("adapter aggregate keys = %d, want 2: %+v", len(aggregateKeys), aggregateKeys)
	}
	if len(defaultDetailKeys) != 3 {
		t.Fatalf("adapter default-schema cached/reasoning/cache-write keys = %d, want 3: %+v", len(defaultDetailKeys), defaultDetailKeys)
	}

	childBound, parentBound := 0, 0
	for _, relationship := range schema.Relationships {
		child, _ := relationship.Child.Normalize()
		parent, _ := relationship.Parent.Normalize()
		if _, ok := nativeKeys[child.CanonicalKey()]; ok {
			childBound++
		} else if _, ok := defaultDetailKeys[child.CanonicalKey()]; ok {
			childBound++
		} else {
			t.Fatalf("schema child %s is not bound to any adapter-emitted key", child.CanonicalKey())
		}
		if _, ok := aggregateKeys[parent.CanonicalKey()]; ok {
			parentBound++
		} else {
			t.Fatalf("schema parent %s is not bound to an adapter aggregate key", parent.CanonicalKey())
		}
	}
	if childBound != 10 {
		t.Fatalf("schema children not bound to adapter keys: bound=%d want 10", childBound)
	}
	if parentBound != 10 {
		t.Fatalf("schema parents not bound to adapter aggregate keys: bound=%d want 10", parentBound)
	}
}

// TestNativeUsageInclusionSchemaParticipatesInTariffIdentity proves the frozen
// provider schema is material tariff identity: publishing the same rules with
// and without the schema yields different content hashes, and the returned
// schema slice is a fresh deep copy each call.
func TestNativeUsageInclusionSchemasReturnDeepCopy(t *testing.T) {
	t.Parallel()
	// The tariff-identity half of this test moved to the Enterprise tree with
	// the rating rules (enterprise/internal/preservation).
	first := openaiusage.NativeUsageInclusionSchemas()
	first[0].Relationships[0].Child.Component = "mutated"
	second := openaiusage.NativeUsageInclusionSchemas()
	if second[0].Relationships[0].Child.Component == "mutated" {
		t.Fatal("NativeUsageInclusionSchemas must return a fresh deep copy")
	}
}

// TestOpenAIUsageEvidence_ConflictingFlatAudioAliasDoesNotFlatten proves the
// adapter keeps the native nested detail authoritative when a conflicting flat
// alias is also present. The frozen-partition 74/0 rating half of the original
// vector is Enterprise-owned.
func TestOpenAIUsageEvidence_ConflictingFlatAudioAliasDoesNotFlatten(t *testing.T) {
	t.Parallel()
	chatJSON := `{"id":"chat","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"input_audio_tokens":99,"output_audio_tokens":99,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`
	server := wireUsageServer(t, chatJSON, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "alias"))[0]
	assertDirectionalAudio(t, []sdkmetering.Observation{observation}, map[string]string{
		"input/audio_token":  "3",
		"output/audio_token": "4",
	})
}

// wireVisionChatSuccess is the official Chat CompletionUsage vision shape:
// prompt_tokens/prompt_tokens_details already include directional text/audio
// AND image input tokens. It is the exact-13/9/22 fixture where the prompt
// details text 8 + audio 3 + image 2 account for the ordinary prompt total.
const wireVisionChatSuccess = `{"id":"chat-vision","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":9,"total_tokens":22,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"image_tokens":2},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`

// TestOpenAIUsageEvidence_ChatNestedImageTokensCannotBeSilentlyIgnored is the
// R7-C1A vector, wire half. The real adapter maps
// prompt_tokens_details.image_tokens to a native directional input image token
// measure, and the exact nested wire path is retained in evidence rather than a
// substitute canonical location. The rating halves (unpriced native image stays
// a typed partial with payable child amounts; aggregate+image and
// aggregate+child typed overlap; never the additive 123) are Enterprise-owned.
//
// Evidence note: the exact nested wire path
// $.usage.prompt_tokens_details.image_tokens is allowlisted in the SDK, so the
// retained evidence keeps the provider's true field identity instead of a
// substitute canonical location.
func TestOpenAIUsageEvidence_ChatNestedImageTokensCannotBeSilentlyIgnored(t *testing.T) {
	t.Parallel()
	server := wireUsageServer(t, wireVisionChatSuccess, wireResponsesSuccess)
	defer server.Close()
	callID := mustWireCallID(t)
	observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "vision"))[0]

	assertNativeInputImageTokens(t, observation, "2")
	assertWireEvidence(t, []sdkmetering.Observation{observation}, map[string]string{
		"$.usage.prompt_tokens_details.image_tokens": "2",
	})
	// The substitute canonical location must not be emitted for the nested
	// provider-family quantity: the exact provider wire identity is retained.
	for _, field := range observation.Evidence {
		if field.Path == "$.usage.input_image_tokens" {
			t.Fatalf("nested image token evidence used the substitute canonical path: %+v", field)
		}
	}
}

// TestOpenAIUsageEvidence_MalformedNestedImageTokensCannotComplete is the
// R7-C1B intact-wire vector for present-but-malformed Chat
// prompt_tokens_details.image_tokens shapes. The real adapter must surface an
// explicit unavailable input image token measure, and the raw malformed lexeme
// must never reach evidence. The child-only fail-closed partial rating is
// Enterprise-owned.
func TestOpenAIUsageEvidence_MalformedNestedImageTokensCannotComplete(t *testing.T) {
	t.Parallel()
	shapes := []struct {
		name      string
		imageJSON string
		forbidden string
	}{
		{name: "string", imageJSON: `"oops"`, forbidden: "oops"},
		{name: "negative", imageJSON: `-1`, forbidden: "-1"},
		{name: "fractional", imageJSON: `2.5`, forbidden: "2.5"},
		{name: "overflow", imageJSON: `1e999`, forbidden: "1e999"},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, wireChatVisionUsage(shape.imageJSON, ""), wireResponsesSuccess)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "malformed-"+shape.name))[0]

			assertUnavailableInputImageToken(t, observation)
			for _, field := range observation.Evidence {
				if field.Lexeme == shape.forbidden {
					t.Fatalf("malformed image token lexeme %q reached evidence: %+v", shape.forbidden, field)
				}
			}
		})
	}
}

// TestOpenAIUsageEvidence_MalformedNestedImageWithFlatAliasCannotComplete proves
// a present-but-invalid nested image token field is not silently supplanted by a
// valid compatible flat alias. Whether the flat alias prices a distinct
// component (input_image_count) or the same canonical image token component
// (input_image_tokens), the malformed nested field stays an explicit
// unavailable measure. The child-only fail-closed partial rating is
// Enterprise-owned.
func TestOpenAIUsageEvidence_MalformedNestedImageWithFlatAliasCannotComplete(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		flatAlias string
	}{
		{name: "image_count_alias", flatAlias: `"input_image_count":2,`},
		{name: "image_token_alias", flatAlias: `"input_image_tokens":5,`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := wireUsageServer(t, wireChatVisionUsage(`"oops"`, tc.flatAlias), wireResponsesSuccess)
			defer server.Close()
			callID := mustWireCallID(t)
			observation := openWireObservations(t, server, "chat", wireIdentityForCall(callID, "alias-"+tc.name))[0]

			assertUnavailableInputImageToken(t, observation)
		})
	}
}

// TestOpenAIUsageEvidence_NonCanonicalIntegerNestedImageLexemesCannotComplete is
// the R7-C1B reviewer follow-up intact-wire vector. The nested Chat
// prompt_tokens_details.image_tokens field is a discrete count. The adapter must
// not accept an integer-valued lexeme through a permissive decimal parser and
// then retain the original raw lexeme: the SDK safe-evidence count contract
// requires a canonical unsigned base-10 integer spelling within uint64, so a
// non-canonical raw lexeme would make Observation.Validate reject and the stock
// ProviderEvidenceBuffer drain the ENTIRE observation. This vector drives three
// such lexemes - exponent notation (1e3), a trailing-fraction zero (2.0) and a
// 20-digit value beyond the canonical count bound (99999999999999999999) - with
// no flat companion and with a valid flat companion alias, proving:
//
//   - the observation is not discarded (the intact wire path still yields it);
//   - the present-but-non-canonical nested field stays an explicit unavailable
//     input image token measure rather than a canonicalized observed count;
//   - the raw non-canonical lexeme never reaches evidence;
//   - a valid flat companion alias does not supersede the nested unavailable
//     value.
//
// The child-only fail-closed partial rating is Enterprise-owned.
func TestOpenAIUsageEvidence_NonCanonicalIntegerNestedImageLexemesCannotComplete(t *testing.T) {
	t.Parallel()
	lexemes := []struct {
		name      string
		imageJSON string
		rawLexeme string
	}{
		{name: "scientific_1e3", imageJSON: `1e3`, rawLexeme: "1e3"},
		{name: "trailing_fraction_zero_2_0", imageJSON: `2.0`, rawLexeme: "2.0"},
		{name: "twenty_digit_overflow", imageJSON: `99999999999999999999`, rawLexeme: "99999999999999999999"},
	}
	aliases := []struct {
		name      string
		flatAlias string
	}{
		{name: "without_alias", flatAlias: ""},
		{name: "with_image_token_alias", flatAlias: `"input_image_tokens":5,`},
		{name: "with_image_count_alias", flatAlias: `"input_image_count":2,`},
	}
	for _, lexeme := range lexemes {
		for _, alias := range aliases {
			lexeme, alias := lexeme, alias
			t.Run(lexeme.name+"_"+alias.name, func(t *testing.T) {
				t.Parallel()
				server := wireUsageServer(t, wireChatVisionUsage(lexeme.imageJSON, alias.flatAlias), wireResponsesSuccess)
				defer server.Close()
				callID := mustWireCallID(t)
				observation := openWireObservations(t, server, "chat",
					wireIdentityForCall(callID, "noncanon-"+lexeme.name+"-"+alias.name))[0]

				assertUnavailableInputImageToken(t, observation)
				for _, field := range observation.Evidence {
					if field.Lexeme == lexeme.rawLexeme {
						t.Fatalf("non-canonical count lexeme %q reached evidence: %+v", lexeme.rawLexeme, field)
					}
				}
			})
		}
	}
}

// wireChatVisionUsage builds the exact-13/9/22 Chat vision fixture with the
// supplied raw image_tokens JSON value and an optional flat alias member inside
// the usage object.
func wireChatVisionUsage(imageJSON, flatAlias string) string {
	return `{"id":"chat-vision","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":9,"total_tokens":22,` +
		flatAlias +
		`"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"image_tokens":` + imageJSON + `},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4}}}`
}

func assertUnavailableInputImageToken(t *testing.T, observation sdkmetering.Observation) {
	t.Helper()
	var found bool
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentImageToken || measure.Key.Direction != sdkmetering.DirectionInput {
			continue
		}
		found = true
		if measure.Quality != sdkmetering.QualityUnavailable {
			t.Fatalf("input image_token measure quality=%q, want unavailable: %+v", measure.Quality, measure)
		}
		if measure.Value != nil {
			t.Fatalf("unavailable input image_token measure carries a value: %+v", measure.Value)
		}
	}
	if !found {
		t.Fatalf("no explicit unavailable input image_token measure: %+v", observation.Measures)
	}
}

// assertNativeInputImageTokens asserts the native input image token measure
// value and unit.
func assertNativeInputImageTokens(t *testing.T, observation sdkmetering.Observation, want string) {
	t.Helper()
	for _, measure := range observation.Measures {
		if measure.Key.Component != sdkmetering.ComponentImageToken || measure.Key.Direction != sdkmetering.DirectionInput {
			continue
		}
		if measure.Key.Unit != sdkmetering.UnitToken {
			t.Fatalf("native input image_token unit = %q, want %q", measure.Key.Unit, sdkmetering.UnitToken)
		}
		if measure.Value == nil || measure.Value.Coefficient != want {
			t.Fatalf("native input image_token = %+v, want %s", measure.Value, want)
		}
		return
	}
	t.Fatalf("native input image_token measure missing: %+v", observation.Measures)
}

func wireUsageServer(t *testing.T, chatJSON, responsesJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/completions":
			_, _ = io.WriteString(w, chatJSON)
		case "/responses":
			_, _ = io.WriteString(w, responsesJSON)
		default:
			http.NotFound(w, r)
		}
	}))
}

func wireIdentityForCall(callID usageevidence.BillingCallID, label string) coremetering.ObservationIdentity {
	return coremetering.ObservationIdentity{
		StoreID: "store", RequestID: label + "-request",
		CallID: callID.String(), BillingCallID: callID.String(),
		ALegID: "a-leg", BLegID: "b-leg", AttemptID: "b-leg",
	}
}

func mustWireCallID(t *testing.T) usageevidence.BillingCallID {
	t.Helper()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatalf("NewBillingCallID: %v", err)
	}
	return callID
}
