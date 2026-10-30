package archtest

import (
	"go/ast"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

// Host-owned authority guards for the multi-user security boundary (issue #701).
//
// The intended design is "connector declares security posture -> host enforces it
// once at the composition boundary". These guards prove the connector subtree
// cannot acquire or re-create that authority, so ordinary connector work can never
// grant shared-use privilege.
//
// Two mechanisms are guarded, because Go's own rules already cover the rest:
//
//  1. Authority dependency: a connector must not import host internals, and in
//     particular must not read the effective access mode, because the host's
//     eligibility decision lives in package-qualified internal code that connector
//     packaging cannot reach.
//  2. Authority re-creation: a connector must not declare a package-local
//     multi-user approval/eligibility/authorization surface that a future reader
//     could mistake for the host decision.
//
// Re-declaring a *name* that also exists in the host policy is deliberately NOT
// guarded: connector modules are separate Go modules with their own import paths,
// so a same-named identifier cannot shadow a package-qualified symbol. Only a
// semantically local multi-user authority is a real bypass signal.

// connectorForbiddenImports are host surfaces a connector must not depend on.
//
//   - any root-module internal package: the approval registry and the host gate
//     are internal, and importing internals would make connector packaging able
//     to reach the decision itself.
//   - internal/core/config specifically: it carries the effective access mode. A
//     connector that reads it would be re-implementing a connector-local
//     single-user check, which this design deliberately forbids.
var connectorForbiddenImports = []struct {
	substr string
	reason string
}{
	{"github.com/aiproxer/aiproxer/internal/", "connector code must not import root-module internal packages (the host owns the multi-user decision)"},
	{"github.com/aiproxer/aiproxer/internal/core/config", "connector code must not read the effective access mode; no connector-local single-user check is allowed"},
}

// connectorAuthorityTokens name the semantics of the host's shared-use authority.
// A connector declaring an identifier that combines them is re-creating the host
// decision locally, which is a bypass attempt regardless of what it returns.
var connectorAuthorityTokens = []string{"approv", "eligib", "authoriz", "allowedformultiuser"}

// forbiddenConnectorImportViolations reports every host import a connector source
// file must not declare.
func forbiddenConnectorImportViolations(rel string, imports []string) []string {
	var violations []string
	for _, imp := range imports {
		for _, forbidden := range connectorForbiddenImports {
			if strings.Contains(imp, forbidden.substr) {
				violations = append(violations, rel+": "+imp+" — "+forbidden.reason)
			}
		}
	}
	return violations
}

// looksLikeLocalMultiUserAuthority reports whether a connector-declared
// identifier tries to own the host's shared-use decision.
func looksLikeLocalMultiUserAuthority(name string) bool {
	lower := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(name))
	if !strings.Contains(lower, "multiuser") {
		return false
	}
	return slices.ContainsFunc(connectorAuthorityTokens, func(tok string) bool { return strings.Contains(lower, tok) })
}

// connectorAuthorityShadowViolations reports locally declared identifiers that
// re-create the host's shared-use decision inside the connector subtree.
func connectorAuthorityShadowViolations(rel string, declared []string) []string {
	var violations []string
	for _, name := range declared {
		if looksLikeLocalMultiUserAuthority(name) {
			violations = append(violations, rel+": declares "+name+", which re-creates the host-owned multi-user authority inside a connector")
		}
	}
	return violations
}

// declaredTopLevelNames collects the package-level identifiers a Go file declares.
func declaredTopLevelNames(file *ast.File) []string {
	var names []string
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				names = append(names, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, name := range s.Names {
						names = append(names, name.Name)
					}
				case *ast.TypeSpec:
					names = append(names, s.Name.Name)
				}
			}
		}
	}
	return names
}

// connectorSubtreeFiles lists every Go source file owned by connector authors,
// including connector tests and the shared connector support modules.
func connectorSubtreeFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	for _, dir := range []string{firstPartyConnectorsDir, firstPartySupportDir} {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		if _, err := os.Stat(abs); err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		err := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if base := d.Name(); base == "testdata" || base == "node_modules" || strings.HasPrefix(base, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files) == 0 {
		t.Fatal("connector subtree is empty; the authority guard would be vacuous")
	}
	return files
}

