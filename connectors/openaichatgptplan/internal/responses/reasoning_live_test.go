package responses_test

// The OPT-IN, RAW provider probe for public exact reasoning support (spec task
// 8.2).
//
// This probe answers one empirical question: does the public SIWC Responses
// route return exact reasoning items with encrypted content, and does it accept
// those items with their JSON content preserved in a later stateless request?
//
// It is deliberately NOT the connector's production path:
//
//   - both request documents are built literally here, not by the projector,
//     the preview policy or the normalizer; their method, public URL and
//     headers mirror the public JSON/SSE request production sends;
//   - it records the provider's own output items and replays their JSON
//     content, in provider order, without changing values or order;
//   - it never reads or writes the connector's advertised capability. A PASS
//     is raw provider evidence only; promoting exact replay needs a separate
//     implementation change with its own deterministic tests. The production
//     connector deliberately remains on baseline canonical history and
//     advertises no exact replay (../reasoning_capability_test.go and
//     reasoning_reduced_test.go).
//
// There is no private fallback. Both requests address the documented public
// endpoint, carry store:false and stream:true, and never use
// previous_response_id, /responses/compact, codex.responses.compaction.v2 or a
// ChatGPT private route.
//
// The probe is opt-in and billable:
//
//	AIP_SIWC_REASONING_REPLAY_LIVE=1 \
//	AIP_SIWC_REASONING_REPLAY_TOKEN=... \
//	AIP_SIWC_REASONING_REPLAY_MODEL=... \
//	[AIP_SIWC_REASONING_REPLAY_EFFORT=medium] \
//	go test ./internal/responses -run '^TestPublicReasoningReplayLiveProbe$' -count=1 -v
//
// Without the gate it SKIPS: no credential means the capability is UNPROVEN,
// not unsupported. When the gate is set without the token or model it fails
// closed with an actionable setup error instead of silently skipping.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
)

const (
	reasoningProbeGateEnv   = "AIP_SIWC_REASONING_REPLAY_LIVE"
	reasoningProbeTokenEnv  = "AIP_SIWC_REASONING_REPLAY_TOKEN"
	reasoningProbeModelEnv  = "AIP_SIWC_REASONING_REPLAY_MODEL"
	reasoningProbeEffortEnv = "AIP_SIWC_REASONING_REPLAY_EFFORT"
	reasoningProbeNextTurn  = "Continue with one short sentence that repeats the word continued."
	// reasoningProbeURL is the documented public Responses resource, pinned as
	// a literal independent of the production constants.
	reasoningProbeURL = "https://api.openai.com/v1/responses"
)

// reasoningProbeConfig is the explicit, non-secret probe configuration.
type reasoningProbeConfig struct {
	Token  string
	Model  string
	Effort string
}

// resolveReasoningProbeConfig applies the opt-in gate. It returns skip=true
// only when the operator did not request the probe; an explicitly requested
// probe with missing prerequisites is an error.
func resolveReasoningProbeConfig(getenv func(string) string) (reasoningProbeConfig, bool, error) {
	if strings.TrimSpace(getenv(reasoningProbeGateEnv)) != "1" {
		return reasoningProbeConfig{}, true, nil
	}
	cfg := reasoningProbeConfig{
		Token:  strings.TrimSpace(getenv(reasoningProbeTokenEnv)),
		Model:  strings.TrimSpace(getenv(reasoningProbeModelEnv)),
		Effort: strings.TrimSpace(getenv(reasoningProbeEffortEnv)),
	}
	if cfg.Token == "" {
		return reasoningProbeConfig{}, false, fmt.Errorf("the live reasoning replay probe is requested but %s is empty", reasoningProbeTokenEnv)
	}
	if cfg.Model == "" {
		return reasoningProbeConfig{}, false, fmt.Errorf("the live reasoning replay probe is requested but %s is empty", reasoningProbeModelEnv)
	}
	if cfg.Effort == "" {
		cfg.Effort = "medium"
	}
	return cfg, false, nil
}

// reasoningProbeTokenProvider presents the opt-in probe token to the raw
// public transport. It is test-only: the connector runtime has no environment
// or file credential path.
type reasoningProbeTokenProvider struct {
	token string
}

func (p reasoningProbeTokenProvider) Selected(context.Context) (string, error) {
	return "public-reasoning-probe", nil
}

func (p reasoningProbeTokenProvider) Token(context.Context, string) (siwc.TokenGrant, error) {
	return siwc.TokenGrant{AccessToken: p.token}, nil
}

