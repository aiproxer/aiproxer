package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

type staticRules struct {
	rules []domain.Rule
}

func (s staticRules) Snapshot(context.Context) (concurrencyapp.RuleSnapshot, error) {
	return concurrencyapp.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateReady},
		Rules:     s.rules,
	}, nil
}

func TestCapacityRows_CountsPerRuleIndependently(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	store := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "cap"})
	rules := []domain.Rule{
		{
			ID: "rule-a", Namespace: "default", Version: "v1", Mode: domain.RuleModeStrict, Limit: 2,
			LeaseTTL: time.Minute,
			Match:    domain.DimensionsMatcher{Principal: domain.DimensionMatcher{Value: scope.Known("alice")}},
		},
		{
			ID: "rule-b", Namespace: "default", Version: "v2", Mode: domain.RuleModeStrict, Limit: 5,
			LeaseTTL: time.Minute,
			Match:    domain.DimensionsMatcher{Principal: domain.DimensionMatcher{Value: scope.Known("bob")}},
		},
	}
	svc := concurrencyapp.NewService(staticRules{rules: rules}, store, fixedClock{t: now})
	ctx := context.Background()
	for _, id := range []string{"a-1", "a-2"} {
		if _, err := svc.Admit(ctx, concurrencyapp.AdmitInput{
			RequestID: id,
			Scope:     scope.PrincipalScopeView{PrincipalID: scope.Known("alice")},
			Namespace: "default",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.Admit(ctx, concurrencyapp.AdmitInput{
		RequestID: "b-1",
		Scope:     scope.PrincipalScopeView{PrincipalID: scope.Known("bob")},
		Namespace: "default",
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := capacityRows(ctx, svc)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]struct{ active, remaining int }{}
	for _, row := range rows {
		byID[row.RuleID] = struct{ active, remaining int }{row.Active, row.RemainingSlots}
		if row.RuleVersion == "" {
			t.Fatalf("missing rule version on %+v", row)
		}
		if row.DimensionKey == "" {
			t.Fatalf("missing dimension key on %+v", row)
		}
	}
	if byID["rule-a"].active != 2 || byID["rule-a"].remaining != 0 {
		t.Fatalf("rule-a=%+v", byID["rule-a"])
	}
	if byID["rule-b"].active != 1 || byID["rule-b"].remaining != 4 {
		t.Fatalf("rule-b=%+v", byID["rule-b"])
	}
}

func TestLeaseQuery_ProjectsExpiringAndRuleVersion(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	store := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "q"})
	rule := domain.Rule{
		ID: "max-active", Namespace: "default", Version: "v9", Mode: domain.RuleModeStrict, Limit: 3,
		LeaseTTL: 20 * time.Second, RenewBefore: 5 * time.Second,
		Match: domain.DimensionsMatcher{Principal: domain.DimensionMatcher{Value: scope.Known("alice")}},
	}
	svc := concurrencyapp.NewService(staticRules{rules: []domain.Rule{rule}}, store, fixedClock{t: now})
	prov := concurrencyapp.NewProvider(svc)
	dec, err := svc.Admit(context.Background(), concurrencyapp.AdmitInput{
		RequestID: "req-exp",
		Scope:     scope.PrincipalScopeView{PrincipalID: scope.Known("alice")},
		Namespace: "default",
		TTL:       10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dec.LeaseID == "" {
		t.Fatal("expected lease")
	}
	page, err := prov.QueryLeases(context.Background(), authority.LeaseQuery{
		RuleID: "max-active",
		State:  authority.LeaseStateExpiring,
		Limit:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Leases) != 1 {
		t.Fatalf("expiring page=%+v", page)
	}
	if page.Leases[0].Version.Version != "v9" {
		t.Fatalf("version=%+v", page.Leases[0].Version)
	}
	if page.Leases[0].DimensionKey == "" {
		t.Fatal("expected dimension key")
	}
	if page.Leases[0].State != authority.LeaseStateExpiring {
		t.Fatalf("state=%s", page.Leases[0].State)
	}
}

func TestLeaseHandler_RejectsUnsupportedFiltersAndExposesVersion(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	store := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "http"})
	rule := domain.Rule{
		ID: "max-active", Namespace: "default", Version: "v3", Mode: domain.RuleModeStrict, Limit: 2,
		LeaseTTL: time.Minute,
		Match:    domain.DimensionsMatcher{Principal: domain.DimensionMatcher{Value: scope.Known("alice")}},
	}
	svc := concurrencyapp.NewService(staticRules{rules: []domain.Rule{rule}}, store, fixedClock{t: now})
	prov := concurrencyapp.NewProvider(svc)
	if _, err := svc.Admit(context.Background(), concurrencyapp.AdmitInput{
		RequestID: "req-http",
		Scope:     scope.PrincipalScopeView{PrincipalID: scope.Known("alice")},
		Namespace: "default",
	}); err != nil {
		t.Fatal(err)
	}
	h := NewConcurrencyAuthorityHandler(ConcurrencyOptions{Provider: prov, Service: svc})

	bad := httptest.NewRequest(http.MethodGet, "/leases?principal=alice", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, bad)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	ok := httptest.NewRequest(http.MethodGet, "/leases?rule_id=max-active&limit=10", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, ok)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Page struct {
			Items []struct {
				RuleID       string `json:"rule_id"`
				RuleVersion  string `json:"rule_version"`
				DimensionKey string `json:"dimension_key"`
			} `json:"items"`
		} `json:"page"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Page.Items) != 1 || body.Page.Items[0].RuleVersion != "v3" || body.Page.Items[0].DimensionKey == "" {
		t.Fatalf("body=%+v", body.Page.Items)
	}
}
