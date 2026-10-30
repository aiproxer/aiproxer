package changesurface

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

func TestClassifyPath_CoversArchitectureZones(t *testing.T) {
	t.Parallel()
	tests := map[string]Category{
		"internal/plugins/frontends/synthetic/mount.go": ExtensionOwnedProduction,
		"connectors/synthetic/main.go":                  ExtensionOwnedProduction,
		"provider-profiles/family/provider.yaml":        ProviderProfileData,
		"provider-profiles/contoso.go":                  Other,
		"internal/providerprofiles/catalog.json":        ProviderProfileData,
		"internal/providerprofiles/compiler.go":         Other,
		"internal/standardplugins/contributions.go":     SharedComposition,
		"internal/pluginreg/registry.go":                SharedComposition,
		"pkg/aipapi/call.go":                            CanonicalContract,
		"internal/core/routing/router.go":               CoreRoutingRuntime,
		"Makefile":                                      SharedComposition,
		"api/backendplugin/v1/backend.proto":            BackendPluginABI,
		"pkg/aipsdk/backendplugin/backend.pb.go":        Generated,
		"internal/core/generated/router.gen.go":         Generated,
		"pkg/aipapi/generated/call.pb.go":               Generated,
		"generated/schema.json":                         Generated,
		"gen/catalog.json":                              Generated,
		"internal/testkit/refbackend/fake.go":           TestsReference,
		"testdata/schema.json":                          TestsReference,
		"fixtures/schema.json":                          TestsReference,
		"docs/extension-authoring.md":                   DocsSpec,
		"internal/core/routing/router.md":               CoreRoutingRuntime,
		"internal/core/routing/unknown.bin":             CoreRoutingRuntime,
	}
	for path, want := range tests {
		if got := ClassifyPath(path); got != want {
			t.Errorf("ClassifyPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestReport_IsDeterministicAndSeparatesBreadthFromCoupling(t *testing.T) {
	t.Parallel()
	paths := []string{
		"docs/extension-authoring.md",
		"internal/testkit/reference_fixture.go",
		"internal/testkit/generated/reference.gen.go",
		"provider-profiles/acme.yaml",
		"pkg/aipapi/call.go",
		"internal/plugins/frontends/synthetic/mount.go",
	}
	first := Build(paths)
	second := Build([]string{paths[5], paths[4], paths[3], paths[2], paths[1], paths[0]})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("report is not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if first.Counts[Generated] != 1 || first.Counts[TestsReference] != 1 || first.Counts[DocsSpec] != 1 {
		t.Fatalf("breadth categories not separated: %+v", first.Counts)
	}
	if first.Counts[CanonicalContract] != 1 || first.Counts[ExtensionOwnedProduction] != 1 {
		t.Fatalf("shared-boundary categories not reported: %+v", first.Counts)
	}
	if got := first.Counts[Generated]; got != 1 {
		t.Fatalf("generated evidence count = %d, want 1", got)
	}
	if err := first.ValidateProfileOnly(); err == nil {
		t.Fatal("expected canonical/extension changes to fail profile-only policy")
	}
}

func TestReport_ProfileOnlyAllowsEvidenceButRejectsSharedBoundaries(t *testing.T) {
	t.Parallel()
	clean := Build([]string{
		"provider-profiles/acme.yaml",
		"internal/providerprofiles/profile_test.go",
		"docs/provider-profiles.md",
		"testdata/generated/acme.schema.json",
	})
	if err := clean.ValidateProfileOnly(); err != nil {
		t.Fatalf("clean profile-only report rejected: %v", err)
	}
	for _, path := range []string{"internal/core/routing/unknown.bin", "pkg/aipapi/unknown.txt", "cmd/aipstd/unknown.go", "scripts/quality-checks.sh", "internal/providerprofiles/compiler.go"} {
		if err := Build([]string{"provider-profiles/acme.yaml", path}).ValidateProfileOnly(); err == nil {
			t.Fatalf("profile-only policy accepted unclassified/protected path %q", path)
		}
	}
	for _, path := range []string{
		"internal/stdhttp/server.go",
		"internal/plugins/frontends/openresponses/mount.go",
		"api/backendplugin/v1/backend.proto",
		"internal/standardplugins/table.go",
		"internal/core/routing/router_test.go",
		"pkg/aipapi/call_test.go",
		"internal/plugins/frontends/contoso/mount_test.go",
	} {
		bad := Build([]string{"provider-profiles/acme.yaml", path})
		if err := bad.ValidateProfileOnly(); err == nil {
			t.Fatalf("profile-only policy accepted forbidden shared path %q", path)
		}
	}
}

func TestParsePorcelainZ_UsesNewPathForRenames(t *testing.T) {
	t.Parallel()
	// Git porcelain v1 -z emits the target/new path first, then the source/old
	// path as the following NUL-delimited field.
	input := "R  new/provider.yaml\x00old/provider.yaml\x00?? docs/new.md\x00"
	got, err := ParsePorcelainZ([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/new.md", "new/provider.yaml", "old/provider.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParsePorcelainZ() = %#v, want %#v", got, want)
	}
}

func TestParsePorcelainZ_PreservesPathWhitespace(t *testing.T) {
	t.Parallel()
	input := "??  leading.txt\x00?? trailing.txt \x00"
	got, err := ParsePorcelainZ([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{" leading.txt", "trailing.txt "}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParsePorcelainZ() = %#v, want %#v", got, want)
	}
}

func TestParsePorcelainZ_RenameRetainsCrossZonePaths(t *testing.T) {
	t.Parallel()
	got, err := ParsePorcelainZ([]byte("R  internal/core/routing/router.go\x00provider-profiles/acme.yaml\x00"))
	if err != nil {
		t.Fatal(err)
	}
	report := Build(got)
	if report.Counts[CoreRoutingRuntime] != 1 || report.Counts[ProviderProfileData] != 1 {
		t.Fatalf("rename paths were not independently classified: %#v", report)
	}
	if err := report.ValidateProfileOnly(); err == nil {
		t.Fatal("cross-zone rename incorrectly passed profile-only validation")
	}
}

func TestParsePorcelainZ_UsesGitRenameRecordOrdering(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		// repo is a throwaway fixture repository. Git exports GIT_DIR to every hook
		// it runs, so an inherited GIT_DIR would initialise and commit inside the
		// real repository instead of the fixture, destroying its index and refs.
		cmd.Env = gitscope.Environ()
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.email", "changesurface@example.invalid")
	runGit("config", "user.name", "changesurface test")
	runGit("config", "commit.gpgsign", "false")
	if err := os.MkdirAll(filepath.Join(repo, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "old", "provider.yaml"), []byte("provider\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-qm", "initial")
	if err := os.MkdirAll(filepath.Join(repo, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit("mv", "old/provider.yaml", "new/provider.yaml")

	status := exec.Command("git", "-C", repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	status.Env = gitscope.Environ()
	data, err := status.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	got, err := ParsePorcelainZ(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"new/provider.yaml", "old/provider.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParsePorcelainZ(actual git rename) = %#v, want %#v", got, want)
	}
}

func TestParsePorcelainZ_HandlesCopyRecords(t *testing.T) {
	t.Parallel()
	got, err := ParsePorcelainZ([]byte("C  new/provider.yaml\x00old/provider.yaml\x00"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"new/provider.yaml", "old/provider.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParsePorcelainZ(copy) = %#v, want %#v", got, want)
	}
}

func TestSplitNULPathsPreservesSpaces(t *testing.T) {
	t.Parallel()
	got := splitNULPaths([]byte("docs/provider profile.md\x00a/ leading.go\x00"))
	want := []string{"docs/provider profile.md", "a/ leading.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitNULPaths() = %#v, want %#v", got, want)
	}
}

func TestClassifyPath_NormalizationIsSingleSourceAndRepositorySafe(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]Category{
		"./internal/core/routing/router.go":  CoreRoutingRuntime,
		"internal\\core\\routing\\router.go": CoreRoutingRuntime,
		"a/internal/core/routing/router.go":  Other,
		"b/pkg/aipapi/call.go":               Other,
	} {
		if got := ClassifyPath(path); got != want {
			t.Errorf("ClassifyPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestNormalizeUnifiedDiffPath_IsExplicitlySeparateFromRepositoryPaths(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"./a/pkg/aipapi/call.go": "pkg/aipapi/call.go",
		"b/pkg/aipapi/call.go":   "pkg/aipapi/call.go",
		"a/package.go":           "package.go",
		"b/package.go":           "package.go",
		"a\\package.go":          "package.go",
	} {
		if got := NormalizeUnifiedDiffPath(path); got != want {
			t.Errorf("NormalizeUnifiedDiffPath(%q) = %q, want %q", path, got, want)
		}
	}
	for path, want := range map[string]Category{
		"a/package.go": Other,
		"b/package.go": Other,
	} {
		if got := ClassifyPath(path); got != want {
			t.Errorf("ClassifyPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestFormatHuman_IsMachineIndependentAndExplainsPolicy(t *testing.T) {
	t.Parallel()
	report := Build([]string{"provider-profiles/acme.yaml", "internal/testkit/generated.gen.go"})
	text := FormatHuman(report)
	for _, needle := range []string{"provider-profile-data", "generated", "profile-only: PASS", "dedicated tests/reference breadth"} {
		if !strings.Contains(text, needle) {
			t.Errorf("human report missing %q:\n%s", needle, text)
		}
	}
}

func TestClassifyPath_SyntheticProtocolAndProviderUseGenericZones(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]Category{
		"internal/plugins/frontends/contoso/mount.go":      ExtensionOwnedProduction,
		"internal/plugins/backends/fabrikam/backend.go":    ExtensionOwnedProduction,
		"provider-profiles/fabrikam/alpha.yaml":            ProviderProfileData,
		"internal/core/routing/fabrikam.go":                CoreRoutingRuntime,
		"internal/core/routing/router_test.go":             CoreRoutingRuntime,
		"pkg/aipapi/call_test.go":                          CanonicalContract,
		"internal/plugins/frontends/contoso/mount_test.go": ExtensionOwnedProduction,
		"internal/core/routing/router.md":                  CoreRoutingRuntime,
		"pkg/aipapi/call.md":                               CanonicalContract,
		"a/package.go":                                     Other,
		"b/package.go":                                     Other,
	} {
		if got := ClassifyPath(path); got != want {
			t.Errorf("synthetic %s classified as %s, want %s", path, got, want)
		}
	}
}

func TestClassifyPath_DomainEvidenceCannotBypassBoundary(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]Category{
		"pkg/aipapi/call_test.go":                                 CanonicalContract,
		"internal/core/routing/router_test.go":                    CoreRoutingRuntime,
		"internal/plugins/frontends/contoso/mount_test.go":        ExtensionOwnedProduction,
		"internal/standardplugins/table_test.go":                  SharedComposition,
		"internal/pluginreg/registry_test.go":                     SharedComposition,
		"internal/stdhttp/contract_test.go":                       SharedComposition,
		"api/backendplugin/v1/backend_test.go":                    BackendPluginABI,
		"pkg/aipsdk/backendplugin/host_test.go":                   BackendPluginABI,
		"internal/plugins/frontends/contoso/testdata/schema.json": ExtensionOwnedProduction,
		"internal/core/testdata/router.json":                      CoreRoutingRuntime,
		"pkg/aipapi/fixtures/call.json":                           CanonicalContract,
	} {
		if got := ClassifyPath(path); got != want {
			t.Errorf("domain evidence %q classified as %s, want %s", path, got, want)
		}
		if err := ValidateProfileOnlyPaths([]string{"provider-profiles/acme.yaml", path}); err == nil {
			t.Errorf("profile-only validation accepted domain evidence %q", path)
		}
	}
}

func TestClassifyPath_RootEvidenceAndMakefileAreNotDeadBranches(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]Category{
		"testdata/root.json": TestsReference,
		"fixtures/root.json": TestsReference,
		"Makefile":           SharedComposition,
		"makefile":           SharedComposition,
	} {
		if got := ClassifyPath(path); got != want {
			t.Errorf("ClassifyPath(%q) = %s, want %s", path, got, want)
		}
	}
}
