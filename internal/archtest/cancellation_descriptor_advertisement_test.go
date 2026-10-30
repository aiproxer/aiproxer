package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestArch_CancellationHandshake_DescriptorAdvertisement(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	serviceDirs := discoverConnectorServiceDirs(t, root)
	var eligible int
	for _, dir := range serviceDirs {
		rel := relativeRepoPath(t, root, dir)
		hasForwardExecute := dirHasForwardExecuteAST(t, dir)
		if hasForwardExecute {
			eligible++
		}
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			if hasForwardExecute {
				if !dirHasIdentAST(t, dir, "ProtocolMinorCancellationHandshake") {
					t.Fatalf("%s: eligible connector must advertise ProtocolMinorCancellationHandshake (minor 8)", rel)
				}
				if !dirHasIdentAST(t, dir, "FeatureCancellationHandshake") {
					t.Fatalf("%s: eligible connector must advertise FeatureCancellationHandshake", rel)
				}
				if dirHasFeatureCancellationHandshakeRequiredTrueAST(t, dir) {
					t.Fatalf("%s: FeatureCancellationHandshake must be optional (Required false)", rel)
				}
				return
			}
			if dirHasIdentAST(t, dir, "FeatureCancellationHandshake") {
				t.Fatalf("%s: connector without ForwardExecute must not advertise FeatureCancellationHandshake", rel)
			}
		})
	}
	if eligible == 0 {
		t.Fatal("no connector service uses ForwardExecute")
	}
}

func discoverConnectorServiceDirs(t *testing.T, root string) []string {
	t.Helper()
	connectorsRoot := filepath.Join(root, "connectors")
	seen := map[string]bool{}
	err := filepath.WalkDir(connectorsRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() || entry.Name() != "service" {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == "internal" {
			seen[path] = true
		}
		return filepath.SkipDir
	})
	if err != nil {
		t.Fatalf("discover connector service dirs: %v", err)
	}
	dirs := make([]string, 0, len(seen))
	for dir := range seen {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

func relativeRepoPath(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("relative path %s: %v", path, err)
	}
	return filepath.ToSlash(rel)
}

func TestArch_CancellationHandshake_DiscoveryFindsNewConnector(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	serviceDir := filepath.Join(root, "connectors", "future", "internal", "service")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "service.go"), []byte(`package service
func execute() { backendplugin.ForwardExecute(nil, nil) }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	dirs := discoverConnectorServiceDirs(t, root)
	if len(dirs) != 1 || dirs[0] != serviceDir {
		t.Fatalf("discovered service dirs = %v, want [%s]", dirs, serviceDir)
	}
	if !dirHasForwardExecuteAST(t, dirs[0]) {
		t.Fatal("new connector ForwardExecute path was not recognized as eligible")
	}
}

func TestArch_CancellationHandshake_CodexPreservesExistingFeatures(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "connectors/codex/internal/service")
	required := []string{
		"FeatureExactReasoningParts",
		"FeatureAccountingEvidence",
		"FeatureOrderedItems",
		"FeatureExactOpenResponsesFields",
		"FeatureProxyOwnedSessionID",
	}
	for _, feat := range required {
		if !dirHasIdentAST(t, dir, feat) {
			t.Fatalf("codex descriptor must preserve %s", feat)
		}
	}
	if !dirHasIdentAST(t, dir, "ProtocolMinorCancellationHandshake") {
		t.Fatalf("codex descriptor must be upgraded to ProtocolMinorCancellationHandshake")
	}
	if !dirHasIdentAST(t, dir, "FeatureCancellationHandshake") {
		t.Fatalf("codex descriptor must include FeatureCancellationHandshake")
	}
}

func dirHasForwardExecuteAST(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		if fileHasForwardExecuteAST(t, path) {
			return true
		}
	}
	return false
}

func fileHasForwardExecuteAST(t *testing.T, path string) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "ForwardExecute" {
			found = true
			return false
		}
		return true
	})
	return found
}

func dirHasIdentAST(t *testing.T, dir, ident string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		if fileHasIdentAST(t, path, ident) {
			return true
		}
	}
	return false
}

func fileHasIdentAST(t *testing.T, path, ident string) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == ident {
			found = true
			return false
		}
		return true
	})
	return found
}

func dirHasFeatureCancellationHandshakeRequiredTrueAST(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		if fileHasFeatureCancellationHandshakeRequiredTrueAST(t, path) {
			return true
		}
	}
	return false
}

func fileHasFeatureCancellationHandshakeRequiredTrueAST(t *testing.T, path string) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	viol := false
	ast.Inspect(file, func(n ast.Node) bool {
		if viol {
			return false
		}
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var hasName, hasRequiredTrue bool
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			keyIdent, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch keyIdent.Name {
			case "Name":
				if strings.Contains(nodeText(kv.Value), "FeatureCancellationHandshake") {
					hasName = true
				}
			case "Required":
				if id, ok := kv.Value.(*ast.Ident); ok && id.Name == "true" {
					hasRequiredTrue = true
				}
			}
		}
		if hasName && hasRequiredTrue {
			viol = true
			return false
		}
		return true
	})
	return viol
}
