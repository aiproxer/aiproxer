package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// These tests exercise the optional spec through the production protocol, not
// through a parser-only implementation. Embedding stdioTransport gives the fake
// the same private, per-process state contract as production.
type optionsTransport struct {
	stdioTransport
	newOptions string
	newCwd     string
	setResult  string
	setErr     error
	setFailAt  int
	calls      []string
	prompts    int
	newCalls   int
}

func (t *optionsTransport) CallUnary(_ context.Context, body []byte, _ int) ([]byte, error) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Cwd      string `json:"cwd"`
			ConfigID string `json:"configId"`
			Value    string `json:"value"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	result := `{}`
	switch req.Method {
	case "session/new":
		t.newCalls++
		t.newCwd = req.Params.Cwd
		result = `{"sessionId":"sid","configOptions":` + t.newOptions + `}`
	case "session/set_config_option":
		t.calls = append(t.calls, req.Params.ConfigID+"="+req.Params.Value)
		if t.setErr != nil && (t.setFailAt == 0 || len(t.calls) == t.setFailAt) {
			return nil, t.setErr
		}
		result = t.setResult
		if result == "" {
			effort := "medium"
			if req.Params.ConfigID == "effort" {
				effort = req.Params.Value
			}
			result = `{"configOptions":` + advertisedOptions("sonnet", effort, "low", "medium", "high") + `}`
		}
	}
	return fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result), nil
}

func (t *optionsTransport) CallPromptStream(context.Context, []byte) (io.ReadCloser, error) {
	t.prompts++
	return io.NopCloser(strings.NewReader("")), nil
}
func (t *optionsTransport) SendJSONRPC(context.Context, []byte) error { return nil }
func (t *optionsTransport) Close() error                              { return nil }

type legacyOptionsSpec struct{}

func (legacyOptionsSpec) VendorID() string     { return "test" }
func (legacyOptionsSpec) VendorPrefix() string { return "test" }
func (legacyOptionsSpec) BuildCommand(_, workspace string) ([]string, string, []string, error) {
	return []string{"test"}, workspace, nil, nil
}

func (legacyOptionsSpec) HandshakeProfile() HandshakeProfile {
	return HandshakeProfile{ProtocolVersion: 1, SkipAuthenticate: true}
}
func (legacyOptionsSpec) CancelProfile() CancelProfile               { return CancelProfile{} }
func (legacyOptionsSpec) ServerRequestHandler() ServerRequestHandler { return nil }
func (legacyOptionsSpec) RequiresExplicitWorkspace() bool            { return false }
func (legacyOptionsSpec) ResolveModel(model string) string           { return model }

type negotiatedOptionsSpec struct{ legacyOptionsSpec }

func (negotiatedOptionsSpec) EffortConfigID() string { return "effort" }
func (negotiatedOptionsSpec) InitialSessionOptions(model string) map[string]string {
	return map[string]string{"model": model}
}

type initialEffortSpec struct{ negotiatedOptionsSpec }

type cwdOptionsSpec struct {
	negotiatedOptionsSpec
	cwd string
}

func (s cwdOptionsSpec) HandshakeProfile() HandshakeProfile {
	hp := s.negotiatedOptionsSpec.HandshakeProfile()
	hp.SessionNewCwd = s.cwd
	return hp
}

// Empty workspaces preserve the spec cwd; empty profiles use the documented default.
func TestSessionOptions_HandshakePreservesCwdWithoutWorkspace(t *testing.T) {
	for _, tc := range []struct{ profile, workspace, want string }{
		{"/configured", "", "/configured"},
		{"/configured", "/workspace", "/workspace"},
		{"", "", "/"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			transport := &optionsTransport{newOptions: advertisedOptions("sonnet", "medium", "medium")}
			proto := NewACPProtocol(cwdOptionsSpec{cwd: tc.profile}, nil)
			if _, err := proto.Handshake(t.Context(), transport, "sonnet", tc.workspace); err != nil {
				t.Fatal(err)
			}
			if transport.newCwd != tc.want {
				t.Fatalf("session/new cwd = %q, want %q", transport.newCwd, tc.want)
			}
		})
	}
}

func (initialEffortSpec) InitialSessionOptions(model string) map[string]string {
	return map[string]string{"effort": "high", "model": model}
}

func TestSessionOptions_InitialEffortUsesRefreshedOptionsAndPreservesDefault(t *testing.T) {
	proto := NewACPProtocol(initialEffortSpec{}, nil)
	transport := &optionsTransport{newOptions: advertisedOptions("opus", "low", "low")}
	if _, err := proto.Handshake(t.Context(), transport, "sonnet", ""); err != nil {
		t.Fatal(err)
	}
	if err := sendOptionsPrompt(t, proto, transport, ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transport.calls, []string{"model=sonnet", "effort=high", "effort=medium"}) || transport.prompts != 1 {
		t.Fatalf("initial option order/default: calls=%v prompts=%d", transport.calls, transport.prompts)
	}
}

func advertisedOptions(model, effort string, efforts ...string) string {
	values := make([]map[string]string, 0, len(efforts))
	for _, value := range efforts {
		values = append(values, map[string]string{"value": value, "name": value})
	}
	options := []map[string]any{
		{"id": "model", "type": "select", "currentValue": model, "options": []any{
			map[string]any{"group": "models", "name": "Models", "options": []any{
				map[string]string{"value": "sonnet", "name": "Sonnet"},
				map[string]string{"value": "opus", "name": "Opus"},
			}},
		}},
		{"id": "effort", "type": "select", "currentValue": effort, "options": values},
	}
	raw, _ := json.Marshal(options)
	return string(raw)
}

func sendOptionsPrompt(t *testing.T, proto SubprocessProtocol, transport Transport, effort string) error {
	t.Helper()
	call := &aipapi.Call{}
	call.Options.ReasoningEffort = effort
	body, _, err := proto.BindSession(transport, "sid").SendPrompt(t.Context(), "sonnet", "hello", call)
	if body != nil {
		if closeErr := body.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	return err
}

func TestSessionOptions_ModelRefreshAndDefaultAcrossFreshHandles(t *testing.T) {
	proto := NewACPProtocol(negotiatedOptionsSpec{}, nil)
	transport := &optionsTransport{newOptions: advertisedOptions("opus", "low", "low")}
	if _, err := proto.Handshake(t.Context(), transport, "sonnet", ""); err != nil {
		t.Fatal(err)
	}
	for _, effort := range []string{"high", ""} {
		if err := sendOptionsPrompt(t, proto, transport, effort); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"model=sonnet", "effort=high", "effort=medium"}
	if !reflect.DeepEqual(transport.calls, want) || transport.prompts != 2 {
		t.Fatalf("calls=%v prompts=%d, want %v and 2 prompts", transport.calls, transport.prompts, want)
	}
}

func TestSessionOptions_RejectBeforePrompt(t *testing.T) {
	for _, tc := range []struct {
		name, options, model, effort, result string
		setErr                               error
		handshakeFails                       bool
	}{
		{name: "missing options", options: `null`, model: "sonnet", handshakeFails: true},
		{name: "missing required effort", options: `[{"id":"model","type":"select","currentValue":"sonnet","options":[{"value":"sonnet"}]}]`, model: "sonnet", handshakeFails: true},
		{name: "malformed group", options: `[{"id":"model","type":"select","currentValue":"sonnet","options":[{"group":"models","options":null}]}]`, model: "sonnet", handshakeFails: true},
		{name: "malformed current", options: strings.Replace(advertisedOptions("opus", "low", "low"), `"currentValue":"opus"`, `"currentValue":null`, 1), model: "sonnet", handshakeFails: true},
		{name: "duplicate values", options: advertisedOptions("opus", "low", "low", "low"), model: "sonnet", handshakeFails: true},
		{name: "unadvertised model", model: "haiku", handshakeFails: true},
		{name: "model substitution", model: "opus", handshakeFails: true},
		{name: "missing refreshed options", model: "sonnet", result: `{}`, handshakeFails: true},
		{name: "unadvertised effort", model: "sonnet", effort: "max"},
		{name: "stale advertised effort", options: advertisedOptions("opus", "max", "max"), model: "sonnet", effort: "max"},
		{name: "effort changes model", model: "sonnet", effort: "high", result: `{"configOptions":` + advertisedOptions("opus", "high", "low", "medium", "high") + `}`},
		{name: "effort substitution", model: "sonnet", effort: "high", result: `{"configOptions":` + advertisedOptions("sonnet", "low", "low", "medium", "high") + `}`},
		{name: "exchange failure", model: "sonnet", effort: "high", setErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := tc.options
			if options == "" {
				options = advertisedOptions("opus", "low", "low")
			}
			transport := &optionsTransport{newOptions: options}
			proto := NewACPProtocol(negotiatedOptionsSpec{}, nil)
			if tc.handshakeFails {
				transport.setResult = tc.result
			}
			_, err := proto.Handshake(t.Context(), transport, tc.model, "")
			if !tc.handshakeFails {
				if err != nil {
					t.Fatal(err)
				}
				transport.setResult, transport.setErr = tc.result, tc.setErr
				err = sendOptionsPrompt(t, proto, transport, tc.effort)
			}
			want := error(aipapi.ErrCapabilityReject)
			if tc.setErr != nil {
				want = tc.setErr
			}
			if !errors.Is(err, want) || (tc.setErr != nil && errors.Is(err, aipapi.ErrCapabilityReject)) || transport.prompts != 0 {
				t.Fatalf("error=%v prompts=%d, want capability rejection before prompt", err, transport.prompts)
			}
			// A fresh per-turn handle cannot revive a failed/ambiguous exchange.
			transport.setErr, transport.setResult = nil, ""
			if err := sendOptionsPrompt(t, proto, transport, ""); !errors.Is(err, aipapi.ErrCapabilityReject) || transport.prompts != 0 {
				t.Fatalf("failed state reused: error=%v prompts=%d", err, transport.prompts)
			}
		})
	}
}

type invalidOptionsSpec struct {
	negotiatedOptionsSpec
	effortID string
	initial  map[string]string
}

func (s invalidOptionsSpec) EffortConfigID() string { return s.effortID }
func (s invalidOptionsSpec) InitialSessionOptions(string) map[string]string {
	return s.initial
}

// A connector spec that cannot express the requested model/effort in session
// options must fail closed before session/new; negotiating it would collide
// with the model option or publish a model other than the requested one.
func TestSessionOptions_InvalidInitialSpecFailsClosedBeforeSessionNew(t *testing.T) {
	for _, tc := range []struct {
		name     string
		effortID string
		initial  map[string]string
	}{
		{name: "empty effort id", initial: map[string]string{"model": "sonnet"}},
		{name: "effort id collides with model", effortID: "model", initial: map[string]string{"model": "sonnet"}},
		{name: "empty model", effortID: "effort", initial: map[string]string{"model": ""}},
		{name: "initial model mismatch", effortID: "effort", initial: map[string]string{"model": "opus", "effort": "high"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &optionsTransport{newOptions: advertisedOptions("sonnet", "medium", "low", "medium", "high")}
			proto := NewACPProtocol(invalidOptionsSpec{effortID: tc.effortID, initial: tc.initial}, nil)
			_, err := proto.Handshake(t.Context(), transport, "sonnet", "")
			if !errors.Is(err, aipapi.ErrCapabilityReject) {
				t.Fatalf("error = %v, want capability rejection", err)
			}
			if transport.newCalls != 0 {
				t.Fatalf("session/new must not run for an invalid spec: %d calls", transport.newCalls)
			}
		})
	}
}

// Client-supplied reasoning effort reaches the negotiated exchange normalized:
// surrounding whitespace and case are not part of the advertised option values.
func TestSessionOptions_ReasoningEffortNormalizedBeforeExchange(t *testing.T) {
	proto := NewACPProtocol(negotiatedOptionsSpec{}, nil)
	transport := &optionsTransport{newOptions: advertisedOptions("opus", "low", "low")}
	if _, err := proto.Handshake(t.Context(), transport, "sonnet", ""); err != nil {
		t.Fatal(err)
	}
	if err := sendOptionsPrompt(t, proto, transport, " HIGH "); err != nil {
		t.Fatal(err)
	}
	if want := []string{"model=sonnet", "effort=high"}; !reflect.DeepEqual(transport.calls, want) {
		t.Fatalf("calls = %v, want %v", transport.calls, want)
	}
}

// Task 3.2: withdrawal must preserve I/O/RPC identity at every exchange stage;
// advertisement/shape/substitution validation remains capability rejection.
func TestSessionOptions_ExchangeFailurePreservesCauseAndWithdraws(t *testing.T) {
	for _, cause := range []error{io.ErrUnexpectedEOF, io.ErrClosedPipe, context.Canceled, &RPCError{Method: "session/set_config_option", Code: -32000}, &RPCError{Method: "session/set_config_option", Code: -32602}} {
		for _, stage := range []string{"model", "initial-effort", "request", "reset"} {
			t.Run(fmt.Sprintf("%T/%v/%s", cause, cause, stage), func(t *testing.T) {
				proto := NewACPProtocol(initialEffortSpec{}, nil)
				transport := &optionsTransport{newOptions: advertisedOptions("opus", "low", "low"), setErr: cause, setFailAt: 1}
				if stage != "model" {
					transport.setFailAt = 2
				}
				if stage == "request" || stage == "reset" {
					transport.setFailAt = 3
				}
				_, err := proto.Handshake(t.Context(), transport, "sonnet", "")
				if stage == "request" || stage == "reset" {
					if err != nil {
						t.Fatal(err)
					}
					effort := "high"
					if stage == "reset" {
						effort = ""
					}
					err = sendOptionsPrompt(t, proto, transport, effort)
				}
				if !errors.Is(err, cause) || errors.Is(err, aipapi.ErrCapabilityReject) || transport.prompts != 0 {
					t.Fatalf("exchange cause lost/misclassified: %v", err)
				}
				transport.setErr = nil
				if err := sendOptionsPrompt(t, proto, transport, ""); !errors.Is(err, aipapi.ErrCapabilityReject) || transport.prompts != 0 {
					t.Fatalf("failed state reused: %v", err)
				}
			})
		}
	}
}

func TestSessionOptions_TransportWithoutStateRejectsExplicitly(t *testing.T) {
	proto := NewACPProtocol(negotiatedOptionsSpec{}, nil)
	fake := &optionsTransport{newOptions: advertisedOptions("opus", "low", "low")}
	// A wrapper exposing only Transport cannot accidentally opt into state.
	transport := struct{ Transport }{fake}
	if _, err := proto.Handshake(t.Context(), transport, "sonnet", ""); !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("unsupported transport handshake: %v", err)
	}
	if err := sendOptionsPrompt(t, proto, transport, "high"); !errors.Is(err, aipapi.ErrCapabilityReject) || fake.prompts != 0 {
		t.Fatalf("unsupported transport prompt: error=%v prompts=%d", err, fake.prompts)
	}
}

func TestSessionOptions_ExplicitDefaultAndTransportWithdrawal(t *testing.T) {
	proto := NewACPProtocol(negotiatedOptionsSpec{}, nil)
	transport := &optionsTransport{
		newOptions: advertisedOptions("opus", "low", "low"),
		setResult:  `{"configOptions":` + strings.Replace(advertisedOptions("sonnet", "high", "medium", "high"), `"id":"effort"`, `"id":"effort","defaultValue":"medium"`, 1) + `}`,
	}
	if _, err := proto.Handshake(t.Context(), transport, "sonnet", ""); err != nil {
		t.Fatal(err)
	}
	transport.setResult = ""
	if err := sendOptionsPrompt(t, proto, transport, ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transport.calls, []string{"model=sonnet", "effort=medium"}) {
		t.Fatal(transport.calls)
	}
	transport.sessionOptions.close()
	if err := sendOptionsPrompt(t, proto, transport, "high"); !errors.Is(err, aipapi.ErrCapabilityReject) || transport.prompts != 1 {
		t.Fatalf("withdrawn state reused: error=%v prompts=%d", err, transport.prompts)
	}
	if _, err := proto.Handshake(t.Context(), transport, "sonnet", ""); !errors.Is(err, io.ErrClosedPipe) || errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("ended transport republished state: %v", err)
	}
}

func TestSessionOptions_IndependentTransportsAndReplacement(t *testing.T) {
	proto := NewACPProtocol(negotiatedOptionsSpec{}, nil)
	for _, defaultEffort := range []string{"low", "medium"} {
		t.Run(defaultEffort, func(t *testing.T) {
			t.Parallel()
			transport := &optionsTransport{
				newOptions: advertisedOptions("opus", "low", "low"),
				setResult:  `{"configOptions":` + advertisedOptions("sonnet", defaultEffort, "low", "medium", "high") + `}`,
			}
			if _, err := proto.Handshake(t.Context(), transport, "sonnet", ""); err != nil {
				t.Fatal(err)
			}
			if err := sendOptionsPrompt(t, proto, transport, ""); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(transport.calls, []string{"model=sonnet", "effort=" + defaultEffort}) {
				t.Fatalf("transport leaked state: %v", transport.calls)
			}
			fresh := &optionsTransport{}
			if err := sendOptionsPrompt(t, proto, fresh, ""); !errors.Is(err, aipapi.ErrCapabilityReject) || fresh.prompts != 0 {
				t.Fatalf("replacement inherited state: error=%v prompts=%d", err, fresh.prompts)
			}
		})
	}
}

func TestSessionOptions_LegacyIgnoresAdvertisement(t *testing.T) {
	proto := NewACPProtocol(legacyOptionsSpec{}, nil)
	transport := &optionsTransport{newOptions: `null`}
	if _, err := proto.Handshake(t.Context(), transport, "unadvertised", ""); err != nil {
		t.Fatal(err)
	}
	if err := sendOptionsPrompt(t, proto, transport, "HIGH"); err != nil {
		t.Fatal(err)
	}
	if err := sendOptionsPrompt(t, proto, transport, ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transport.calls, []string{"reasoning_effort=high"}) || transport.prompts != 2 {
		t.Fatalf("legacy changed: calls=%v prompts=%d", transport.calls, transport.prompts)
	}
}

func FuzzDecodeSessionOptions(f *testing.F) {
	f.Add([]byte(`{"configOptions":` + advertisedOptions("sonnet", "medium", "low", "medium", "high") + `}`))
	f.Add([]byte(`{"configOptions":null}`))
	f.Add([]byte(`{"configOptions":[{"id":"model","type":"select","currentValue":"x","options":[{"group":"g","options":null}]}]}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		options, err := decodeSessionOptions(capBytes(raw, 8<<10))
		if err != nil {
			return
		}
		if len(options) == 0 {
			t.Fatal("accepted missing options")
		}
		for id, option := range options {
			if id == "" || !option.values[option.current] || !option.values[option.defaultValue] {
				t.Fatalf("accepted unadvertised current/default for %q", id)
			}
		}
	})
}
