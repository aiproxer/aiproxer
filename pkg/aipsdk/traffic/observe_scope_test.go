package traffic_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// TestObservation_carriesOptionalScope proves the traffic observation contract carries an
// optional safe scope snapshot while preserving the existing PrincipalID field (req 6.4, 7.3).
func TestObservation_carriesOptionalScope(t *testing.T) {
	t.Parallel()
	o := traffic.Observation{
		PrincipalID: "scope-user",
		Scope: scope.PrincipalScopeView{
			SubjectKind: scope.SubjectHuman,
			PrincipalID: scope.Known("scope-user"),
			TenantID:    scope.Known("t1"),
		},
	}
	if o.Scope.PrincipalID.String() != "scope-user" {
		t.Fatalf("Scope.PrincipalID: got %q", o.Scope.PrincipalID)
	}
	if o.PrincipalID != "scope-user" {
		t.Fatalf("PrincipalID: got %q", o.PrincipalID)
	}
}

// TestCaptureMeta_carriesOptionalScope proves capture meta can carry scope for emission paths.
func TestCaptureMeta_carriesOptionalScope(t *testing.T) {
	t.Parallel()
	m := traffic.CaptureMeta{
		TraceID: "tr",
		Scope: scope.PrincipalScopeView{
			SubjectKind: scope.SubjectService,
			PrincipalID: scope.Known("svc"),
		},
	}
	if m.Scope.PrincipalID.String() != "svc" {
		t.Fatalf("Scope.PrincipalID: got %q", m.Scope.PrincipalID)
	}
}

// TestPortBundle_Emit_propagatesMetaScope proves Emit copies CaptureMeta.Scope onto the
// observation so runtime emission can carry safe scope through to observers (req 6.4).
func TestPortBundle_Emit_propagatesMetaScope(t *testing.T) {
	t.Parallel()
	var got traffic.Observation
	obs := captureObs{fn: func(ev traffic.Observation) { got = ev }}
	meta := traffic.CaptureMeta{
		TraceID: "tr",
		Scope: scope.PrincipalScopeView{
			SubjectKind: scope.SubjectHuman,
			PrincipalID: scope.Known("scope-user"),
			TenantID:    scope.Known("t1"),
			Roles:       []string{"admin"},
		},
	}
	b := traffic.PortBundle{Obs: obs}
	b.Emit(context.Background(), traffic.LegBTP, meta, "p", "c", []byte("body"))
	if !got.Scope.PrincipalID.Equal(scope.Known("scope-user")) {
		t.Fatalf("observation Scope.PrincipalID: %+v", got.Scope.PrincipalID)
	}
	if !got.Scope.TenantID.Equal(scope.Known("t1")) {
		t.Fatalf("observation Scope.TenantID: %+v", got.Scope.TenantID)
	}
}

// TestPortBundle_Emit_scopeCloneIsolation proves the scope delivered to the observer is a copy
// so callers cannot mutate the authoritative scope through the meta after emission (req 5.5).
func TestPortBundle_Emit_scopeCloneIsolation(t *testing.T) {
	t.Parallel()
	var got traffic.Observation
	obs := captureObs{fn: func(ev traffic.Observation) { got = ev }}
	roles := []string{"admin"}
	meta := traffic.CaptureMeta{
		TraceID: "tr",
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("u"),
			Roles:       roles,
		},
	}
	b := traffic.PortBundle{Obs: obs}
	b.Emit(context.Background(), traffic.LegBTP, meta, "p", "c", []byte("body"))
	roles[0] = "mutated"
	if got.Scope.Roles[0] == "mutated" {
		t.Fatal("observer scope roles must be isolated from caller mutation")
	}
}

// TestPortBundle_Emit_payloadBytesUnchangedByScope proves adding scope to meta does not alter
// the payload bytes delivered to the observer (scope is metadata, not body content) — required
// for "scope must not be forwarded to backend/client payloads" (req 7.4, 7.1).
func TestPortBundle_Emit_payloadBytesUnchangedByScope(t *testing.T) {
	t.Parallel()
	var got traffic.Observation
	obs := captureObs{fn: func(ev traffic.Observation) { got = ev }}
	body := []byte(`{"hello":"world"}`)
	meta := traffic.CaptureMeta{
		TraceID: "tr",
		Scope: scope.PrincipalScopeView{
			PrincipalID: scope.Known("secret-principal"),
			TenantID:    scope.Known("secret-tenant"),
		},
	}
	b := traffic.PortBundle{Obs: obs}
	b.Emit(context.Background(), traffic.LegPTB, meta, "p", "c", body)
	if !bytes.Equal(got.Body, body) {
		t.Fatalf("payload bytes changed: got %q want %q", got.Body, body)
	}
	if bytes.Contains(got.Body, []byte("secret-principal")) || bytes.Contains(got.Body, []byte("secret-tenant")) {
		t.Fatalf("scope values leaked into payload: %q", got.Body)
	}
}

// legacyTrafficObserverIgnorer proves an observer implemented against the pre-scope contract
// still satisfies traffic.Observer after scope is added (req 6.4, design observer compatibility).
type legacyTrafficObserverIgnorer struct{}

func (legacyTrafficObserverIgnorer) OnObservation(_ context.Context, ev traffic.Observation) error {
	_ = ev.PrincipalID
	return nil
}

func TestLegacyTrafficObserver_stillSatisfiesInterface(t *testing.T) {
	t.Parallel()
	var obs traffic.Observer = legacyTrafficObserverIgnorer{}
	if err := obs.OnObservation(context.Background(), traffic.Observation{}); err != nil {
		t.Fatal(err)
	}
}
