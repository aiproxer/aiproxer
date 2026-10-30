package contract_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// TestSelfDefenseIsPartOfTheCycleNeutralSecurityProjection pins the normative
// cycle-neutral projection: the standard data-plane security input carries one
// self-defense member, so a composition root can hand the immutable generation
// policy, the non-owning process state, the shared client-address trust config
// and the bounded observer to stdhttp without importing stdhttp.
func TestSelfDefenseIsPartOfTheCycleNeutralSecurityProjection(t *testing.T) {
	t.Parallel()

	field, ok := reflect.TypeFor[contract.HTTPSecurityInput]().FieldByName("SelfDefense")
	if !ok {
		t.Fatal("contract.HTTPSecurityInput must carry a SelfDefense member")
	}
	if want := reflect.TypeFor[contract.SelfDefenseSecurityInput](); field.Type != want {
		t.Fatalf("SelfDefense type = %s, want %s", field.Type, want)
	}
	if field.PkgPath != "" {
		t.Fatal("the self-defense projection member must be exported so runtimebundle and stdhttp share it")
	}
}

// TestSelfDefenseProjectionCarriesNoProcessStateLimits seals the design rule
// that process-state sizing is never carried into a request generation:
// max_entries and state_ttl are restart-required because they size the
// process-owned table, and a generation that could see them could resize or
// re-validate process state it does not own.
func TestSelfDefenseProjectionCarriesNoProcessStateLimits(t *testing.T) {
	t.Parallel()

	limits := reflect.TypeFor[ingressdefense.StateLimits]()
	for _, owner := range []reflect.Type{
		reflect.TypeFor[contract.SelfDefenseSecurityInput](),
		reflect.TypeFor[contract.HTTPSecurityInput](),
	} {
		t.Run(owner.Name(), func(t *testing.T) {
			t.Parallel()
			if owner == limits {
				t.Fatal("the projection must not be the process state limits type")
			}
			if path := structPathFor(owner, limits, map[reflect.Type]bool{}); path != "" {
				t.Fatalf("process state limits reached the per-request projection at %s; "+
					"capacity and TTL are startup-fixed and restart-required", path)
			}
		})
	}
}

// TestSelfDefenseProjectionMemberSetIsExact pins the members the projection is
// allowed to carry, so adding a process-state sizing member later is a visible
// contract change rather than a silent growth of the per-request surface.
func TestSelfDefenseProjectionMemberSetIsExact(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		"Policy":          true,
		"State":           true,
		"Resolver":        true,
		"ImpossiblePaths": true,
		// OwnedRouteCandidates and OwnedRoutes are this generation's routing
		// inventory: immutable per-generation data derived from the operator's own
		// configuration and then resolved against the real router. They are the same
		// category as Resolver and ImpossiblePaths, and neither is a sizing member.
		"OwnedRouteCandidates": true,
		"OwnedRoutes":          true,
		"Probe":                true,
		"Observer":             true,
		"Now":                  true,
	}
	typ := reflect.TypeFor[contract.SelfDefenseSecurityInput]()
	if typ.NumField() != len(want) {
		t.Fatalf("SelfDefenseSecurityInput has %d members, want exactly %d", typ.NumField(), len(want))
	}
	for field := range typ.Fields() {
		name := field.Name
		if !want[name] {
			t.Fatalf("unexpected self-defense projection member %q; the projection carries policy, "+
				"non-owning state, the shared resolver config, the matcher toggle, the published-route "+
				"inventory, the credential probe, the bounded observer and the request clock only", name)
		}
	}
	// The published-route inventory must never become a second sizing surface.
	for field := range typ.Fields() {
		switch field.Name {
		case "MaxEntries", "StateTTL", "Limits":
			t.Fatalf("self-defense projection member %q is process-state sizing and must stay "+
				"restart-required, never a per-request projection", field.Name)
		}
	}
}

// TestCredentialDispositionDefaultsToMayAuthenticate pins the fail-open
// default: a zero disposition must never license a pre-auth refusal, so an
// unprojected probe, an unknown provider and any future disposition all reach
// the normal transport-auth chain.
func TestCredentialDispositionDefaultsToMayAuthenticate(t *testing.T) {
	t.Parallel()

	var zero contract.CredentialDisposition
	if zero != contract.MayAuthenticate {
		t.Fatalf("zero CredentialDisposition = %d, want MayAuthenticate (%d)", zero, contract.MayAuthenticate)
	}
	if contract.MayAuthenticate == contract.DefinitelyNoCredential {
		t.Fatal("the credential disposition enum must keep two distinct members")
	}
}

// TestCredentialProbeTypeCarriesOnlyTheDisposition pins the probe shape: the
// cycle-neutral contract answers a presence question about the request, never a
// credential value, so the type returns only the closed disposition.
func TestCredentialProbeTypeCarriesOnlyTheDisposition(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[contract.CredentialProbe]()
	if typ == nil {
		t.Fatal("contract.CredentialProbe must be a defined function type")
	}
	want := reflect.TypeFor[func(*http.Request) contract.CredentialDisposition]()
	if typ.Kind() != reflect.Func || typ.NumIn() != 1 || typ.NumOut() != 1 {
		t.Fatalf("CredentialProbe = %s, want func(*http.Request) contract.CredentialDisposition", typ)
	}
	if typ.Out(0) != want.Out(0) {
		t.Fatalf("CredentialProbe result = %s, want %s", typ.Out(0), want.Out(0))
	}
	if typ.In(0) != reflect.TypeFor[*http.Request]() {
		t.Fatalf("CredentialProbe parameter = %s, want *http.Request", typ.In(0))
	}
}

// structPathFor returns the first struct field path from root that reaches want,
// following struct fields only. Interface and pointer-shaped capability members
// are not followed: the projection exposes capabilities by interface, so their
// dynamic types are not part of the per-request contract surface.
func structPathFor(root, want reflect.Type, seen map[reflect.Type]bool) string {
	if seen[root] {
		return ""
	}
	seen[root] = true
	for field := range root.Fields() {
		if field.Type == want {
			return root.Name() + "." + field.Name
		}
		if field.Type.Kind() == reflect.Struct {
			if path := structPathFor(field.Type, want, seen); path != "" {
				return path
			}
		}
	}
	return ""
}
