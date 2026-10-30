package main

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func TestExternalFeatureSDK_BuildAndRun(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	cmd := exec.CommandContext(context.Background(), "go", "run", ".")
	cmd.Dir = dir
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "external_feature_sdk: ok") {
		t.Fatalf("output=%q", out)
	}
}

func TestExternalFeatureSDK_FeatureBundleAndReplay(t *testing.T) {
	t.Parallel()
	bundle, err := BuildTinyFeatureBundle("hook-test-1", 10)
	if err != nil {
		t.Fatalf("BuildTinyFeatureBundle: %v", err)
	}

	// Test bundle/plane value
	hooksList := feature.Get(bundle.PlaneSet, feature.PlaneSubmitHooks)
	if len(hooksList) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooksList))
	}
	if hooksList[0].ID() != "hook-test-1" {
		t.Errorf("hook ID=%q, want %q", hooksList[0].ID(), "hook-test-1")
	}
	if hooksList[0].Order() != 10 {
		t.Errorf("hook order=%d, want 10", hooksList[0].Order())
	}
	if hooksList[0].FailureMode() != hooks.FailOpen {
		t.Errorf("hook failure mode=%v, want %v", hooksList[0].FailureMode(), hooks.FailOpen)
	}

	// Test public replay/read
	dst := feature.NewContributionSet()
	if err := bundle.PlaneSet.ReplayTo(dst, "test-consumer"); err != nil {
		t.Fatalf("ReplayTo failed: %v", err)
	}
	replayed := feature.Get(dst.Freeze(), feature.PlaneSubmitHooks)
	if len(replayed) != 1 {
		t.Fatalf("expected 1 replayed hook, got %d", len(replayed))
	}
	if replayed[0].ID() != "hook-test-1" {
		t.Errorf("replayed hook ID=%q, want %q", replayed[0].ID(), "hook-test-1")
	}
}

func TestExternalFeatureSDK_UngeneratedPlaneFails(t *testing.T) {
	t.Parallel()
	cs := feature.NewContributionSet()
	err := feature.Contribute(cs, ArbitraryUngeneratedPlane, "test-plugin", []string{"bad"})
	if err == nil {
		t.Fatal("expected error contributing ungenerated plane, got nil")
	}
	if !errors.Is(err, feature.ErrUngeneratedPlane) {
		t.Fatalf("expected errors.Is(err, feature.ErrUngeneratedPlane), got: %v", err)
	}
}

func TestExternalFeatureSDK_NoInternalImports(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)

	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			impPath := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(impPath, "/internal/") || strings.HasSuffix(impPath, "/internal") {
				t.Fatalf("forbidden internal import %q in %s", impPath, entry.Name())
			}
		}
	}
}

func testEnv() []string {
	out := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GOWORK=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "GOWORK=off")
}
