package acp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCallRouteModel_selectorWithColon(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "cursorcliacp:claude-3.5-sonnet"}}
	if got := CallRouteModel(call, "acp.model"); got != "claude-3.5-sonnet" {
		t.Fatalf("got %q, want claude-3.5-sonnet", got)
	}
}

func TestCallRouteModel_selectorWithoutColon(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{Route: aipapi.RouteIntent{Selector: "claude-3.5-sonnet"}}
	if got := CallRouteModel(call, "acp.model"); got != "claude-3.5-sonnet" {
		t.Fatalf("got %q, want claude-3.5-sonnet", got)
	}
}

func TestCallRouteModel_extensionFallback(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Extensions: map[string]json.RawMessage{
			"codex.model": json.RawMessage(`"gpt-5.4"`),
		},
	}
	if got := CallRouteModel(call, "codex.model"); got != "gpt-5.4" {
		t.Fatalf("got %q, want gpt-5.4", got)
	}
}

func TestCallRouteModel_extensionKeyIsolation(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Extensions: map[string]json.RawMessage{
			"acp.model": json.RawMessage(`"acp-model"`),
		},
	}
	// Asking for the codex key must not pick up the acp key.
	if got := CallRouteModel(call, "codex.model"); got != "" {
		t.Fatalf("got %q, want empty (key isolation)", got)
	}
}

func TestCallRouteModel_selectorBeatsExtension(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "backend:sel-model"},
		Extensions: map[string]json.RawMessage{
			"acp.model": json.RawMessage(`"ext-model"`),
		},
	}
	if got := CallRouteModel(call, "acp.model"); got != "sel-model" {
		t.Fatalf("selector precedence: got %q, want sel-model", got)
	}
}

func TestCallRouteModel_nilCall(t *testing.T) {
	t.Parallel()
	if got := CallRouteModel(nil, "acp.model"); got != "" {
		t.Fatalf("nil call: got %q, want empty", got)
	}
}

func TestCallRouteModel_emptySelectorFallsToExtension(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "   "},
		Extensions: map[string]json.RawMessage{"acp.model": json.RawMessage(`"m"`)},
	}
	if got := CallRouteModel(call, "acp.model"); got != "m" {
		t.Fatalf("whitespace selector should fall to extension: got %q", got)
	}
}

func TestCallClientSession_canonicalField(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{Session: aipapi.SessionRef{ClientSessionID: "sess-1"}}
	if got := CallClientSession(call); got != "sess-1" {
		t.Fatalf("got %q, want sess-1", got)
	}
}

func TestCallClientSession_extensionFallback(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Extensions: map[string]json.RawMessage{
			"session.id": json.RawMessage(`"ext-sess"`),
		},
	}
	if got := CallClientSession(call); got != "ext-sess" {
		t.Fatalf("got %q, want ext-sess", got)
	}
}

func TestCallClientSession_canonicalBeatsExtension(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Session:    aipapi.SessionRef{ClientSessionID: "canonical"},
		Extensions: map[string]json.RawMessage{"session.id": json.RawMessage(`"ext"`)},
	}
	if got := CallClientSession(call); got != "canonical" {
		t.Fatalf("canonical precedence: got %q, want canonical", got)
	}
}

func TestCallClientSession_default(t *testing.T) {
	t.Parallel()
	if got := CallClientSession(&aipapi.Call{}); got != "default" {
		t.Fatalf("got %q, want default", got)
	}
	if got := CallClientSession(nil); got != "default" {
		t.Fatalf("nil call: got %q, want default", got)
	}
}

func TestCallClientSession_emptyExtensionFallsToDefault(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Extensions: map[string]json.RawMessage{"session.id": json.RawMessage(`"   "`)},
	}
	if got := CallClientSession(call); got != "default" {
		t.Fatalf("whitespace session.id should fall to default: got %q", got)
	}
}

// The runtime identity is conversation-scoped: two requests that carry no
// session identity must never share one agent process.
func TestCallRuntimeIdentity_authoritativeSessionWins(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Session:    aipapi.SessionRef{AuthoritativeSessionID: "sess-1", ClientSessionID: "client-1"},
		Extensions: map[string]json.RawMessage{"session.id": json.RawMessage(`"ext-1"`)},
	}
	if got := CallRuntimeIdentity(call); got != "session:sess-1" {
		t.Fatalf("got %q, want session:sess-1", got)
	}
}

// A client-carried identifier is not proof of conversation identity: it must
// never decide which agent process serves the request.
func TestCallRuntimeIdentity_ignoresClientCarriedIdentifiers(t *testing.T) {
	t.Parallel()
	client := &aipapi.Call{ID: "req-1", Session: aipapi.SessionRef{ClientSessionID: "client-1"}}
	extension := &aipapi.Call{ID: "req-2", Extensions: map[string]json.RawMessage{"session.id": json.RawMessage(`"ext-1"`)}}
	if got := CallRuntimeIdentity(client); !strings.HasPrefix(got, "request:") {
		t.Fatalf("client session hint leaked into the runtime identity: %q", got)
	}
	if got := CallRuntimeIdentity(extension); !strings.HasPrefix(got, "request:") {
		t.Fatalf("session.id extension leaked into the runtime identity: %q", got)
	}
}

// Two requests that open with the same text but carry different request ids
// are different conversations: they must not share agent runtime state.
func TestCallRuntimeIdentity_distinctRequestsNeverShare(t *testing.T) {
	t.Parallel()
	first := &aipapi.Call{ID: "req-1", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
	second := &aipapi.Call{ID: "req-2", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
	if got, other := CallRuntimeIdentity(first), CallRuntimeIdentity(second); got == other {
		t.Fatalf("two requests resolved to the same identity %q", got)
	}
}

// Source: PR #1323 re-review. Trace IDs/content do not authorize process reuse.
func TestCallRuntimeIdentity_requestFallback(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"same-id", ""} {
		call := &aipapi.Call{ID: id, Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
		if first, second := CallRuntimeIdentity(call), CallRuntimeIdentity(call); first == second {
			t.Fatalf("independent executions with id %q shared identity %q", id, first)
		}
	}
}
