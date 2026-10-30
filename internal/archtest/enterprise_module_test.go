package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	enterpriseModuleRelPath         = "testdata/enterprise_module"
	externalConnectorModuleRelPath  = "testdata/external_connector"
	externalFeatureSDKModuleRelPath = "testdata/external_feature_sdk"
)

// TestEnterpriseModulePublicOnlyCompileGate proves a sibling module can build
// and run against public packages only (requirements 12.6, 12.9, 17.7).
func TestEnterpriseModulePublicOnlyCompileGate(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, enterpriseModuleRelPath)

	assertNoInternalImportsInDir(t, dir)

	cmd := exec.Command("go", "run", nativeArchBuildContexts()[0].trimpathFlag(), ".")
	cmd.Dir = dir
	cmd.Env = enterpriseModuleTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("enterprise module go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "enterprise_module: ok") {
		t.Fatalf("output=%q", out)
	}
}

// TestExternalConnectorModulePublicHostCompileGate proves an external-style
// connector can compile the supported host path without importing internal
// packages or concrete adapters.
func TestExternalConnectorModulePublicHostCompileGate(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, externalConnectorModuleRelPath)
	assertNoInternalImportsInDir(t, dir)
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	cmd.Env = enterpriseModuleTestEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("external connector module go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "external_connector: ok") {
		t.Fatalf("output=%q", out)
	}
}

// TestExternalFeatureSDKModulePublicOnlyCompileGate proves an external-style
// feature SDK fixture can build and pass tests against public packages only
// (requirements 8.1, 8.2, 8.3).
func TestExternalFeatureSDKModulePublicOnlyCompileGate(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, externalFeatureSDKModuleRelPath)
	assertNoInternalImportsInDir(t, dir)

	cmdTest := exec.Command("go", "test", "./...")
	cmdTest.Dir = dir
	cmdTest.Env = enterpriseModuleTestEnv()
	outTest, err := cmdTest.CombinedOutput()
	if err != nil {
		t.Fatalf("external feature sdk module go test: %v\n%s", err, outTest)
	}

	cmdRun := exec.Command("go", "run", ".")
	cmdRun.Dir = dir
	cmdRun.Env = enterpriseModuleTestEnv()
	outRun, err := cmdRun.CombinedOutput()
	if err != nil {
		t.Fatalf("external feature sdk module go run: %v\n%s", err, outRun)
	}
	if !strings.Contains(string(outRun), "external_feature_sdk: ok") {
		t.Fatalf("output=%q", outRun)
	}
}

func enterpriseModuleTestEnv() []string {
	out := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GOWORK=") || strings.HasPrefix(e, "AIP_ENTERPRISE_CONFIG=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "GOWORK=off")
}

// TestEnterpriseModuleInternalImportScannerRejectsInternalImport locks the
// source scanner that fails architecture CI when the fixture imports internal/.
func TestEnterpriseModuleInternalImportScannerRejectsInternalImport(t *testing.T) {
	t.Parallel()
	src := `package bad
import "github.com/aiproxer/aiproxer/internal/core/config"
`
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	err := scanFileForInternalImports(path)
	if err == nil {
		t.Fatal("expected internal import to be rejected")
	}
	if !strings.Contains(err.Error(), "internal/") {
		t.Fatalf("err=%v", err)
	}
}

func assertNoInternalImportsInDir(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		return scanFileForInternalImports(path)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func scanFileForInternalImports(filePath string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, nil, parser.ImportsOnly)
	if err != nil {
		return err
	}
	for _, imp := range f.Imports {
		impPath := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(impPath, "/internal/") || strings.HasSuffix(impPath, "/internal") {
			return &internalImportError{File: filePath, Import: impPath}
		}
	}
	return nil
}

type internalImportError struct {
	File   string
	Import string
}

func (e *internalImportError) Error() string {
	return e.File + ": forbidden internal/ import " + e.Import + " (requirement 12.6)"
}
