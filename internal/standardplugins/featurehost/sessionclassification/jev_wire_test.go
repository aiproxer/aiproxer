package sessionclassification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/agentfacts"
	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// jevMaximalRemoteInput is the largest remote input the closed vocabularies can
// express: every defined tool bit is set and every string member is populated.
// Wire-shape assertions use it so a test cannot pass by inspecting an unusually
// small payload.
func jevMaximalRemoteInput() featurestate.RemoteInput {
	return featurestate.RemoteInput{
		Operation:          aipapi.OperationContextCompaction,
		ClientFamily:       agentfacts.FamilyOpenCode,
		HasAmbiguousClient: false,
		ToolCategories:     sdkclassification.DefinedToolCategoryBits,
		WorkspaceClass:     featurestate.WorkspaceClassProjectMarker,
		LocalEvidenceCode:  featurestate.EvidenceCodeDistinctCluster,
	}
}

// TestJevStateCoversEveryRemoteInputField is the closed-surface proof: the
// serialized state must name every field of the frozen remote contract and
// nothing else. Adding a field to RemoteInput without a derived representation
// fails this test instead of silently dropping the fact (requirements 7.1, 7.2,
// 11.5).
func TestJevStateCoversEveryRemoteInputField(t *testing.T) {
	t.Parallel()

	contractFields := reflect.TypeFor[featurestate.RemoteInput]()
	if got, want := len(jevStateMembers), contractFields.NumField(); got != want {
		t.Fatalf("state declares %d derived members for %d remote input fields (%v)",
			got, want, slices.Sorted(maps.Keys(jevStateMembers)))
	}
	encoded := jevStateJSONKeys()
	for field := range contractFields.Fields() {
		name := field.Name
		member, ok := jevStateMembers[name]
		if !ok {
			t.Errorf("remote input field %q has no derived state member", name)
			continue
		}
		if !slices.Contains(encoded, member) {
			t.Errorf("derived state member %q for field %q is not serialized", member, name)
		}
	}
	for field, member := range jevStateMembers {
		if member == "" {
			t.Errorf("remote input field %q has an empty derived member name", field)
			continue
		}
		if !slices.Contains(encoded, member) {
			t.Errorf("declared state member %q for field %q is not serialized", member, field)
		}
	}
}

// TestJevStateCarriesOnlyDerivedFacts asserts the state is exactly the six
// bounded derived members and nothing resembling request content, and that every
// member is fed from its own remote input field rather than defaulted
// (requirements 7.1, 7.2, 7.3; design.md "Remote egress").
func TestJevStateCarriesOnlyDerivedFacts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		input          featurestate.RemoteInput
		want           map[string]any
		wantCategories []string
	}{
		{
			name:  "populated evidence",
			input: jevMaximalRemoteInput(),
			want: map[string]any{
				"operation":            string(aipapi.OperationContextCompaction),
				"client_family":        string(agentfacts.FamilyOpenCode),
				"has_ambiguous_client": false,
				"workspace_class":      string(featurestate.WorkspaceClassProjectMarker),
				"local_evidence_code":  string(featurestate.EvidenceCodeDistinctCluster),
			},
			wantCategories: []string{
				"file_read", "file_search", "os_command",
				"file_edit", "file_remove", "web_access", "unknown_seen",
			},
		},
		{
			name: "no evidence at all",
			input: featurestate.RemoteInput{
				Operation: aipapi.OperationAnthropicMessages,
			},
			want: map[string]any{
				"operation":            string(aipapi.OperationAnthropicMessages),
				"client_family":        "",
				"has_ambiguous_client": false,
				"workspace_class":      "",
				"local_evidence_code":  "",
			},
			wantCategories: []string{},
		},
		{
			name: "an ambiguous client identity",
			input: featurestate.RemoteInput{
				Operation:          aipapi.OperationOpenResponsesCreate,
				HasAmbiguousClient: true,
				ToolCategories:     sdkclassification.ToolCategoryFileSearch,
			},
			want: map[string]any{
				"operation":            string(aipapi.OperationOpenResponsesCreate),
				"client_family":        "",
				"has_ambiguous_client": true,
				"workspace_class":      "",
				"local_evidence_code":  "",
			},
			wantCategories: []string{"file_search"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(newJevState(tc.input))
			if err != nil {
				t.Fatalf("marshal state: %v", err)
			}
			got := decodeJSONObject(t, encoded)

			wantKeys := []string{
				"client_family",
				"has_ambiguous_client",
				"local_evidence_code",
				"operation",
				"tool_categories",
				"workspace_class",
			}
			if keys := sortedKeys(got); !slices.Equal(keys, wantKeys) {
				t.Fatalf("state keys = %v, want exactly %v", keys, wantKeys)
			}
			for member, want := range tc.want {
				if got[member] != want {
					t.Errorf("state[%q] = %#v, want %#v", member, got[member], want)
				}
			}
			categories, ok := got["tool_categories"].([]any)
			if !ok {
				t.Fatalf("tool_categories is %T, want a JSON array", got["tool_categories"])
			}
			gotCategories := make([]string, 0, len(categories))
			for _, entry := range categories {
				name, ok := entry.(string)
				if !ok {
					t.Fatalf("tool_categories entry is %T, want a string", entry)
				}
				gotCategories = append(gotCategories, name)
			}
			if !slices.Equal(gotCategories, tc.wantCategories) {
				t.Fatalf("tool_categories = %v, want %v", gotCategories, tc.wantCategories)
			}
		})
	}
}

