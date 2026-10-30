package product

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// 1.4/4.1/4.2: syntax-only normalization and atomic discovered authorization.
// A static fallback, partial publication or borrowed metadata breaks this test.
func TestClaudeSpec_DiscoveredSnapshotIsExactAtomicAndCloned(t *testing.T) {
	t.Parallel()
	spec := claudeTestSpec(t, nil)
	if _, err := spec.Inventory().LoadModels(t.Context()); err == nil {
		t.Fatal("unwired discovery published defaults")
	}
	rows := []DiscoveredModel{
		{NativeID: "claude-nebula-17-3-20261013[1m]", DisplayName: "Nebula", Capabilities: map[string]json.RawMessage{"supportsEffort": json.RawMessage(`true`), "supportedEffortLevels": json.RawMessage(`["low","high"]`)}},
		{NativeID: "arbitrary-family-9.2", DisplayName: "Arbitrary"},
	}
	fetched := time.Now().UTC()
	if err := spec.PublishModels(ModelDiscovery{Version: 1, CurrentModelID: rows[0].NativeID, Models: rows}, fetched); err != nil {
		t.Fatal(err)
	}
	rows[0].Capabilities["supportsEffort"][0] = 'f'
	rows[0].NativeID = "changed"
	if got := string(spec.models.Load().rows[0].Capabilities["supportsEffort"]); got != "true" {
		t.Fatalf("published capabilities borrowed caller memory: %s", got)
	}
	snapshot, err := spec.Inventory().LoadModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Source != "remote" || !snapshot.LoadedAt.Equal(fetched) || snapshot.Models[0].CanonicalID != "anthropic/claude-nebula-17.3-20261013[1m]" {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	// The connector designates its current model as the instance's default, so
	// the host derives the default route from that designation instead of the
	// enumeration order.
	if !snapshot.Models[0].IsDefault {
		t.Fatal("the current model must be published as the instance's default model")
	}
	if snapshot.Models[1].IsDefault {
		t.Fatal("only the current model may be published as the default model")
	}
	for _, selector := range []string{"auto", "", snapshot.Models[0].CanonicalID, snapshot.Models[0].NativeID} {
		if got := spec.ResolveModel(selector); got != snapshot.Models[0].NativeID {
			t.Fatalf("%q resolved to %q", selector, got)
		}
	}
	if got := spec.ResolveModel("nebula"); got != "" {
		t.Fatalf("alias resolved: %q", got)
	}
	for _, bad := range []ModelDiscovery{
		{Version: 2},
		{Version: 1},
		{Version: 1, CurrentModelID: "moving", Models: []DiscoveredModel{{NativeID: "moving"}}},
		{Version: 1, CurrentModelID: "new-1-2", Models: []DiscoveredModel{{NativeID: "new-1-2"}, {NativeID: "new-1.2"}}},
		{Version: 1, CurrentModelID: "new-1-2", Models: []DiscoveredModel{{NativeID: "new-1-2", Capabilities: map[string]json.RawMessage{"supportsEffort": json.RawMessage(` null `)}}}},
	} {
		if err := spec.PublishModels(bad, fetched); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
		if spec.ResolveModel("auto") != snapshot.Models[0].NativeID {
			t.Fatal("failed snapshot changed authorization")
		}
	}
	snapshot.Models[0].NativeID = "borrowed"
	if _, _, _, err := spec.BuildCommand(spec.ResolveModel("auto"), "/workspace"); err != nil {
		t.Fatal(err)
	}
	if err := spec.PublishModels(ModelDiscovery{Version: 1, CurrentModelID: "replacement-8-9", Models: []DiscoveredModel{{NativeID: "replacement-8-9"}}}, fetched); err != nil {
		t.Fatal(err)
	}
	if spec.ResolveModel("auto") != "replacement-8-9" || spec.ResolveModel("arbitrary-family-9.2") != "" {
		t.Fatal("successful replacement retained old authorization")
	}
}

// Claim: PublishModels validates the whole untrusted discovery before one
// atomic replacement, so an out-of-bound or malformed response is rejected
// without disturbing the previously published authorization.
// Source: spec.go PublishModels (bounded private _aip/models_v1 response).
// Mutants: drop the model-count bound, the capability-size bound, the
// effort-level checks or the native-id bound.
func TestClaudeSpec_PublishRejectsUnboundedOrMalformedDiscovery(t *testing.T) {
	t.Parallel()
	spec := claudeTestSpec(t, []string{"baseline-family-3-4"})
	rows := make([]DiscoveredModel, 0, 257)
	for i := range 257 {
		rows = append(rows, DiscoveredModel{NativeID: fmt.Sprintf("model%d-1-2", i), DisplayName: "row"})
	}
	effortRows := []DiscoveredModel{{NativeID: "effort-family-3-4", DisplayName: "row", Capabilities: map[string]json.RawMessage{"supportedEffortLevels": json.RawMessage(`["low","low"]`)}}}
	for _, tc := range []struct {
		name      string
		discovery ModelDiscovery
		fetchedAt time.Time
	}{
		{"model count above bound", ModelDiscovery{Version: 1, CurrentModelID: "model0-1-2", Models: rows}, time.Now()},
		{"zero fetch time", ModelDiscovery{Version: 1, CurrentModelID: "baseline-family-3-4", Models: []DiscoveredModel{{NativeID: "baseline-family-3-4"}}}, time.Time{}},
		{"capability aggregate above bound", ModelDiscovery{Version: 1, CurrentModelID: "blob-family-3-4", Models: []DiscoveredModel{{NativeID: "blob-family-3-4", DisplayName: "row", Capabilities: map[string]json.RawMessage{"blob": json.RawMessage(`"` + strings.Repeat("a", 17000) + `"`)}}}}, time.Now()},
		{"effort levels not an array", ModelDiscovery{Version: 1, CurrentModelID: "effort-family-3-4", Models: []DiscoveredModel{{NativeID: "effort-family-3-4", DisplayName: "row", Capabilities: map[string]json.RawMessage{"supportedEffortLevels": json.RawMessage(`"high"`)}}}}, time.Now()},
		{"duplicate effort levels", ModelDiscovery{Version: 1, CurrentModelID: "effort-family-3-4", Models: effortRows}, time.Now()},
		{"control character in display name", ModelDiscovery{Version: 1, CurrentModelID: "name-family-3-4", Models: []DiscoveredModel{{NativeID: "name-family-3-4", DisplayName: "row\u0000"}}}, time.Now()},
		{"native id above bound", ModelDiscovery{Version: 1, CurrentModelID: strings.Repeat("a", 299) + "1", Models: []DiscoveredModel{{NativeID: strings.Repeat("a", 299) + "1", DisplayName: "row"}}}, time.Now()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := spec.PublishModels(tc.discovery, tc.fetchedAt); !errors.Is(err, aipapi.ErrCapabilityReject) {
				t.Fatalf("accepted %s: %v", tc.name, err)
			}
			if got := spec.ResolveModel("auto"); got != "baseline-family-3-4" {
				t.Fatalf("%s replaced published authorization with %q", tc.name, got)
			}
			if got := spec.ResolveModel("model0-1-2"); got != "" {
				t.Fatalf("%s published a rejected snapshot partially: %q", tc.name, got)
			}
		})
	}
}

