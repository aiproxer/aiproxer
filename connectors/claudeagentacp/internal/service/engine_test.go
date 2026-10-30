package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/product"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Requirements 1.1/2.1/2.6/5.1: the real service and shared stdio pool must
// preserve credential roots, canonical ordering, options and query admission.
func TestService_AllSourcesOrderedTwoTurnsAndCleanup(t *testing.T) {
	for _, source := range []string{"api_key", "subscription_token", "host_login", "gateway"} {
		t.Run(source, func(t *testing.T) {
			svc, adapter, _, probes := serviceFixture(t, source)
			var logs bytes.Buffer
			svc.deps.log = slog.New(slog.NewTextHandler(&logs, nil))
			inst := configureColdFixture(t, svc, source)
			// Stock host requires this declaration to consume the ListModels RPC.
			profileBefore, err := inst.Resolve(t.Context(), nil)
			if err != nil || !profileBefore.SupportsDynamicInventory {
				t.Fatalf("model inventory RPC not advertised: %v", err)
			}
			models, err := inst.ListModels(t.Context(), 2)
			if err != nil || len(models.Models) != 2 || models.Models[1].CanonicalModelID != "anthropic/other-family-8.2" {
				t.Fatalf("inventory: %+v %v", models, err)
			}
			messages := []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{wireText("first")}}}
			for turn := range 2 {
				inv := backendplugin.Invocation{RequestID: fmt.Sprintf("turn-%d", turn), AttemptID: "attempt", ALegID: "a", BLegID: "b", NativeModelID: "future-family-17-3", CanonicalModelID: "anthropic/future-family-17.3", ProxyOwnedSessionID: "client", Messages: messages}
				if turn == 0 {
					effort := "high"
					inv.Options.ReasoningEffort = &effort
				}
				wire := &executeCapture{ctx: t.Context(), invocation: &inv}
				if err := inst.Execute(wire); err != nil {
					t.Fatal(err)
				}
				var deltas []string
				for _, frame := range wire.frames {
					if frame.Event != nil && frame.Event.Delta != nil {
						deltas = append(deltas, string(frame.Event.Kind)+":"+*frame.Event.Delta)
					}
				}
				if !reflect.DeepEqual(deltas, []string{"text_delta:one", "reasoning_delta:think", "text_delta:two"}) {
					t.Fatalf("ordered deltas: %v", deltas)
				}
				if last := wire.frames[len(wire.frames)-1]; last.Terminal == nil || last.Terminal.Status != backendplugin.TerminalSuccess {
					t.Fatalf("terminal: %+v", last)
				}
				messages = append(messages, backendplugin.Message{Role: backendplugin.RoleAssistant, Parts: []backendplugin.Part{wireText("onetwo")}}, backendplugin.Message{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{wireText("second")}})
			}
			if adapter.starts != 2 || len(*probes) != 2 {
				t.Fatalf("runtime/probes: %d/%d", adapter.starts, len(*probes))
			}
			if strings.Count(logs.String(), "claudeagentacp startup") != 2 || strings.Contains(logs.String(), "synthetic-private") || strings.Contains(logs.String(), "foreign") {
				t.Fatal("startup disclosure leaked secrets or was repeated on reuse")
			}
			proc := adapter.processes[1]
			proc.mu.Lock()
			methods, effort, inputs, prompts, sessionNew := append([]string(nil), proc.methods...), append([]string(nil), proc.efforts...), append([]string(nil), proc.inputs...), proc.prompts, proc.sessionNew
			proc.mu.Unlock()
			wantMethods := []string{"initialize"}
			if source == "gateway" {
				wantMethods = append(wantMethods, "authenticate")
			}
			wantMethods = append(wantMethods, "session/new", "session/set_config_option", "session/set_config_option", "_aip/effective_identity_v1", "session/prompt", "session/set_config_option", "_aip/effective_identity_v1", "session/prompt")
			if !reflect.DeepEqual(methods, wantMethods) || !reflect.DeepEqual(effort, []string{"high", "low"}) || prompts != 2 || sessionNew != 1 {
				t.Fatalf("session/options/admission: %v %v %d/%d", methods, effort, prompts, sessionNew)
			}
			if len(inputs) != 2 || !strings.Contains(inputs[0], "first") || !strings.Contains(inputs[1], "second") || strings.Contains(inputs[1], "first") {
				t.Fatalf("conversation was replayed instead of continued: %v", inputs)
			}
			if !reflect.DeepEqual(adapter.environments[1], (*probes)[1]) {
				t.Fatal("diagnostic probe did not use exact process environment")
			}
			profile, err := inst.Resolve(t.Context(), nil)
			wantIdentity := "firstParty/" + source
			if source == "gateway" {
				wantIdentity = "gateway/gw.example.com"
			}
			if err != nil || !strings.Contains(profile.EvidenceSource, `"permission_policy":"bypass"`) || !strings.Contains(profile.EvidenceSource, wantIdentity) || !strings.Contains(profile.EvidenceSource, "honored_potential_server_policy") || strings.Contains(profile.EvidenceSource, "synthetic-private") || profile.ProfileVersion != "fixture-certified-closure" {
				t.Fatalf("disclosure: %+v %v", profile, err)
			}
			state := inst.eng.stateDir
			if err := inst.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatal("instance root survives Close")
			}
			if !proc.stopped() {
				t.Fatal("process not joined on Close")
			}
			if source != "host_login" {
				if _, err := os.Stat(environmentValue(adapter.environments[1], "HOME")); !os.IsNotExist(err) {
					t.Fatal("process root survives Close")
				}
			}
		})
	}
}

// Requirement 5.2: the resolved profile names the gateway posture and host
// before the first conversation and again after a pooled restart.
func TestService_GatewayResolvedProfileNamesHostBeforeConversation(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "gateway")
	inst := configureColdFixture(t, svc, "gateway")
	profile, err := inst.Resolve(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gateway/gw.example.com", "credential_source\":\"gateway"} {
		if !strings.Contains(profile.EvidenceSource, want) {
			t.Fatalf("gateway evidence missing %q: %s", want, profile.EvidenceSource)
		}
	}
	if strings.Contains(profile.EvidenceSource, "gateway-header-secret") {
		t.Fatal("header secret leaked into evidence")
	}
	if adapter.starts != 0 {
		t.Fatal("disclosure resolved without any spawn")
	}
}

func TestService_PolicyChangeReplacesExistingPoolProcess(t *testing.T) {
	svc, adapter, policy, probes := serviceFixture(t, "host_login")
	inst := configureFixture(t, svc, "host_login")
	for turn := range 2 {
		if turn == 1 {
			policy["etc/claude-code/managed-settings.json"] = &fstest.MapFile{Data: []byte(`{"disableAllHooks":true}`)}
		}
		stream, err := inst.eng.Open(t.Context(), turnCall())
		if err != nil {
			t.Fatal(err)
		}
		consume(t, stream)
	}
	if adapter.starts != 2 || len(*probes) != 2 || !adapter.processes[0].stopped() {
		t.Fatal("policy change did not replace and re-gate the claimed runtime")
	}
	profile, err := inst.Resolve(t.Context(), nil)
	if err != nil || !strings.Contains(profile.EvidenceSource, "honored_administrator") {
		t.Fatalf("policy disclosure: %+v %v", profile, err)
	}
}