// TestMultiUserBackendPolicy_HostOwnedRegistryExists proves the privileged
// approval registry is host/distribution owned, live outside connectors/*, and
// reachable only through the distribution's own read-only enumeration API.
func TestMultiUserBackendPolicy_HostOwnedRegistryExists(t *testing.T) {
	t.Parallel()
	if strings.HasPrefix(hostPolicyFile, firstPartyConnectorsDir+"/") {
		t.Fatalf("%s must live in the host/distribution tree, not connectors/*", hostPolicyFile)
	}
	if _, err := os.Stat(filepath.Join(repoRoot(t), filepath.FromSlash(hostPolicyFile))); err != nil {
		t.Fatalf("host-owned multi-user approval registry must exist: %v", err)
	}
	host := loadHostMultiUserDecision()
	if len(host.ApprovedKinds) == 0 {
		t.Fatal("host multi-user policy approves nothing; the approval surface must stay enumerable and testable")
	}
	if len(host.PersonalAuthKinds) == 0 {
		t.Fatal("host multi-user policy declares no personal-auth factory table")
	}
	if len(host.EssentialKinds) == 0 {
		t.Fatal("essential built-in catalog is empty; built-in approvals could not be separated from connector approvals")
	}
	for _, kind := range host.ApprovedKinds {
		if strings.TrimSpace(kind) == "" {
			t.Fatal("host approval table contains a blank factory kind")
		}
	}
	// The read-only API must hand back a copy: a caller cannot mutate the policy.
	first := standardplugins.HostMultiUserBackendPolicy().Approvals()
	if len(first) == 0 {
		t.Fatal("HostMultiUserBackendPolicy().Approvals() returned nothing")
	}
	first[0].FactoryKind = "mutated-by-caller"
	if again := standardplugins.HostMultiUserBackendPolicy().Approvals(); again[0].FactoryKind == "mutated-by-caller" {
		t.Fatal("Approvals() must return a copy; the census read-only contract is broken")
	}
}

// TestConnectorSubtree_CannotImportHostSecurityAuthority proves no connector
// source can reach the host's access-mode or approval internals. A connector
// receiving the deployment mode and branching on it is the anti-pattern the
// central gate replaces.
func TestConnectorSubtree_CannotImportHostSecurityAuthority(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, path := range connectorSubtreeFiles(t, root) {
		rel := SlashPath(strings.TrimPrefix(path, root+string(filepath.Separator)))
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		_, file, err := ParseGoSource(rel, src)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		for _, violation := range forbiddenConnectorImportViolations(rel, FileImportPaths(file)) {
			t.Error(violation)
		}
	}
}

// TestConnectorSubtree_CannotReCreateHostMultiUserAuthority proves the connector
// subtree cannot invent its own local multi-user approval/eligibility/authorization
// authority. A same-named identifier is not a violation on its own: connector
// modules are separate Go modules, so only a semantically local multi-user
// authority is a real bypass.
func TestConnectorSubtree_CannotReCreateHostMultiUserAuthority(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, path := range connectorSubtreeFiles(t, root) {
		rel := SlashPath(strings.TrimPrefix(path, root+string(filepath.Separator)))
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		_, file, err := ParseGoSource(rel, src)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		for _, violation := range connectorAuthorityShadowViolations(rel, declaredTopLevelNames(file)) {
			t.Error(violation)
		}
	}
}

// TestConnectorAuthorityFixtures_DetectBypass proves the two authority detectors
// reject synthetic bypass attempts instead of trusting the live tree by default.
func TestConnectorAuthorityFixtures_DetectBypass(t *testing.T) {
	t.Parallel()
	fixtureDir := filepath.Join(repoRoot(t), filepath.FromSlash(multiUserPolicyFixtureDir), "authority")

	imports := readFixtureImports(t, fixtureDir, "connector_imports_host_internal.go.txt")
	got := forbiddenConnectorImportViolations("connectors/fixture/authority.go", imports)
	if len(got) < len(connectorForbiddenImports) {
		t.Fatalf("fixture must violate every forbidden host import, got %v", got)
	}
	for _, forbidden := range connectorForbiddenImports {
		if !slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, forbidden.reason) }) {
			t.Errorf("detector did not report %q for fixture imports %v", forbidden.reason, imports)
		}
	}

	shadow := readFixtureNames(t, fixtureDir, "connector_recreates_multi_user_authority.go.txt")
	gotShadow := connectorAuthorityShadowViolations("connectors/fixture/authority.go", shadow)
	if len(gotShadow) != 2 {
		t.Fatalf("fixture must be rejected for both a local approval table and a local eligibility function, got %v", gotShadow)
	}
	// A real connector surface that never mentions the boundary must stay clean,
	// including a same-named-but-unrelated identifier: package-qualified authority
	// cannot be shadowed across module boundaries.
	clean := connectorAuthorityShadowViolations("connectors/acp/service.go", []string{
		"Service", "New", "FactoryKind", "Configure", "Describe", "IsApproved", "Lookup",
	})
	if len(clean) != 0 {
		t.Fatalf("detector falsely rejected ordinary connector identifiers: %v", clean)
	}
}

func readFixtureImports(t *testing.T, dir, name string) []string {
	t.Helper()
	src := readFixtureSource(t, dir, name)
	_, file, err := ParseGoSource(name, src)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return FileImportPaths(file)
}

func readFixtureNames(t *testing.T, dir, name string) []string {
	t.Helper()
	src := readFixtureSource(t, dir, name)
	_, file, err := ParseGoSource(name, src)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return declaredTopLevelNames(file)
}

func readFixtureSource(t *testing.T, dir, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return raw
}
