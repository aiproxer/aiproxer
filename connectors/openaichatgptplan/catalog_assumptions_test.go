package openaichatgptplan_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestNewModuleHasNoLegacyCodexCatalogAssumptions is the static architecture
// proof of requirement 7.5 and task 3.2.
//
// The new connector owns the account-scoped public inventory. It must not
// inherit the retired Codex catalog machinery: it does not run a Codex
// subcommand, does not resolve a Codex executable, does not gate the catalog
// on a Codex client version, does not parse the private Codex catalog and
// does not ship a model snapshot.
//
// The check scans the module's PRODUCTION Go files. Test files are excluded on
// purpose: a test may name the retired machinery in order to prove its
// absence, and that is not a production dependency.
//
// The markers are deliberately narrow. The bare word "Codex" is NOT a marker:
// the connector legitimately talks about the Codex app-server runtime, and the
// migration keeps that factory. The scan is textual - a conservative grep -
// and a marker is accepted only when it names one of the retired catalog
// mechanisms itself, never the runtime that remains supported.
func TestNewModuleHasNoLegacyCodexCatalogAssumptions(t *testing.T) {
	t.Parallel()
	files, err := productionGoFiles(".")
	if err != nil {
		t.Fatalf("scan the production files: %v", err)
	}
	// A walk that stops finding files would pass every absence check
	// vacuously. The count and the known files make the proof non-vacuous:
	// if the scanner loses the source tree, the test fails for that reason.
	if len(files) < 20 {
		t.Fatalf("the scan found %d production Go files, want the connector's source tree", len(files))
	}
	for _, required := range []string{
		filepath.Join("internal", "service", "inventory.go"),
		filepath.Join("internal", "siwc", "models.go"),
	} {
		if !containsPath(files, required) {
			t.Fatalf("the scan did not find the production file %q", required)
		}
	}

	overlay := testOverlay(t)
	for _, file := range files {
		content := productionSource(t, file, overlay)
		text := string(content)
		for _, marker := range forbiddenCatalogMarkers() {
			if strings.Contains(text, marker.text) {
				t.Errorf(
					"the production file %q contains %q; the new connector must not depend on %s",
					file, marker.text, marker.reason,
				)
			}
		}
	}
}

// catalogMarker is one forbidden dependency and the mechanism it names.
type catalogMarker struct {
	// text is the exact substring searched for in production files.
	text string
	// reason names the retired mechanism the marker stands for, so a failure
	// says why the dependency is forbidden and not only that it exists.
	reason string
}

// forbiddenCatalogMarkers returns the retired catalog mechanisms.
//
// Each marker is narrow enough that a legitimate mention of the Codex
// app-server runtime cannot match it:
//
//   - the CLI subcommand marker is the full invocation;
//   - the executable markers are the PATH lookup of a Codex executable and
//     the names a resolver of one would use;
//   - the version markers are names of a Codex client-version gate;
//   - the catalog markers are the retired shipped catalog's file, snapshot
//     variable and provenance names, and the private-catalog names;
//   - the import marker is the retired connector's internal tree.
func forbiddenCatalogMarkers() []catalogMarker {
	return []catalogMarker{
		{text: "codex debug models", reason: "the retired Codex CLI catalog subcommand"},
		{text: `LookPath("codex`, reason: "resolution of a Codex executable from PATH"},
		{text: "codexExecutable", reason: "a Codex executable path"},
		{text: "codexBinaryPath", reason: "a Codex executable path"},
		{text: "codexName", reason: "a Codex executable name used for resolution"},
		{text: "resolveCodexBinary", reason: "a Codex executable resolver"},
		{text: "minCodexVersion", reason: "a Codex client-version gate"},
		{text: "MinCodexVersion", reason: "a Codex client-version gate"},
		{text: "minimumCodexVersion", reason: "a Codex client-version gate"},
		{text: "codexClientVersion", reason: "a Codex client-version gate"},
		{text: "codex_model_catalog", reason: "the retired shipped catalog document"},
		{text: "fallbackSnapshot", reason: "the retired shipped catalog snapshot"},
		{text: "SourceShippedFallback", reason: "the retired shipped-catalog provenance"},
		{text: "shipped_fallback", reason: "the retired shipped-catalog provenance"},
		{text: "shippedModelSnapshot", reason: "the retired shipped catalog snapshot"},
		{text: "codexModelSnapshot", reason: "the retired shipped catalog snapshot"},
		{text: "privateCatalog", reason: "private Codex catalog parsing"},
		{text: "private_catalog", reason: "private Codex catalog parsing"},
		{text: "codexCatalog", reason: "private Codex catalog parsing"},
		{text: "CodexCatalog", reason: "private Codex catalog parsing"},
		{text: "parseCodexCatalog", reason: "private Codex catalog parsing"},
		{text: "connectors/codex/internal", reason: "an import of the retired connector's internals"},
	}
}

// productionGoFiles returns the module's non-test Go files.
//
// The module root is the directory of this test's package. Directories the Go
// tool itself does not compile - hidden directories, vendor and testdata -
// are skipped, so the scan covers the source the connector really ships.
func productionGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() {
			if path == root {
				return nil
			}
			switch {
			case strings.HasPrefix(name, "."),
				name == "vendor",
				name == "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, relative)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// containsPath reports whether the list holds the path.
func containsPath(paths []string, want string) bool {
	return slices.Contains(paths, want)
}

// testOverlay returns the go command's overlay mapping. A normal run has
// none.
//
// The overlay exists because `go test -overlay` changes what the COMPILER
// sees, not the files on disk, and this proof reads the production source. A
// discrimination run sets the same overlay for the test through this
// environment variable, so the static check examines the source the build
// examined. Nothing in the connector reads the variable: it is read by this
// test only, and only to keep the source view and the compiler view equal.
func testOverlay(t *testing.T) map[string]string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("SIWC_STATIC_CHECK_OVERLAY"))
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the test overlay: %v", err)
	}
	var document struct {
		Replace map[string]string `json:"Replace"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("read the test overlay: %v", err)
	}
	replacements := make(map[string]string, len(document.Replace))
	for from, to := range document.Replace {
		absolute, err := filepath.Abs(from)
		if err != nil {
			t.Fatalf("resolve the overlaid path %q: %v", from, err)
		}
		replacements[filepath.Clean(absolute)] = to
	}
	return replacements
}

// productionSource returns the source of one production file, with the test
// overlay applied when one is supplied.
func productionSource(t *testing.T, file string, overlay map[string]string) []byte {
	t.Helper()
	absolute, err := filepath.Abs(file)
	if err != nil {
		t.Fatalf("resolve %q: %v", file, err)
	}
	path := file
	if replacement, overlaid := overlay[filepath.Clean(absolute)]; overlaid {
		if replacement == "" {
			t.Fatalf("the overlay deletes %q; the scan's file list and the source view disagree", file)
		}
		path = replacement
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", file, err)
	}
	return content
}