func TestService_AdmissionMismatchKillsBeforePromptAndResets(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	adapter.wrongIdentity = true
	inst := configureFixture(t, svc, "api_key")
	_, err := inst.eng.Open(t.Context(), turnCall())
	if err == nil || adapter.starts != 1 || !adapter.processes[0].stopped() {
		t.Fatalf("admission refusal: %v", err)
	}
	if adapter.processes[0].prompts != 0 {
		t.Fatal("mismatched identity reached SDK input")
	}
	adapter.wrongIdentity = false
	stream, err := inst.eng.Open(t.Context(), turnCall())
	if err != nil {
		t.Fatal(err)
	}
	consume(t, stream)
	if adapter.starts != 2 || adapter.processes[1].prompts != 1 {
		t.Fatal("admission failure did not replace/reset the runtime")
	}
}

func TestService_StructuredRPCClassificationAndSecretSafety(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			svc, adapter, _, _ := serviceFixture(t, "subscription_token")
			adapter.promptError, adapter.outputBeforeError = true, after
			inst := configureFixture(t, svc, "subscription_token")
			stream, err := inst.eng.Open(t.Context(), turnCall())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stream.Close() }()
			for {
				_, err = stream.Recv(t.Context())
				if err != nil {
					break
				}
			}
			rpc, ok := errors.AsType[*acp.RPCError](err)
			if !ok || rpc.Code != -32000 || rpc.Method != "session/prompt" || rpc.Message != "" || !strings.Contains(err.Error(), "claude setup-token") || strings.Contains(fmt.Sprintf("%+v %#v", err, rpc), "synthetic-private") || aipapi.IsRecoverablePreOutput(err) == after {
				t.Fatalf("classification (post=%v): %v", after, err)
			}
		})
	}
}

func TestService_OptionRPCAuthenticationKeepsCodeAndRemediation(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "host_login")
	adapter.optionAuth = true
	inst := configureFixture(t, svc, "host_login")
	_, err := inst.eng.Open(t.Context(), turnCall())
	rpc, ok := errors.AsType[*acp.RPCError](err)
	if !ok || rpc.Code != -32000 || !strings.Contains(err.Error(), "claude auth login") || !aipapi.IsRecoverablePreOutput(err) || errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("option auth classification: %v", err)
	}
}

func TestService_ContextBoundStartupStopsBeforeSpawn(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	key := startupContextKey{}
	observed := false
	svc.deps.identity.Probe = func(ctx context.Context, _, _ string, _ []string) ([]byte, error) {
		observed = ctx.Value(key) == "turn"
		<-ctx.Done()
		return nil, ctx.Err()
	}
	inst := configureColdFixture(t, svc, "api_key")
	ctx, cancel := context.WithTimeout(context.WithValue(t.Context(), key, "turn"), 50*time.Millisecond)
	defer cancel()
	_, err := inst.eng.Open(ctx, turnCall())
	if err == nil || adapter.starts != 0 || !observed {
		t.Fatalf("startup ignored lifecycle context: %v", err)
	}
	entries, readErr := os.ReadDir(inst.eng.stateDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatal("failed startup leaked prepared root")
	}
}

func TestConfigure_UncertifiedInstalledAdapterNeverExecutes(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	exe := filepath.Join(dir, "adapter")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf executed > '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	req := backendplugin.ConfigureRequest{ConfigYAML: []byte("admin_policy: honor\nexecutable: " + exe), Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("synthetic-private")}}}
	_, err := New().Configure(t.Context(), req)
	if err == nil || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatalf("uncertified profile admitted: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("uncertified executable ran")
	}
}

func TestService_UnknownModelAndMissingWorkspaceNeverSpawn(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	inst := configureFixture(t, svc, "api_key")
	call := turnCall()
	call.Route.Selector = "claude-agent/unknown"
	_, err := inst.eng.Open(t.Context(), call)
	if !errors.Is(err, aipapi.ErrCapabilityReject) || adapter.starts != 0 {
		t.Fatalf("unknown model spawned or substituted: %v", err)
	}
	withoutDefault, err := svc.Configure(t.Context(), backendplugin.ConfigureRequest{ConfigYAML: []byte("admin_policy: honor"), Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("synthetic-private-key")}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = withoutDefault.Close(context.Background()) })
	configured, ok := withoutDefault.(*instance)
	if !ok {
		t.Fatalf("configured instance type: %T", withoutDefault)
	}
	_, err = configured.eng.Open(t.Context(), turnCall())
	if err == nil || adapter.starts != 0 {
		t.Fatal("missing explicit workspace spawned")
	}
}

func turnCall() *aipapi.Call {
	// Pooling tests model a genuinely resumed, host-authorized session.
	return &aipapi.Call{ID: "turn", Session: aipapi.SessionRef{AuthoritativeSessionID: "client"}, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}}}
}

