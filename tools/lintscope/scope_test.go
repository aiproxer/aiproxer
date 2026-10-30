package main

import (
	"reflect"
	"testing"
)

func TestAffectedLintPackagesIncludesProductionAndTestConsumers(t *testing.T) {
	graph := []listedPackage{
		{ImportPath: "example/base", Dir: "/repo/base"},
		{ImportPath: "example/consumer", Dir: "/repo/consumer", Imports: []string{"example/base"}},
		{ImportPath: "example/outer", Dir: "/repo/outer", Imports: []string{"example/consumer"}},
		{ImportPath: "example/xtest", Dir: "/repo/xtest", XTestImports: []string{"example/base"}},
		{ImportPath: "example/test", Dir: "/repo/test", TestImports: []string{"example/outer"}},
		{ImportPath: "example/unrelated", Dir: "/repo/unrelated"},
	}
	got := affectedPackages(graph, []string{"example/base"})
	want := []string{"example/base", "example/consumer", "example/outer", "example/test", "example/xtest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scope=%v want %v", got, want)
	}
}

func TestAffectedLintPackagesRetainsConsumersOfDeletedPackage(t *testing.T) {
	graph := []listedPackage{{ImportPath: "example/consumer", Imports: []string{"example/deleted"}}}
	if got := affectedPackages(graph, []string{"example/deleted"}); !reflect.DeepEqual(got, []string{"example/consumer"}) {
		t.Fatalf("deleted package consumers=%v", got)
	}
}

func TestSharedLintChangesForceComprehensiveCertification(t *testing.T) {
	for _, path := range []string{"pkg/aipapi/types.go", "pkg/aipsdk/backendplugin/api.go", "pkg/credpool/pool.go", "connector-support/acp/client.go", "internal/testkit/stub.go", "internal/core/config/config.go", "go.mod", "go.sum", "go.work", ".golangci.yml", "scripts/lint-all-modules.ps1", "tools/lintscope/main.go", "Makefile", "connectors/openrouter/go.mod"} {
		if !requiresFullLint(path) {
			t.Errorf("shared change %s must force full lint", path)
		}
	}
	for _, path := range []string{"internal/core/routing/policy.go", "connectors/openrouter/backend_test.go", "docs/development-iteration.md", ".agents/skills/golang-testing/examples/example.go"} {
		if requiresFullLint(path) {
			t.Errorf("ordinary change %s unexpectedly forces full lint", path)
		}
	}
}
