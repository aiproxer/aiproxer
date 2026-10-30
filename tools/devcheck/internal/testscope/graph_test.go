package testscope

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestStatusPathsRemainNULDelimited(t *testing.T) {
	t.Parallel()
	got, err := statusPaths([]byte(" M a path.go\x00A  added.go\x00?? line\nbreak.go\x00D  removed.go\x00"))
	want := []string{"a path.go", "added.go", "line\nbreak.go", "removed.go"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("status paths=%v err=%v", got, err)
	}
	if _, err := statusPaths([]byte("R  new.go\x00old.go\x00")); err == nil {
		t.Fatal("accepted unexpected rename records")
	}
}

func TestProductionClosureStopsAtTestEdges(t *testing.T) {
	t.Parallel()
	graph := []listedPackage{
		{ImportPath: "m/base"},
		{ImportPath: "m/consumer", Imports: []string{"m/base"}},
		{ImportPath: "m/outer", Imports: []string{"m/consumer"}},
		{ImportPath: "m/internaltest", TestImports: []string{"m/outer"}},
		{ImportPath: "m/externaltest", XTestImports: []string{"m/base"}},
		{ImportPath: "m/unaffected", Imports: []string{"m/internaltest", "m/externaltest"}},
	}
	got := affectedPackages(graph, map[string]bool{"m/base": true}, nil)
	want := []string{"m/base", "m/consumer", "m/externaltest", "m/internaltest", "m/outer"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected=%v want %v", got, want)
	}
}

func TestInputOwnershipIncludesTestEmbedsAndCompilerInputs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	graph := []listedPackage{{ImportPath: "m/base", Dir: filepath.Join(root, "base"), EmbedFiles: []string{"data.txt"}, TestEmbedFiles: []string{"tests.txt"}, XTestEmbedFiles: []string{"external.txt"}}}
	for _, tc := range []struct {
		path       string
		production bool
	}{
		{"base/data.txt", true},
		{"base/tests.txt", false},
		{"base/external.txt", false},
		{"base/source.go", true},
		{"base/header.h", true},
		{"base/source_test.go", false},
		{"base/testdata/nested/fixture.json", false},
	} {
		pkg, production, found := inputOwner(root, tc.path, graph)
		if !found || production != tc.production || pkg.ImportPath != "m/base" {
			t.Errorf("input=%s package=%s production=%v found=%v", tc.path, pkg.ImportPath, production, found)
		}
	}
	for _, input := range []string{"base/unknown.txt", "base/testdata-other/fixture.json", "other/data.txt"} {
		if _, _, found := inputOwner(root, input, graph); found {
			t.Errorf("claimed unrelated input %q", input)
		}
	}
}

func TestTestOnlySeedDoesNotChangeProduction(t *testing.T) {
	t.Parallel()
	graph := []listedPackage{
		{ImportPath: "m/base"},
		{ImportPath: "m/consumer", Imports: []string{"m/base"}},
		{ImportPath: "m/testconsumer", TestImports: []string{"m/base"}},
	}
	got := affectedPackages(graph, nil, map[string]bool{"m/base": true})
	if !reflect.DeepEqual(got, []string{"m/base"}) {
		t.Fatalf("test-only scope=%v", got)
	}
}
