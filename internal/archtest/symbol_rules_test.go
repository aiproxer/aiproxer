package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestForbiddenDeclarations_ReconstructedFeatureMergeSymbolsCaught(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pkg      string
		kind     SymbolKind
		receiver string
		name     string
		src      string
	}{
		{
			pkg:  "internal/featurebundle",
			kind: SymbolType,
			name: "Merged" + "Feature" + "Surface",
			src:  "package featurebundle\ntype " + "Merged" + "Feature" + "Surface" + " struct{}\n",
		},
		{
			pkg:  "internal/featurebundle",
			kind: SymbolFunc,
			name: "Merge" + "Bundles",
			src:  "package featurebundle\nfunc " + "Merge" + "Bundles" + "() {}\n",
		},
		{
			pkg:  "internal/featurebundle",
			kind: SymbolFunc,
			name: "Merge" + "BundlesChecked",
			src:  "package featurebundle\nfunc " + "Merge" + "BundlesChecked" + "() {}\n",
		},
		{
			pkg:  "internal/featurebundle",
			kind: SymbolFunc,
			name: "Merge" + "FeatureSurface",
			src:  "package featurebundle\nfunc " + "Merge" + "FeatureSurface" + "() {}\n",
		},
		{
			pkg:  "internal/featurebundle",
			kind: SymbolFunc,
			name: "Merge" + "FeatureSurfaces",
			src:  "package featurebundle\nfunc " + "Merge" + "FeatureSurfaces" + "() {}\n",
		},
		{
			pkg:  "internal/featurebundle",
			kind: SymbolFunc,
			name: "Merge" + "BundlesViaGenerated",
			src:  "package featurebundle\nfunc " + "Merge" + "BundlesViaGenerated" + "() {}\n",
		},
		{
			pkg:  "internal/featurebundle",
			kind: SymbolFunc,
			name: "Merge" + "FeatureSurfaceViaGenerated",
			src:  "package featurebundle\nfunc " + "Merge" + "FeatureSurfaceViaGenerated" + "() {}\n",
		},
		{
			pkg:  "internal/featurebundle",
			kind: SymbolMethod,
			name: "To" + "Merged" + "Feature" + "Surface",
			src:  "package featurebundle\ntype S struct{}\nfunc (S) " + "To" + "Merged" + "Feature" + "Surface" + "() {}\n",
		},
		{
			pkg:      "internal/featurebundle",
			kind:     SymbolMethod,
			receiver: "Merged" + "Feature" + "Surface",
			name:     "App" + "end",
			src:      "package featurebundle\ntype " + "Merged" + "Feature" + "Surface" + " struct{}\nfunc (" + "Merged" + "Feature" + "Surface" + ") Append() {}\n",
		},
		{
			pkg:  "internal/infra/runtimebundle",
			kind: SymbolFunc,
			name: "extensions" + "FromMerged",
			src:  "package runtimebundle\nfunc " + "extensions" + "FromMerged" + "() {}\n",
		},
		{
			pkg:  "internal/infra/runtimebundle",
			kind: SymbolFunc,
			name: "hooksConfig" + "FromMerged",
			src:  "package runtimebundle\nfunc " + "hooksConfig" + "FromMerged" + "() {}\n",
		},
		{
			pkg:  "internal/testkit/planeparity",
			kind: SymbolFunc,
			name: "Assert" + "MergedSurfacesEqual",
			src:  "package planeparity\nfunc " + "Assert" + "MergedSurfacesEqual" + "() {}\n",
		},
		{
			pkg:  "internal/testkit/planeparity",
			kind: SymbolFunc,
			name: "Assert" + "DualPathParity",
			src:  "package planeparity\nfunc " + "Assert" + "DualPathParity" + "() {}\n",
		},
		{
			pkg:  "internal/testkit/planeparity",
			kind: SymbolFunc,
			name: "Assert" + "GeneratedSurfaceInvariants",
			src:  "package planeparity\nfunc " + "Assert" + "GeneratedSurfaceInvariants" + "() {}\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.pkg+"."+tc.name, func(t *testing.T) {
			t.Parallel()

			// 1. Verify that the rule exists in ForbiddenDeclarations
			found := false
			for _, r := range ForbiddenDeclarations {
				if r.Package == tc.pkg && r.Kind == tc.kind && r.Name == tc.name {
					if tc.receiver == "" || r.Receiver == tc.receiver {
						found = true
						break
					}
				}
			}
			if !found {
				t.Fatalf("expected ForbiddenDeclarations to contain rule for %s %s.%s (receiver=%q)", tc.kind, tc.pkg, tc.name, tc.receiver)
			}

			// 2. Verify DeclExists detects it in AST
			_, f, err := ParseGoSource("synthetic.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("parse synthetic: %v", err)
			}
			rule := ForbiddenDeclRule{Package: tc.pkg, Kind: tc.kind, Receiver: tc.receiver, Name: tc.name}
			if !DeclExists(f, rule) {
				t.Fatalf("expected DeclExists to detect %s %s (receiver=%q) in synthetic AST", tc.kind, tc.name, tc.receiver)
			}
		})
	}
}

