package aipruntime

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

// Deleted public Options fields (Task 8.4). Must not reappear.
var legacyAbsentOptionFields = []string{
	"RequestProviders",
	"AttemptProviders",
	"ConcurrencyProvider",
	"Rater",
	"ProviderDescriptors",
}

// Deleted production helper/id symbols (Task 8.4).
var legacyAbsentHelperIdents = []string{
	"adaptLegacyOptions",
	"prepareCanonicalProduction",
	"legacyProductionRaterID",
	"filterDescriptorsByFamily",
	"descriptorHasFamily",
	"legacyRequestRegistrations",
	"legacyAttemptRegistrations",
	"legacyConcurrencyRegistration",
	"stageFamily",
	"stageFamilyRequest",
	"stageFamilyAttempt",
	"stageFamilyLease",
}

func TestOptions_LegacyAbsent_PublicStructCanonicalOnly(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[Options]()
	for _, name := range legacyAbsentOptionFields {
		if _, ok := typ.FieldByName(name); ok {
			t.Fatalf("Options still exports deleted legacy field %q", name)
		}
	}
	for _, name := range []string{
		"RequestRegistrations",
		"AttemptRegistrations",
		"ConcurrencyRegistration",
	} {
		if _, ok := typ.FieldByName(name); !ok {
			t.Fatalf("Options missing canonical registration field %q", name)
		}
	}
}

func TestLegacyAbsent_ProductionHelpersAndAdapterGone(t *testing.T) {
	t.Parallel()
	dir := siblingDir(t)
	adapter := filepath.Join(dir, "legacy_options.go")
	if _, err := os.Stat(adapter); err == nil {
		t.Fatal("pkg/aipruntime/legacy_options.go must be deleted (Task 8.4)")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat legacy_options.go: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := append(append([]string{}, legacyAbsentOptionFields...), legacyAbsentHelperIdents...)
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		var hits []string
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				for _, id := range forbidden {
					if x.Name == id {
						hits = append(hits, id)
					}
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING && strings.Contains(x.Value, "legacy-production-rater") {
					hits = append(hits, "legacy-production-rater")
				}
			}
			return true
		})
		if len(hits) > 0 {
			t.Fatalf("%s still contains deleted legacy symbols: %v", name, uniqueSorted(hits))
		}
	}
}

func TestRegistration_CanonicalNormalizeWithoutLegacyFields(t *testing.T) {
	t.Parallel()
	in := Options{
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: requestDesc("canonical-req"),
			Priority:   authority.RequestPriorityQuotaBudgetRate,
			Provider:   allowReq{},
		}},
		AttemptRegistrations: []authority.AttemptRegistration{{
			Descriptor: attemptDesc("canonical-att"),
			Priority:   authority.AttemptPriorityHardSpend,
			Provider:   allowAtt{},
		}},
		ConcurrencyRegistration: &authority.ConcurrencyRegistration{
			Descriptor: leaseDesc("canonical-lease"),
			Provider:   allowConc{},
		},
	}
	norm, err := normalizeCanonicalOptions(in, false)
	if err != nil {
		t.Fatalf("normalizeCanonicalOptions: %v", err)
	}
	if len(norm.RequestRegistrations) != 1 || norm.RequestRegistrations[0].Descriptor.ID != "canonical-req" {
		t.Fatalf("request=%+v", norm.RequestRegistrations)
	}
	if len(norm.AttemptRegistrations) != 1 || norm.AttemptRegistrations[0].Descriptor.ID != "canonical-att" {
		t.Fatalf("attempt=%+v", norm.AttemptRegistrations)
	}
	if norm.ConcurrencyRegistration == nil || norm.ConcurrencyRegistration.Descriptor.ID != "canonical-lease" {
		t.Fatalf("concurrency=%+v", norm.ConcurrencyRegistration)
	}
}

func siblingDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(file)
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func requestDesc(id string) authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID:   id,
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageRequestAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func attemptDesc(id string) authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID:   id,
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageAttemptAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func leaseDesc(id string) authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID:   id,
		Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

type allowReq struct{}

func (allowReq) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (allowReq) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}
func (allowReq) ReleaseRequest(context.Context, authority.RequestRelease) error { return nil }

type allowAtt struct{}

func (allowAtt) AdmitAttempt(context.Context, authority.AttemptAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (allowAtt) SettleAttempt(_ context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}
func (allowAtt) ReleaseAttempt(context.Context, authority.AttemptRelease) error { return nil }

type allowConc struct{}

func (allowConc) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "l"}, nil
}

func (allowConc) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow, LeaseID: "l"}, nil
}
func (allowConc) ReleaseLease(context.Context, authority.LeaseRelease) error { return nil }
func (allowConc) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}