func consume(t *testing.T, stream aipapi.ManagedEventStream) {
	t.Helper()
	defer func() {
		if err := stream.Close(); err != nil {
			t.Error(err)
		}
	}()
	for {
		_, err := stream.Recv(t.Context())
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func configureFixture(t *testing.T, svc *Service, source string, extra ...string) *instance {
	t.Helper()
	inst := configureColdFixture(t, svc, source, extra...)
	// These legacy option/admission/cancellation/tree tests intentionally isolate
	// the already-loaded inventory path: fault injection targets the TURN peer,
	// not discovery. Cold discovery and full two-turn assembly are tested below
	// and in TestService_AllSourcesOrderedTwoTurnsAndCleanup without this seed.
	if err := inst.eng.spec.PublishModels(product.ModelDiscovery{Version: 1, CurrentModelID: "future-family-17-3", Models: []product.DiscoveredModel{{NativeID: "future-family-17-3", DisplayName: "Future"}, {NativeID: "other-family-8-2", DisplayName: "Other"}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return inst
}

func configureColdFixture(t *testing.T, svc *Service, source string, extra ...string) *instance {
	t.Helper()
	gatewayYAML := ""
	if source == "gateway" {
		gatewayYAML = "gateway:\n  base_url: https://gw.example.com\n  headers:\n    - name: X-Route\n"
	}
	cfg := fmt.Sprintf("default_workspace: %q\ncredential_source: %s\nadmin_policy: honor\n", t.TempDir(), source)
	cfg += strings.Join(extra, "\n") + gatewayYAML
	secrets := map[string][]byte{"api_key": []byte("synthetic-private-key"), "claude_oauth_token": []byte("synthetic-private-token")}
	if source == "gateway" {
		secrets = map[string][]byte{"X-Route": []byte("gateway-header-secret")}
	}
	inst, err := svc.Configure(t.Context(), backendplugin.ConfigureRequest{ConfigYAML: []byte(cfg), Secrets: backendplugin.SecretBundle{Values: secrets}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := inst.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	configured, ok := inst.(*instance)
	if !ok {
		t.Fatalf("configured instance type: %T", inst)
	}
	return configured
}

// Task 3.1: ListModels must discover rather than rely on a seeded inventory,
// and return the exact atomic snapshot and real successful fetch timestamp.
func TestService_ColdDiscoveryPublishesExactSnapshot(t *testing.T) {
	svc, adapter, _, probes := serviceFixture(t, "api_key")
	inst := configureColdFixture(t, svc, "api_key")
	before := time.Now()
	listed, err := inst.ListModels(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := inst.eng.spec.Inventory().LoadModels(t.Context())
	if err != nil || listed.InventorySource != string(snapshot.Source) || listed.FetchedUnixMS != snapshot.LoadedAt.UnixMilli() || snapshot.LoadedAt.Before(before) || snapshot.LoadedAt.After(time.Now()) {
		t.Fatalf("snapshot provenance: %+v %+v %v", listed, snapshot, err)
	}
	if len(listed.Models) != 2 || listed.Models[0].CanonicalModelID != "anthropic/future-family-17.3" || listed.Models[0].NativeModelID != "future-family-17-3" || listed.Models[0].DisplayName != "Future" {
		t.Fatalf("exact discovery: %+v", listed)
	}
	if adapter.starts != 1 || len(*probes) != 1 || !adapter.processes[0].stopped() || adapter.processes[0].prompts != 0 {
		t.Fatal("discovery bypassed gates, prompted, or survived its owner")
	}
	entries, err := os.ReadDir(inst.eng.stateDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("discovery leaked owned roots")
	}
}

// Requirement 2.1 and the discovery contract in engine.discoverModels: the
// gateway source authenticates with the exact connector-owned payload before
// the discovery process issues its first models RPC, so discovery and pooled
// turns establish the same adapter provider state from one payload. A discovery
// that skips authenticate publishes models for a source the adapter never
// configured.
func TestService_GatewayDiscoveryAuthenticatesBeforeModelsRPC(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "gateway")
	inst := configureColdFixture(t, svc, "gateway")
	if _, err := inst.ListModels(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	if adapter.starts != 1 {
		t.Fatalf("discovery starts: %d", adapter.starts)
	}
	proc := adapter.processes[0]
	proc.mu.Lock()
	methods, prompts := append([]string(nil), proc.methods...), proc.prompts
	proc.mu.Unlock()
	if want := []string{"initialize", "authenticate", "_aip/models_v1"}; !reflect.DeepEqual(methods, want) {
		t.Fatalf("gateway discovery methods = %v, want %v", methods, want)
	}
	if prompts != 0 || !proc.stopped() {
		t.Fatal("discovery prompted or survived its owner")
	}
}

func TestService_RejectsManualModelsIncludingEmpty(t *testing.T) {
	for _, yaml := range []string{"models: []", "models: [future-family-17-3]", "models: null"} {
		if _, err := ParseConfigYAML([]byte(yaml)); err == nil {
			t.Fatalf("accepted manual inventory %q", yaml)
		}
	}
}

func TestService_ColdOpenDiscoversOnceAndKeepsCallWorkspace(t *testing.T) {
	svc, adapter, _, probes := serviceFixture(t, "api_key")
	inst := configureColdFixture(t, svc, "api_key")
	project := t.TempDir()
	raw, _ := json.Marshal(project)
	call := turnCall()
	call.Extensions = map[string]json.RawMessage{"acp.cwd": raw}
	for range 2 {
		stream, err := inst.eng.Open(t.Context(), call)
		if err != nil {
			t.Fatal(err)
		}
		consume(t, stream)
		call.Messages = append(call.Messages, aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("onetwo")}}, aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}})
	}
	if adapter.starts != 2 || len(*probes) != 2 || !adapter.processes[0].stopped() || adapter.processes[0].prompts != 0 || adapter.processes[1].prompts != 2 {
		t.Fatal("cold Open did not retire one discovery and reuse one turn process")
	}
	if adapter.workspaces[0] == project || adapter.workspaces[1] != project || adapter.processes[1].sessionCwd != project || adapter.processes[0].discoveryCwd != adapter.workspaces[0] {
		t.Fatalf("discovery/turn cwd separation: %v", adapter.workspaces)
	}
}

func TestService_DiscoveryFailureNeverFabricatesOrReplacesSnapshot(t *testing.T) {
	for _, mode := range []string{"empty", "malformed", "invalid", "rpc", "source", "init-invalid", "init-rpc", "init-crash"} {
		t.Run(mode, func(t *testing.T) {
			svc, adapter, _, _ := serviceFixture(t, "api_key")
			inst := configureColdFixture(t, svc, "api_key")
			if _, err := inst.ListModels(t.Context(), 0); err != nil {
				t.Fatal(err)
			}
			old, _ := inst.eng.spec.Inventory().LoadModels(t.Context())
			adapter.discoveryMode = mode
			if mode == "source" {
				// Inject mismatch only at the exact prepared-environment probe.
				adapter.sourceMismatch = true
			}
			listed, err := inst.ListModels(t.Context(), 0)
			if err == nil || len(listed.Models) != 0 || strings.Contains(fmt.Sprintf("%+v", err), "synthetic-private") {
				t.Fatalf("failed refresh fabricated inventory or leaked response: %+v %v", listed, err)
			}
			now, _ := inst.eng.spec.Inventory().LoadModels(t.Context())
			if !reflect.DeepEqual(old, now) {
				t.Fatal("failed refresh replaced atomic snapshot")
			}
			if mode == "source" && adapter.starts != 1 {
				t.Fatal("source mismatch bypassed startup gate")
			}
			for _, proc := range adapter.processes {
				if !proc.stopped() || proc.prompts != 0 {
					t.Fatal("discovery failure left process or user input")
				}
			}
			entries, err := os.ReadDir(inst.eng.stateDir)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed discovery leaked root")
			}
		})
	}
}

// The timeout is virtual; no real provider, sleeping wall-clock minute or
// asynchronous discovery owner is involved. Close waits for the whole cleanup.
func TestService_DiscoveryDeadlineSerialAdmissionAndClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, adapter, _, _ := serviceFixture(t, "api_key")
		adapter.discoveryMode = "block"
		adapter.discoveryEntered = make(chan struct{})
		inst := configureColdFixture(t, svc, "api_key")
		started := time.Now()
		result := make(chan error, 1)
		go func() { _, err := inst.ListModels(t.Context(), 0); result <- err }()
		<-adapter.discoveryEntered
		waitCtx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := inst.ListModels(waitCtx, 0); !errors.Is(err, context.DeadlineExceeded) || adapter.starts != 1 {
			t.Fatalf("queued caller ignored cancellation or spawned: %v", err)
		}
		if err := <-result; !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != 60*time.Second || !adapter.processes[0].stopped() {
			t.Fatalf("discovery deadline/Close: %v elapsed=%s", err, time.Since(started))
		}
		if err := inst.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(inst.eng.stateDir); !os.IsNotExist(err) {
			t.Fatal("Close removed state before discovery retired, or leaked it")
		}
	})
}