// TestJevToolCategoryNamesStayInFixedOrder pins the derived category vocabulary
// and its ordering: an unordered, renamed, or widened set would make the egress
// payload unstable and could put an unexpected name on the wire.
func TestJevToolCategoryNamesStayInFixedOrder(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		categories sdkclassification.ToolCategorySet
		want       []string
	}{
		{name: "none", categories: 0, want: []string{}},
		{name: "read only", categories: sdkclassification.ToolCategoryFileRead, want: []string{"file_read"}},
		{
			name:       "edit and web",
			categories: sdkclassification.ToolCategoryFileEdit | sdkclassification.ToolCategoryWebAccess,
			want:       []string{"file_edit", "web_access"},
		},
		{
			name:       "every defined category",
			categories: sdkclassification.DefinedToolCategoryBits,
			want: []string{
				"file_read", "file_search", "os_command",
				"file_edit", "file_remove", "web_access", "unknown_seen",
			},
		},
		{
			name:       "an undefined bit is never named",
			categories: sdkclassification.DefinedToolCategoryBits | 1<<15,
			want: []string{
				"file_read", "file_search", "os_command",
				"file_edit", "file_remove", "web_access", "unknown_seen",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := jevCategoryNames(tc.categories)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("jevCategoryNames(%d) = %v, want %v", tc.categories, got, tc.want)
			}
			if got == nil {
				t.Fatal("jevCategoryNames must return an empty slice, never nil, so the member is always a JSON array")
			}
		})
	}
}

// TestJevMarshalRequestSendsOnlyDerivedEvidence is the end-to-end request-shape
// proof: the exact egress document is pinned byte for byte, so no member can be
// added, removed, or filled with content without this test failing
// (requirements 7.1, 7.2).
// The expected document spells every byte out, including the adapter-owned question
// text, so neither a member nor the wording of the question can change without this
// test failing (requirements 7.1, 7.2).
func TestJevMarshalRequestSendsOnlyDerivedEvidence(t *testing.T) {
	t.Parallel()

	body, err := jevMarshalRequest(jevMaximalRemoteInput(), jevModelLatest)
	if err != nil {
		t.Fatalf("jevMarshalRequest: %v", err)
	}
	want := `{"state":{"operation":"context.compaction","client_family":"opencode",` +
		`"has_ambiguous_client":false,"tool_categories":["file_read","file_search","os_command",` +
		`"file_edit","file_remove","web_access","unknown_seen"],"workspace_class":"project_marker",` +
		`"local_evidence_code":"tooling.distinct_coding_cluster"},"model":"jev-latest",` +
		`"questions":{"is_coding_agent_session":{"type":"noul","instructions":"Does this ` +
		`derived session metadata describe an autonomous coding agent harness making ` +
		`this request? The metadata holds only bounded derived facts: the API operation, ` +
		`the normalized client family or an ambiguity bit, canonical tool categories, ` +
		`a workspace marker summary, and the decisive local evidence code. It contains ` +
		`no prompt, transcript, or request content."}}}`
	if string(body) != want {
		t.Fatalf("request body\n got: %s\nwant: %s", body, want)
	}
}

