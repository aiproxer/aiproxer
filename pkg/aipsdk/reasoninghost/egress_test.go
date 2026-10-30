package reasoninghost_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/reasoninghost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestEgressAction_String(t *testing.T) {
	t.Parallel()

	cases := []struct {
		action reasoninghost.EgressAction
		want   string
	}{
		{reasoninghost.EgressDeny, "deny"},
		{reasoninghost.EgressAllow, "allow"},
		{reasoninghost.EgressRedactThenAllow, "redact_then_allow"},
		{reasoninghost.EgressAction(99), "deny"},
	}

	for _, tc := range cases {
		if got := tc.action.String(); got != tc.want {
			t.Errorf("EgressAction(%d).String() = %q, want %q", tc.action, got, tc.want)
		}
	}
}

func TestEgressInput_DefensiveScopeClone(t *testing.T) {
	t.Parallel()

	orig := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-1"),
		Roles:       []string{"role1"},
		SafeClaims:  map[string]string{"k1": "v1"},
	}

	in := reasoninghost.NewEgressInput("route-1", "purpose-1", "class-1", orig)
	if in.Route != "route-1" || in.Purpose != "purpose-1" || in.SourceClass != "class-1" {
		t.Fatalf("unexpected fields in EgressInput: %+v", in)
	}

	// Mutate the original scope.
	orig.Roles = append(orig.Roles, "role2")
	orig.SafeClaims["k1"] = "mutated"

	got1 := in.Scope()
	if len(got1.Roles) != 1 || got1.Roles[0] != "role1" {
		t.Fatalf("scope roles leaked mutation: %+v", got1.Roles)
	}
	if got1.SafeClaims["k1"] != "v1" {
		t.Fatalf("scope claims leaked mutation: %+v", got1.SafeClaims)
	}

	// Mutate the returned scope copy.
	got1.Roles = append(got1.Roles, "role3")
	got1.SafeClaims["k2"] = "v2"

	got2 := in.Scope()
	if len(got2.Roles) != 1 || got2.Roles[0] != "role1" {
		t.Fatalf("second call leaked mutation from first call: %+v", got2.Roles)
	}
	if _, exists := got2.SafeClaims["k2"]; exists {
		t.Fatalf("second call leaked new claim from first call: %+v", got2.SafeClaims)
	}
}

type stubPolicy struct {
	decision reasoninghost.EgressDecision
}

func (s stubPolicy) Decide(_ context.Context, _ reasoninghost.EgressInput) (reasoninghost.EgressDecision, error) {
	return s.decision, nil
}

func TestEgressPolicy_InterfaceContract(t *testing.T) {
	t.Parallel()

	var policy reasoninghost.EgressPolicy = stubPolicy{
		decision: reasoninghost.EgressDecision{
			Action:        reasoninghost.EgressAllow,
			PolicyVersion: "v1",
		},
	}

	dec, err := policy.Decide(context.Background(), reasoninghost.EgressInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Action != reasoninghost.EgressAllow || dec.PolicyVersion != "v1" {
		t.Fatalf("unexpected decision: %+v", dec)
	}
}
