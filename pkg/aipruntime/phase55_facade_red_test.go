package aipruntime_test

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestPhase55_FacadeBuildsWithEnterpriseRegistrations(t *testing.T) {
	t.Parallel()
	assertAipruntimeImportsPublicOnly(t)
	ctx := context.Background()
	rt, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath: repoConfigPath(t),
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID: "facade-req", Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: facadePhase55Req{},
		}},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if !rt.Ready() {
		t.Fatal("expected ready runtime")
	}
	if rt.ExecutorView() == nil {
		t.Fatal("expected ExecutorView")
	}
	if rt.ExecutableGenerationID() == 0 {
		t.Fatal("ExecutableGenerationID required")
	}
	if rt.ExecutableGenerationVersion() == "" {
		t.Fatal("ExecutableGenerationVersion required")
	}
	state := rt.ExecutableGenerationState()
	if state != cp.CapabilityReady && string(state) != "ready" {
		t.Fatalf("state=%q", state)
	}
	if rt.SnapshotGenerationID() == 0 {
		t.Fatal("compatibility metadata SnapshotGenerationID must remain")
	}
	report := rt.ReadinessReport()
	if report == nil {
		t.Fatal("ReadinessReport required")
	}
	if _, err := report.Report(ctx); err != nil {
		t.Fatalf("Report: %v", err)
	}
}

func TestPhase55_DocDistinguishesExecutableFromMetadataPublication(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	docPath := filepath.Join(root, "pkg", "aipruntime", "doc.go")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, needle := range []string{"executable generation", "metadata", "Build"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(needle)) {
			t.Fatalf("doc.go missing %q guidance", needle)
		}
	}
	if strings.Contains(text, "authoritycoord") || strings.Contains(text, "RequestCoordinator") {
		t.Fatal("public docs must not expose internal coordinator types")
	}
}

func assertAipruntimeImportsPublicOnly(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, "authoritycoord") || strings.Contains(path, "internal/core/runtime") {
				t.Fatalf("aipruntime must not import %s", path)
			}
		}
	}
}

type facadePhase55Req struct{}

func (facadePhase55Req) Describe() authority.ProviderDescriptor {
	return authority.ProviderDescriptor{
		ID: "facade-req", Kind: authority.ProviderKindAuthority,
		Postures: []authority.StagePosture{{
			Stage: authority.StageRequestAdmit, Strength: authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
}

func (facadePhase55Req) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (facadePhase55Req) SettleRequest(context.Context, authority.RequestSettlement) (authority.Settlement, error) {
	return authority.Settlement{}, nil
}

func (facadePhase55Req) ReleaseRequest(context.Context, authority.RequestRelease) error { return nil }