// TestJevRequestBodyCarriesNoSessionContent asserts the egress document contains
// no prompt, transcript, reasoning, tool argument, path, resume token, provider
// credential, secret finding, or raw header material (requirements 7.1, 7.2,
// 7.3, 7.5).
func TestJevRequestBodyCarriesNoSessionContent(t *testing.T) {
	t.Parallel()

	body, err := jevMarshalRequest(jevMaximalRemoteInput(), jevModelLatest)
	if err != nil {
		t.Fatalf("jevMarshalRequest: %v", err)
	}
	document := string(body)
	forbidden := []string{
		"/Users/", "/home/", "\\Users\\", ".git/", "go.mod", "package main",
		"Bearer ", "authorization", "Authorization", "x-api-key", "resume_token", "session_id",
		"User-Agent", "system-reminder", "<prompt>", "reasoning", "tool_args", "tool_arguments",
		"secret", "api_key", "sk-", "BEGIN ",
	}
	for _, marker := range forbidden {
		if strings.Contains(document, marker) {
			t.Errorf("egress request leaks %q: %s", marker, document)
		}
	}
	if len(body) > jevMaxRequestBytes {
		t.Errorf("egress request is %d bytes, above the %d byte bound", len(body), jevMaxRequestBytes)
	}
	if strings.ContainsAny(document, "\n\r\t") {
		t.Errorf("egress request carries whitespace a header could smuggle: %q", document)
	}
}

// TestJevQuestionTextIsAFixedConstant proves the adapter-owned question is a
// bounded literal that interpolates nothing: no format verb, no template
// placeholder, and no per-turn variation (requirement 7.2).
func TestJevQuestionTextIsAFixedConstant(t *testing.T) {
	t.Parallel()

	if jevInstruction == "" {
		t.Fatal("the vendor question text must not be empty")
	}
	if len(jevInstruction) > jevMaxInstructionBytes {
		t.Fatalf("question text is %d bytes, above the %d byte bound", len(jevInstruction), jevMaxInstructionBytes)
	}
	for _, forbidden := range []string{"%", "{{", "}}", "${"} {
		if strings.Contains(jevInstruction, forbidden) {
			t.Errorf("question text carries the interpolation marker %q", forbidden)
		}
	}
	if jevQuestionID != "is_coding_agent_session" {
		t.Fatalf("question id = %q, want the documented single-question id", jevQuestionID)
	}
	if jevQuestionTypeNoul != "noul" {
		t.Fatalf("question type = %q, want the documented noul type", jevQuestionTypeNoul)
	}
	if jevModelLatest != "jev-latest" {
		t.Fatalf("model = %q, want the vendor's documented alias", jevModelLatest)
	}
}

// TestJevMarshalRequestRefusesOversizedRequest proves the egress size bound is
// enforced before a request exists, not merely assumed from the closed
// vocabularies.
func TestJevMarshalRequestRefusesOversizedRequest(t *testing.T) {
	t.Parallel()

	body, err := jevMarshalRequest(jevMaximalRemoteInput(), strings.Repeat("m", jevMaxRequestBytes))
	if err == nil {
		t.Fatalf("an oversized model produced a %d byte request instead of a refusal", len(body))
	}
	if got := JevFailureKindOf(err); got != JevFailureRequestRefused {
		t.Fatalf("failure kind = %q, want %q", got, JevFailureRequestRefused)
	}
	if len(body) != 0 {
		t.Fatalf("a refused request must not be returned, got %d bytes", len(body))
	}
}