func TestService_DiscoveryCancellationRemovesOwnedWorkspaceAndRoot(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	adapter.discoveryMode = "block"
	adapter.discoveryEntered = make(chan struct{})
	configured, err := svc.Configure(t.Context(), backendplugin.ConfigureRequest{ConfigYAML: []byte("admin_policy: honor"), Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("synthetic-private-key")}}})
	if err != nil {
		t.Fatal(err)
	}
	inst, ok := configured.(*instance)
	if !ok {
		t.Fatalf("configured instance type: %T", configured)
	}
	t.Cleanup(func() { _ = inst.Close(context.Background()) })
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := inst.ListModels(ctx, 0); result <- err }()
	<-adapter.discoveryEntered
	cwd := adapter.workspaces[0]
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 0 || !strings.HasPrefix(cwd, inst.eng.stateDir+string(os.PathSeparator)) || adapter.processes[0].discoveryCwd != cwd {
		t.Fatal("no-default discovery did not use empty owned workspace")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) || !adapter.processes[0].stopped() {
		t.Fatalf("cancellation did not join process: %v", err)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatal("owned discovery workspace survived")
	}
	entries, err = os.ReadDir(inst.eng.stateDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("cancelled discovery root survived")
	}
	if inst.eng.spec.ResolveModel("") != "" {
		t.Fatal("cancelled discovery fabricated snapshot")
	}
}

// Requirement 5.4: Close joins the in-flight discovery through the instance
// lease. It must neither return nor retire the state root while a cancelled
// discovery owner still holds that lease.
func TestService_CloseWaitsForCancelledDiscoveryCleanup(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	adapter.discoveryMode = "block"
	adapter.discoveryEntered = make(chan struct{})
	inst := configureColdFixture(t, svc, "api_key")
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := inst.ListModels(ctx, 0); result <- err }()
	<-adapter.discoveryEntered
	closed := make(chan error, 1)
	go func() { closed <- inst.Close(t.Context()) }()
	// Bounded negative assertion, not a sleep as synchronization: the discovery
	// deadline is 300ms, so a 100ms observation window cannot contain a
	// legitimate Close or discovery return.
	observation := time.NewTimer(100 * time.Millisecond)
	select {
	case err := <-closed:
		observation.Stop()
		t.Fatalf("Close returned before the cancelled discovery retired: %v", err)
	case err := <-result:
		observation.Stop()
		t.Fatalf("discovery returned before its own deadline: %v", err)
	case <-observation.C:
	}
	if _, err := os.Stat(inst.eng.stateDir); err != nil {
		t.Fatalf("Close retired the state root while discovery still owned it: %v", err)
	}
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) || !adapter.processes[0].stopped() {
		t.Fatalf("Close abandoned discovery: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inst.eng.stateDir); !os.IsNotExist(err) {
		t.Fatal("Close leaked instance root")
	}
}

func TestService_FailedRefreshCannotAuthorizeStaleSessionModels(t *testing.T) {
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	inst := configureColdFixture(t, svc, "api_key")
	if _, err := inst.ListModels(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	adapter.discoveryMode = "empty"
	if _, err := inst.ListModels(t.Context(), 0); err == nil {
		t.Fatal("empty refresh accepted")
	}
	adapter.omitFirstModel = true
	_, err := inst.eng.Open(t.Context(), turnCall())
	if !errors.Is(err, aipapi.ErrCapabilityReject) || adapter.starts != 3 {
		t.Fatalf("stale model reached input: %v", err)
	}
	proc := adapter.processes[2]
	proc.mu.Lock()
	selected, prompts := append([]string(nil), proc.selectedModels...), proc.prompts
	proc.mu.Unlock()
	if slices.Contains(selected, "future-family-17-3") {
		t.Fatalf("stale inventory model was submitted to a session that never advertised it: %v", selected)
	}
	if prompts != 0 || !proc.stopped() {
		t.Fatalf("stale model reached input: prompts=%d stopped=%v", prompts, proc.stopped())
	}
}

func TestService_ConcurrentColdOpensShareOneDiscovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, adapter, _, _ := serviceFixture(t, "api_key")
		adapter.discoveryMode = "pause"
		adapter.discoveryEntered = make(chan struct{})
		adapter.discoveryRelease = make(chan struct{})
		inst := configureColdFixture(t, svc, "api_key")
		results := make(chan error, 2)
		open := func(call *aipapi.Call) {
			stream, err := inst.eng.Open(t.Context(), call)
			if err == nil {
				for {
					_, err = stream.Recv(t.Context())
					if err != nil {
						break
					}
				}
				if errors.Is(err, io.EOF) {
					err = stream.Close()
				} else {
					_ = stream.Close()
				}
			}
			results <- err
		}
		go open(turnCall())
		<-adapter.discoveryEntered
		ctx, cancel := context.WithCancel(t.Context())
		go func() { _, err := inst.eng.Open(ctx, turnCall()); results <- err }()
		synctest.Wait()
		if adapter.starts != 1 {
			t.Fatal("concurrent cold Open spawned duplicate discovery")
		}
		cancel()
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cold Open ignored cancellation: %v", err)
		}
		close(adapter.discoveryRelease)
		if err := <-results; err != nil {
			t.Fatal(err)
		}
		if adapter.starts != 2 || adapter.processes[1].prompts != 1 {
			t.Fatal("queued cold Open started unowned work")
		}
		if err := inst.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

type startupContextKey struct{}

func TestService_StreamRetirementJoinsInstanceCloseBeforeRemovingState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, adapter, _, _ := serviceFixture(t, "api_key")
		gate := &streamRetirementStarter{adapter: adapter, entered: make(chan struct{}), release: make(chan struct{})}
		svc.deps.starter = gate
		inst := configureFixture(t, svc, "api_key")
		stream, err := inst.eng.Open(t.Context(), turnCall())
		if err != nil {
			t.Fatal(err)
		}
		// No successful terminal was consumed: Close must retire this runtime.
		retired := make(chan error, 1)
		go func() { retired <- stream.Close() }()
		<-gate.entered
		closed := make(chan error, 1)
		go func() { closed <- inst.Close(t.Context()) }()
		synctest.Wait()
		closedEarly := false
		select {
		case err := <-closed:
			closedEarly = true
			t.Errorf("instance Close bypassed outstanding retirement: %v", err)
		default:
		}
		for _, path := range []string{gate.marker, filepath.Dir(gate.marker)} {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("lifecycle state removed during pending shutdown: %v", err)
			}
		}
		close(gate.release)
		if err := <-retired; err != nil {
			t.Error(err)
		}
		if !closedEarly {
			if err := <-closed; err != nil {
				t.Error(err)
			}
		}
		if _, err := os.Stat(inst.eng.stateDir); !os.IsNotExist(err) {
			t.Error("state survived completed retirement")
		}
	})
}