// reasoningProbeUserMessage is the documented user input item.
func reasoningProbeUserMessage(text string) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"type": "message",
		"role": "user",
		"content": []any{
			map[string]any{"type": "input_text", "text": text},
		},
	})
}

// publicReasoningProbeBody builds the documented stateless request. A nil input
// starts a fresh probe turn; otherwise the supplied items are replayed with
// their JSON content preserved and followed by the next user message.
func publicReasoningProbeBody(cfg reasoningProbeConfig, input []json.RawMessage) ([]byte, error) {
	if len(input) == 0 {
		first, err := reasoningProbeUserMessage(
			"Solve 17 * 23 step by step, then answer with the result and one short sentence.",
		)
		if err != nil {
			return nil, err
		}
		input = []json.RawMessage{first}
	}
	return json.Marshal(map[string]any{
		"model":     cfg.Model,
		"input":     input,
		"reasoning": map[string]any{"effort": cfg.Effort},
		"include":   []string{"reasoning.encrypted_content"},
		"store":     false,
		"stream":    true,
	})
}

// publicReasoningProbeRequest builds the one raw public request form shared by
// the deterministic fixture and the live probe. Its method, literal public URL
// and headers mirror the connector's production public request; the credential
// stays out of this value and is added only by the transport.
func publicReasoningProbeRequest(cfg reasoningProbeConfig, input []json.RawMessage) (responses.WireRequest, error) {
	body, err := publicReasoningProbeBody(cfg, input)
	if err != nil {
		return responses.WireRequest{}, err
	}
	headers := make(http.Header, 3)
	headers.Set("Accept", "text/event-stream")
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", responses.UserAgent)
	return responses.WireRequest{
		Method:  http.MethodPost,
		URL:     responses.PublicBaseURL + responses.ResponsesPath,
		Headers: headers,
		Body:    body,
	}, nil
}

// publicReasoningProbeReplayRequest replays every output item of the first
// answer with its JSON content preserved, in provider order, followed by the
// next user message.
func publicReasoningProbeReplayRequest(cfg reasoningProbeConfig, outputItems []json.RawMessage) (responses.WireRequest, error) {
	if len(outputItems) == 0 {
		return responses.WireRequest{}, errors.New("the first answer carried no output items to replay")
	}
	next, err := reasoningProbeUserMessage(reasoningProbeNextTurn)
	if err != nil {
		return responses.WireRequest{}, err
	}
	input := make([]json.RawMessage, 0, len(outputItems)+1)
	input = append(input, outputItems...)
	input = append(input, next)
	return publicReasoningProbeRequest(cfg, input)
}

// assertPublicReasoningProbeRequest refuses a private route, a continuation
// field, or a request that is not the documented stateless streaming form. It
// also pins the public JSON/SSE request contract and keeps the credential out
// of everything except the transport's Authorization header.
func assertPublicReasoningProbeRequest(t *testing.T, req responses.WireRequest, credential string) {
	t.Helper()
	if req.Method != http.MethodPost {
		t.Fatalf("probe request method = %q, want %q", req.Method, http.MethodPost)
	}
	if req.URL != reasoningProbeURL {
		t.Fatalf("probe request URL = %q, want the documented literal public Responses endpoint %q", req.URL, reasoningProbeURL)
	}
	for _, header := range []struct {
		name string
		want string
	}{
		{name: "Content-Type", want: "application/json"},
		{name: "Accept", want: "text/event-stream"},
		{name: "User-Agent", want: responses.UserAgent},
	} {
		if got := req.Headers.Get(header.name); got != header.want {
			t.Fatalf("probe request %s = %q, want %q", header.name, got, header.want)
		}
	}
	for name := range req.Headers {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization") {
			t.Fatalf("probe request must leave %q to the transport", name)
		}
	}
	if credential != "" {
		for name, values := range req.Headers {
			for _, value := range values {
				if strings.Contains(value, credential) {
					t.Fatalf("probe request header %q leaks the credential", name)
				}
			}
		}
		if strings.Contains(req.URL, credential) || strings.Contains(string(req.Body), credential) {
			t.Fatal("probe request leaks the credential outside the transport's Authorization header")
		}
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &document); err != nil {
		t.Fatalf("probe request is not a JSON object: %v", err)
	}
	for _, member := range []string{"previous_response_id", "conversation_id"} {
		if _, ok := document[member]; ok {
			t.Fatalf("probe request must not carry %q", member)
		}
	}
	var model string
	if err := json.Unmarshal(document["model"], &model); err != nil || strings.TrimSpace(model) == "" {
		t.Fatalf("probe request must name a model: %s", req.Body)
	}
	var include []string
	if err := json.Unmarshal(document["include"], &include); err != nil {
		t.Fatalf("probe request include = %s, want [reasoning.encrypted_content]", document["include"])
	}
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("probe request include = %v, want [reasoning.encrypted_content]", include)
	}
	for _, flag := range []struct {
		name string
		want bool
	}{
		{name: "store", want: false},
		{name: "stream", want: true},
	} {
		var value bool
		if err := json.Unmarshal(document[flag.name], &value); err != nil || value != flag.want {
			t.Fatalf("probe request %s = %s, want %t", flag.name, document[flag.name], flag.want)
		}
	}
	wire := string(req.Body)
	for _, forbidden := range []string{
		"/responses/compact",
		"codex.responses.compaction.v2",
		"/backend-api",
		"chatgpt.com",
	} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("probe request must not carry %q", forbidden)
		}
	}
}