// TestJevValidateEndpointAcceptsOnlyPinnedOrigins covers the origin posture: TLS
// anywhere, plaintext only on loopback, and never a URL that could carry a
// credential, a query, or an unbounded path (design.md "Jev adapter").
func TestJevValidateEndpointAcceptsOnlyPinnedOrigins(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		endpoint string
		wantErr  bool
		// wantReason pins which origin check refused the value, so one refusal
		// cannot silently stand in for another.
		wantReason string
	}{
		{name: "vendor https", endpoint: jevEndpointDefault},
		{name: "vendor https with a trailing slash", endpoint: "https://api.typesafe.ai/v1/systemone/"},
		{name: "loopback ipv4", endpoint: "http://127.0.0.1:8080/v1/systemone"},
		{name: "loopback ipv6", endpoint: "http://[::1]:8080/v1/systemone"},
		{name: "loopback name", endpoint: "http://localhost:8080/v1/systemone"},
		{name: "empty", endpoint: "", wantErr: true, wantReason: "is empty"},
		{name: "relative", endpoint: "/v1/systemone", wantErr: true, wantReason: "not an absolute URL"},
		{name: "no scheme", endpoint: "api.typesafe.ai/v1/systemone", wantErr: true, wantReason: "not an absolute URL"},
		{name: "unparsable escape", endpoint: "https://api.typesafe.ai/%zz", wantErr: true, wantReason: "not a parsable URL"},
		{name: "no host", endpoint: "https:///v1/systemone", wantErr: true, wantReason: "not a permitted origin"},
		{
			name: "plaintext to the internet", endpoint: "http://api.typesafe.ai/v1/systemone",
			wantErr: true, wantReason: "not a permitted origin",
		},
		{
			name: "lookalike loopback host", endpoint: "http://127.0.0.1.evil.test/v1/systemone",
			wantErr: true, wantReason: "not a permitted origin",
		},
		{
			name: "embedded credential", endpoint: "https://user:pass@api.typesafe.ai/v1/systemone",
			wantErr: true, wantReason: "embedded credentials",
		},
		{
			name: "query string", endpoint: "https://api.typesafe.ai/v1/systemone?api_key=secret",
			wantErr: true, wantReason: "query or fragment",
		},
		{
			name: "fragment", endpoint: "https://api.typesafe.ai/v1/systemone#frag",
			wantErr: true, wantReason: "query or fragment",
		},
		{
			name: "file scheme", endpoint: "file:///etc/passwd",
			wantErr: true, wantReason: "not a permitted origin",
		},
		{
			name: "ftp to a real host", endpoint: "ftp://api.typesafe.ai/v1/systemone",
			wantErr: true, wantReason: "not a permitted origin",
		},
		{
			name: "gopher to a real host", endpoint: "gopher://api.typesafe.ai/v1/systemone",
			wantErr: true, wantReason: "not a permitted origin",
		},
		{
			name: "too long", endpoint: jevEndpointDefault + strings.Repeat("/a", jevMaxEndpointBytes),
			wantErr: true, wantReason: "exceeds the bounded origin size",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := jevValidateEndpoint(tc.endpoint)
			if tc.wantErr != (err != nil) {
				t.Fatalf("jevValidateEndpoint(%q) error = %v, wantErr %v", tc.endpoint, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("jevValidateEndpoint(%q) error %q does not report %q", tc.endpoint, err, tc.wantReason)
			}
		})
	}
}

