package usage_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// TestEvent_carriesOptionalScope proves the usage event contract carries an optional safe scope
// snapshot while preserving the existing PrincipalID compatibility field (requirements 6.4, 7.3).
func TestEvent_carriesOptionalScope(t *testing.T) {
	t.Parallel()
	ev := usage.Event{
		PrincipalID: "scope-user",
		Scope: scope.PrincipalScopeView{
			SubjectKind: scope.SubjectHuman,
			PrincipalID: scope.Known("scope-user"),
			TenantID:    scope.Known("t1"),
		},
	}
	if ev.Scope.PrincipalID.String() != "scope-user" {
		t.Fatalf("Scope.PrincipalID: got %q", ev.Scope.PrincipalID)
	}
	if ev.PrincipalID != "scope-user" {
		t.Fatalf("PrincipalID: got %q", ev.PrincipalID)
	}
}

// TestEvent_zeroScopeCompiles proves existing observer implementations keep working without
// inspecting scope: a zero-scope event is a valid value and legacy observers compile unchanged
// (requirement 6.4, design "do not require observer implementations to inspect Scope").
func TestEvent_zeroScopeCompiles(t *testing.T) {
	t.Parallel()
	var ev usage.Event
	if ev.Scope.PrincipalID.IsKnown() {
		t.Fatal("zero event scope must be unknown")
	}
	var obs usage.Observer = usage.NoopObserver{}
	if err := obs.OnUsage(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
}

// legacyUsageObserverIgnorer is a compile-time check that an observer implemented against the
// pre-scope contract (only reading PrincipalID) still satisfies usage.Observer.
type legacyUsageObserverIgnorer struct{}

func (legacyUsageObserverIgnorer) OnUsage(_ context.Context, ev usage.Event) error {
	_ = ev.PrincipalID
	return nil
}

func TestLegacyUsageObserver_stillSatisfiesInterface(t *testing.T) {
	t.Parallel()
	var obs usage.Observer = legacyUsageObserverIgnorer{}
	if err := obs.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
}
