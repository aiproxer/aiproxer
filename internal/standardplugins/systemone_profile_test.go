package standardplugins_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestSystemOneProfiles_BuildDecisionBackend(t *testing.T) {
	t.Parallel()
	p, err := providerprofiles.EmbeddedProfile("typesafe")
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := providerprofiles.CompileProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	be, err := standardplugins.BuildProviderProfileBackend(compiled, "decision", &http.Client{}, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	_, decision := be.Caps[aipapi.CapabilityDecisions]
	if !decision || len(be.Caps) != 1 || be.ModelInventory == nil {
		t.Fatalf("profile backend lacks decision capability or static inventory: %+v", be)
	}
}

func TestSystemOneProfiles_ForwardValidatedOperatorHeaders(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Provider-Label") != "operator-title" {
			t.Error("profile header missing")
		}
		_, _ = io.WriteString(w, `{"model":"jev-latest","answers":{"q":{"type":"noul","noul":0.7}},"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()
	p, err := providerprofiles.EmbeddedProfile("typesafe")
	if err != nil {
		t.Fatal(err)
	}
	p.Endpoint.BaseURL = srv.URL
	p.Headers = []providerprofiles.SafeHeader{{Name: "X-Provider-Label", Value: "operator-title"}}
	compiled, err := providerprofiles.CompileProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	be, err := standardplugins.BuildProviderProfileBackend(compiled, "decision", srv.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	r := aipapi.DecisionRequest{Evidence: []byte(`"x"`), Questions: []aipapi.DecisionQuestion{{ID: "q", Kind: aipapi.DecisionKindNoul}}}
	stream, err := be.Open(t.Context(), aipapi.Call{Decision: &r, Invocation: aipapi.Invocation{Operation: aipapi.OperationDecisionEvaluate}}, routing.AttemptCandidate{Primary: routing.Primary{Model: "jev-latest"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestSystemOneProfiles_SubQDecisionBackendWireFacts proves the
// subquadratic-systemone profile produces a working decision-only backend with
// SubQ-specific wire facts: POST <base>/systemone, Bearer SUBQ_API_KEY,
// native subq-snap-preview passthrough, ordered questions/criteria, and no
// inbound header forwarding.
func TestSystemOneProfiles_SubQDecisionBackendWireFacts(t *testing.T) {
	const sentinel = "subq-sentinel-operator-key-9f31"
	const inboundSpoof = "Bearer inbound-client-secret"
	t.Setenv("SUBQ_API_KEY", sentinel)
	// The handler runs on its own goroutine and the assertions below read its
	// captures, so hand them over a buffered channel: the receive is the
	// happens-before edge. A TCP response body is not one.
	type observed struct {
		body, auth, label, cookie, method, path, userAgent, contentType string
	}
	captured := make(chan observed, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		captured <- observed{
			body: string(data), auth: r.Header.Get("Authorization"), label: r.Header.Get("X-Provider-Label"),
			cookie: r.Header.Get("Cookie"), method: r.Method, path: r.URL.Path,
			userAgent: r.Header.Get("User-Agent"), contentType: r.Header.Get("Content-Type"),
		}
		_, _ = io.WriteString(w, `{"model":"subq-snap-preview","answers":{"q_noul":{"type":"noul","noul":0.7},"q_pick":{"type":"choice","choice":"alpha","probabilities":{"beta":0.4,"alpha":0.6}}},"usage":{"input_tokens":12,"output_tokens":0}}`)
	}))
	defer srv.Close()
	p, err := providerprofiles.EmbeddedProfile("subquadratic-systemone")
	if err != nil {
		t.Fatal(err)
	}
	// No profile Headers are configured here on purpose: wire.go forwards only
	// profile-configured safe headers, so any inbound-supplied X-Provider-Label
	// or Cookie must stay absent below.
	//
	// The base URL keeps its /v1 root so the test proves the adapter appends to
	// the configured root instead of discarding its path: the stub must observe
	// /v1/systemone, matching the real https://api.subq.ai/v1/systemone.
	p.Endpoint.BaseURL = srv.URL + "/v1"
	if len(p.Models.Static) != 1 {
		t.Fatalf("subq static models = %+v, want exactly subq-snap-preview", p.Models.Static)
	}
	// Derive the candidate from the profile's configured native id: this keeps
	// the test sensitive to the catalog row (native id passthrough, no
	// prefixing) instead of hardcoding the model twice.
	nativeModel := p.Models.Static[0].NativeID
	compiled, err := providerprofiles.CompileProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	be, err := standardplugins.BuildProviderProfileBackend(compiled, "decision", srv.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, decision := be.Caps[aipapi.CapabilityDecisions]; !decision || len(be.Caps) != 1 {
		t.Fatalf("subq backend caps = %+v, want exactly {decisions}", be.Caps)
	}
	if be.ModelInventory == nil {
		t.Fatal("subq backend lacks static model inventory")
	}
	req := aipapi.DecisionRequest{Evidence: json.RawMessage(`"subq-evidence"`), Questions: []aipapi.DecisionQuestion{
		{ID: "q_noul", Kind: aipapi.DecisionKindNoul, Instructions: json.RawMessage(`"judge"`)},
		{ID: "q_pick", Kind: aipapi.DecisionKindChoice, Instructions: json.RawMessage(`"pick one"`), Options: []aipapi.DecisionOption{{Name: "beta"}, {Name: "alpha", Description: json.RawMessage(`"first"`)}}},
	}}
	// The backend Open path takes no caller headers; smuggle spoof values
	// through every caller-controlled Call field so a leak would be visible.
	call := aipapi.Call{
		Decision:   &req,
		Invocation: aipapi.Invocation{Operation: aipapi.OperationDecisionEvaluate, ClientUserAgent: "spoofed-agent/1.0"},
		Extensions: map[string]json.RawMessage{
			"inbound-authorization": json.RawMessage(`"Bearer inbound-client-secret"`),
			"x-provider-label":      json.RawMessage(`"spoofed"`),
			"cookie":                json.RawMessage(`"session=spoofed"`),
		},
	}
	stream, err := be.Open(t.Context(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: nativeModel}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	// The receive is what makes the handler's captures safe to read.
	got := <-captured
	// family_default path policy appends /systemone to the configured root, so
	// the /v1 root must survive: the stub observes /v1/systemone, the same shape
	// as the real https://api.subq.ai/v1/systemone.
	if got.method != http.MethodPost || got.path != "/v1/systemone" {
		t.Fatalf("request = %s %s, want POST /v1/systemone", got.method, got.path)
	}
	if got.auth != "Bearer "+sentinel {
		t.Fatalf("authorization = %q, want Bearer <SUBQ_API_KEY sentinel>", got.auth)
	}
	if got.auth == inboundSpoof {
		t.Fatal("inbound client authorization reached upstream")
	}
	if got.label != "" {
		t.Fatalf("X-Provider-Label = %q, want absent (no profile headers configured)", got.label)
	}
	if got.cookie != "" {
		t.Fatalf("Cookie = %q, want absent", got.cookie)
	}
	if strings.Contains(got.userAgent, "spoofed-agent") {
		t.Fatalf("inbound User-Agent leaked upstream: %q", got.userAgent)
	}
	if got.contentType != "application/json" {
		t.Fatalf("Content-Type = %q", got.contentType)
	}
	var outbound struct {
		Model     string                     `json:"model"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal([]byte(got.body), &outbound); err != nil || outbound.Model != "subq-snap-preview" {
		t.Fatalf("outbound model = %q (body %s)", outbound.Model, got.body)
	}
	// Question ids are unique, so their first-offset order in the encoded body
	// is the emitted order; a reordered or dropped question fails here.
	noulAt, pickAt := strings.Index(got.body, `"q_noul"`), strings.Index(got.body, `"q_pick"`)
	if len(outbound.Questions) != 2 || noulAt < 0 || pickAt < 0 || noulAt > pickAt {
		t.Fatalf("questions lost client order: %s", got.body)
	}
	// Choice option names must arrive as the criteria object's keys, in order,
	// with the option description as the value. Assert the decoded structure so a
	// renamed property cannot satisfy this check by accident.
	var pick struct {
		Kind     aipapi.DecisionKind        `json:"type"`
		Criteria map[string]json.RawMessage `json:"criteria"`
		Options  json.RawMessage            `json:"options"`
	}
	if err := json.Unmarshal(outbound.Questions["q_pick"], &pick); err != nil {
		t.Fatalf("decode q_pick: %v (body %s)", err, got.body)
	}
	if pick.Kind != aipapi.DecisionKindChoice || len(pick.Options) != 0 || len(pick.Criteria) != 2 || string(pick.Criteria["beta"]) != "null" || string(pick.Criteria["alpha"]) != `"first"` {
		t.Fatalf("choice criteria = %+v, want keys beta->null and alpha->\"first\"", pick.Criteria)
	}
	var noul struct {
		Kind aipapi.DecisionKind `json:"type"`
	}
	if err := json.Unmarshal(outbound.Questions["q_noul"], &noul); err != nil || noul.Kind != aipapi.DecisionKindNoul {
		t.Fatalf("q_noul kind = %q (body %s)", noul.Kind, got.body)
	}
	var collected []aipapi.Event
	for {
		ev, recvErr := stream.Recv(t.Context())
		if recvErr != nil {
			if recvErr == io.EOF {
				break
			}
			t.Fatalf("stream recv: %v", recvErr)
		}
		collected = append(collected, ev)
	}
	if len(collected) != 4 || collected[0].Kind != aipapi.EventResponseStarted || collected[1].Kind != aipapi.EventDecisionResult || collected[2].Kind != aipapi.EventUsageDelta || collected[3].Kind != aipapi.EventResponseFinished {
		t.Fatalf("event sequence = %+v", collected)
	}
	result := collected[1].Decision
	if result == nil || result.Model != "subq-snap-preview" || len(result.Answers) != 2 || result.Answers[0].QuestionID != "q_noul" || result.Answers[1].QuestionID != "q_pick" {
		t.Fatalf("decision result = %+v", result)
	}
	if err := result.ValidateFor(req); err != nil {
		t.Fatalf("decision result fails ValidateFor: %v", err)
	}
	usage := collected[2]
	if !usage.UsagePresence.InputTokens || !usage.UsagePresence.OutputTokens || usage.InputTokens != 12 || usage.OutputTokens != 0 {
		t.Fatalf("usage = %+v", usage)
	}
}
