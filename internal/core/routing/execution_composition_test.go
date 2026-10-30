package routing

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type mapExecutionResolver map[string]aipsdk.BackendExecutionClass

func (m mapExecutionResolver) ResolveBackendExecution(backendID string) (aipsdk.BackendExecutionClass, bool) {
	c, ok := m[backendID]
	return c, ok
}

func TestValidateExecutionComposition_Matrix(t *testing.T) {
	t.Parallel()

	resolver := mapExecutionResolver{
		"inf1":   aipsdk.BackendExecutionInference,
		"inf2":   aipsdk.BackendExecutionInference,
		"agent1": aipsdk.BackendExecutionAgentRuntime,
		"agent2": aipsdk.BackendExecutionAgentRuntime,
		"unk1":   aipsdk.BackendExecutionUnknown,
	}

	cases := []struct {
		name         string
		selector     string
		policy       config.ExecutionCompositionPolicy
		wantErr      bool
		wantComp     string
		wantOffender string
		wantClass    aipsdk.BackendExecutionClass
	}{
		// Direct single leaf routes (allowed under safe regardless of class)
		{
			name:     "direct inference allowed",
			selector: "inf1:gpt-4o",
			policy:   config.ExecutionCompositionSafe,
			wantErr:  false,
		},
		{
			name:     "direct agent runtime allowed",
			selector: "agent1:coder",
			policy:   config.ExecutionCompositionSafe,
			wantErr:  false,
		},
		{
			name:     "direct unknown allowed",
			selector: "unk1:legacy-model",
			policy:   config.ExecutionCompositionSafe,
			wantErr:  false,
		},
		{
			name:     "direct agent runtime with query params and ttft allowed",
			selector: "agent1:coder?temp=0.7&ttft=5s",
			policy:   config.ExecutionCompositionSafe,
			wantErr:  false,
		},

		// Weighted composition
		{
			name:     "weighted inference + inference allowed",
			selector: "inf1:m1^inf2:m2",
			policy:   config.ExecutionCompositionSafe,
			wantErr:  false,
		},
		{
			name:         "weighted agent + inference denied",
			selector:     "agent1:m1^inf1:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "weighted selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
		{
			name:         "weighted inference + agent denied",
			selector:     "inf1:m1^agent1:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "weighted selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
		{
			name:         "weighted agent + agent denied",
			selector:     "agent1:m1^agent2:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "weighted selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
		{
			name:         "weighted unknown + inference denied",
			selector:     "unk1:m1^inf1:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "weighted selector",
			wantOffender: "unk1",
			wantClass:    aipsdk.BackendExecutionUnknown,
		},

		// Parallel composition
		{
			name:     "parallel inference + inference allowed",
			selector: "inf1:m1!inf2:m2",
			policy:   config.ExecutionCompositionSafe,
			wantErr:  false,
		},
		{
			name:         "parallel agent + inference denied",
			selector:     "agent1:m1!inf1:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "parallel selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
		{
			name:         "parallel agent + agent denied",
			selector:     "agent1:m1!agent2:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "parallel selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
		{
			name:         "parallel unknown + inference denied",
			selector:     "unk1:m1!inf1:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "parallel selector",
			wantOffender: "unk1",
			wantClass:    aipsdk.BackendExecutionUnknown,
		},

		// Failover composition
		{
			name:     "failover inference | inference allowed",
			selector: "inf1:m1|inf2:m2",
			policy:   config.ExecutionCompositionSafe,
			wantErr:  false,
		},
		{
			name:         "failover agent | inference denied",
			selector:     "agent1:m1|inf1:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "failover selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
		{
			name:         "failover inference | agent denied",
			selector:     "inf1:m1|agent1:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "failover selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
		{
			name:         "failover agent | agent denied",
			selector:     "agent1:m1|agent2:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "failover selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
		{
			name:         "failover unknown | inference denied",
			selector:     "unk1:m1|inf1:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "failover selector",
			wantOffender: "unk1",
			wantClass:    aipsdk.BackendExecutionUnknown,
		},

		// Thinker hybrid composition
		{
			name:     "thinker hybrid inference + inference allowed",
			selector: "[thinker]inf1:plan^inf2:exec",
			policy:   config.ExecutionCompositionSafe,
			wantErr:  false,
		},
		{
			name:         "thinker hybrid agent thinker denied",
			selector:     "[thinker]agent1:plan^inf1:exec",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "thinker hybrid selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
		{
			name:         "thinker hybrid agent executor denied",
			selector:     "[thinker]inf1:plan^agent1:exec",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "thinker hybrid selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
		{
			name:         "thinker hybrid unknown thinker denied",
			selector:     "[thinker]unk1:plan^inf1:exec",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "thinker hybrid selector",
			wantOffender: "unk1",
			wantClass:    aipsdk.BackendExecutionUnknown,
		},

		// Thinker with embedded parallel executor
		{
			name:     "thinker with embedded parallel inference allowed",
			selector: "[thinker]inf1:plan^(inf1:exec1!inf2:exec2)",
			policy:   config.ExecutionCompositionSafe,
			wantErr:  false,
		},
		{
			name:         "thinker with embedded parallel agent leaf denied",
			selector:     "[thinker]inf1:plan^(inf1:exec1!agent1:exec2)",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "thinker hybrid selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
		{
			name:         "thinker agent with embedded parallel inference denied",
			selector:     "[thinker]agent1:plan^(inf1:exec1!inf2:exec2)",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "thinker hybrid selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},

		// Unrestricted policy (all compositions permitted)
		{
			name:     "unrestricted policy allows agent in weighted",
			selector: "agent1:m1^inf1:m2",
			policy:   config.ExecutionCompositionUnrestricted,
			wantErr:  false,
		},
		{
			name:     "unrestricted policy allows agent in parallel",
			selector: "agent1:m1!inf1:m2",
			policy:   config.ExecutionCompositionUnrestricted,
			wantErr:  false,
		},
		{
			name:     "unrestricted policy allows agent in failover",
			selector: "agent1:m1|inf1:m2",
			policy:   config.ExecutionCompositionUnrestricted,
			wantErr:  false,
		},
		{
			name:     "unrestricted policy allows agent in thinker",
			selector: "[thinker]agent1:plan^agent2:exec",
			policy:   config.ExecutionCompositionUnrestricted,
			wantErr:  false,
		},
		{
			name:     "unrestricted policy allows unknown class in composite",
			selector: "unk1:m1^inf1:m2",
			policy:   config.ExecutionCompositionUnrestricted,
			wantErr:  false,
		},

		// Absent backend preservation (should not be rejected as unsafe execution composition)
		{
			name:     "absent backends in failover ignored by composition validator",
			selector: "absent1:m1|absent2:m2",
			policy:   config.ExecutionCompositionSafe,
			wantErr:  false, // Defer to RejectUnknownBackends
		},
		{
			name:     "absent backend with inference backend in parallel",
			selector: "absent1:m1!inf1:m2",
			policy:   config.ExecutionCompositionSafe,
			wantErr:  false, // Defer to RejectUnknownBackends
		},
		{
			name:         "absent backend with agent backend in parallel denies on agent",
			selector:     "absent1:m1!agent1:m2",
			policy:       config.ExecutionCompositionSafe,
			wantErr:      true,
			wantComp:     "parallel selector",
			wantOffender: "agent1",
			wantClass:    aipsdk.BackendExecutionAgentRuntime,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.selector)
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tc.selector, err)
			}

			err = ValidateExecutionComposition(sel, resolver, tc.policy)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateExecutionComposition(%q, %q) got nil error, want error", tc.selector, tc.policy)
				}
				if !errors.Is(err, ErrUnsafeExecutionComposition) {
					t.Fatalf("ValidateExecutionComposition err = %v, want errors.Is(err, ErrUnsafeExecutionComposition)", err)
				}
				var unsafeErr *UnsafeExecutionCompositionError
				if !errors.As(err, &unsafeErr) {
					t.Fatalf("errors.As failed to extract *UnsafeExecutionCompositionError from %v", err)
				}
				if tc.wantOffender != "" && unsafeErr.BackendID != tc.wantOffender {
					t.Fatalf("unsafeErr.BackendID = %q, want %q", unsafeErr.BackendID, tc.wantOffender)
				}
				if unsafeErr.Class != tc.wantClass {
					t.Fatalf("unsafeErr.Class = %q, want %q", unsafeErr.Class, tc.wantClass)
				}
				if tc.wantComp != "" && unsafeErr.Composition != tc.wantComp {
					t.Fatalf("unsafeErr.Composition = %q, want %q", unsafeErr.Composition, tc.wantComp)
				}
				// Diagnostic message checks (Requirement 9)
				msg := err.Error()
				if !strings.Contains(msg, "unsafe backend execution composition") {
					t.Fatalf("error message %q missing expected prefix", msg)
				}
				if !strings.Contains(msg, "direct routing is supported") {
					t.Fatalf("error message %q missing direct routing hint", msg)
				}
			} else if err != nil {
				t.Fatalf("ValidateExecutionComposition(%q, %q) unexpected error: %v", tc.selector, tc.policy, err)
			}
		})
	}
}

func TestValidateExecutionComposition_NilResolverFailsClosed(t *testing.T) {
	t.Parallel()

	// Direct primary is allowed even with nil resolver
	directSel, err := Parse("agent1:coder")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateExecutionComposition(directSel, nil, config.ExecutionCompositionSafe); err != nil {
		t.Fatalf("direct primary with nil resolver should succeed, got: %v", err)
	}

	// Composed selector with nil resolver must fail closed under safe
	compSel, err := Parse("b1:m1^b2:m2")
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateExecutionComposition(compSel, nil, config.ExecutionCompositionSafe)
	if err == nil {
		t.Fatal("expected error with nil resolver on composed selector, got nil")
	}
	if !errors.Is(err, ErrUnsafeExecutionComposition) {
		t.Fatalf("expected errors.Is(err, ErrUnsafeExecutionComposition), got: %v", err)
	}

	// Composed selector with nil resolver succeeds under unrestricted
	if err := ValidateExecutionComposition(compSel, nil, config.ExecutionCompositionUnrestricted); err != nil {
		t.Fatalf("unrestricted with nil resolver should succeed, got: %v", err)
	}
}

func TestValidateExecutionComposition_AliasAndModelOnlyExpanded(t *testing.T) {
	t.Parallel()

	resolver := mapExecutionResolver{
		"default_inf": aipsdk.BackendExecutionInference,
		"cursor_sdk":  aipsdk.BackendExecutionAgentRuntime,
	}

	aliases, err := NewAliasResolver([]ModelAliasRule{
		{
			Pattern:     "^agent_alias$",
			Replacement: "cursor_sdk:agent-v1",
		},
		{
			Pattern:     "^parallel_alias$",
			Replacement: "cursor_sdk:agent-v1!default_inf:gpt-4o",
		},
	})
	if err != nil {
		t.Fatalf("NewAliasResolver failed: %v", err)
	}

	// Direct alias expanding to agent_runtime -> direct allowed
	sel1, err := CompileSelector("agent_alias", aliases, "default_inf")
	if err != nil {
		t.Fatalf("CompileSelector failed: %v", err)
	}
	if err := ValidateExecutionComposition(sel1, resolver, config.ExecutionCompositionSafe); err != nil {
		t.Fatalf("direct alias to agent should be allowed, got: %v", err)
	}

	// Alias expanding to parallel composition containing agent -> denied
	sel2, err := CompileSelector("parallel_alias", aliases, "default_inf")
	if err != nil {
		t.Fatalf("CompileSelector failed: %v", err)
	}
	err = ValidateExecutionComposition(sel2, resolver, config.ExecutionCompositionSafe)
	if err == nil {
		t.Fatal("expected parallel alias containing agent to be rejected, got nil")
	}
	if !errors.Is(err, ErrUnsafeExecutionComposition) {
		t.Fatalf("expected ErrUnsafeExecutionComposition, got %v", err)
	}

	// Model-only selector applying default inference backend
	sel3, err := CompileSelector("gpt-4o", nil, "default_inf")
	if err != nil {
		t.Fatalf("CompileSelector failed: %v", err)
	}
	if err := ValidateExecutionComposition(sel3, resolver, config.ExecutionCompositionSafe); err != nil {
		t.Fatalf("model-only direct with default inference should be allowed, got: %v", err)
	}
}

func TestIsDirectPrimary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		selector string
		want     bool
	}{
		{"openai:gpt-4o", true},
		{"openai:gpt-4o?temp=0.7", true},
		{"openai:gpt-4o?temp=0.7&ttft=10s", true},
		{"openai:gpt-4o|azure:gpt-4o", false},
		{"openai:gpt-4o^azure:gpt-4o", false},
		{"openai:gpt-4o!azure:gpt-4o", false},
		{"[thinker]openai:o3^azure:gpt-4o", false},
	}

	for _, tc := range cases {
		t.Run(tc.selector, func(t *testing.T) {
			t.Parallel()
			sel, err := Parse(tc.selector)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", tc.selector, err)
			}
			got := IsDirectPrimary(sel)
			if got != tc.want {
				t.Fatalf("IsDirectPrimary(%q) = %v, want %v", tc.selector, got, tc.want)
			}
		})
	}
}
