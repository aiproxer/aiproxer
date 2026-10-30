package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/connector-support/acp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Task 3.2: the canonical workspace_path hint must survive host capability +
// exact-extension admission against the actual Resolve profile. Unknown
// opaque/item/content extensions must still reject (no silent drop).
func TestResolve_DeclaresWorkspacePathAdmission(t *testing.T) {
	t.Parallel()
	svc, _, _, _ := serviceFixture(t, "api_key")
	inst := configureFixture(t, svc, "api_key")
	profile, err := inst.Resolve(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Capabilities.OpaqueExtensions {
		t.Fatalf("Resolve must declare OpaqueExtensions: %+v", profile.Capabilities)
	}
	wantExt := []backendplugin.ExtensionRequirementDTO{{Namespace: "call", Type: "workspace_path"}}
	if len(profile.DialectSupport.ExtensionTypes) != 1 || profile.DialectSupport.ExtensionTypes[0] != wantExt[0] {
		t.Fatalf("Resolve ExtensionTypes=%+v want %+v", profile.DialectSupport.ExtensionTypes, wantExt)
	}

	dir := t.TempDir()
	raw, _ := json.Marshal(dir)
	call := turnCall()
	call.Extensions = map[string]json.RawMessage{"workspace_path": raw}
	req := aipapi.DeriveProtocolRequirements(*call)

	var supportedCaps []aipapi.Capability
	if profile.Capabilities.Streaming {
		supportedCaps = append(supportedCaps, aipapi.CapabilityStreaming)
	}
	if profile.Capabilities.Reasoning {
		supportedCaps = append(supportedCaps, aipapi.CapabilityReasoning)
	}
	if profile.Capabilities.OpaqueExtensions {
		supportedCaps = append(supportedCaps, aipapi.CapabilityOpaqueExtensions)
	}
	if res := aipapi.Negotiate(req.Capabilities, aipapi.NewBackendCaps(supportedCaps...)); res.Kind == aipapi.NegotiationReject {
		t.Fatalf("capability admission rejected workspace hint: %+v", res)
	}
	supported := aipapi.ProtocolRequirements{
		Capabilities:   supportedCaps,
		ExtensionTypes: backendplugin.DialectSupportToAipapi(profile.DialectSupport).ExtensionTypes,
	}
	if res := aipapi.MatchRequirements(req, supported, aipapi.ReasoningReplaySupport{}); res.Kind == aipapi.NegotiationReject {
		t.Fatalf("extension admission rejected workspace hint: %+v", res)
	}

	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "al", BLegID: "bl",
		CanonicalModelID: "anthropic/x", NativeModelID: "x",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{wireText(text)}}},
	}
	if err := backendplugin.ApplyCallWireMetadata(&inv, *call, nil); err != nil {
		t.Fatal(err)
	}
	restored, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored.Extensions["workspace_path"]) != string(raw) {
		t.Fatalf("DTO roundtrip dropped workspace_path: %+v", restored.Extensions)
	}
	if _, err := (acp.WorkspacePolicy{DefaultDir: dir, RequireExplicit: true}).ResolveWorkspace(acp.WorkspaceHintsFromCall(call)); err != nil {
		t.Fatalf("workspace consumer dropped hint: %v", err)
	}

	unknownItem := turnCall()
	unknownItem.Items = []aipapi.Item{{
		Kind: aipapi.ItemKindExtension, ID: "x",
		Extension: &aipapi.OpaqueExtension{Namespace: "other", Type: "thing"},
	}}
	unknownContent := turnCall()
	unknownContent.Items = []aipapi.Item{{
		Kind: aipapi.ItemKindMessage, ID: "m",
		Content: []aipapi.ContentPart{{
			Kind:      aipapi.ContentPartExtension,
			Extension: &aipapi.ExtensionContentPart{Namespace: "other", Type: "other:part"},
		}},
	}}
	unknownCall := turnCall()
	unknownCall.Extensions = map[string]json.RawMessage{"custom_unknown": json.RawMessage(`"x"`)}
	for _, tc := range []struct {
		name string
		call *aipapi.Call
	}{
		{"unknown call extension", unknownCall},
		{"unknown item extension", unknownItem},
		{"unknown content extension", unknownContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bad := aipapi.DeriveProtocolRequirements(*tc.call)
			if res := aipapi.MatchRequirements(bad, supported, aipapi.ReasoningReplaySupport{}); res.Kind != aipapi.NegotiationReject {
				t.Fatalf("unknown extension admitted: %+v", res)
			}
		})
	}
}

