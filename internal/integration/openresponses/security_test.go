package openresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	frontopenairesponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	frontopenresponses "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/testkit/conformance"
	"github.com/gorilla/websocket"
)

// Task 7.4 base security, failover, and existing-adapter regression proofs.
//
// These tests drive the full client → real frontend → core executor → real
// OpenResponses backend → injectable (including adversarial) provider origin
// path. Adversarial origins are injected through [conformance.DeploymentSpec]
// [conformance.DeploymentSpec.OriginHandler] while the observing harness proxy
// still counts every request and keeps bounded redacted capture artifacts.

// rawORCreate posts an arbitrary body to the OpenResponses create endpoint and
// returns the HTTP status and response body so zero-upstream and error-shape
// assertions can be made precisely. A proxy-owned session id is attached so
// continuation scope is authoritative and never zero (the session middleware
// provides it in the standard composition).
func rawORCreate(t *testing.T, d *conformance.Deployment, rawBody string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, d.Server.URL+"/openresponses/v1/responses", strings.NewReader(rawBody))
	if err != nil {
		t.Fatalf("build raw create: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AIP-Session-Id", "sess-task74")
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("raw create: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// storeTrueCreate performs one store:true non-streaming create and returns the
// proxy-issued response id (the continuation handle) or an error.
func storeTrueCreate(t *testing.T, d *conformance.Deployment, prevID string) (string, error) {
	t.Helper()
	payload := map[string]any{"model": "gpt-4o-mini", "input": "x", "store": true}
	if prevID != "" {
		payload["previous_response_id"] = prevID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, d.Server.URL+"/openresponses/v1/responses", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AIP-Session-Id", "sess-task74")
	resp, err := d.Server.Client().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode create resource: %w", err)
	}
	if out.ID == "" {
		return "", errors.New("create resource has no id")
	}
	return out.ID, nil
}

// adversarialOROrigin is a deterministic provider origin responder that serves
// one fixed SSE body for streaming creates, one fixed JSON resource for
// non-streaming creates, and one fixed compact resource, all under the
// observing harness proxy. status overrides every response with a plain error
// status when nonzero.
type adversarialOROrigin struct {
	streamBody  string
	resource    string
	compactBody string
	status      int
}

func (s *adversarialOROrigin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/responses/compact") {
		if s.status != 0 {
			w.WriteHeader(s.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, s.compactBody)
		return
	}
	var probe struct {
		Stream *bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &probe)
	if probe.Stream != nil && *probe.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, s.streamBody)
		return
	}
	if s.status != 0 {
		w.WriteHeader(s.status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, s.resource)
}

// nativeSSEBase is the well-formed incremental stream the adversarial origins
// build on. It carries a provider-native response id and item id that must stay
// private, and appends `trailer` raw SSE records after [DONE] when non-empty.
func nativeSSEBase(trailer string) string {
	sse := "event: response.created\n" +
		"data: " + `{"type":"response.created","sequence_number":1,"response":{"id":"resp_native_secret","object":"response","created_at":1,"status":"in_progress","model":"gpt-4o-mini","output":[]}}` + "\n\n" +
		"event: response.output_item.added\n" +
		"data: " + `{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"type":"message","id":"msg_native_1","status":"in_progress","role":"assistant","content":[{"type":"output_text","text":""}]}}` + "\n\n" +
		"event: response.content_part.added\n" +
		"data: " + `{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_native_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		"data: " + `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_native_1","output_index":0,"content_index":0,"delta":"hello"}` + "\n\n" +
		"event: response.output_text.done\n" +
		"data: " + `{"type":"response.output_text.done","sequence_number":5,"item_id":"msg_native_1","output_index":0,"content_index":0,"text":"hello"}` + "\n\n" +
		"event: response.content_part.done\n" +
		"data: " + `{"type":"response.content_part.done","sequence_number":6,"item_id":"msg_native_1","output_index":0,"content_index":0}` + "\n\n" +
		"event: response.output_item.done\n" +
		"data: " + `{"type":"response.output_item.done","sequence_number":7,"output_index":0,"item":{"type":"message","id":"msg_native_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}` + "\n\n" +
		"event: response.completed\n" +
		"data: " + `{"type":"response.completed","sequence_number":8,"response":{"id":"resp_native_secret","object":"response","created_at":1,"status":"completed","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_native_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}}` + "\n\n" +
		"data: [DONE]\n\n" + trailer
	return sse
}

// TestSecurity_ExtensionSmugglingRejectedBeforeNetwork proves a vendor-prefixed
// extension item a client smuggles into a create is decoded into a canonical
// extension requirement and rejected by candidate admission before any upstream
// network work, because the generic OpenResponses backend declares no
// compatible extension type.
func TestSecurity_ExtensionSmugglingRejectedBeforeNetwork(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{
			name: "prefixed_extension_undeclared",
			body: `{"model":"gpt-4o-mini","store":false,"input":[{"type":"acme:telemetry","namespace":"acme","data":{"sample":1}}]}`,
		},
		{
			name: "unprefixed_unknown_type",
			body: `{"model":"gpt-4o-mini","store":false,"input":[{"type":"smuggled"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := conformance.Deploy(t, conformance.DeploymentSpec{
				Frontend:  conformance.FrontendOpenResponses,
				Backend:   conformance.BackendOpenResponses,
				Transport: conformance.TransportJSON,
			})
			if d == nil {
				t.Fatal("Deploy failed")
			}
			defer func() { _ = d.Close() }()

			status, _ := rawORCreate(t, d, tc.body)
			if status == http.StatusOK {
				t.Fatal("extension-smuggling create unexpectedly succeeded")
			}
			if got := d.RequestCount(conformance.BackendOpenResponses); got != 0 {
				t.Fatalf("extension smuggling caused %d upstream requests, want 0", got)
			}
		})
	}
}

// TestSecurity_IncompatibleFailoverZeroNetwork proves a call whose requirements
// no candidate satisfies is rejected before upstream work across a multi-candidate
// failover chain: both the primary and the candidate origins observe zero requests.
func TestSecurity_IncompatibleFailoverZeroNetwork(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportJSON,
		Candidates: []conformance.Candidate{
			{Backend: conformance.BackendOpenResponses},
		},
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	status, _ := rawORCreate(t, d, `{"model":"gpt-4o-mini","store":false,"input":[{"type":"acme:telemetry","namespace":"acme","data":{"sample":1}}]}`)
	if status == http.StatusOK {
		t.Fatal("incompatible create unexpectedly succeeded")
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 0 {
		t.Fatalf("primary origin request count = %d, want 0", got)
	}
	if cand := d.CandidateOrigin(0); cand != nil && cand.Count() != 0 {
		t.Fatalf("candidate origin request count = %d, want 0 (failover must not contact incompatible candidates)", cand.Count())
	}
}

// TestSecurity_IDProbingIndistinguishableNotFound proves probing the
// continuation store with missing, malformed, or short-entropy response IDs
// yields the identical client-visible error shape and zero upstream work.
func TestSecurity_IDProbingIndistinguishableNotFound(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportJSON,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	var wantCode, wantMessage, wantType string
	for i, tc := range []struct {
		parentID string
	}{
		{parentID: "resp_missing_9999999999999999"},
		{parentID: "invalid_id_no_prefix"},
		{parentID: "resp_short"},
		{parentID: "resp_" + strings.Repeat("0", 40)},
	} {
		status, body := rawORCreate(t, d, `{"model":"gpt-4o-mini","input":"x","previous_response_id":"`+tc.parentID+`"}`)
		if status != http.StatusBadRequest {
			t.Fatalf("probe %d status = %d, want 400", i, status)
		}
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			t.Fatalf("probe %d body is not an error envelope: %v", i, err)
		}
		if i == 0 {
			wantCode, wantMessage, wantType = env.Error.Code, env.Error.Message, env.Error.Type
		}
		if env.Error.Code != wantCode || env.Error.Message != wantMessage || env.Error.Type != wantType {
			t.Fatalf("probe %d error shape drifted: code=%q msg=%q type=%q; first code=%q msg=%q type=%q",
				i, env.Error.Code, env.Error.Message, env.Error.Type, wantCode, wantMessage, wantType)
		}
	}
	if wantCode != "previous_response_not_found" {
		t.Fatalf("probe error code = %q, want previous_response_not_found", wantCode)
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 0 {
		t.Fatalf("id probing caused %d upstream requests, want 0", got)
	}
}

// TestSecurity_NativeResponseIDNotLeaked proves provider-native response and
// item ids captured by the backend remain private attempt evidence and never
// appear in client-visible SSE or JSON responses; the client only ever sees the
// proxy-issued response id.
func TestSecurity_NativeResponseIDNotLeaked(t *testing.T) {
	t.Parallel()
	for _, transport := range []conformance.ClientTransport{conformance.TransportSSE, conformance.TransportJSON} {
		t.Run(string(transport), func(t *testing.T) {
			t.Parallel()
			origin := &adversarialOROrigin{
				streamBody: nativeSSEBase(""),
				resource:   `{"id":"resp_native_secret","object":"response","created_at":1,"status":"completed","model":"gpt-4o-mini","output":[{"type":"message","id":"msg_native_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
			}
			d := conformance.Deploy(t, conformance.DeploymentSpec{
				Frontend:      conformance.FrontendOpenResponses,
				Backend:       conformance.BackendOpenResponses,
				Transport:     transport,
				OriginHandler: origin,
			})
			if d == nil {
				t.Fatal("Deploy failed")
			}
			defer func() { _ = d.Close() }()

			res, err := d.Client.RoundTrip(context.Background(), "ping")
			if err != nil {
				t.Fatalf("round trip over %s: %v", transport, err)
			}
			if res.ResponseID == "" {
				t.Fatal("client-visible response id is empty")
			}
			if res.ResponseID == "resp_native_secret" {
				t.Fatal("client-visible response id leaked the provider-native id")
			}
			if strings.Contains(res.ResponseID, "native_secret") {
				t.Fatalf("client-visible response id contains native material: %q", res.ResponseID)
			}
			if transport == conformance.TransportSSE {
				if res.Text != "hello" {
					t.Fatalf("SSE text = %q, want hello", res.Text)
				}
				for _, ev := range res.Events {
					if ev == "response.error" {
						t.Fatal("native-id stream surfaced an error")
					}
				}
			} else {
				status, body := rawORCreate(t, d, `{"model":"gpt-4o-mini","input":"ping","store":false}`)
				if status != http.StatusOK {
					t.Fatalf("non-streaming status = %d", status)
				}
				if strings.Contains(body, "native_secret") || strings.Contains(body, "msg_native_1") {
					t.Fatalf("client-visible response resource leaked native ids: %s", body)
				}
			}
		})
	}
}

// TestSecurity_EventInjectionAfterTerminalIgnored proves injected SSE content
// records after the terminal + [DONE] never reach the client: the stream ends
// at the single terminal and the assembled text contains only the legitimate
// delta.
func TestSecurity_EventInjectionAfterTerminalIgnored(t *testing.T) {
	t.Parallel()
	trailer := "event: response.output_text.delta\n" +
		"data: " + `{"type":"response.output_text.delta","sequence_number":9,"item_id":"msg_native_1","output_index":0,"content_index":0,"delta":"INJECTED_AFTER_TERMINAL"}` + "\n\n"
	origin := &adversarialOROrigin{streamBody: nativeSSEBase(trailer)}
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:      conformance.FrontendOpenResponses,
		Backend:       conformance.BackendOpenResponses,
		Transport:     conformance.TransportSSE,
		OriginHandler: origin,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("SSE round trip: %v", err)
	}
	if strings.Contains(res.Text, "INJECTED_AFTER_TERMINAL") {
		t.Fatalf("injected post-terminal content reached the client: %q", res.Text)
	}
	if res.Text != "hello" {
		t.Fatalf("SSE text = %q, want hello", res.Text)
	}
	if res.Status != "completed" {
		t.Fatalf("SSE status = %q, want completed", res.Status)
	}
	terminals := 0
	for _, ev := range res.Events {
		if ev == "response.completed" {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("SSE terminals = %d, want exactly 1 (event injection must not duplicate the terminal)", terminals)
	}
	if len(res.Events) > 0 && res.Events[len(res.Events)-1] != "response.completed" {
		t.Fatalf("SSE terminal event order broken: %v", res.Events)
	}
}

// TestSecurity_EventInjectionBeforeStartRejected proves a provider injecting
// content-bearing events before the lifecycle allows them is rejected as a
// malformed stream before any client-visible content is emitted.
func TestSecurity_EventInjectionBeforeStartRejected(t *testing.T) {
	t.Parallel()
	origin := &adversarialOROrigin{
		streamBody: "event: response.output_text.delta\n" +
			"data: " + `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_x","output_index":0,"content_index":0,"delta":"INJECTED_BEFORE_START"}` + "\n\n",
	}
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:      conformance.FrontendOpenResponses,
		Backend:       conformance.BackendOpenResponses,
		Transport:     conformance.TransportSSE,
		OriginHandler: origin,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	if _, err := d.Client.RoundTrip(context.Background(), "ping"); err == nil {
		t.Fatal("injected pre-start stream unexpectedly succeeded")
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 1 {
		t.Fatalf("adversarial stream request count = %d, want exactly 1", got)
	}
}

// TestSecurity_OpaqueReplayRequiresCompatibleDialect proves provider-bound
// replay material (a reasoning item carrying a reasoning dialect requirement) is
// never routed to the generic OpenResponses backend, which declares no reasoning
// dialect: candidate admission rejects before upstream work.
func TestSecurity_OpaqueReplayRequiresCompatibleDialect(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportJSON,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	status, _ := rawORCreate(t, d, `{"model":"gpt-4o-mini","store":false,"input":[{"type":"reasoning","reasoning":"think carefully"}]}`)
	if status == http.StatusOK {
		t.Fatal("provider-bound reasoning replay unexpectedly succeeded")
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 0 {
		t.Fatalf("opaque replay caused %d upstream requests, want 0", got)
	}
}

// TestSecurity_OpaqueReplayNotLeakedAsText proves opaque replay material
// carried on a reasoning output item (a field the pinned profile does not map to
// canonical content) never surfaces in the client-visible output text.
func TestSecurity_OpaqueReplayNotLeakedAsText(t *testing.T) {
	t.Parallel()
	stream := "event: response.created\n" +
		"data: " + `{"type":"response.created","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"gpt-4o-mini","output":[]}}` + "\n\n" +
		"event: response.output_item.added\n" +
		"data: " + `{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"type":"reasoning","id":"rs_native_1","status":"completed","reasoning":"brief reasoning","opaque":"OPAQUE_SECRET_123"}}` + "\n\n" +
		"event: response.output_item.done\n" +
		"data: " + `{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"type":"reasoning","id":"rs_native_1","status":"completed","reasoning":"brief reasoning"}}` + "\n\n" +
		"event: response.output_item.added\n" +
		"data: " + `{"type":"response.output_item.added","sequence_number":4,"output_index":1,"item":{"type":"message","id":"msg_2","status":"in_progress","role":"assistant","content":[{"type":"output_text","text":""}]}}` + "\n\n" +
		"event: response.content_part.added\n" +
		"data: " + `{"type":"response.content_part.added","sequence_number":5,"item_id":"msg_2","output_index":1,"content_index":0,"part":{"type":"output_text","text":""}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		"data: " + `{"type":"response.output_text.delta","sequence_number":6,"item_id":"msg_2","output_index":1,"content_index":0,"delta":"visible"}` + "\n\n" +
		"event: response.output_text.done\n" +
		"data: " + `{"type":"response.output_text.done","sequence_number":7,"item_id":"msg_2","output_index":1,"content_index":0,"text":"visible"}` + "\n\n" +
		"event: response.content_part.done\n" +
		"data: " + `{"type":"response.content_part.done","sequence_number":8,"item_id":"msg_2","output_index":1,"content_index":0}` + "\n\n" +
		"event: response.output_item.done\n" +
		"data: " + `{"type":"response.output_item.done","sequence_number":9,"output_index":1,"item":{"type":"message","id":"msg_2","status":"completed","role":"assistant","content":[{"type":"output_text","text":"visible"}]}}` + "\n\n" +
		"event: response.completed\n" +
		"data: " + `{"type":"response.completed","sequence_number":10,"response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-4o-mini","output":[]}}` + "\n\n" +
		"data: [DONE]\n\n"
	origin := &adversarialOROrigin{streamBody: stream}
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:      conformance.FrontendOpenResponses,
		Backend:       conformance.BackendOpenResponses,
		Transport:     conformance.TransportSSE,
		OriginHandler: origin,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("SSE round trip: %v", err)
	}
	if strings.Contains(res.Text, "OPAQUE_SECRET_123") {
		t.Fatalf("opaque replay material leaked into client text: %q", res.Text)
	}
	if !strings.Contains(res.Text, "visible") {
		t.Fatalf("client text %q missing the visible output", res.Text)
	}
	if res.Status != "completed" {
		t.Fatalf("SSE status = %q, want completed", res.Status)
	}
}

// TestSecurity_AmplificationChainDepthRejected proves continuation materialization
// is bounded: a create that would reconstruct a chain deeper than the configured
// max_chain_depth fails with the indistinguishable previous_response_not_found
// shape and performs zero upstream work.
func TestSecurity_AmplificationChainDepthRejected(t *testing.T) {
	t.Parallel()
	const depth = 3
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:                  conformance.FrontendOpenResponses,
		Backend:                   conformance.BackendOpenResponses,
		Transport:                 conformance.TransportJSON,
		ContinuationMaxChainDepth: depth,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	prev := ""
	for i := range depth + 1 {
		id, err := storeTrueCreate(t, d, prev)
		if err != nil {
			t.Fatalf("create at depth %d: %v", i+1, err)
		}
		prev = id
	}
	before := d.RequestCount(conformance.BackendOpenResponses)

	status, body := rawORCreate(t, d, `{"model":"gpt-4o-mini","input":"x","previous_response_id":"`+prev+`"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("amplifying create status = %d, want 400", status)
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("amplifying create body is not an error envelope: %v", err)
	}
	if env.Error.Code != "previous_response_not_found" {
		t.Fatalf("amplifying create error code = %q, want indistinguishable previous_response_not_found", env.Error.Code)
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != before {
		t.Fatalf("amplifying create caused %d upstream requests (before %d), want 0 additional", got, before)
	}
}

// TestSecurity_RouteCollisionRejectedBeforeServing proves a canonical route
// takeover conflict is rejected by the production claim seam before any handler
// is mounted, with a deterministic conflict naming both owners and an atomic
// (unchanged) registry.
func TestSecurity_RouteCollisionRejectedBeforeServing(t *testing.T) {
	t.Parallel()
	reg := httpcontract.NewRouteRegistry()
	openaiClaims, err := frontopenairesponses.RouteClaims("openai-responses")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(openaiClaims); err != nil {
		t.Fatal(err)
	}
	before := len(reg.Claims())

	_, err = frontopenresponses.RegisterClaimsForOwner(reg, frontopenresponses.Config{
		Profile:  frontopenresponses.DefaultProfile,
		BasePath: "/v1",
		WebSocket: frontopenresponses.WebSocketConfig{
			Enabled:          true,
			MaxConnectionAge: "60m",
			IdleTimeout:      "5m",
			MaxQueuedTurns:   1,
		},
	}, "openresponses")
	if err == nil {
		t.Fatal("expected canonical route takeover to be rejected")
	}
	var detail httpcontract.RouteConflictError
	if !errors.As(err, &detail) {
		t.Fatalf("expected RouteConflictError, got %T: %v", err, err)
	}
	if detail.ExistingOwner == "" || detail.NewOwner == "" {
		t.Fatalf("conflict detail must name both owners: %+v", detail)
	}
	if len(reg.Claims()) != before {
		t.Fatalf("failed registration mutated the registry: before=%d after=%d", before, len(reg.Claims()))
	}
}

// TestSecurity_WebSocketOriginAbuseRejected proves a browser origin that is not
// allowlisted is rejected at the upgrade with a 403 and never reaches any
// backend or allocates turn state.
func TestSecurity_WebSocketOriginAbuseRejected(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:  conformance.FrontendOpenResponses,
		Backend:   conformance.BackendOpenResponses,
		Transport: conformance.TransportWebSocket,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	u, err := url.Parse(strings.TrimRight(d.BaseURL(), "/") + "/openresponses/v1/responses")
	if err != nil {
		t.Fatal(err)
	}
	u.Scheme = "ws"
	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), http.Header{"Origin": []string{"https://evil.example"}})
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("disallowed-origin WebSocket upgrade unexpectedly succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disallowed-origin upgrade status = %v, want 403", resp)
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 0 {
		t.Fatalf("disallowed-origin upgrade caused %d upstream requests, want 0", got)
	}
}

// TestSecurity_RedactedArtifactsBounded proves the observing harness keeps its
// request-capture artifacts bounded for the OpenResponses backend path: repeated
// traffic cannot grow the capture past its configured limit, every request is
// still counted, and any credential-named header an origin ever sees is retained
// only as its redacted sentinel. (Credential-header redaction is exercised
// directly by the harness conformance suite; this test pins the same property
// through the full Task 7.4 deployment path.)
func TestSecurity_RedactedArtifactsBounded(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:      conformance.FrontendOpenResponses,
		Backend:       conformance.BackendOpenResponses,
		Transport:     conformance.TransportJSON,
		ArtifactLimit: 2,
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	for i := range 5 {
		if _, err := d.Client.RoundTrip(context.Background(), "ping"); err != nil {
			t.Fatalf("round trip %d: %v", i, err)
		}
	}
	origin := d.OriginFor(conformance.BackendOpenResponses)
	captured := origin.Capture()
	if len(captured) > 2 {
		t.Fatalf("capture length = %d, want bounded at 2", len(captured))
	}
	for _, obs := range captured {
		for key, values := range obs.Headers {
			for _, v := range values {
				switch key {
				case "Authorization", "x-api-key", "X-API-Key":
					if v != "[REDACTED]" {
						t.Fatalf("header %q not redacted: %q", key, v)
					}
				}
			}
		}
	}
	if got := origin.Count(); got != 5 {
		t.Fatalf("request count = %d, want 5", got)
	}
}

// TestCommitment_NoRetryAfterVisibleOutput proves the no-retry-after-visible-output
// invariant end to end: a primary origin that streams user-visible deltas and then
// dies mid-stream never triggers failover to the healthy candidate, and the client
// receives the terminal failure rather than a silent retry.
func TestCommitment_NoRetryAfterVisibleOutput(t *testing.T) {
	t.Parallel()
	dying := "event: response.created\n" +
		"data: " + `{"type":"response.created","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"gpt-4o-mini","output":[]}}` + "\n\n" +
		"event: response.output_item.added\n" +
		"data: " + `{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"type":"message","id":"msg_1","status":"in_progress","role":"assistant","content":[{"type":"output_text","text":""}]}}` + "\n\n" +
		"event: response.content_part.added\n" +
		"data: " + `{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		"data: " + `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"partial-"}` + "\n\n"

	origin := &adversarialOROrigin{streamBody: dying}
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:      conformance.FrontendOpenResponses,
		Backend:       conformance.BackendOpenResponses,
		Transport:     conformance.TransportSSE,
		OriginHandler: origin,
		Candidates: []conformance.Candidate{
			{Backend: conformance.BackendOpenResponses},
		},
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err == nil {
		// The SSE frontend surfaces a post-output failure as a response.failed
		// terminal (the connection already delivered the partial delta), never
		// as a silent success or a retry.
		if res.Status != "failed" {
			t.Fatalf("mid-stream death after visible output status = %q, want failed", res.Status)
		}
		if !strings.Contains(res.Text, "partial-") {
			t.Fatalf("post-output failure lost the committed partial text: %q", res.Text)
		}
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got != 1 {
		t.Fatalf("primary origin request count = %d, want exactly 1 (no retry after output)", got)
	}
	if cand := d.CandidateOrigin(0); cand != nil && cand.Count() != 0 {
		t.Fatalf("candidate origin was contacted after committed output: %d requests", cand.Count())
	}
}

// TestCommitment_PreOutputClassifiedFailoverProceeds proves pre-output failure
// is classified as recoverable and the executor fails over to the healthy
// candidate before any output is committed (the complement of the post-output
// commitment proof above).
func TestCommitment_PreOutputClassifiedFailoverProceeds(t *testing.T) {
	t.Parallel()
	d := conformance.Deploy(t, conformance.DeploymentSpec{
		Frontend:   conformance.FrontendOpenResponses,
		Backend:    conformance.BackendOpenResponses,
		Transport:  conformance.TransportSSE,
		OriginFail: conformance.OriginFailServerError,
		Candidates: []conformance.Candidate{
			{Backend: conformance.BackendOpenResponses},
		},
	})
	if d == nil {
		t.Fatal("Deploy failed")
	}
	defer func() { _ = d.Close() }()

	res, err := d.Client.RoundTrip(context.Background(), "ping")
	if err != nil {
		t.Fatalf("pre-output failover round trip: %v", err)
	}
	if !strings.Contains(res.Text, conformance.HarnessFakeText) {
		t.Fatalf("failover text %q missing %q", res.Text, conformance.HarnessFakeText)
	}
	if got := d.RequestCount(conformance.BackendOpenResponses); got < 1 {
		t.Fatal("primary failing origin was never attempted")
	}
	if cand := d.CandidateOrigin(0); cand == nil || cand.Count() < 1 {
		t.Fatal("healthy candidate was not reached after pre-output classification")
	}
}
