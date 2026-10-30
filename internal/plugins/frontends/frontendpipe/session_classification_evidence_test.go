package frontendpipe_test

// Task 7.2 differential fixtures: a certified large-body proof compiler must
// compile the *same* bounded classification evidence the canonical request path
// compiles for the identical client turn (requirements 3.9, 5.2, 5.3, 5.4, 5.5,
// 11.1, 11.2, 12.7).
//
// The canonical side of every comparison is derived from a real canonical
// aipapi.Call produced by the lane's own decoder, using exactly the primitives
// the canonical runtime evidence builder uses (see
// TestCanonicalEvidenceDerivationSourceStaysPinned). The wire side is whatever
// the certified profile put into largebody.Proof.ClassificationEvidence. The two
// sides are then compared bit-for-bit with require.Equal, which compares the
// whole three-field SDK value: operation, accepted identity string and every
// category bit.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// The certified profile ids, reused so no fixture can silently target a lane it
// does not exercise.
const (
	laneOpenAIResponses = "openai_responses_v1"
	laneOpenAIChat      = "openai_chat_v1"
	laneOpenResponses   = "openresponses_v1"
)

const (
	// evidenceRouteSelector is a stable route for every lane so the differential
	// fixture compares classification input only, never routing.
	evidenceRouteSelector = "stub:differential"

	// evidenceUserAgent is a bounded, control-free coding-harness identity that
	// the shared acceptance policy keeps intact.
	evidenceUserAgent = "codex-cli/0.20.0 (linux; x86_64)"

	// emptyToolParameters is the smallest tool schema both the canonical decoder
	// and the certified proof compiler accept.
	emptyToolParameters = `{"type":"object","properties":{}}`

	// largeTurnCount and largeTurnWidth keep the streamed fixture comfortably
	// above 2x the reduced fact budget these tests install, so the profile takes
	// its chunked streaming branch instead of buffering the whole envelope.
	largeTurnCount = 8
	largeTurnWidth = 4096

	// largeBodyFactBudget is the reduced semantic-fact budget the streamed cases
	// install. It forces the large-array/chunked branches while keeping the whole
	// fixture small enough for an ordinary unit test.
	largeBodyFactBudget = 4 * 1024
)

// evidenceLane describes one certified large-body profile together with the
// canonical decoder for the same request, so both paths observe one client turn.
type evidenceLane struct {
	laneID   string
	profile  frontendpipe.FrontendProfile
	urlPath  string
	decode   func(t *testing.T, body []byte, headers http.Header) *aipapi.Call
	smallReq func(toolsJSON string) []byte
	largeReq func(toolsJSON string) []byte
}

func evidenceLanes() []evidenceLane {
	return []evidenceLane{
		{
			laneID:  laneOpenAIResponses,
			profile: openairesponses.NewProfile(),
			urlPath: "/v1/responses",
			decode: func(t *testing.T, body []byte, headers http.Header) *aipapi.Call {
				t.Helper()
				decoded, err := openairesponses.DecodeCreateRequest(body, openairesponses.DecodeOptions{
					RouteSelector: evidenceRouteSelector,
					Headers:       headers,
				})
				require.NoError(t, err)
				return decoded.Call
			},
			smallReq: func(toolsJSON string) []byte {
				return fmt.Appendf(nil,
					`{"model":"gpt-4o","input":"hello","tools":%s}`, toolsJSON)
			},
			largeReq: func(toolsJSON string) []byte {
				return fmt.Appendf(nil,
					`{"model":"gpt-4o","input":%s,"tools":%s}`,
					largeTurnArray(responsesTurnJSON), toolsJSON)
			},
		},
		{
			laneID:  laneOpenAIChat,
			profile: openailegacy.NewProfile(),
			urlPath: "/v1/chat/completions",
			decode: func(t *testing.T, body []byte, headers http.Header) *aipapi.Call {
				t.Helper()
				decoded, err := openailegacy.DecodeChatRequest(body, openailegacy.DecodeOptions{
					RouteSelector: evidenceRouteSelector,
					Headers:       headers,
				})
				require.NoError(t, err)
				return decoded.Call
			},
			smallReq: func(toolsJSON string) []byte {
				return fmt.Appendf(nil,
					`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"tools":%s}`,
					toolsJSON)
			},
			largeReq: func(toolsJSON string) []byte {
				return fmt.Appendf(nil,
					`{"model":"gpt-4o","messages":%s,"tools":%s}`,
					largeTurnArray(chatTurnJSON), toolsJSON)
			},
		},
		{
			laneID:  laneOpenResponses,
			profile: openresponses.NewProfile(),
			urlPath: "/openresponses/v1/responses",
			decode: func(t *testing.T, body []byte, headers http.Header) *aipapi.Call {
				t.Helper()
				decoded, err := openresponses.AuthenticateAndDecodeCreate(
					context.Background(), body, openresponses.DecodeCreateOptions{
						RouteSelector: evidenceRouteSelector,
						Headers:       headers,
					})
				require.NoError(t, err)
				return decoded.Call
			},
			smallReq: func(toolsJSON string) []byte {
				return fmt.Appendf(nil,
					`{"model":"gpt-4o","input":"hello","store":false,"tools":%s}`, toolsJSON)
			},
			largeReq: func(toolsJSON string) []byte {
				return fmt.Appendf(nil,
					`{"model":"gpt-4o","input":%s,"store":false,"tools":%s}`,
					largeTurnArray(openResponsesTurnJSON), toolsJSON)
			},
		},
	}
}

