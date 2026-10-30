package diag_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/diag"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

func TestScopedOwnerAuthorizer_wrongScope_detailDenyAsNotFound(t *testing.T) {
	t.Parallel()
	a := diag.NewScopedOwnerAuthorizer()
	rec := domain.Record{Owner: domain.PrincipalRef{ID: "alice"}}
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(diag.HeaderOwnerScope, "bob")
	d, err := a.AuthorizeSession(req, rec, diag.OpSessionDetail, "standard")
	if err != nil {
		t.Fatal(err)
	}
	if d.Allow || !d.DenyAsNotFound {
		t.Fatalf("want deny not-found, got %#v", d)
	}
}

func TestScopedOwnerAuthorizer_emptyScope_allows(t *testing.T) {
	t.Parallel()
	a := diag.NewScopedOwnerAuthorizer()
	rec := domain.Record{
		Owner:  domain.PrincipalRef{ID: "alice"},
		Policy: domain.PolicyMetadata{AuditMode: "best_effort", RedactionProfile: "standard"},
	}
	req := httptest.NewRequest("GET", "/x", nil)
	d, err := a.AuthorizeSession(req, rec, diag.OpSessionDetail, "standard")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allow || d.DenyAsNotFound {
		t.Fatalf("want allow, got %#v", d)
	}
	if d.RawAuditAllowed {
		t.Fatal("best_effort must not allow raw audit")
	}
	if app.RawAuditAllowed(rec.Policy) {
		t.Fatal("policy mismatch")
	}
}

func TestScopedOwnerAuthorizer_rawAuditAllowedWhenPolicyFull(t *testing.T) {
	t.Parallel()
	a := diag.NewScopedOwnerAuthorizer()
	rec := domain.Record{
		Owner:  domain.PrincipalRef{ID: "alice"},
		Policy: domain.PolicyMetadata{AuditMode: "full"},
	}
	req := httptest.NewRequest("GET", "/x", nil)
	d, err := a.AuthorizeSession(req, rec, diag.OpAudit, "standard")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allow || !d.RawAuditAllowed {
		t.Fatalf("want raw audit allowed, got %#v", d)
	}
}

func TestScopedOwnerAuthorizer_effectiveStrictFromDefault(t *testing.T) {
	t.Parallel()
	a := diag.NewScopedOwnerAuthorizer()
	rec := domain.Record{
		Owner:  domain.PrincipalRef{ID: "alice"},
		Policy: domain.PolicyMetadata{RedactionProfile: "standard"},
	}
	req := httptest.NewRequest("GET", "/x", nil)
	d, err := a.AuthorizeSession(req, rec, diag.OpTranscript, "strict")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(d.EffectivePolicy.RedactionProfile, "strict") {
		t.Fatalf("want strict effective profile, got %q", d.EffectivePolicy.RedactionProfile)
	}
}

func TestScopedOwnerAuthorizer_listDenyWhenOwnerQueryConflictsScope(t *testing.T) {
	t.Parallel()
	a := diag.NewScopedOwnerAuthorizer()
	req := httptest.NewRequest("GET", "/?owner=other", nil)
	req.Header.Set(diag.HeaderOwnerScope, "alice")
	owner, ws, deny := a.ListFilters(req, "standard")
	if !deny || owner != "" || ws != "" {
		t.Fatalf("want deny empty, got owner=%q ws=%q deny=%v", owner, ws, deny)
	}
}

func TestScopedOwnerAuthorizer_listForcesOwnerFromScope(t *testing.T) {
	t.Parallel()
	a := diag.NewScopedOwnerAuthorizer()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(diag.HeaderOwnerScope, "alice")
	owner, _, deny := a.ListFilters(req, "standard")
	if deny || owner != "alice" {
		t.Fatalf("want owner alice, deny=false, got owner=%q deny=%v", owner, deny)
	}
}