// TestJevValidateEndpointDoesNotEchoTheEndpoint keeps endpoint validation
// failures free of the rejected value so an operator typo cannot spill a
// credential-shaped URL into a diagnostic (requirement 7.5).
func TestJevValidateEndpointDoesNotEchoTheEndpoint(t *testing.T) {
	t.Parallel()

	err := jevValidateEndpoint("https://user:hunter2@api.typesafe.ai/v1/systemone")
	if err == nil {
		t.Fatal("an embedded credential must be refused")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("endpoint error echoed the credential: %v", err)
	}
}

// TestJevDecisionRejectsUnusableProbabilities proves the value the vendor reports
// is validated by the port's own validator, including the non-finite values the
// JSON grammar cannot even express (requirements 6.8, 6.9; remote.go
// ValidateRemoteDecision).
func TestJevDecisionRejectsUnusableProbabilities(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		probability float64
		wantErr     bool
	}{
		{name: "negative", probability: -0.1, wantErr: true},
		{name: "above one", probability: 1.5, wantErr: true},
		{name: "far above one", probability: 1e308, wantErr: true},
		{name: "not a number", probability: math.NaN(), wantErr: true},
		{name: "positive infinity", probability: math.Inf(1), wantErr: true},
		{name: "negative infinity", probability: math.Inf(-1), wantErr: true},
		{name: "no", probability: 0},
		{name: "midpoint", probability: 0.5},
		{name: "at the threshold", probability: 0.9},
		{name: "yes", probability: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decision, err := jevDecision(tc.probability)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("probability %v produced a usable decision", tc.probability)
				}
				if !errors.Is(err, featurestate.ErrInvalidRemoteDecision) {
					t.Fatalf("error %v does not chain to ErrInvalidRemoteDecision", err)
				}
				if got := JevFailureKindOf(err); got != JevFailureMalformedResponse {
					t.Fatalf("failure kind = %q, want %q", got, JevFailureMalformedResponse)
				}
				if decision != (featurestate.RemoteDecision{}) {
					t.Fatalf("a refused decision must be zero, got %+v", decision)
				}
				return
			}
			if err != nil {
				t.Fatalf("probability %v was refused: %v", tc.probability, err)
			}
			if decision.CodingProbability != tc.probability {
				t.Fatalf("CodingProbability = %v, want %v", decision.CodingProbability, tc.probability)
			}
			if decision.Confidence != 0 {
				t.Fatalf("Confidence = %v; a noul answer reports no confidence, so none may be invented",
					decision.Confidence)
			}
			if err := featurestate.ValidateRemoteDecision(decision); err != nil {
				t.Fatalf("the mapped decision must satisfy the port validator: %v", err)
			}
		})
	}
}

// TestJevDecisionPromotesOnProbabilityAlone is the noul-has-no-confidence
// resolution: because the vendor reports no confidence for a noul answer, the
// adapter leaves Confidence at its zero value, and requirement 6.8 must still
// promote on the configured positive_threshold alone.
func TestJevDecisionPromotesOnProbabilityAlone(t *testing.T) {
	t.Parallel()

	decision, err := jevDecision(0.95)
	if err != nil {
		t.Fatalf("jevDecision: %v", err)
	}
	if decision.Confidence != 0 {
		t.Fatalf("Confidence = %v, want the unreported zero value", decision.Confidence)
	}
	if !decision.Positive(0.9) {
		t.Fatal("a 0.95 probability must promote at a 0.9 threshold despite a zero confidence")
	}
	if !decision.Positive(0.95) {
		t.Fatal("a probability exactly at the threshold must promote")
	}
	for _, above := range []float64{0.96, 1} {
		if decision.Positive(above) {
			t.Fatalf("a zero-confidence decision must still respect a higher threshold %v", above)
		}
	}
	for _, unusable := range []float64{0, -0.1, 1.5, math.NaN()} {
		if decision.Positive(unusable) {
			t.Fatalf("threshold %v must never promote", unusable)
		}
	}
}