// Per-lane streamed turn shapes. Each certified profile declines streamed
// history it cannot represent exactly, so the differential fixture uses the
// narrowest shape that still exceeds the reduced fact budget and therefore takes
// the chunked proof branch:
//
//   - OpenAI Chat Completions accepts only plain role/content messages; the
//     developer and tool roles are canonical-repair shapes.
//   - OpenAI Responses accepts only role/content/type items; "status" is an
//     unsupported item field on this lane.
//   - OpenResponses is item-authoritative and requires every streamed item to be
//     a completed message with no item id.
func chatTurnJSON(i int) string {
	return fmt.Sprintf(`{"role":"user","content":%q}`, largeTurnText(i))
}

func responsesTurnJSON(i int) string {
	return fmt.Sprintf(`{"role":"user","type":"message","content":%q}`, largeTurnText(i))
}

func openResponsesTurnJSON(i int) string {
	return fmt.Sprintf(`{"type":"message","role":"user","status":"completed","content":%q}`, largeTurnText(i))
}

func largeTurnText(i int) string {
	return fmt.Sprintf("turn-%02d-%s", i, strings.Repeat("a", largeTurnWidth))
}

// largeTurnArray builds a streamed (non-string) turn array whose serialized size
// exceeds twice the reduced fact budget these fixtures install, so the profile
// never buffers the whole envelope and must derive its evidence from the
// streaming scan instead.
func largeTurnArray(turn func(int) string) string {
	var b strings.Builder
	b.WriteString(`[`)
	for i := range largeTurnCount {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(turn(i))
	}
	b.WriteString(`]`)
	return b.String()
}

// laneToolEntry renders one tool definition in the shape the lane's canonical
// decoder and its certified proof compiler share. OpenAI Chat Completions nests
// the function object; OpenAI Responses and OpenResponses accept it flat.
func laneToolEntry(laneID, name, description, parameters string) string {
	if laneID == laneOpenAIChat {
		return fmt.Sprintf(
			`{"type":"function","function":{"name":%q,"description":%q,"parameters":%s}}`,
			name, description, parameters)
	}
	return fmt.Sprintf(
		`{"type":"function","name":%q,"description":%q,"parameters":%s}`,
		name, description, parameters)
}

// laneNamedTools builds a tools array whose only varying fact is the tool name,
// which is all the classification accumulator may observe.
func laneNamedTools(laneID string, names ...string) string {
	entries := make([]string, 0, len(names))
	for _, name := range names {
		entries = append(entries, laneToolEntry(laneID, name, "d", emptyToolParameters))
	}
	return "[" + strings.Join(entries, ",") + "]"
}