// P1: a present canonical workspace_path hint must be validated before
// discovery/pool admission. Non-string JSON, malformed JSON, oversize,
// control-bearing or non-absolute values must reject with a bounded
// capability error and must not reuse the warm default workspace.
func TestOpen_RejectsPresentWorkspacePathSyntaxBeforeReuse(t *testing.T) {
	t.Parallel()
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	inst := configureFixture(t, svc, "api_key")
	first, err := inst.eng.Open(t.Context(), turnCall())
	if err != nil {
		t.Fatal(err)
	}
	consume(t, first)
	if adapter.starts != 1 || adapter.processes[0].prompts != 1 {
		t.Fatalf("warm turn: starts=%d prompts=%d", adapter.starts, adapter.processes[0].prompts)
	}
	starts, prompts := adapter.starts, adapter.processes[0].prompts
	inputs := len(adapter.processes[0].inputs)

	oversize, _ := json.Marshal("/" + strings.Repeat("a", 4096))
	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"relative", json.RawMessage(`"../B"`)},
		{"dot", json.RawMessage(`"."`)},
		{"dotdot", json.RawMessage(`".."`)},
		{"null", json.RawMessage(`null`)},
		{"bool", json.RawMessage(`true`)},
		{"number", json.RawMessage(`123`)},
		{"object", json.RawMessage(`{"a":1}`)},
		{"array", json.RawMessage(`["/tmp"]`)},
		{"control", json.RawMessage("\"/tmp/a\nb\"")},
		{"oversize", oversize},
		{"raw whitespace oversize", json.RawMessage(strings.Repeat(" ", maxWorkspacePathRawBytes+1))},
		{"malformed", json.RawMessage(`"/tmp/unclosed`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := turnCall()
			bad.Extensions = map[string]json.RawMessage{"workspace_path": tc.raw}
			before := map[string]json.RawMessage{"workspace_path": append(json.RawMessage(nil), tc.raw...)}
			_, err := inst.eng.Open(t.Context(), bad)
			if !errors.Is(err, aipapi.ErrCapabilityReject) || aipapi.IsRecoverablePreOutput(err) {
				t.Fatalf("invalid workspace_path admitted: %v", err)
			}
			if err.Error() != "claudeagentacp: invalid workspace_path" {
				t.Fatalf("workspace rejection diagnostic: %v", err)
			}
			if strings.Contains(err.Error(), "../B") || strings.Contains(err.Error(), "/tmp/a\nb") {
				t.Fatalf("rejection echoed input: %q", err.Error())
			}
			if string(bad.Extensions["workspace_path"]) != string(before["workspace_path"]) {
				t.Fatal("validation mutated the canonical call")
			}
			if adapter.starts != starts || adapter.processes[0].prompts != prompts || len(adapter.processes[0].inputs) != inputs || adapter.processes[0].stopped() {
				t.Fatalf("invalid hint touched warm runtime: starts=%d prompts=%d inputs=%d stopped=%v", adapter.starts, adapter.processes[0].prompts, len(adapter.processes[0].inputs), adapter.processes[0].stopped())
			}
		})
	}

	// The warm conversation must remain intact: a valid continuation on the
	// original workspace reuses the same process/session without the bad turn.
	next := turnCall()
	next.Messages = append(next.Messages, aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("onetwo")}}, aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}})
	stream, err := inst.eng.Open(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	consume(t, stream)
	if adapter.starts != 1 || adapter.processes[0].prompts != 2 {
		t.Fatalf("valid continuation did not reuse warm runtime: starts=%d prompts=%d", adapter.starts, adapter.processes[0].prompts)
	}
	if got := strings.Join(adapter.processes[0].inputs, ""); !strings.Contains(got, "second") || strings.Contains(got, "../B") {
		t.Fatalf("continuation history contaminated: %q", got)
	}
}