type streamRetirementStarter struct {
	adapter          *adapterStarter
	entered, release chan struct{}
	marker           string
	waitErr          error
}

func (s *streamRetirementStarter) Start(cmd []string, cwd string, env []string) (acp.Process, error) {
	proc, err := s.adapter.Start(cmd, cwd, env)
	if err == nil && s.adapter.starts == 1 {
		s.marker = environmentValue(env, "AIP_CLAUDE_ACP_SHUTDOWN_FILE")
		return &streamRetirementProcess{Process: proc, owner: s}, nil
	}
	return proc, err
}

type streamRetirementProcess struct {
	acp.Process
	owner *streamRetirementStarter
}

func (p *streamRetirementProcess) Wait() error { return errors.Join(p.Process.Wait(), p.owner.waitErr) }

func TestService_IncompleteCleanupRetainsStateAndCloseError(t *testing.T) {
	for _, discovery := range []bool{false, true} {
		svc, adapter, _, _ := serviceFixture(t, "api_key")
		gate := &streamRetirementStarter{adapter: adapter, entered: make(chan struct{}), release: make(chan struct{}), waitErr: acp.ErrCleanupIncomplete}
		close(gate.release)
		svc.deps.starter = gate
		configured, err := svc.Configure(t.Context(), backendplugin.ConfigureRequest{ConfigYAML: fmt.Appendf(nil, "default_workspace: %q\ncredential_source: api_key\nadmin_policy: honor\n", t.TempDir()), Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("synthetic-private-key")}}})
		if err != nil {
			t.Fatal(err)
		}
		inst, ok := configured.(*instance)
		if !ok {
			t.Fatalf("configured instance type: %T", configured)
		}
		t.Cleanup(func() { _ = removeState(inst.eng.stateDir) }) // fixture's fake process is known to be dead
		if !discovery {
			if err := inst.eng.spec.PublishModels(product.ModelDiscovery{Version: 1, CurrentModelID: "future-family-17-3", Models: []product.DiscoveredModel{{NativeID: "future-family-17-3"}}}, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		if discovery {
			if _, err := inst.ListModels(t.Context(), 0); err == nil {
				t.Error("discovery cleanup failure accepted")
			}
		} else {
			stream, err := inst.eng.Open(t.Context(), turnCall())
			if err != nil {
				t.Fatal(err)
			}
			_ = stream.Close()
		}
		for range 2 {
			if err := inst.Close(t.Context()); !errors.Is(err, acp.ErrCleanupIncomplete) {
				t.Errorf("Close lost incomplete cleanup: %v", err)
			}
			if _, err := os.Stat(inst.eng.stateDir); err != nil {
				t.Error("instance state deleted without extinction proof", err)
			}
		}
	}
}

// A recorded cleanup failure must stay observable through the model and open
// boundaries. The classification must not flatten the lifecycle failure into a
// generic recoverable failure, and the runtime must refuse to be reused.
func TestService_CleanupFailureKeepsItsIdentity(t *testing.T) {
	svc, _, _, _ := serviceFixture(t, "api_key")
	configured, err := svc.Configure(t.Context(), backendplugin.ConfigureRequest{ConfigYAML: fmt.Appendf(nil, "default_workspace: %q\ncredential_source: api_key\nadmin_policy: honor\n", t.TempDir()), Secrets: backendplugin.SecretBundle{Values: map[string][]byte{"api_key": []byte("synthetic-private-key")}}})
	if err != nil {
		t.Fatal(err)
	}
	inst, ok := configured.(*instance)
	if !ok {
		t.Fatalf("configured instance type: %T", configured)
	}
	t.Cleanup(func() { _ = removeState(inst.eng.stateDir) })
	inst.eng.mu.Lock()
	inst.eng.cleanupErr = acp.ErrCleanupIncomplete
	inst.eng.mu.Unlock()
	if _, err := inst.ListModels(t.Context(), 0); !errors.Is(err, acp.ErrCleanupIncomplete) {
		t.Errorf("ListModels lost the cleanup identity: %v", err)
	}
	if _, err := inst.eng.Open(t.Context(), turnCall()); !errors.Is(err, acp.ErrCleanupIncomplete) {
		t.Errorf("Open lost the cleanup identity: %v", err)
	}
	if err := inst.Close(t.Context()); !errors.Is(err, acp.ErrCleanupIncomplete) {
		t.Errorf("Close lost the cleanup identity: %v", err)
	}
}

func (p *streamRetirementProcess) Kill() error {
	close(p.owner.entered)
	<-p.owner.release
	return p.Process.Kill()
}

// Task 3.1 / design discovery lease: an unsolicited refusal can make the
// transport reader own shutdown before discovery's caller cancels. Neither
// ListModels nor instance Close may return while that owner is still in Wait.
func TestService_DiscoveryLeaseJoinsConcurrentTransportClose(t *testing.T) {
	svc, _, _, _ := serviceFixture(t, "api_key")
	starter := &discoveryShutdownStarter{started: make(chan *discoveryShutdownPeer, 1)}
	svc.deps.starter = starter
	inst := configureColdFixture(t, svc, "api_key")
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := inst.ListModels(ctx, 0); result <- err }()
	peer := <-starter.started
	// Failure cleanup always releases and joins the external peer; no test-owned
	// process, reader or blocked Wait survives a failing assertion.
	defer func() { peer.releaseOnce.Do(func() { close(peer.releaseWait) }); <-peer.served; <-peer.waitFinished }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	select {
	case <-peer.waitEntered:
	case <-deadline.C:
		t.Fatal("reader-triggered refusal timeout did not enter process Wait")
	}
	<-ctx.Done()
	closed := make(chan error, 1)
	go func() { closed <- inst.Close(context.Background()) }()
	// A bounded negative assertion, not a sleep/poll or shutdown workaround:
	// the peer deliberately cannot finish Wait until the test releases it.
	observation := time.NewTimer(100 * time.Millisecond)
	select {
	case err := <-result:
		observation.Stop()
		t.Fatalf("discovery released its lease before concurrent Wait/root cleanup: %v", err)
	case err := <-closed:
		observation.Stop()
		t.Fatalf("instance Close retired state before concurrent Wait/root cleanup: %v", err)
	case <-observation.C:
	}
	entries, err := os.ReadDir(inst.eng.stateDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("instance/prepared state removed during Wait: %v entries=%d", err, len(entries))
	}
	// Permit the external Wait to finish only after proving the lifecycle lease.
	peer.releaseOnce.Do(func() { close(peer.releaseWait) })
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("discovery lost caller deadline: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inst.eng.stateDir); !os.IsNotExist(err) {
		t.Fatal("instance root survives completed shutdown")
	}
}

// Only the external peer's Wait is delayed. AuthEnv's real sync.Once Wait and
// owned-root cleanup remain in the path under the production gated starter.
type discoveryShutdownPeer struct {
	stdin                                          *io.PipeWriter
	input                                          *io.PipeReader
	stdout                                         *io.PipeReader
	output                                         *io.PipeWriter
	killOnce                                       sync.Once
	waitEntered, releaseWait, waitFinished, served chan struct{}
	releaseOnce                                    sync.Once
}