// compileLaneProof runs the certified profile proof compiler over body. A
// factBudget of 0 keeps the configured default.
func compileLaneProof(t *testing.T, lane evidenceLane, body []byte, headers http.Header, factBudget int64) frontendpipe.ProofOutput {
	t.Helper()

	source, err := largebody.NewCompletedSource(largebody.CompletedSourceConfig{
		Memory: body,
		Size:   int64(len(body)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = source.Close() })

	ctx := context.Background()
	if factBudget > 0 {
		ctx = largebody.WithSemanticFactBudget(ctx, factBudget)
	}
	output, err := lane.profile.CompileProof(ctx, frontendpipe.ProofInput{
		Ctx:                  ctx,
		Headers:              headers,
		URLPath:              lane.urlPath,
		RouteSelector:        evidenceRouteSelector,
		RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
		DefaultRouteSelector: "stub:default",
		RouteFromBodyModel:   true,
		Source:               source,
		BodyBytes:            int64(len(body)),
	})
	require.NoError(t, err, "certified profile %s must accept its own differential fixture", lane.laneID)
	return output
}

// canonicalEvidenceFromCall mirrors internal/core/runtime.sessionClassificationEvidence
// exactly: the canonical evidence for a client turn is the canonical operation,
// the accepted canonical client User-Agent, and the canonical tool-name category
// bits. Only the identity acceptance and the tool accumulator are used, so no
// message, instruction, tool description or tool parameter can influence it.
//
// TestCanonicalEvidenceDerivationSourceStaysPinned fails if the canonical
// builder stops deriving from these three inputs, so this mirror cannot silently
// drift away from the canonical path it claims to be differential against.
func canonicalEvidenceFromCall(call *aipapi.Call) sessionclassification.Evidence {
	categories := sessionclassification.ToolCategorySet(0)
	for _, tool := range call.Tools {
		categories = categories.AddToolName(tool.Name)
	}
	userAgent, ok := identity.AcceptClientUserAgent(call.Invocation.ClientUserAgent)
	if !ok {
		userAgent = ""
	}
	return sessionclassification.Evidence{
		Operation:       call.Invocation.Operation,
		ClientUserAgent: userAgent,
		ToolCategories:  categories,
	}
}

// headersWithUserAgent builds the request headers for one case. An empty
// userAgent leaves the header absent, which is a legal client turn.
func headersWithUserAgent(userAgent string) http.Header {
	headers := make(http.Header)
	if userAgent != "" {
		headers.Set("User-Agent", userAgent)
	}
	return headers
}

// canonicalIdentityKind names the acceptance outcome of a compiled identity so a
// table row can state "accepted", "absent" or "rejected" without restating the
// shared policy.
func canonicalIdentityKind(t *testing.T, headers http.Header, accepted string) string {
	t.Helper()
	if accepted != "" {
		if _, ok := identity.AcceptClientUserAgent(accepted); !ok {
			t.Fatalf("compiled identity %q is not an accepted canonical User-Agent", accepted)
		}
		return "accepted"
	}
	// An empty compiled identity is legal either way: the client sent no
	// User-Agent at all, or it sent one the shared acceptance policy refused.
	// Only the former is "absent"; the latter is a deliberate rejection.
	if headers.Get("User-Agent") == "" {
		return "absent"
	}
	return "rejected"
}

// TestCertifiedProfilesCompileCanonicalEquivalentEvidence is the load-bearing
// differential: for one client turn observed twice - once through the lane's
// canonical decoder and once through the certified streaming proof compiler -
// both paths must compile a bit-for-bit identical sessionclassification.Evidence.
//
// Every case is a distinct load-bearing property:
//   - accepted identity plus a full coding tool cluster;
//   - the same tool catalog in a different field order, because the accumulator
//     is a bitset and order is not a fact;
//   - an absent User-Agent, which is legal and yields an empty identity;
//   - a User-Agent the shared acceptance policy rejects (control characters and
//     an over-long value), which must be dropped rather than truncated;
//   - an unknown tool name, which is legal and only records unknown_seen.
func TestCertifiedProfilesCompileCanonicalEquivalentEvidence(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		userAgent  string
		toolNames  []string
		reordered  []string
		wantBits   sessionclassification.ToolCategorySet
		wantUAKind string
	}{
		{
			name:      "accepted identity and full coding tool cluster",
			userAgent: evidenceUserAgent,
			toolNames: []string{"read_file", "grep", "bash", "apply_patch"},
			reordered: []string{"apply_patch", "bash", "grep", "read_file"},
			wantBits: sessionclassification.ToolCategoryFileRead |
				sessionclassification.ToolCategoryFileSearch |
				sessionclassification.ToolCategoryOSCommand |
				sessionclassification.ToolCategoryFileEdit,
			wantUAKind: "accepted",
		},
		{
			name:       "absent user agent is legal",
			userAgent:  "",
			toolNames:  []string{"read_file"},
			reordered:  []string{"read_file"},
			wantBits:   sessionclassification.ToolCategoryFileRead,
			wantUAKind: "absent",
		},
		{
			// Whitespace padding is the only input class where the openresponses
			// decoder's bare strings.TrimSpace capture is not trivially identical
			// to a direct capture. Both routes must still accept it identically, so
			// this row pins AcceptClientUserAgent∘TrimSpace == AcceptClientUserAgent
			// — the invariant the openresponses repair deferral rests on.
			name:       "whitespace padded user agent is accepted identically on both routes",
			userAgent:  "  " + evidenceUserAgent + "  ",
			toolNames:  []string{"bash"},
			reordered:  []string{"bash"},
			wantBits:   sessionclassification.ToolCategoryOSCommand,
			wantUAKind: "accepted",
		},
		{
			name:       "rejected control-character user agent is dropped",
			userAgent:  "codex-cli/0.20.0\r\nX-Injected: 1",
			toolNames:  []string{"bash"},
			reordered:  []string{"bash"},
			wantBits:   sessionclassification.ToolCategoryOSCommand,
			wantUAKind: "rejected",
		},
		{
			name:       "rejected over-long user agent is dropped",
			userAgent:  "codex-cli/" + strings.Repeat("u", identity.MaxUserAgentBytes),
			toolNames:  []string{"bash"},
			reordered:  []string{"bash"},
			wantBits:   sessionclassification.ToolCategoryOSCommand,
			wantUAKind: "rejected",
		},
		{
			name:       "unknown tool name only records unknown_seen",
			userAgent:  evidenceUserAgent,
			toolNames:  []string{"totally_unknown_harness_tool"},
			reordered:  []string{"totally_unknown_harness_tool"},
			wantBits:   sessionclassification.ToolCategoryUnknownSeen,
			wantUAKind: "accepted",
		},
		{
			name:       "single generic shell tool stays narrow",
			userAgent:  "curl/8.5.0",
			toolNames:  []string{"shell_command"},
			reordered:  []string{"shell_command"},
			wantBits:   sessionclassification.ToolCategoryOSCommand,
			wantUAKind: "accepted",
		},
		{
			name:       "duplicated tool names do not accumulate",
			userAgent:  evidenceUserAgent,
			toolNames:  []string{"read_file", "read_file", "read_file"},
			reordered:  []string{"read_file"},
			wantBits:   sessionclassification.ToolCategoryFileRead,
			wantUAKind: "accepted",
		},
	}

	for _, lane := range evidenceLanes() {
		t.Run(lane.laneID, func(t *testing.T) {
			t.Parallel()

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()

					headers := headersWithUserAgent(tc.userAgent)
					body := lane.smallReq(laneNamedTools(lane.laneID, tc.toolNames...))

					canonical := canonicalEvidenceFromCall(lane.decode(t, body, headers))
					proof := compileLaneProof(t, lane, body, headers, 0).Proof()

					// The canonical derivation itself is pinned, so a lane that
					// silently dropped its tool catalog or its identity cannot make
					// the differential comparison vacuously equal.
					require.False(t, canonical.IsZero(), "canonical evidence must be compiled")
					require.Equal(t, tc.wantBits, canonical.ToolCategories)
					require.Equal(t, tc.wantUAKind, canonicalIdentityKind(t, headers, canonical.ClientUserAgent))

					// Bit-for-bit parity between the canonical call and the wire proof.
					require.Equal(t, canonical, proof.ClassificationEvidence,
						"certified profile must compile canonical-equivalent evidence (requirements 5.3, 12.7)")
					require.Equal(t, proof.Operation, proof.ClassificationEvidence.Operation)

					// Tool field order is not a fact: the reordered tool catalog must
					// compile the identical value.
					reorderedBody := lane.smallReq(laneNamedTools(lane.laneID, tc.reordered...))
					reorderedCanonical := canonicalEvidenceFromCall(lane.decode(t, reorderedBody, headers))
					reorderedProof := compileLaneProof(t, lane, reorderedBody, headers, 0).Proof()
					require.Equal(t, canonical, reorderedCanonical)
					require.Equal(t, proof.ClassificationEvidence, reorderedProof.ClassificationEvidence,
						"tool field order must not change the compiled evidence")
				})
			}
		})
	}
}