// reasoningProbeOutputItem is the bounded view used to find the exact
// reasoning item in the provider output. The documented fields are typed so
// presence is not confused with a nonempty value: null or [] summary is empty,
// and non-string encrypted_content is not a carrier.
type reasoningProbeOutputItem struct {
	Type             string            `json:"type"`
	ID               string            `json:"id"`
	Summary          []json.RawMessage `json:"summary"`
	EncryptedContent string            `json:"encrypted_content"`
}

// TestReasoningProbeGateIsFailClosed proves the gate skips only when the probe
// was not requested and fails closed when it was requested without its
// prerequisites.
func TestReasoningProbeGateIsFailClosed(t *testing.T) {
	t.Parallel()
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	cases := []struct {
		name    string
		values  map[string]string
		skip    bool
		wantErr bool
	}{
		{name: "not requested skips", values: map[string]string{}, skip: true},
		{name: "requested without token fails closed", values: map[string]string{reasoningProbeGateEnv: "1", reasoningProbeModelEnv: "gpt"}, wantErr: true},
		{name: "requested without model fails closed", values: map[string]string{reasoningProbeGateEnv: "1", reasoningProbeTokenEnv: "secret"}, wantErr: true},
		{name: "requested with prerequisites runs", values: map[string]string{reasoningProbeGateEnv: "1", reasoningProbeTokenEnv: "secret", reasoningProbeModelEnv: "gpt"}, skip: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg, skip, err := resolveReasoningProbeConfig(env(tc.values))
			if tc.wantErr {
				if err == nil {
					t.Fatal("the explicitly requested probe must fail closed when a prerequisite is missing")
				}
				if skip {
					t.Fatal("a fail-closed probe must not report a skip")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected setup error: %v", err)
			}
			if skip != tc.skip {
				t.Fatalf("skip = %v, want %v", skip, tc.skip)
			}
			if !skip && (cfg.Token != tc.values[reasoningProbeTokenEnv] || cfg.Model != tc.values[reasoningProbeModelEnv]) {
				t.Fatalf("config = %+v, want the supplied non-secret values", cfg)
			}
		})
	}
}