func (*discoveryShutdownPeer) PID() int                { return 0 }
func (p *discoveryShutdownPeer) Stdin() io.WriteCloser { return p.stdin }
func (p *discoveryShutdownPeer) Stdout() io.ReadCloser { return p.stdout }
func (*discoveryShutdownPeer) Stderr() io.ReadCloser {
	return io.NopCloser(strings.NewReader(""))
}

func (p *discoveryShutdownPeer) Kill() error {
	p.killOnce.Do(func() { _ = p.input.Close(); _ = p.output.Close() })
	return nil
}

func (p *discoveryShutdownPeer) Wait() error {
	close(p.waitEntered)
	<-p.releaseWait
	close(p.waitFinished)
	return nil
}

type discoveryShutdownStarter struct{ started chan *discoveryShutdownPeer }

func (s *discoveryShutdownStarter) Start([]string, string, []string) (acp.Process, error) {
	in, stdin := io.Pipe()
	stdout, out := io.Pipe()
	p := &discoveryShutdownPeer{stdin: stdin, input: in, stdout: stdout, output: out, waitEntered: make(chan struct{}), releaseWait: make(chan struct{}), waitFinished: make(chan struct{}), served: make(chan struct{})}
	s.started <- p
	go func() {
		defer close(p.served)
		scanner := bufio.NewScanner(in)
		if scanner.Scan() {
			// Never read the refusal. Its bounded write owns transport Close,
			// whose atomic flag is already set when discovery later cancels.
			_, _ = io.WriteString(out, "{\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"session/request_permission\",\"params\":{}}\n")
		}
	}()
	return p, nil
}

func serviceFixture(t *testing.T, source string) (*Service, *adapterStarter, fstest.MapFS, *[][]string) {
	t.Helper()
	root := t.TempDir()
	adapter := &adapterStarter{source: source}
	policy := fstest.MapFS{}
	var probes [][]string
	deps := &engineDeps{host: []string{"HOME=" + root, "PATH=/usr/bin:/bin", "ANTHROPIC_API_KEY=foreign"}, starter: adapter, policy: product.PolicyGate{Files: policy}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	deps.identity.Probe = func(ctx context.Context, executable, cwd string, env []string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if executable != "/fixture/cli" || cwd == "" {
			return nil, errors.New("wrong startup identity boundary")
		}
		probes = append(probes, append([]string(nil), env...))
		if adapter.sourceMismatch {
			return []byte(`{"loggedIn":true,"apiProvider":"bedrock","authMethod":"api_key","apiKeySource":"ANTHROPIC_API_KEY"}`), nil
		}
		status := map[string]any{"loggedIn": true, "apiProvider": "firstParty"}
		switch source {
		case "api_key":
			status["authMethod"], status["apiKeySource"] = "api_key", "ANTHROPIC_API_KEY"
		case "subscription_token":
			status["authMethod"] = "oauth_token"
		case "host_login":
			status["authMethod"], status["subscriptionType"] = "claude.ai", "max"
		case "gateway":
			status["loggedIn"], status["authMethod"] = false, "none"
		}
		return json.Marshal(status)
	}
	deps.certify = func(ctx context.Context, r product.AdapterResolver, cfg Config, host, env []string, cwd string) (product.RuntimeProfile, product.IdentityExtension, error) {
		_, err := r.Policy.Check(ctx, environmentValue(env, "CLAUDE_CONFIG_DIR"), cfg.AdminPolicy)
		return product.RuntimeProfile{Executable: "/fixture/adapter/dist/index.js", CLIExecutable: "/fixture/cli"}, product.IdentityExtension{SupervisorExecutable: "/fixture/connector", NodeExecutable: "/fixture/node", Bootstrap: "/fixture/sidecar/bootstrap.mjs", AdapterRoot: "/fixture/adapter", BundleSHA256: "fixture-certified-closure"}, err
	}
	return &Service{deps: deps}, adapter, policy, &probes
}

type executeCapture struct {
	ctx        context.Context
	invocation *backendplugin.Invocation
	frames     []backendplugin.ServerFrame
	cancelTurn bool
	negotiated bool
}

func (s *executeCapture) Negotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{Compatible: s.negotiated, NegotiatedMinor: backendplugin.ProtocolMinorCancellationHandshake, EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake}}
}

func (s *executeCapture) Context() context.Context { return s.ctx }
func (s *executeCapture) Recv() (backendplugin.ClientFrame, error) {
	if s.invocation == nil {
		if s.cancelTurn {
			s.cancelTurn = false
			return backendplugin.ClientFrame{Kind: backendplugin.ClientFrameCancel, CancelReason: backendplugin.CancelReasonClient, CancelDeadlineUnixMS: time.Now().Add(100 * time.Millisecond).UnixMilli()}, nil
		}
		return backendplugin.ClientFrame{}, io.EOF
	}
	inv := s.invocation
	s.invocation = nil
	return backendplugin.ClientFrame{Kind: backendplugin.ClientFrameStart, InstanceID: "agent", Invocation: inv}, nil
}

func (s *executeCapture) Send(frame backendplugin.ServerFrame) error {
	s.frames = append(s.frames, frame)
	return nil
}

// Only the external ACP peer is faked; all framing, session options, admission,
// transcript history and canonical mapping are the real shared implementation.
type adapterStarter struct {
	omitFirstModel                                            bool
	discoveryRelease                                          chan struct{}
	discoveryMode                                             string
	discoveryEntered                                          chan struct{}
	sourceMismatch                                            bool
	workspaces                                                []string
	failAfterTurn                                             bool
	source                                                    string
	starts                                                    int
	wrongIdentity, promptError, outputBeforeError, optionAuth bool
	crash, blockPrompt, malformedIdentity, malformedOptions   bool
	stallCancel                                               bool
	handshakeCrash, handshakeAuth                             bool
	handshakeMethod                                           string
	handshakeOption                                           int
	processes                                                 []*adapterProcess
	environments                                              [][]string
}