// TestDecodeJevResponseCoversTheDocumentedResponseShape pins the response mapping
// against the current early-access schema: model, answers, and usage are all
// required, the answer must be the noul type we asked for, and nothing after the
// document is tolerated (requirements 6.9, 7.5).
func TestDecodeJevResponseCoversTheDocumentedResponseShape(t *testing.T) {
	t.Parallel()

	documented := func(probability string) string {
		return `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
			`":{"type":"noul","noul":` + probability + `}},"usage":{"input_tokens":296,"output_tokens":20}}`
	}
	for _, tc := range []struct {
		name     string
		body     string
		wantErr  bool
		wantKind JevFailure
		wantProb float64
	}{
		{name: "documented success", body: documented("0.95"), wantProb: 0.95},
		{name: "probable no", body: documented("0"), wantProb: 0},
		{name: "probable yes", body: documented("1"), wantProb: 1},
		{name: "exactly at a 0.9 threshold", body: documented("0.9"), wantProb: 0.9},
		{name: "above one", body: documented("1.5"), wantErr: true},
		{name: "negative", body: documented("-0.1"), wantErr: true},
		{name: "beyond float64 range", body: documented("1e999"), wantErr: true},
		{name: "below float64 range", body: documented("-1e999"), wantErr: true},
		{name: "probability as text", body: documented(`"0.95"`), wantErr: true},
		{name: "probability as a boolean", body: documented("true"), wantErr: true},
		{name: "probability as null", body: documented("null"), wantErr: true},
		{
			name:     "empty body",
			body:     "",
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name:     "an html error page",
			body:     "<html>gateway timeout</html>",
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name:     "truncated document",
			body:     `{"model":"jev-1.13.0","answers":`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name:     "a second document",
			body:     documented("0.95") + `{"model":"jev-1.13.0"}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name:     "trailing garbage",
			body:     documented("0.95") + " trailing",
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name:     "json null",
			body:     "null",
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "missing model member",
			body: `{"answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.95}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "empty model",
			body: `{"model":"","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.95}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "missing usage",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.95}}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "null usage",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.95}},"usage":null}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "negative token count",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.95}},"usage":{"input_tokens":-1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "negative output tokens",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.95}},"usage":{"input_tokens":1,"output_tokens":-2}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			// The vendor reference marks only usage itself as required; both counts
			// are optional properties. research.md records that Jev applies no
			// output-token charge, so requiring it would refuse every successful
			// classification and silently disable the feature while still failing
			// open.
			name: "usage present with no counts is accepted",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.95}},"usage":{}}`,
			wantProb: 0.95,
		},
		{
			name: "absent output tokens is accepted",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.95}},"usage":{"input_tokens":296}}`,
			wantProb: 0.95,
		},
		{
			name: "absent input tokens is accepted",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.95}},"usage":{"output_tokens":20}}`,
			wantProb: 0.95,
		},
		{
			name: "usage entirely absent is refused",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.95}}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name:     "missing answers",
			body:     `{"model":"jev-1.13.0","usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "null answers",
			body: `{"model":"jev-1.13.0","answers":null,` +
				`"usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "answer under an unknown id",
			body: `{"model":"jev-1.13.0","answers":{"other":{"type":"noul","noul":0.95}},` +
				`"usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "a choice answer where noul was asked",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"choice","choice":"yes","confidence":0.8}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "a score answer where noul was asked",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"score","score":1.0,"confidence":0.7}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "missing answer type",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"noul":0.95}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "missing noul",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul"}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "a nested noul object",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":{"value":0.95}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
		{
			name: "an answer that is not an object",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":"noul","usage":{"input_tokens":1,"output_tokens":1}}`,
			wantErr:  true,
			wantKind: JevFailureMalformedResponse,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decision, err := decodeJevResponse([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("body %q produced %+v", tc.body, decision)
				}
				if decision != (featurestate.RemoteDecision{}) {
					t.Fatalf("a malformed response must yield the zero decision, got %+v", decision)
				}
				if got := JevFailureKindOf(err); got != JevFailureMalformedResponse || tc.wantKind != "" && got != tc.wantKind {
					t.Fatalf("failure kind = %q, want %q (err %v)", got, tc.wantKind, err)
				}
				if !errors.Is(err, featurestate.ErrInvalidRemoteDecision) {
					t.Fatalf("error %v does not chain to ErrInvalidRemoteDecision", err)
				}
				if len(tc.body) > 8 && strings.Contains(err.Error(), tc.body) {
					t.Fatalf("error echoed the raw vendor body: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("body %q was refused: %v", tc.body, err)
			}
			if decision.CodingProbability != tc.wantProb {
				t.Fatalf("CodingProbability = %v, want %v", decision.CodingProbability, tc.wantProb)
			}
		})
	}
}

