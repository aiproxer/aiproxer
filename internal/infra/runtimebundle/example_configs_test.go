package runtimebundle_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestConfigExamples_passInspectRoutes(t *testing.T) {
	t.Parallel()
	root := repoRootFromRuntimebundleTest(t)
	dir := filepath.Join(root, "config", "examples")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var yamls []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(e.Name()), ".yaml") || strings.HasSuffix(strings.ToLower(e.Name()), ".yml") {
			yamls = append(yamls, filepath.Join(dir, e.Name()))
		}
	}
	if len(yamls) == 0 {
		t.Fatal("no yaml files in config/examples")
	}
	mandatory := aipsdk.StandardDistributionRequirements()
	for _, path := range yamls {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			cfgPath := runtimebundle.MaterializeExampleConfigForTest(t, path)
			_, err := runtimebundle.InspectRoutes(context.Background(), runtimebundle.InspectInput{
				ConfigPath: cfgPath,
				Mandatory:  mandatory,
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func repoRootFromRuntimebundleTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := wd
	for range 8 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("go.mod not found above %s", wd)
	return ""
}