// TestPublicReasoningProbeBodyIsStatelessAndExact pins the literal requests
// the raw probe sends, through the same constructor the live probe uses. It is
// deterministic and offline.
func TestPublicReasoningProbeBodyIsStatelessAndExact(t *testing.T) {
	t.Parallel()
	cfg := reasoningProbeConfig{Token: "probe-token-not-sent", Model: "gpt", Effort: "medium"}
	reasoning := json.RawMessage(`{
		"id": "rs_1",
		"type": "reasoning",
		"summary": [{"type": "summary_text", "text": "analysis"}],
		"encrypted_content": "cipher"
	}`)
	assistant := json.RawMessage(`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"391"}]}`)

	first, err := publicReasoningProbeRequest(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicReasoningProbeRequest(t, first, cfg.Token)

	replay, err := publicReasoningProbeReplayRequest(cfg, []json.RawMessage{reasoning, assistant})
	if err != nil {
		t.Fatal(err)
	}
	assertPublicReasoningProbeRequest(t, replay, cfg.Token)

	var document struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(replay.Body, &document); err != nil {
		t.Fatalf("the replay body is not JSON: %v", err)
	}
	if len(document.Input) != 3 {
		t.Fatalf("replay input items = %d, want the two provider items and the next user message", len(document.Input))
	}
	if !sameJSONContent(t, document.Input[0], reasoning) || !sameJSONContent(t, document.Input[1], assistant) {
		t.Fatalf("replay must preserve the provider items' JSON content in provider order: %s", replay.Body)
	}
	var next struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(document.Input[2], &next); err != nil || next.Type != "message" || next.Role != "user" {
		t.Fatalf("replay must end with a user message: %s", document.Input[2])
	}
}

// sameJSONContent compares two JSON values by decoded content. The replay
// preserves the provider's values and item order, not its original wire bytes
// or insignificant whitespace.
func sameJSONContent(t *testing.T, got, want json.RawMessage) bool {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("replayed item is not JSON: %v", err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("fixture item is not JSON: %v", err)
	}
	return reflect.DeepEqual(gotValue, wantValue)
}

// TestPublicReasoningReplayLiveProbe is the opt-in, billable RAW provider
// proof. It is not part of the production path.
func TestPublicReasoningReplayLiveProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("the raw public reasoning replay probe is not part of the short suite")
	}
	cfg, skip, err := resolveReasoningProbeConfig(os.Getenv)
	if skip {
		t.Skipf("set %s=1 with %s and %s to opt into raw live validation of public exact reasoning replay",
			reasoningProbeGateEnv, reasoningProbeTokenEnv, reasoningProbeModelEnv)
	}
	if err != nil {
		t.Fatal(err)
	}

	client, err := responses.NewClient(responses.ClientConfig{
		Tokens: reasoningProbeTokenProvider{token: cfg.Token},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	first, err := publicReasoningProbeRequest(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicReasoningProbeRequest(t, first, cfg.Token)

	outputItems, err := reasoningProbeCompletedOutput(ctx, client, first)
	if err != nil {
		t.Fatalf("the raw probe's first request failed before a completed answer: %v", err)
	}
	reasoning := reasoningProbeFindReasoning(outputItems)
	if reasoning == nil {
		t.Fatal("the public route returned no exact reasoning item with encrypted_content; this run did not validate exact reasoning replay (the production connector remains reduced)")
	}
	if len(reasoning.Summary) == 0 {
		t.Fatal("the public route's reasoning item carried no summary entries")
	}

	replay, err := publicReasoningProbeReplayRequest(cfg, outputItems)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicReasoningProbeRequest(t, replay, cfg.Token)

	secondItems, err := reasoningProbeCompletedOutput(ctx, client, replay)
	if err != nil {
		t.Fatalf("the public route rejected the exact reasoning replay: %v", err)
	}

	t.Logf("RAW provider probe PASS: output_items=%d reasoning_id_present=%t encrypted_content_bytes=%d replay_items=%d replay_output_items=%d. "+
		"This is raw provider evidence only; the production connector still advertises no exact reasoning replay.",
		len(outputItems), reasoning.ID != "", len(reasoning.EncryptedContent), len(outputItems)+1, len(secondItems))
}

// reasoningProbeInMemoryProvider is the probe's in-memory provider boundary:
// the test prepares every answer, so a probe regression needs no network and no
// real credential.
type reasoningProbeInMemoryProvider func(*http.Request) (*http.Response, error)

func (p reasoningProbeInMemoryProvider) RoundTrip(request *http.Request) (*http.Response, error) {
	return p(request)
}

// TestReasoningProbeDoesNotPrintProviderEchoedCredential is the response-side
// credential-safety regression for the opt-in probe (J6). The probe reads raw
// provider events and prints the error it returns, so a provider that echoes
// the bearer into a provider-controlled field must not turn that field into a
// printed credential. The provider is entirely in memory: no network and no
// real credential.
func TestReasoningProbeDoesNotPrintProviderEchoedCredential(t *testing.T) {
	t.Parallel()
	const token = "synthetic-token-000000000000"
	cfg := reasoningProbeConfig{Token: token, Model: "gpt", Effort: "medium"}

	completedEvent := func(t *testing.T, status string) string {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"status": status,
				"output": []any{map[string]any{"type": "message"}},
			},
		})
		if err != nil {
			t.Fatalf("marshalling the synthetic provider event: %v", err)
		}
		return "data: " + string(payload) + "\n\n"
	}
	eventStream := func(body string) *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
	}
	// probeError runs the probe helper over one in-memory provider answer and
	// returns the error the live probe would print.
	probeError := func(t *testing.T, provider reasoningProbeInMemoryProvider) error {
		t.Helper()
		client, err := responses.NewClient(responses.ClientConfig{
			Tokens:     reasoningProbeTokenProvider{token: token},
			HTTPClient: &http.Client{Transport: provider},
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		request, err := publicReasoningProbeRequest(cfg, nil)
		if err != nil {
			t.Fatalf("building the probe request: %v", err)
		}
		_, err = reasoningProbeCompletedOutput(context.Background(), client, request)
		if err == nil {
			t.Fatal("a provider answer that is not a completed terminal must be an error")
		}
		if strings.Contains(err.Error(), token) {
			t.Fatalf("the probe error printed by the live test discloses the bearer: %v", err)
		}
		return err
	}

	// The provider echoes the EXACT bearer the transport added, in the one
	// field that carries the completed status.
	echoed := probeError(t, func(request *http.Request) (*http.Response, error) {
		authorization := request.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") {
			t.Fatal("the probe request reached the provider without its bearer credential")
		}
		return eventStream(completedEvent(t, strings.TrimPrefix(authorization, "Bearer "))), nil
	})
	benign := probeError(t, func(*http.Request) (*http.Response, error) {
		return eventStream(completedEvent(t, "in_progress")), nil
	})
	if echoed.Error() != benign.Error() {
		t.Fatalf("the unexpected-status diagnostic depends on provider text: %q vs %q", echoed, benign)
	}

	// Audit of the helper's remaining provider-derived renderings: a provider
	// event type, the raw event bytes and a refused-answer document all echo
	// the bearer, and none of them may reach the printed error either.
	for _, tc := range []struct {
		name     string
		provider func(*http.Request) (*http.Response, error)
	}{
		{
			name: "provider event type",
			provider: func(*http.Request) (*http.Response, error) {
				return eventStream("data: {\"type\":\"" + token + "\"}\n\n"), nil
			},
		},
		{
			name: "malformed provider event",
			provider: func(*http.Request) (*http.Response, error) {
				return eventStream("data: " + token + "\n\n"), nil
			},
		},
		{
			name: "refused answer metadata",
			provider: func(*http.Request) (*http.Response, error) {
				body := `{"error":{"message":"` + token + `","type":"` + token + `","code":"` + token + `"}}`
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Header: http.Header{
						"Content-Type": {"application/json"},
						"x-request-id": {token},
					},
					Body: io.NopCloser(strings.NewReader(body)),
				}, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// probeError fails the subtest when the answer returns no error or
			// prints the bearer, so only its assertion matters here.
			_ = probeError(t, tc.provider)
		})
	}
}

