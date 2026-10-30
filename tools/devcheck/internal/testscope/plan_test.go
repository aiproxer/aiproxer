package testscope

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPlanInputsAndFallbacks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	graph := []listedPackage{
		{ImportPath: "m/base", Dir: filepath.Join(root, "base")},
		{ImportPath: "m/consumer", Dir: filepath.Join(root, "consumer"), Imports: []string{"m/base"}},
		{ImportPath: "m/app", Dir: filepath.Join(root, "cmd", "app")},
	}
	for _, tc := range []struct {
		name, path string
		packages   []string
		module     string
		full       bool
	}{
		{"cli", "cmd/app/main.go", []string{"./cmd/app"}, ".", false},
		{"production", "base/base.go", []string{"./base", "./consumer"}, ".", false},
		{"fixture", "base/testdata/input.json", []string{"./base"}, ".", false},
		{"docs", "docs/guide.md", nil, "", false},
		{"connector", "connectors/one/one_test.go", []string{"."}, "connectors/one", false},
		{"unknown", "assets/unknown.txt", nil, "", true},
		{"shared SDK", "pkg/aipapi/shared.go", nil, "", true},
		{"shared testkit", "internal/testkit/helper.go", nil, "", true},
		{"policy", "Makefile", nil, "", true},
		{"shared support", "connector-support/acp/helper.go", nil, "", true},
		{"dependency", "connectors/one/go.mod", nil, "", true},
		{"broken graph", "base/broken.go", nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := func(_ context.Context, _, module string) ([]listedPackage, error) {
				if tc.name == "broken graph" {
					return nil, errors.New("invalid package metadata")
				}
				if module == "connectors/one" {
					return []listedPackage{{ImportPath: "m/one", Dir: filepath.Join(root, "connectors", "one")}}, nil
				}
				return graph, nil
			}
			plan := selectChanges(context.Background(), root, Plan{Changed: []string{tc.path}}, []string{".", "connectors/one"}, list)
			if tc.full {
				if plan.Fallback == "" || len(plan.Modules) != 2 || plan.Modules[0].Packages[0] != "./..." {
					t.Fatalf("expected full root and connector fallback: %+v", plan)
				}
				return
			}
			if plan.Fallback != "" {
				t.Fatalf("unexpected fallback: %+v", plan)
			}
			if tc.packages == nil {
				if len(plan.Modules) != 0 {
					t.Fatalf("documentation selected tests: %+v", plan)
				}
				return
			}
			if len(plan.Modules) != 1 || plan.Modules[0].Directory != tc.module || !reflect.DeepEqual(plan.Modules[0].Packages, tc.packages) {
				t.Fatalf("plan=%+v want module=%s packages=%v", plan, tc.module, tc.packages)
			}
		})
	}
}
