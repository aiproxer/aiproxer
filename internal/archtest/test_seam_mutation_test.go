package archtest

import (
	"go/ast"
	"go/token"
	"os"
	"strings"
	"testing"
)

// mutableTestSeam is a package-level variable that exists only as a test
// override seam (hexagonal-audit follow-up). Production code must treat these
// as immutable after declaration: writes outside *_test.go files are data-race
// surface and hidden coupling. Assignments inside exported swap helpers named
// *ForTest are the sanctioned override path.
type mutableTestSeam struct {
	PackageDir string
	Var        string
}

var mutableTestSeams = []mutableTestSeam{
	{PackageDir: "cmd/aipstd", Var: "runPostgresMigrate"},
	{PackageDir: "cmd/aipstd", Var: "managementListenAddress"},
	{PackageDir: "internal/stdhttp", Var: "listenAndServe"},
	{PackageDir: "internal/stdhttp", Var: "httpServerShutdown"},
	{PackageDir: "internal/stdhttp", Var: "runningAsAdmin"},
	{PackageDir: "internal/infra/runtimebundle", Var: "openPostgresBun"},
	{PackageDir: "internal/infra/runtimebundle", Var: "closePostgresBun"},
}

// TestMutableTestSeamsAssignedOnlyFromTests asserts the listed package-level
// test-override seams are never assigned from production code (excluding the
// declaration itself and *ForTest swap helpers).
func TestMutableTestSeamsAssignedOnlyFromTests(t *testing.T) {
	t.Parallel()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root, err := FindRepoRoot(wd)
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	byDir := make(map[string][]string, len(mutableTestSeams))
	for _, seam := range mutableTestSeams {
		byDir[seam.PackageDir] = append(byDir[seam.PackageDir], seam.Var)
	}

	var findings []string
	err = WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		vars, ok := byDir[PackageDirFromRel(rel)]
		if !ok {
			return nil
		}
		watched := make(map[string]bool, len(vars))
		for _, v := range vars {
			watched[v] = true
		}
		_, f, err := ParseGoSource(abs, src)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name == nil {
				continue
			}
			if strings.HasSuffix(fn.Name.Name, "ForTest") {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok || assign.Tok != token.ASSIGN {
					return true
				}
				for _, lhs := range assign.Lhs {
					ident, ok := lhs.(*ast.Ident)
					if ok && watched[ident.Name] {
						findings = append(findings, rel+": production assignment to test seam "+ident.Name+" in "+fn.Name.Name)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("test seams must be assigned only from *_test.go files or *ForTest helpers:\n%s", strings.Join(findings, "\n"))
	}
}