// Cold invalid hints must reject before discovery: zero spawns, zero SDK
// input and an empty inventory.
func TestOpen_ColdInvalidWorkspacePathSkipsDiscovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"relative", json.RawMessage(`"../B"`)},
		{"null", json.RawMessage(`null`)},
		{"raw whitespace oversize", json.RawMessage(strings.Repeat(" ", maxWorkspacePathRawBytes+1))},
		{"oversize", mustMarshal(t, "/"+strings.Repeat("a", 4096))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, adapter, _, _ := serviceFixture(t, "api_key")
			inst := configureColdFixture(t, svc, "api_key")
			bad := turnCall()
			bad.Extensions = map[string]json.RawMessage{"workspace_path": tc.raw}
			_, err := inst.eng.Open(t.Context(), bad)
			if !errors.Is(err, aipapi.ErrCapabilityReject) {
				t.Fatalf("cold invalid workspace_path admitted: %v", err)
			}
			if err.Error() != "claudeagentacp: invalid workspace_path" {
				t.Fatalf("cold workspace rejection diagnostic: %v", err)
			}
			if adapter.starts != 0 || len(adapter.processes) != 0 {
				t.Fatalf("cold invalid hint spawned: starts=%d processes=%d", adapter.starts, len(adapter.processes))
			}
			if inst.eng.spec.ResolveModel("") != "" {
				t.Fatal("cold invalid hint published inventory")
			}
		})
	}
}

// Valid absolute workspaces switch pool entries and return to the original
// without new spawns; absent/blank hints keep the configured default.
func TestOpen_ValidWorkspaceSwitchReuses(t *testing.T) {
	t.Parallel()
	svc, adapter, _, _ := serviceFixture(t, "api_key")
	inst := configureFixture(t, svc, "api_key")
	workspaceA, workspaceB := t.TempDir(), t.TempDir()

	openWithWorkspace := func(workspace string, messages []aipapi.Message) {
		t.Helper()
		call := turnCall()
		call.Messages = messages
		if workspace != "" {
			raw, err := json.Marshal(workspace)
			if err != nil {
				t.Fatal(err)
			}
			call.Extensions = map[string]json.RawMessage{"workspace_path": raw}
		}
		stream, err := inst.eng.Open(t.Context(), call)
		if err != nil {
			t.Fatal(err)
		}
		consume(t, stream)
	}

	base := turnCall().Messages
	openWithWorkspace(workspaceA, base)
	openWithWorkspace(workspaceB, base)
	if adapter.starts != 2 || adapter.workspaces[0] != workspaceA || adapter.workspaces[1] != workspaceB {
		t.Fatalf("workspace isolation: starts=%d workspaces=%v", adapter.starts, adapter.workspaces)
	}
	next := append(append([]aipapi.Message(nil), base...), aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("onetwo")}}, aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("second")}})
	openWithWorkspace(workspaceA, next)
	if adapter.starts != 2 || adapter.processes[0].prompts != 2 {
		t.Fatalf("return did not reuse original runtime: starts=%d prompts=%d", adapter.starts, adapter.processes[0].prompts)
	}

	for _, tc := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"absent", nil},
		{"blank", json.RawMessage(`""`)},
		{"whitespace", json.RawMessage(`"   "`)},
	} {
		t.Run("default/"+tc.name, func(t *testing.T) {
			freshSvc, freshAdapter, _, _ := serviceFixture(t, "api_key")
			fresh := configureFixture(t, freshSvc, "api_key")
			call := turnCall()
			if tc.raw != nil {
				call.Extensions = map[string]json.RawMessage{"workspace_path": tc.raw}
			}
			stream, err := fresh.eng.Open(t.Context(), call)
			if err != nil {
				t.Fatalf("default hint rejected: %v", err)
			}
			consume(t, stream)
			if freshAdapter.starts != 1 || freshAdapter.workspaces[0] != fresh.eng.workspace.DefaultDir {
				t.Fatalf("default fallback: starts=%d workspaces=%v default=%q", freshAdapter.starts, freshAdapter.workspaces, fresh.eng.workspace.DefaultDir)
			}
		})
	}
}

