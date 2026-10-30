package terminal_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestCommand_AllowedScopes_Exhaustive(t *testing.T) {
	t.Parallel()

	requestOnly := map[terminal.Command]struct{}{
		terminal.CommandFrontendEncoderFailure: {},
		terminal.CommandGateReplacement:        {},
	}
	attemptOnly := map[terminal.Command]struct{}{
		terminal.CommandParallelLoser:      {},
		terminal.CommandSwallowedAttempt:   {},
		terminal.CommandPreBackendDenial:   {},
		terminal.CommandBackendOpenFailure: {},
	}
	both := map[terminal.Command]struct{}{
		terminal.CommandNormalFinish: {},
		terminal.CommandPartialError: {},
		terminal.CommandCancel:       {},
		terminal.CommandClose:        {},
		terminal.CommandTimeout:      {},
		terminal.CommandPanic:        {},
		terminal.CommandEOF:          {},
	}

	for _, cmd := range terminal.AllCommands() {
		t.Run(string(cmd), func(t *testing.T) {
			t.Parallel()
			scopes := cmd.AllowedScopes()
			switch {
			case hasCmd(requestOnly, cmd):
				assertScopesExact(t, scopes, terminal.ScopeRequest)
				if cmd.AllowsScope(terminal.ScopeAttempt) {
					t.Fatal("request-only command must not allow attempt")
				}
			case hasCmd(attemptOnly, cmd):
				assertScopesExact(t, scopes, terminal.ScopeAttempt)
				if cmd.AllowsScope(terminal.ScopeRequest) {
					t.Fatal("attempt-only command must not allow request")
				}
			case hasCmd(both, cmd):
				assertScopesExact(t, scopes, terminal.ScopeRequest, terminal.ScopeAttempt)
			default:
				t.Fatalf("command %q missing from scope classification", cmd)
			}
			if len(scopes) == 0 {
				t.Fatal("known command must declare at least one allowed scope")
			}
		})
	}

	if terminal.Command("nope").AllowsScope(terminal.ScopeRequest) {
		t.Fatal("unknown command must not allow any scope")
	}
}

func hasCmd(m map[terminal.Command]struct{}, c terminal.Command) bool {
	_, ok := m[c]
	return ok
}

func assertScopesExact(t *testing.T, got []terminal.Scope, want ...terminal.Scope) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("scopes=%v want %v", got, want)
	}
	seen := map[terminal.Scope]bool{}
	for _, s := range got {
		seen[s] = true
	}
	for _, s := range want {
		if !seen[s] {
			t.Fatalf("scopes=%v missing %q", got, s)
		}
	}
}