// reasoningProbeCompletedOutput reads one raw answer to its completed terminal
// and returns the terminal's output items. A missing terminal is an error, not
// an empty success.
//
// Every error this helper CREATES is constant and value-free. The raw events
// are provider-controlled: response.status can echo the request's bearer, and
// the live probe prints this error, so no decoded provider value is rendered.
// Errors from the transport and the stream keep their own contracts, which
// already redact or replace provider text.
func reasoningProbeCompletedOutput(ctx context.Context, client *responses.Client, req responses.WireRequest) ([]json.RawMessage, error) {
	stream, err := client.OpenStream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("provider answer carried no completed terminal")
		}
		if err != nil {
			return nil, err
		}
		var payload struct {
			Type     string `json:"type"`
			Response *struct {
				Status string            `json:"status"`
				Output []json.RawMessage `json:"output"`
			} `json:"response"`
		}
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return nil, errors.New("provider event is not readable JSON")
		}
		switch payload.Type {
		case "response.completed":
			if payload.Response == nil {
				return nil, errors.New("the completed terminal carries no response resource")
			}
			if status := strings.TrimSpace(payload.Response.Status); status != "" && status != "completed" {
				// A constant diagnostic: the status is provider text and can
				// echo the bearer, and this error is printed by the live probe.
				return nil, errors.New("the completed terminal reports an unexpected status")
			}
			if len(payload.Response.Output) == 0 {
				return nil, errors.New("the completed terminal carried no output items")
			}
			return payload.Response.Output, nil
		case "response.failed":
			return nil, errors.New("the provider answer ended with a failed terminal")
		case "response.incomplete":
			return nil, errors.New("the provider answer ended with an incomplete terminal")
		case "response.cancelled":
			return nil, errors.New("the provider answer ended with a cancelled terminal")
		case "error":
			return nil, errors.New("the provider answer ended with a provider error event")
		}
	}
}

// reasoningProbeFindReasoning returns the first exact reasoning item carrying
// a nonempty string encrypted_content, or nil.
func reasoningProbeFindReasoning(items []json.RawMessage) *reasoningProbeOutputItem {
	for _, raw := range items {
		var item reasoningProbeOutputItem
		if err := json.Unmarshal(raw, &item); err != nil || item.Type != "reasoning" {
			continue
		}
		if item.EncryptedContent == "" {
			continue
		}
		return &item
	}
	return nil
}