func (s *adapterStarter) Start(cmd []string, cwd string, env []string) (acp.Process, error) {
	want := []string{"/fixture/node", "/fixture/sidecar/bootstrap.mjs", "--adapter-root", "/fixture/adapter", "--credential-source", s.source}
	if s.source == "api_key" {
		want = append(want, "--hide-claude-auth")
	}
	if environmentValue(env, "AIP_CLAUDE_ACP_SHUTDOWN_FILE") != "" {
		want = append([]string{"/fixture/connector", "--runtime-supervisor"}, want...)
	}
	if !reflect.DeepEqual(cmd, want) || cwd == "" {
		return nil, errors.New("wrong bootstrap command")
	}
	hasCredentials := environmentValue(env, "ANTHROPIC_API_KEY") != "" || environmentValue(env, "CLAUDE_CODE_OAUTH_TOKEN") != ""
	switch s.source {
	case "api_key":
		if environmentValue(env, "ANTHROPIC_API_KEY") != "synthetic-private-key" {
			return nil, errors.New("wrong credential environment")
		}
	case "subscription_token":
		if environmentValue(env, "CLAUDE_CODE_OAUTH_TOKEN") != "synthetic-private-token" {
			return nil, errors.New("wrong credential environment")
		}
	case "host_login":
	case "gateway":
		if hasCredentials || environmentValue(env, "CLAUDE_GATEWAY_BASE_URL") != "https://gw.example.com" {
			return nil, errors.New("wrong gateway environment")
		}
	}
	s.starts++
	in, stdin := io.Pipe()
	stdout, out := io.Pipe()
	p := &adapterProcess{stdin: stdin, stdout: stdout, input: in, output: out, done: make(chan struct{}), stop: make(chan struct{}), crash: s.crash, blockPrompt: s.blockPrompt, stallCancel: s.stallCancel, malformedIdentity: s.malformedIdentity, malformedOptions: s.malformedOptions, handshakeCrash: s.handshakeCrash, handshakeAuth: s.handshakeAuth, handshakeMethod: s.handshakeMethod}
	p.failAfterTurn = s.failAfterTurn
	p.handshakeOption = s.handshakeOption
	p.discoveryMode, p.discoveryEntered = s.discoveryMode, s.discoveryEntered
	p.omitFirstModel, p.discoveryRelease = s.omitFirstModel, s.discoveryRelease
	s.processes = append(s.processes, p)
	s.environments = append(s.environments, append([]string(nil), env...))
	s.workspaces = append(s.workspaces, cwd)
	go p.serve(s.source, s.wrongIdentity, s.promptError, s.outputBeforeError, s.optionAuth)
	return p, nil
}

type adapterProcess struct {
	omitFirstModel                                          bool
	discoveryRelease                                        chan struct{}
	discoveryMode                                           string
	discoveryEntered                                        chan struct{}
	sessionCwd, discoveryCwd                                string
	sessionNewParams                                        []string
	failAfterTurn                                           bool
	stdin                                                   *io.PipeWriter
	stdout                                                  *io.PipeReader
	input                                                   io.ReadCloser
	output                                                  io.WriteCloser
	done                                                    chan struct{}
	mu                                                      sync.Mutex
	methods, efforts, inputs                                []string
	selectedModels                                          []string
	prompts, sessionNew                                     int
	crash, blockPrompt, malformedIdentity, malformedOptions bool
	cancels                                                 int
	stallCancel                                             bool
	stop                                                    chan struct{}
	killOnce                                                sync.Once
	handshakeCrash, handshakeAuth                           bool
	handshakeMethod                                         string
	handshakeOption                                         int
}

func (*adapterProcess) PID() int                { return 0 }
func (p *adapterProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *adapterProcess) Stdout() io.ReadCloser { return p.stdout }
func (*adapterProcess) Stderr() io.ReadCloser {
	return io.NopCloser(strings.NewReader("synthetic-private-stderr"))
}