// Claim: LoadModels honours its context contract: a nil context returns
// modelinventory.ErrNilContext and a cancelled context surfaces the
// cancellation instead of a stale snapshot.
// Source: spec.go LoadModels godoc and provider contract.
// Mutants: drop the nil-context check (panics) or the ctx.Err() propagation
// (returns a snapshot after cancellation).
func TestClaudeSpec_LoadModelsRefusesNilAndCancelledContext(t *testing.T) {
	t.Parallel()
	spec := claudeTestSpec(t, []string{"context-family-3-4"})
	_, err := spec.Inventory().LoadModels(nil) //nolint:staticcheck // SA1012: nil Context is the API contract under test.
	if !errors.Is(err, modelinventory.ErrNilContext) {
		t.Fatalf("nil context: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	snapshot, err := spec.Inventory().LoadModels(ctx)
	if !errors.Is(err, context.Canceled) || len(snapshot.Models) != 0 {
		t.Fatalf("cancelled context: %+v %v", snapshot, err)
	}
}

func TestCanonicalModelID_SyntaxOnlyPreservesDatesContextAndUnknownGrammar(t *testing.T) {
	t.Parallel()
	for native, want := range map[string]string{
		"arbitrary-family-17-3":             "anthropic/arbitrary-family-17.3",
		"arbitrary-family-17.3":             "anthropic/arbitrary-family-17.3",
		"arbitrary-family-17-3-20261013-1m": "anthropic/arbitrary-family-17.3-20261013-1m",
		"arbitrary-family-17-3[1m]":         "anthropic/arbitrary-family-17.3[1m]",
		"arbitrary-family-17-20261013":      "anthropic/arbitrary-family-17-20261013",
		"arbitrary-family-1-2-3-4":          "anthropic/arbitrary-family-1-2-3-4",
		"custom2-model":                     "anthropic/custom2-model",
	} {
		if got := canonicalModelID(native); got != want {
			t.Fatalf("%s: got %s want %s", native, got, want)
		}
	}
}

func claudeTestSpecForSource(t *testing.T, source string) *ClaudeSpec {
	t.Helper()
	var auth *AuthEnv
	var err error
	switch source {
	case "gateway":
		auth, err = NewAuthEnv(AuthConfig{
			CredentialSource: source,
			Gateway:          GatewayConfig{BaseURL: "https://gw.example.com"},
		}, map[string]string{}, []string{"HOME=/host"})
	default:
		auth, err = NewAuthEnv(AuthConfig{}, map[string]string{"api_key": "test-only"}, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	spec, err := NewClaudeSpec("test-adapter", t.TempDir(), auth)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func claudeTestSpec(t *testing.T, models []string) *ClaudeSpec {
	t.Helper()
	auth, err := NewAuthEnv(AuthConfig{PermissionPolicy: "deny"}, map[string]string{"api_key": "test-only"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := NewClaudeSpec("test-adapter", t.TempDir(), auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) > 0 {
		rows := make([]DiscoveredModel, 0, len(models))
		for _, id := range models {
			rows = append(rows, DiscoveredModel{NativeID: id, DisplayName: id})
		}
		if err := spec.PublishModels(ModelDiscovery{Version: 1, CurrentModelID: models[0], Models: rows}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return spec
}

func TestClaudeSpec_DiscoveredInventoryAndUnknownRoutesNeverSpawn(t *testing.T) {
	t.Parallel()
	for _, models := range [][]string{{"future-family-17-3"}, {"custom-model-8-2"}} {
		spec := claudeTestSpec(t, models)
		inventory := spec.Inventory()
		snapshot, err := inventory.LoadModels(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want := models
		if len(snapshot.Models) != len(want) {
			t.Fatalf("inventory: %+v", snapshot)
		}
		for i, model := range snapshot.Models {
			if model.NativeID != want[i] || !strings.HasPrefix(model.CanonicalID, "anthropic/") {
				t.Fatalf("model: %+v", model)
			}
			cmd, cwd, env, err := spec.BuildCommand(spec.ResolveModel(model.CanonicalID), "/workspace")
			if err != nil || !reflect.DeepEqual(cmd, []string{"test-adapter"}) || cwd != "/workspace" || len(env) != 0 {
				t.Fatalf("command: %v %q %v %v", cmd, cwd, env, err)
			}
		}
		starter := &authStarter{}
		engine := acp.NewSubprocessBackend(acp.SubprocessBackendConfig{Protocol: acp.NewACPProtocol(spec, nil), Workspace: acp.WorkspacePolicy{DefaultDir: t.TempDir(), RequireExplicit: true}, ProcessStarter: starter})
		for _, model := range []string{"unknown", "anthropic/sonnet", "claude-agent/unknown"} {
			cwd, err := json.Marshal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			call := &aipapi.Call{Extensions: map[string]json.RawMessage{"acp.cwd": cwd}}
			call.Route.Selector = "claudeagentacp:" + model
			if _, err := engine.Open(t.Context(), call); !errors.Is(err, aipapi.ErrCapabilityReject) {
				t.Fatalf("%s: %v", model, err)
			}
		}
		if starter.starts != 0 {
			t.Fatalf("unknown routes spawned %d processes", starter.starts)
		}
		if err := engine.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeSpec_RejectsInvalidConstruction(t *testing.T) {
	auth, err := NewAuthEnv(AuthConfig{}, map[string]string{"api_key": "test-only"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewClaudeSpec("", "/workspace", auth); err == nil {
		t.Fatal("accepted missing executable")
	}
	if _, err := NewClaudeSpec("adapter", "/workspace", nil); err == nil {
		t.Fatal("accepted missing authentication profile")
	}
}

func TestClaudeSpec_ExactModelRefreshedEffortAndOmittedReset(t *testing.T) {
	for _, tc := range []struct {
		model, effort string
		reject        bool
	}{{"future-family-17-3", "high", false}, {"other-family-8-2", "high", true}, {"missing-family-9-1", "high", true}} {
		t.Run(tc.model, func(t *testing.T) {
			spec := claudeTestSpec(t, []string{"future-family-17-3", "other-family-8-2"})
			proc := newClaudeOptionsProcess(t)
			transport := acp.NewStdioTransport(proc, slog.New(slog.NewTextHandler(io.Discard, nil)))
			t.Cleanup(func() {
				if err := transport.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			proto := acp.NewACPProtocol(spec, nil)
			sid, err := proto.Handshake(ctx, transport, tc.model, "/workspace")
			if tc.model == "missing-family-9-1" {
				if !errors.Is(err, aipapi.ErrCapabilityReject) {
					t.Fatalf("unadvertised model: %v", err)
				}
				if err := transport.Close(); err != nil {
					t.Fatal(err)
				}
				if len(proc.calls) != 0 {
					t.Fatalf("unadvertised model sent options or prompt: %v", proc.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, effort := range []string{tc.effort, ""} {
				call := &aipapi.Call{Extensions: map[string]json.RawMessage{"acp.session_new_meta": json.RawMessage(`{"claudeCode":{"options":{"permissionMode":"bypassPermissions"}}}`)}}
				call.Options.ReasoningEffort = effort
				body, _, err := proto.BindSession(transport, sid).SendPrompt(ctx, tc.model, "hello", call)
				if tc.reject {
					if !errors.Is(err, aipapi.ErrCapabilityReject) {
						t.Fatalf("unsupported effort: %v", err)
					}
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, body); err != nil {
					t.Fatal(err)
				}
				if err := body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := transport.Close(); err != nil {
				t.Fatal(err)
			}
			want := []string{"model=future-family-17-3", "effort=high", "prompt", "effort=medium", "prompt"}
			if tc.reject {
				want = []string{"model=other-family-8-2"}
			}
			if !reflect.DeepEqual(proc.calls, want) {
				t.Fatalf("calls=%v want=%v", proc.calls, want)
			}
			var meta struct {
				ClaudeCode struct {
					Options struct {
						PermissionMode                  string
						AllowDangerouslySkipPermissions bool
						Tools                           []string
					}
				}
			}
			if err := json.Unmarshal(proc.meta, &meta); err != nil {
				t.Fatal(err)
			}
			options := meta.ClaudeCode.Options
			if options.PermissionMode != "default" || options.AllowDangerouslySkipPermissions || options.Tools == nil || len(options.Tools) != 0 {
				t.Fatalf("unsafe metadata: %s", proc.meta)
			}
		})
	}
}

// Only the external JSON-RPC peer is fake; the production stdio transport owns
// negotiated state across fresh per-turn handles.
type claudeOptionsProcess struct {
	in    *io.PipeWriter
	out   *io.PipeReader
	done  chan error
	calls []string
	meta  json.RawMessage
}

func newClaudeOptionsProcess(t *testing.T) *claudeOptionsProcess {
	t.Helper()
	in, writer := io.Pipe()
	out, reply := io.Pipe()
	p := &claudeOptionsProcess{in: writer, out: out, done: make(chan error, 1)}
	go func() {
		var runErr error
		defer func() { p.done <- errors.Join(runErr, in.Close(), reply.Close()) }()
		model, effort := "other-family-8-2", "low"
		sequence := 0
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			var req struct {
				ID     json.RawMessage
				Method string
				Params struct {
					ConfigID, Value, SessionID, CheckpointID string
					Meta                                     json.RawMessage `json:"_meta"`
				}
			}
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				runErr = err
				return
			}
			result := map[string]any{}
			switch req.Method {
			case "initialize":
				result["protocolVersion"] = 1
			case "session/new":
				p.meta = req.Params.Meta
				result["sessionId"] = "sid"
			case "session/set_config_option":
				p.calls = append(p.calls, req.Params.ConfigID+"="+req.Params.Value)
				if req.Params.ConfigID == "model" {
					model = req.Params.Value
					if model == "future-family-17-3" {
						effort = "medium"
					}
				} else {
					effort = req.Params.Value
				}
			case "session/prompt":
				p.calls = append(p.calls, "prompt")
				result["stopReason"] = "end_turn"
			case "_aip/effective_identity_v1":
				sequence++
				result = map[string]any{"version": 1, "sessionId": req.Params.SessionID, "checkpointId": req.Params.CheckpointID, "processGeneration": strings.Repeat("a", 64), "queryGeneration": 1, "observationSequence": sequence, "provider": "firstParty", "credentialSource": "api_key"}
			}
			if req.Method == "session/new" || req.Method == "session/set_config_option" {
				values := []any{map[string]string{"value": "low"}}
				if model == "future-family-17-3" {
					values = append(values, map[string]string{"value": "medium"}, map[string]string{"value": "high"})
				}
				result["configOptions"] = []any{
					map[string]any{"id": "model", "type": "select", "currentValue": model, "options": []any{map[string]string{"value": "future-family-17-3"}, map[string]string{"value": "other-family-8-2"}}},
					map[string]any{"id": "effort", "type": "select", "currentValue": effort, "options": values},
				}
			}
			data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			if err == nil {
				_, err = fmt.Fprintln(reply, string(data))
			}
			if err != nil {
				runErr = err
				return
			}
		}
		runErr = scanner.Err()
	}()
	return p
}

func (*claudeOptionsProcess) PID() int                { return 0 }
func (p *claudeOptionsProcess) Stdin() io.WriteCloser { return p.in }
func (p *claudeOptionsProcess) Stdout() io.ReadCloser { return p.out }
func (*claudeOptionsProcess) Stderr() io.ReadCloser   { return io.NopCloser(strings.NewReader("")) }
func (p *claudeOptionsProcess) Kill() error           { return p.in.Close() }
func (p *claudeOptionsProcess) Wait() error           { err := <-p.done; p.done <- err; return err }