func mustMarshal(t *testing.T, s string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Syntax bounds for the canonical value: decoded 4096 plus a pre-parse raw
// cap covering the worst 6x JSON escape expansion. A valid 4096-byte value
// with escape overhead must not reject merely for raw size.
func TestValidateWorkspacePathExtension_Bounds(t *testing.T) {
	t.Parallel()
	atCap := "/" + strings.Repeat("a", 4095)
	escaped := "/" + strings.Repeat("<", 4095)
	rawAt := mustMarshal(t, atCap)
	rawEscaped := mustMarshal(t, escaped)
	if len(atCap) != maxWorkspacePathDecodedBytes || len(escaped) != maxWorkspacePathDecodedBytes {
		t.Fatalf("fixture lengths: %d/%d", len(atCap), len(escaped))
	}
	if len(rawEscaped) <= maxWorkspacePathDecodedBytes || len(rawEscaped) > maxWorkspacePathRawBytes {
		t.Fatalf("escaped raw must prove overhead within cap: %d", len(rawEscaped))
	}
	cases := []struct {
		name  string
		raw   json.RawMessage
		allow bool
	}{
		{"decoded cap", rawAt, true},
		{"decoded over", mustMarshal(t, "/"+strings.Repeat("a", 4096)), false},
		{"escaped cap", rawEscaped, true},
		{"raw cap", json.RawMessage(strings.Repeat("x", maxWorkspacePathRawBytes+1)), false},
		{"raw whitespace oversize", json.RawMessage(strings.Repeat(" ", maxWorkspacePathRawBytes+1)), false},
		{"raw whitespace under bound", json.RawMessage(strings.Repeat(" ", 100)), true},
		{"drive relative", json.RawMessage(`"C:foo"`), false},
		{"single backslash", json.RawMessage(`"\\server"`), false},
		{"nul", mustMarshal(t, "/tmp/a\x00b"), false},
		{"del", mustMarshal(t, "/tmp/a\x7fb"), false},
		{"invalid utf8 raw", json.RawMessage{'"', '/', 0xff, '"'}, false},
		{"empty raw", json.RawMessage{}, true},
		{"blank", json.RawMessage(`""`), true},
		{"whitespace", json.RawMessage(`"   "`), true},
		{"valid abs", json.RawMessage(`"/tmp/a"`), true},
		{"padded abs", json.RawMessage(`"  /tmp/a  "`), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := &aipapi.Call{Extensions: map[string]json.RawMessage{"workspace_path": tc.raw}}
			err := validateWorkspacePathExtension(call)
			if tc.allow {
				if err != nil {
					t.Fatalf("valid hint rejected: %v", err)
				}
				return
			}
			var rej *aipapi.RejectError
			if !errors.As(err, &rej) || !errors.Is(err, aipapi.ErrCapabilityReject) {
				t.Fatalf("invalid hint not a capability reject: %v", err)
			}
			if rej.Reason != "claudeagentacp: invalid workspace_path" {
				t.Fatalf("unbounded rejection reason: %q", rej.Reason)
			}
			if string(call.Extensions["workspace_path"]) != string(tc.raw) {
				t.Fatal("validation mutated the canonical call")
			}
		})
	}
	if err := validateWorkspacePathExtension(nil); err != nil {
		t.Fatalf("nil call rejected: %v", err)
	}
	if err := validateWorkspacePathExtension(&aipapi.Call{}); err != nil {
		t.Fatalf("missing extensions rejected: %v", err)
	}
}
