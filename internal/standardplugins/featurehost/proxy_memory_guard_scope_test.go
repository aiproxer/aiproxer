package featurehost

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
)

// scopedStubGuard governs exactly the roles in govern.
type scopedStubGuard struct {
	govern map[string]bool
}

func (g scopedStubGuard) CheckAttempt(context.Context, proxymemory.AttemptGuardInput) error {
	return nil
}

func (g scopedStubGuard) GovernsRole(role string) bool {
	return g.govern[role]
}

// unscopedStubGuard lacks the role selector.
type unscopedStubGuard struct{}

func (unscopedStubGuard) CheckAttempt(context.Context, proxymemory.AttemptGuardInput) error {
	return nil
}

// TestProxyMemoryAttemptGuardRoleScope pins the adapter boundary: it
// delegates to the feature selector, governs nothing without a feature
// guard, and never silently bypasses an unknown guard.
func TestProxyMemoryAttemptGuardRoleScope(t *testing.T) {
	t.Parallel()

	var nilAdapter proxyMemoryAttemptGuard
	if nilAdapter.ShouldCheckAttempt("proxy_memory_phase1") {
		t.Fatal("nil feature guard must govern nothing")
	}
	if nilAdapter.ShouldCheckAttempt("") {
		t.Fatal("nil feature guard must govern nothing on ordinary traffic")
	}

	// A guard without the selector governs everything, so an unknown
	// guard is never silently bypassed.
	unscoped := proxyMemoryAttemptGuard{guard: unscopedStubGuard{}}
	if !unscoped.ShouldCheckAttempt("") {
		t.Fatal("unscoped guard must govern ordinary traffic")
	}
	if !unscoped.ShouldCheckAttempt("some_other_role") {
		t.Fatal("unscoped guard must govern unknown roles")
	}

	// A scoped stub delegates exactly.
	scoped := proxyMemoryAttemptGuard{guard: scopedStubGuard{govern: map[string]bool{
		"proxy_memory_phase1": true,
		"proxy_memory_phase2": true,
	}}}
	for _, tc := range []struct {
		role string
		want bool
	}{
		{"", false},
		{"some_other_role", false},
		{"proxy_memory_phase1", true},
		{"proxy_memory_phase2", true},
	} {
		if got := scoped.ShouldCheckAttempt(tc.role); got != tc.want {
			t.Fatalf("ShouldCheckAttempt(%q) = %v, want %v", tc.role, got, tc.want)
		}
	}

	// The real feature guard with a declared policy governs both phases
	// and nothing else; without a declared policy it governs nothing.
	declared := proxyMemoryAttemptGuard{guard: proxymemory.NewAttemptGuard(proxymemory.Config{ModelsDeclared: true}, nil, nil)}
	if !declared.ShouldCheckAttempt("proxy_memory_phase1") {
		t.Fatal("declared policy must govern phase1")
	}
	if !declared.ShouldCheckAttempt("proxy_memory_phase2") {
		t.Fatal("declared policy must govern phase2")
	}
	if declared.ShouldCheckAttempt("") {
		t.Fatal("declared policy must not govern ordinary traffic")
	}
	if declared.ShouldCheckAttempt("some_other_role") {
		t.Fatal("declared policy must not govern unrelated roles")
	}

	undeclared := proxyMemoryAttemptGuard{guard: proxymemory.NewAttemptGuard(proxymemory.Config{}, nil, nil)}
	if undeclared.ShouldCheckAttempt("proxy_memory_phase1") {
		t.Fatal("undeclared policy must govern nothing")
	}
	if undeclared.ShouldCheckAttempt("") {
		t.Fatal("undeclared policy must govern nothing on ordinary traffic")
	}

	var _ runtime.AttemptGuardRoleScope = proxyMemoryAttemptGuard{}
}