func (p *adapterProcess) Kill() error {
	p.killOnce.Do(func() {
		_ = p.input.Close()
		_ = p.output.Close()
		if p.stop != nil {
			close(p.stop)
		}
	})
	return nil
}
func (p *adapterProcess) Wait() error { <-p.done; return nil }
func (p *adapterProcess) stopped() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *adapterProcess) serve(source string, wrong, promptError, after, optionAuth bool) {
	defer close(p.done)
	defer func() { _ = p.input.Close(); _ = p.output.Close() }()
	encoder := json.NewEncoder(p.output)
	scanner := bufio.NewScanner(p.input)
	model, effort := "future-family-17-3", "low"
	sequence := 0
	var ticket map[string]any
	var promptID any
	optionExchanges := 0
	options := func() []any {
		models := []any{map[string]any{"value": "future-family-17-3"}, map[string]any{"value": "other-family-8-2"}}
		if p.omitFirstModel {
			models = models[1:]
			model = "other-family-8-2"
		}
		return []any{
			map[string]any{"id": "model", "type": "select", "currentValue": model, "options": models},
			map[string]any{"id": "effort", "type": "select", "currentValue": effort, "defaultValue": "low", "options": []any{map[string]any{"value": "low"}, map[string]any{"value": "high"}}},
		}
	}
	for scanner.Scan() {
		var request struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			return
		}
		p.mu.Lock()
		p.methods = append(p.methods, request.Method)
		p.mu.Unlock()
		result := map[string]any{}
		if request.Method == "session/set_config_option" {
			optionExchanges++
		}
		if request.Method == p.handshakeMethod && (p.handshakeOption == 0 || optionExchanges == p.handshakeOption) {
			if p.handshakeCrash {
				return
			}
			if p.handshakeAuth {
				if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32000, "message": "synthetic-private-key"}}) != nil {
					return
				}
				continue
			}
		}
		switch request.Method {
		case "initialize":
			result["protocolVersion"] = 1
			switch p.discoveryMode {
			case "init-invalid":
				result["protocolVersion"] = 2
			case "init-crash":
				return
			case "init-rpc":
				if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32000, "message": "synthetic-private"}}) != nil {
					return
				}
				continue
			}
		case "_aip/models_v1":
			if request.Params["version"] != float64(1) || request.Params["cwd"] == "" || len(request.Params) != 2 {
				return
			}
			result = map[string]any{"version": 1, "currentModelId": "future-family-17-3", "models": []any{map[string]any{"nativeId": "future-family-17-3", "displayName": "Future"}, map[string]any{"nativeId": "other-family-8-2", "displayName": "Other"}}}
			p.discoveryCwd, _ = request.Params["cwd"].(string)
			if p.discoveryEntered != nil {
				close(p.discoveryEntered)
			}
			switch p.discoveryMode {
			case "pause":
				select {
				case <-p.discoveryRelease:
				case <-p.stop:
					return
				}
			case "graded-pause":
				// Hold admission with a graded response pending: the mode is
				// captured at entry, so a later global switch to "empty" for
				// the queued refresh does not change this attempt's outcome.
				select {
				case <-p.discoveryRelease:
				case <-p.stop:
					return
				}
				result["models"] = []any{
					map[string]any{
						"nativeId": "future-family-17-3", "displayName": "Future", "description": "Future",
						"capabilities": map[string]any{"supportsEffort": true, "supportedEffortLevels": []any{"low", "high"}},
					},
					map[string]any{"nativeId": "other-family-8-2", "displayName": "Other", "description": "Other"},
				}
			case "block":
				<-p.stop
				return
			case "empty":
				result["models"] = []any{}
			case "malformed":
				result["models"] = "synthetic-private-malformed"
			case "invalid":
				result["currentModelId"] = "missing-1"
			case "graded":
				// Provider-verified graded facts for the engine freshness
				// boundary: the first model carries a graded list, the second
				// carries none. Description fields satisfy snapshot validation.
				result["models"] = []any{
					map[string]any{
						"nativeId": "future-family-17-3", "displayName": "Future", "description": "Future",
						"capabilities": map[string]any{"supportsEffort": true, "supportedEffortLevels": []any{"low", "high"}},
					},
					map[string]any{"nativeId": "other-family-8-2", "displayName": "Other", "description": "Other"},
				}
			case "rpc":
				if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32000, "message": "synthetic-private"}}) != nil {
					return
				}
				continue
			}
		case "session/new":
			p.mu.Lock()
			p.sessionNew++
			p.sessionCwd, _ = request.Params["cwd"].(string)
			raw, _ := json.Marshal(request.Params)
			p.sessionNewParams = append(p.sessionNewParams, string(raw))
			p.mu.Unlock()
			result["sessionId"], result["configOptions"] = "agent-session", options()
		case "session/set_config_option":
			if p.malformedOptions && (!p.failAfterTurn || p.prompts > 0) {
				result["configOptions"] = "malformed"
				break
			}
			if optionAuth {
				if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32000, "message": "synthetic-private"}}) != nil {
					return
				}
				continue
			}
			if request.Params["configId"] == "model" {
				model, _ = request.Params["value"].(string)
				p.mu.Lock()
				p.selectedModels = append(p.selectedModels, model)
				p.mu.Unlock()
			} else {
				effort, _ = request.Params["value"].(string)
				p.mu.Lock()
				p.efforts = append(p.efforts, effort)
				p.mu.Unlock()
			}
			result["configOptions"] = options()
		case "_aip/effective_identity_v1":
			sequence++
			selected := source
			if wrong {
				selected = "host_login"
			}
			ticket = map[string]any{"version": 1, "sessionId": "agent-session", "checkpointId": request.Params["checkpointId"], "processGeneration": strings.Repeat("a", 64), "queryGeneration": 1, "observationSequence": sequence, "provider": "firstParty", "credentialSource": selected}
			result = ticket
			if p.malformedIdentity && (!p.failAfterTurn || p.prompts > 0) {
				result = map[string]any{"version": 1}
			}
		case "session/cancel":
			p.mu.Lock()
			p.cancels++
			p.mu.Unlock()
			if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": promptID, "result": map[string]any{"stopReason": "cancelled"}}) != nil {
				return
			}
			continue
		case "session/prompt":
			meta, _ := request.Params["_meta"].(map[string]any)
			got, _ := json.Marshal(meta["aipIdentity"])
			want, _ := json.Marshal(ticket)
			if ticket == nil || string(got) != string(want) || request.Params["sessionId"] != "agent-session" {
				return
			}
			ticket = nil
			var input strings.Builder
			blocks, _ := request.Params["prompt"].([]any)
			for _, block := range blocks {
				if content, ok := block.(map[string]any); ok {
					text, _ := content["text"].(string)
					input.WriteString(text)
				}
			}
			p.mu.Lock()
			p.prompts++
			p.inputs = append(p.inputs, input.String())
			p.mu.Unlock()
			promptID = request.ID
			if p.blockPrompt {
				if p.stallCancel {
					<-p.stop
					return
				}
				continue
			}
			if (!promptError && !p.crash) || after {
				for _, chunk := range []struct{ kind, text string }{{"agent_message_chunk", "one"}, {"agent_thought_chunk", "think"}, {"agent_message_chunk", "two"}} {
					if encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "agent-session", "update": map[string]any{"sessionUpdate": chunk.kind, "content": map[string]any{"type": "text", "text": chunk.text}}}}) != nil {
						return
					}
				}
			}
			if p.crash {
				return
			}
			if promptError {
				if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32000, "message": "synthetic-private-token", "data": "synthetic-private-details"}}) != nil {
					return
				}
				continue
			}
			result["stopReason"] = "end_turn"
		case "authenticate":
			// Wire contract: exactly the connector-baked payload per source —
			// method, endpoint and the full secret-header map the gateway
			// fixture provisioned. A mismatch is an explicit invalid-params
			// failure, never a silent hang.
			meta, _ := request.Params["_meta"].(map[string]any)
			gateway, _ := meta["gateway"].(map[string]any)
			if request.Params["methodId"] != "gateway" || gateway["baseUrl"] != "https://gw.example.com" ||
				!reflect.DeepEqual(gateway["headers"], map[string]any{"X-Route": "gateway-header-secret"}) {
				if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32602, "message": "invalid authenticate payload"}}) != nil {
					return
				}
				continue
			}
		default:
			return
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) != nil {
			return
		}
	}
}

func wireText(text string) backendplugin.Part {
	return backendplugin.Part{Kind: backendplugin.PartKindText, Text: &text}
}

// Claude Code runs as the agent itself: a client harness's system prompt,
// developer messages and instructions (persona, tool catalogues) must never
// reach the runtime. They are dropped with a content-free warning and the turn
// is still served.
func TestService_ClientHarnessInstructionsNeverReachRuntime(t *testing.T) {
	const canary = "HARNESS-CANARY-7731"
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	var logs bytes.Buffer
	svc.deps.log = slog.New(slog.NewTextHandler(&logs, nil))
	inst := configureColdFixture(t, svc, "api_key")
	t.Cleanup(func() { _ = inst.Close(context.Background()) })
	inv := backendplugin.Invocation{
		RequestID: "harness", AttemptID: "attempt", ALegID: "a", BLegID: "b",
		NativeModelID: "future-family-17-3", CanonicalModelID: "anthropic/future-family-17.3", ProxyOwnedSessionID: "client",
		Instructions: []backendplugin.Message{{Role: backendplugin.RoleSystem, Parts: []backendplugin.Part{wireText("You are " + canary + " with tools acme_run_shell")}}},
		Messages: []backendplugin.Message{
			{Role: backendplugin.RoleSystem, Parts: []backendplugin.Part{wireText("system " + canary)}},
			{Role: backendplugin.RoleDeveloper, Parts: []backendplugin.Part{wireText("developer " + canary)}},
			{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{wireText("question")}},
		},
	}
	wire := &executeCapture{ctx: t.Context(), invocation: &inv}
	if err := inst.Execute(wire); err != nil {
		t.Fatal(err)
	}
	if last := wire.frames[len(wire.frames)-1]; last.Terminal == nil || last.Terminal.Status != backendplugin.TerminalSuccess {
		t.Fatalf("harness turn must still be served: %+v", last)
	}
	proc := adapter.processes[len(adapter.processes)-1]
	proc.mu.Lock()
	sent := strings.Join(append(append([]string(nil), proc.sessionNewParams...), proc.inputs...), "\n")
	proc.mu.Unlock()
	if !strings.Contains(sent, "question") {
		t.Fatalf("user turn missing from runtime input: %s", sent)
	}
	if strings.Contains(sent, canary) {
		t.Fatalf("client harness context reached the runtime: %s", sent)
	}
	if !strings.Contains(logs.String(), "client instructions dropped") || strings.Contains(logs.String(), canary) {
		t.Fatalf("degradation must be logged without content: %s", logs.String())
	}
}