// TestCertifiedProfilesCompileCanonicalEquivalentEvidenceForStreamedBodies runs
// the same differential on the chunked streaming proof branches: a request whose
// serialized envelope exceeds the installed semantic-fact budget is never
// buffered whole, so evidence compiled only from a small-envelope buffer would
// be missing or wrong for exactly the requests that most need classification
// (requirements 5.2, 5.3, 12.7).
func TestCertifiedProfilesCompileCanonicalEquivalentEvidenceForStreamedBodies(t *testing.T) {
	t.Parallel()

	const toolName = "read_file"

	for _, lane := range evidenceLanes() {
		t.Run(lane.laneID, func(t *testing.T) {
			t.Parallel()

			headers := headersWithUserAgent(evidenceUserAgent)
			body := lane.largeReq(laneNamedTools(lane.laneID, toolName))
			require.Greater(t, int64(len(body)), int64(2*largeBodyFactBudget),
				"the streamed fixture must exceed twice the reduced fact budget")

			canonical := canonicalEvidenceFromCall(lane.decode(t, body, headers))
			proof := compileLaneProof(t, lane, body, headers, largeBodyFactBudget).Proof()

			// Every streamed turn is observed. Because the envelope cannot be
			// buffered, this also proves the streamed scan actually ran instead of
			// reading an envelope buffer already discarded for being oversized.
			require.Len(t, proof.Turn.Items, largeTurnCount,
				"the streamed scan must observe every turn of an envelope it cannot buffer")
			require.Equal(t, sessionclassification.ToolCategoryFileRead, canonical.ToolCategories)
			require.Equal(t, canonical, proof.ClassificationEvidence,
				"the streamed proof branch must compile canonical-equivalent evidence")
			require.NoError(t, proof.Validate(largebody.SemanticFactBudget(
				largebody.WithSemanticFactBudget(context.Background(), largeBodyFactBudget))))
		})
	}
}