// TestDecodeJevResponseIgnoresUnknownMembers documents that the early-access
// schema may grow: an unknown member is not a failure, while every member this
// mapping depends on is still required.
func TestDecodeJevResponseIgnoresUnknownMembers(t *testing.T) {
	t.Parallel()

	body := `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
		`":{"type":"noul","noul":0.93,"future_member":"ignored"}},"usage":{"input_tokens":1,"output_tokens":1,"future_member":7}}`
	decision, err := decodeJevResponse([]byte(body))
	if err != nil {
		t.Fatalf("an added member must not break the mapping: %v", err)
	}
	if decision.CodingProbability != 0.93 {
		t.Fatalf("CodingProbability = %v, want 0.93", decision.CodingProbability)
	}
}

// TestJevResponseRefusalsNameTheOffendingMember pins which documented-shape check
// produced a refusal. Without it a refusal could silently degrade into a
// neighbouring reason while still failing, and an operator debugging a vendor change
// would learn nothing about which member moved (requirements 6.9, 7.5).
func TestJevResponseRefusalsNameTheOffendingMember(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "undecodable",
			body: "<html>gateway timeout</html>",
			want: "is not one decodable document",
		},
		{
			name: "trailing bytes",
			body: `{"model":"m","answers":{"` + jevQuestionID + `":{"type":"noul","noul":0.5}},` +
				`"usage":{"input_tokens":1,"output_tokens":1}} trailing`,
			want: "trailing data",
		},
		// json.Decoder.More reports false for these because it reads a trailing
		// ']' or '}' as an enclosing-delimiter token, so each of them once
		// passed as a complete single document.
		{
			name: "trailing close bracket",
			body: `{"model":"m","answers":{"` + jevQuestionID + `":{"type":"noul","noul":0.5}},` +
				`"usage":{"input_tokens":1,"output_tokens":1}}]`,
			want: "trailing data",
		},
		{
			name: "trailing close brace",
			body: `{"model":"m","answers":{"` + jevQuestionID + `":{"type":"noul","noul":0.5}},` +
				`"usage":{"input_tokens":1,"output_tokens":1}}}`,
			want: "trailing data",
		},
		{
			name: "trailing bracket run",
			body: `{"model":"m","answers":{"` + jevQuestionID + `":{"type":"noul","noul":0.5}},` +
				`"usage":{"input_tokens":1,"output_tokens":1}}]}}`,
			want: "trailing data",
		},
		{
			name: "absent model",
			body: `{"answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: "reports no model",
		},
		{
			name: "absent usage",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID + `":{"type":"noul","noul":0.5}}}`,
			want: "no usable token usage",
		},
		{
			name: "negative usage",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":0.5}},"usage":{"input_tokens":0,"output_tokens":-1}}`,
			want: "no usable token usage",
		},
		{
			name: "no answer under the asked id",
			body: `{"model":"jev-1.13.0","answers":{"other":{"type":"noul","noul":0.5}},` +
				`"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: "no answer for the asked question",
		},
		{
			name: "a choice answer",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"choice","choice":"yes","confidence":0.9}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: "not the asked noul answer type",
		},
		{
			name: "absent probability",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul"}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: "reports no probability",
		},
		{
			name: "a quoted probability",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":"0.5"}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: "is not a number",
		},
		{
			name: "an out of range probability",
			body: `{"model":"jev-1.13.0","answers":{"` + jevQuestionID +
				`":{"type":"noul","noul":1.5}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: "cannot be thresholded safely",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := decodeJevResponse([]byte(tc.body))
			if err == nil {
				t.Fatalf("body %q was accepted", tc.body)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not report %q", err, tc.want)
			}
		})
	}
}

// TestReadJevBodyStopsAtTheBound proves the response read is bounded: a stream
// that would run forever is cut off after the bound plus one byte, so a hostile
// or broken endpoint cannot exhaust memory (requirements 6.7, 7.2).
func TestReadJevBodyStopsAtTheBound(t *testing.T) {
	t.Parallel()

	t.Run("stops reading at the bound", func(t *testing.T) {
		t.Parallel()
		counter := &countingReader{remaining: jevMaxResponseBytes * 64}
		payload, err := readJevBody(counter)
		if err == nil {
			t.Fatalf("an unbounded body was accepted (%d bytes)", len(payload))
		}
		if got := JevFailureKindOf(err); got != JevFailureResponseOversized {
			t.Fatalf("failure kind = %q, want %q", got, JevFailureResponseOversized)
		}
		if counter.read != jevMaxResponseBytes+1 {
			t.Fatalf("read %d bytes, want exactly the bound plus one", counter.read)
		}
		if len(payload) != 0 {
			t.Fatalf("an oversized body must not be returned, got %d bytes", len(payload))
		}
	})

	t.Run("accepts a body exactly at the bound", func(t *testing.T) {
		t.Parallel()
		counter := &countingReader{remaining: jevMaxResponseBytes}
		payload, err := readJevBody(counter)
		if err != nil {
			t.Fatalf("a body at the bound was refused: %v", err)
		}
		if len(payload) != jevMaxResponseBytes || counter.read != jevMaxResponseBytes {
			t.Fatalf("read %d bytes and returned %d, want %d", counter.read, len(payload), jevMaxResponseBytes)
		}
	})

	t.Run("reports a read failure", func(t *testing.T) {
		t.Parallel()
		payload, err := readJevBody(errReader{})
		if err == nil {
			t.Fatalf("a failing read produced %d bytes", len(payload))
		}
		if got := JevFailureKindOf(err); got != JevFailureTransport {
			t.Fatalf("failure kind = %q, want %q", got, JevFailureTransport)
		}
	})
}

// TestJevFailureKindOfIgnoresForeignErrors keeps the failure vocabulary specific
// to this adapter so a caller cannot mistake an unrelated error for a remote
// failure (requirement 6.9).
func TestJevFailureKindOfIgnoresForeignErrors(t *testing.T) {
	t.Parallel()

	for _, err := range []error{nil, context.Canceled, featurestate.ErrInvalidRemoteDecision, errors.New("other")} {
		if got := JevFailureKindOf(err); got != "" {
			t.Errorf("JevFailureKindOf(%v) = %q, want the empty kind", err, got)
		}
	}
	wrapped := &JevError{Kind: JevFailureRateLimited, Err: errors.New("jev returned status 429")}
	if got := JevFailureKindOf(wrapped); got != JevFailureRateLimited {
		t.Fatalf("JevFailureKindOf(wrapped) = %q", got)
	}
	if !strings.Contains(wrapped.Error(), string(JevFailureRateLimited)) {
		t.Fatalf("error text %q must name the failure kind", wrapped.Error())
	}
	if !errors.Is(wrapped, wrapped.Err) {
		t.Fatal("JevError must unwrap to its cause")
	}
	if errors.Unwrap(wrapped) == nil {
		t.Fatal("JevError must expose a cause")
	}
}

func decodeJSONObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return decoded
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// countingReader serves a fixed number of filler bytes and records how many were
// consumed, so a bounded read can be observed rather than assumed.
type countingReader struct {
	remaining int
	read      int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		p[i] = 'x'
	}
	r.remaining -= len(p)
	r.read += len(p)
	return len(p), nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }
