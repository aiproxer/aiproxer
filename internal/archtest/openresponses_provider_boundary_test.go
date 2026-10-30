package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openResponsesCompatGenericImportsForbidden targets the generic OpenResponses
// backend package tree (internal/plugins/backends/openresponsescompat).
const openResponsesCompatGenericImportsForbidden = "./internal/plugins/backends/openresponsescompat/..."

// TestProviderBoundary_GenericOpenResponsesDoesNotImportProviders verifies the
// generic OpenResponses backend never depends on provider-specific connector
// modules, connector support, or provider SDKs. A future OpenRouter/NVIDIA
// wrapper must reuse the shared codec through the explicit options seam, never
// by being imported into the generic package.
func TestProviderBoundary_GenericOpenResponsesDoesNotImportProviders(t *testing.T) {
	t.Parallel()
	rules := []forbiddenDep{
		{Substr: "/connectors/openrouter", ErrMsg: "generic OpenResponses backend must not import openrouter connector"},
		{Substr: "/connectors/nvidia", ErrMsg: "generic OpenResponses backend must not import nvidia connector"},
		{Substr: "/connector-support/openaicompat", ErrMsg: "generic OpenResponses backend must not import provider connector support"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "generic OpenResponses backend must not import OpenAI Go SDK"},
	}
	assertDepsExcludeForbidden(t, []string{openResponsesCompatGenericImportsForbidden}, rules)
}

// TestProviderBoundary_ConnectorsDoNotReverseImportGenericOpenResponses scans
// external connector modules and connector support for reverse imports of the
// internal generic OpenResponses backend or its wire codec. Provider wrappers
// may reuse the explicit codec options seam, but they must not reach into
// internal generic details.
func TestProviderBoundary_ConnectorsDoNotReverseImportGenericOpenResponses(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	forbidden := []string{
		"internal/plugins/backends/openresponsescompat",
		"internal/plugins/protocols/openresponses",
	}
	for _, dir := range []string{"connectors", "connector-support"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				name := info.Name()
				if name == "vendor" || name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, marker := range forbidden {
				if strings.Contains(string(b), marker) {
					t.Errorf("%s must not reverse-import %s", path, marker)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

// TestProviderBoundary_ForbiddenImportRulesRegistered verifies the permanent
// package deny-list registers the generic-to-provider boundary for the
// OpenResponses backend.
func TestProviderBoundary_ForbiddenImportRulesRegistered(t *testing.T) {
	t.Parallel()
	required := []struct {
		source string
		target string
	}{
		{"internal/plugins/backends/openresponsescompat", "/connectors/"},
		{"internal/plugins/backends/openresponsescompat", "/connector-support/"},
	}
	for _, req := range required {
		found := false
		for _, rule := range ForbiddenImports {
			if rule.SourcePattern == req.source && rule.TargetPattern == req.target {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ForbiddenImports missing generic→provider rule source %q target %q", req.source, req.target)
		}
	}
}