// TestCertifiedProfileEvidenceRetainsNoToolDefinitionContent proves the proof
// cannot carry tool material: two requests that differ only in tool
// descriptions and parameter schemas must compile identical evidence and charge
// identical aggregate proof facts. Only the fixed category bitset and the
// bounded accepted identity may survive tool scanning (requirements 5.2, 5.5,
// 12.7).
func TestCertifiedProfileEvidenceRetainsNoToolDefinitionContent(t *testing.T) {
	t.Parallel()

	const descriptionBytes = 512
	toolNames := []string{"read_file", "bash", "delete_file"}

	// build returns a complete lane request whose tool definitions carry the
	// supplied description and parameter schema. Only the tool names stay fixed,
	// so the two requests differ in exactly the material the proof must not keep.
	build := func(lane evidenceLane, description, parameters string) []byte {
		entries := make([]string, 0, len(toolNames))
		for _, name := range toolNames {
			entries = append(entries, laneToolEntry(lane.laneID, name, description, parameters))
		}
		return lane.smallReq("[" + strings.Join(entries, ",") + "]")
	}

	for _, lane := range evidenceLanes() {
		t.Run(lane.laneID, func(t *testing.T) {
			t.Parallel()

			small := build(lane, "d", emptyToolParameters)
			large := build(lane, strings.Repeat("D", descriptionBytes),
				fmt.Sprintf(`{"type":"object","description":%q,"properties":{"a":{"type":"string"}}}`,
					strings.Repeat("P", descriptionBytes)))
			require.NotEqual(t, small, large)

			headers := headersWithUserAgent(evidenceUserAgent)

			// The canonical calls really do carry the extra tool material, so the
			// equality asserted below is about the wire carrier rather than about
			// two identical fixtures.
			smallCanonical := canonicalEvidenceFromCall(lane.decode(t, small, headers))
			largeCanonical := canonicalEvidenceFromCall(lane.decode(t, large, headers))
			require.Equal(t, smallCanonical, largeCanonical,
				"canonical evidence is name-driven, so identical tool names must agree")
			require.Equal(t, sessionclassification.ToolCategoryFileRead|
				sessionclassification.ToolCategoryOSCommand|
				sessionclassification.ToolCategoryFileRemove,
				smallCanonical.ToolCategories)

			smallProof := compileLaneProof(t, lane, small, headers, 0).Proof()
			largeProof := compileLaneProof(t, lane, large, headers, 0).Proof()

			require.Equal(t, smallCanonical, smallProof.ClassificationEvidence)
			require.Equal(t, smallProof.ClassificationEvidence, largeProof.ClassificationEvidence,
				"tool descriptions and parameter schemas must not reach the proof")
			require.Equal(t, smallProof.AggregateFactBytes(), largeProof.AggregateFactBytes(),
				"tool definition bytes must not be charged to proof metadata")
			require.Equal(t, int64(len(evidenceUserAgent))+sessionclassification.ToolCategorySetBytes,
				smallProof.ClassificationEvidence.MetadataBytes(),
				"evidence cost is the accepted identity length plus the fixed bitset width")
		})
	}
}