func TestForbiddenDeclarations_ReceiverQualifiedAppendVariants(t *testing.T) {
	t.Parallel()

	obsoleteRecv := "Merged" + "Feature" + "Surface"
	rule := ForbiddenDeclRule{
		Package:  "internal/featurebundle",
		Kind:     SymbolMethod,
		Receiver: obsoleteRecv,
		Name:     "Append",
		Reason:   "legacy mutable append method deleted",
	}

	caughtCases := []struct {
		name string
		src  string
	}{
		{
			name: "value receiver",
			src:  "package featurebundle\ntype " + obsoleteRecv + " struct{}\nfunc (m " + obsoleteRecv + ") Append() {}\n",
		},
		{
			name: "pointer receiver",
			src:  "package featurebundle\ntype " + obsoleteRecv + " struct{}\nfunc (m *" + obsoleteRecv + ") Append() {}\n",
		},
		{
			name: "generic value receiver",
			src:  "package featurebundle\ntype " + obsoleteRecv + "[T any] struct{}\nfunc (m " + obsoleteRecv + "[T]) Append() {}\n",
		},
		{
			name: "generic pointer receiver",
			src:  "package featurebundle\ntype " + obsoleteRecv + "[T any] struct{}\nfunc (m *" + obsoleteRecv + "[T]) Append() {}\n",
		},
		{
			name: "multi-param generic pointer receiver",
			src:  "package featurebundle\ntype " + obsoleteRecv + "[K comparable, V any] struct{}\nfunc (m *" + obsoleteRecv + "[K, V]) Append() {}\n",
		},
		{
			name: "parenthesized pointer receiver",
			src:  "package featurebundle\ntype " + obsoleteRecv + " struct{}\nfunc (m (*" + obsoleteRecv + ")) Append() {}\n",
		},
	}

	for _, tc := range caughtCases {
		t.Run("caught_"+tc.name, func(t *testing.T) {
			t.Parallel()
			_, f, err := ParseGoSource("synthetic.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !DeclExists(f, rule) {
				t.Fatalf("expected DeclExists to catch obsolete receiver in %s", tc.name)
			}
		})
	}

	allowedCases := []struct {
		name string
		src  string
	}{
		{
			name: "OtherSurface value receiver in featurebundle",
			src:  "package featurebundle\ntype OtherSurface struct{}\nfunc (o OtherSurface) Append() {}\n",
		},
		{
			name: "OtherSurface pointer receiver in featurebundle",
			src:  "package featurebundle\ntype OtherSurface struct{}\nfunc (o *OtherSurface) Append() {}\n",
		},
		{
			name: "OtherSurface generic receiver in featurebundle",
			src:  "package featurebundle\ntype OtherSurface[T any] struct{}\nfunc (o *OtherSurface[T]) Append() {}\n",
		},
		{
			name: "unrelated method name on obsolete receiver",
			src:  "package featurebundle\ntype " + obsoleteRecv + " struct{}\nfunc (m *" + obsoleteRecv + ") OtherMethod() {}\n",
		},
	}

	for _, tc := range allowedCases {
		t.Run("allowed_"+tc.name, func(t *testing.T) {
			t.Parallel()
			_, f, err := ParseGoSource("synthetic.go", []byte(tc.src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if DeclExists(f, rule) {
				t.Fatalf("DeclExists incorrectly matched allowed declaration in %s", tc.name)
			}
		})
	}
}

func TestForbiddenDeclarations_LegitimateCurrentSymbolsNotBanned(t *testing.T) {
	t.Parallel()

	legitSrc := `package featurebundle
import aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"

func appendLifecycles(dst []aipplugin.Lifecycle, incoming []aipplugin.Lifecycle) []aipplugin.Lifecycle {
	return append(dst, incoming...)
}

type OtherSurface struct{}
func (OtherSurface) Append() {}
func (*OtherSurface) Append() {}

func MergeBundlesGenerated() {}
func MergeFeatureSurfaceGenerated() {}
`
	_, f, err := ParseGoSource("internal/featurebundle/merge_surface.go", []byte(legitSrc))
	if err != nil {
		t.Fatalf("parse legit: %v", err)
	}

	for _, r := range ForbiddenDeclarations {
		if r.Package == "internal/featurebundle" {
			if DeclExists(f, r) {
				t.Fatalf("legitimate symbol in featurebundle was incorrectly flagged as forbidden: %s (recv=%q) %s", r.Kind, r.Receiver, r.Name)
			}
		}
	}

	// Unrelated Append method in a different package
	otherRel := "internal/core/runtime/other.go"
	otherPkgDir := PackageDirFromRel(otherRel)
	otherPkgSrc := `package runtime
type Other struct{}
func (Other) Append() {}
`
	_, fOther, err := ParseGoSource(otherRel, []byte(otherPkgSrc))
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range ForbiddenDeclarations {
		if r.Package == otherPkgDir {
			if DeclExists(fOther, r) {
				t.Fatalf("unrelated Append method in runtime was incorrectly flagged by %s rule: %s %s", r.Package, r.Kind, r.Name)
			}
		}
	}
}

func TestScanForbiddenDeclarationsIncludingTests_CatchesTestFileDeclarations(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	// Create fake go.mod
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module testpkg\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	symParity := "Assert" + "DualPath" + "Parity"
	symType := "Merged" + "Feature" + "Surface"

	// 1. Create a test file in internal/testkit/planeparity with forbidden test helper
	planeparityDir := filepath.Join(tmp, "internal", "testkit", "planeparity")
	if err := os.MkdirAll(planeparityDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parityTestSrc := fmt.Sprintf("package planeparity\n\nfunc %s() {}\n", symParity)
	if err := os.WriteFile(filepath.Join(planeparityDir, "foo_test.go"), []byte(parityTestSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// 2. Create a test file in internal/featurebundle with obsolete receiver Append
	fbDir := filepath.Join(tmp, "internal", "featurebundle")
	if err := os.MkdirAll(fbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fbTestSrc := fmt.Sprintf("package featurebundle\n\ntype %s struct{}\nfunc (%s) Append() {}\n", symType, symType)
	if err := os.WriteFile(filepath.Join(fbDir, "obsolete_test.go"), []byte(fbTestSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. Create a test file in internal/featurebundle with allowed OtherSurface.Append
	fbAllowedSrc := "package featurebundle\n\ntype AllowedSurface struct{}\nfunc (AllowedSurface) Append() {}\n"
	if err := os.WriteFile(filepath.Join(fbDir, "allowed_test.go"), []byte(fbAllowedSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// 4. Create a generated file in internal/testkit/planeparity (should be ignored)
	genSrc := fmt.Sprintf("// Code generated by tool. DO NOT EDIT.\npackage planeparity\n\nfunc %s() {}\n", symParity)
	if err := os.WriteFile(filepath.Join(planeparityDir, "ignored_generated.go"), []byte(genSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// Verify production-only scan finds 0 (since all forbidden symbols are in _test.go)
	prodGot, err := ScanForbiddenDeclarations(tmp)
	if err != nil {
		t.Fatalf("ScanForbiddenDeclarations: %v", err)
	}
	if len(prodGot) != 0 {
		t.Fatalf("expected ScanForbiddenDeclarations to ignore _test.go files, got: %v", prodGot)
	}

	// Verify ScanForbiddenDeclarationsIncludingTests finds the test-file violations
	testGot, err := ScanForbiddenDeclarationsIncludingTests(tmp)
	if err != nil {
		t.Fatalf("ScanForbiddenDeclarationsIncludingTests: %v", err)
	}

	// Expecting 3 findings across the synthetic test files
	if len(testGot) != 3 {
		t.Fatalf("expected 3 findings from ScanForbiddenDeclarationsIncludingTests, got %d: %v", len(testGot), testGot)
	}

	foundParity := false
	foundType := false
	foundMethod := false
	for _, f := range testGot {
		if f.Path == "internal/testkit/planeparity/foo_test.go" {
			foundParity = true
		}
		if f.Path == "internal/featurebundle/obsolete_test.go" && f.Detail != "" {
			if f.Detail[:4] == "type" {
				foundType = true
			}
			if f.Detail[:6] == "method" {
				foundMethod = true
			}
		}
	}

	if !foundParity {
		t.Errorf("expected finding for %s in foo_test.go", symParity)
	}
	if !foundType {
		t.Errorf("expected finding for %s in obsolete_test.go", symType)
	}
	if !foundMethod {
		t.Errorf("expected finding for (%s).Append in obsolete_test.go", symType)
	}
}

func TestScanForbiddenDeclarationsIncludingTests_CurrentRepoPasses(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got, err := ScanForbiddenDeclarationsIncludingTests(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 0 {
		t.Fatalf("current repository has forbidden declarations in production or test files (%d):\n%v", len(got), got)
	}
}