// TestCertifiedProfileEvidenceSurvivesWireFactsPropagation ties the three
// certified lanes to the provider-neutral wire carrier: the compiled evidence
// must reach WireTurnFacts unchanged, still without a shadow canonical call.
func TestCertifiedProfileEvidenceSurvivesWireFactsPropagation(t *testing.T) {
	t.Parallel()

	const toolName = "read_file"

	for _, lane := range evidenceLanes() {
		t.Run(lane.laneID, func(t *testing.T) {
			t.Parallel()

			headers := headersWithUserAgent(evidenceUserAgent)
			body := lane.smallReq(laneNamedTools(lane.laneID, toolName))
			canonical := canonicalEvidenceFromCall(lane.decode(t, body, headers))
			proof := compileLaneProof(t, lane, body, headers, 0).Proof()

			stamp, err := largebody.BindAssessmentStamp("gen-session-classification-wire", proof)
			require.NoError(t, err)
			facts, err := largebody.NewWireTurnFactsFromProof(proof, stamp,
				"req-evidence", "trace-evidence", "bill-evidence")
			require.NoError(t, err)
			require.NoError(t, facts.AssertNoShadowCall())
			require.Equal(t, canonical, facts.Session.ClassificationEvidence,
				"the wire carrier must observe the canonical-equivalent evidence")
		})
	}
}

// TestCanonicalEvidenceDerivationSourceStaysPinned keeps the differential
// fixtures honest. They claim to compare against the canonical path, but they
// cannot call the canonical builder (it is unexported, and generic core must not
// depend on frontends). Instead they pin the canonical builder's derivation
// inputs in source: if a future change GROSSLY rewrites the derivation — dropping
// the canonical operation, the accepted canonical User-Agent or the canonical
// tool-name accumulator entirely — this fails and forces the fixture to be
// revisited instead of silently passing (requirements 11.1, 11.2, 12.7).
//
// Scope limit, stated honestly: this is a substring sentinel, so it detects gross
// rewrites only. It does NOT catch semantic drift that keeps every pinned
// substring present, such as an extra unconditional category bit or a suffix
// appended to the tool name before classification. The exact output of the
// canonical builder is pinned by internal/core/runtime's
// TestSessionClassificationStageBuildsCanonicalEvidence,
// TestSessionClassificationEvidenceIgnoresRequestContent and
// TestSessionClassificationEvidenceRejectsUnacceptedUserAgent; those are the
// tests that own canonical derivation semantics, not this one.
func TestCanonicalEvidenceDerivationSourceStaysPinned(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "core", "runtime", "executor_session_classification.go"))
	require.NoError(t, err)
	text := string(source)

	for _, want := range []string{
		"func sessionClassificationEvidence(call *aipapi.Call) sessionclassification.Evidence",
		"identity.AcceptClientUserAgent(call.Invocation.ClientUserAgent)",
		"categories.AddToolName(tool.Name)",
		"range call.Tools",
		"Operation:       call.Invocation.Operation",
	} {
		require.Contains(t, text, want,
			"the canonical evidence builder changed its derivation inputs; the wire differential fixtures must be updated")
	}
}
