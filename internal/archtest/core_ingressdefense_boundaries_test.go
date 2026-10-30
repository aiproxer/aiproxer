package archtest

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCoreIngressDefenseIsAdmittedKernelPackage locks the kernel admission of
// internal/core/ingressdefense. Default ingress self-defense is provider- and
// protocol-neutral security that the standard distribution still requires when
// optional feature plugins are absent, so it is a core kernel invariant rather
// than an infrastructure-owned policy or a feature plugin.
func TestCoreIngressDefenseIsAdmittedKernelPackage(t *testing.T) {
	t.Parallel()

	entry, ok := coreOwnershipByPackage()["ingressdefense"]
	if !ok {
		t.Fatal("internal/core/ingressdefense holds production code but has no core-ownership manifest entry; " +
			"add the explicit kernel-invariant rationale before the package may stay in core")
	}
	if entry.Category != CoreOwnershipKernelInvariant {
		t.Fatalf("ingressdefense category = %q, want %q", entry.Category, CoreOwnershipKernelInvariant)
	}
	reason := strings.ToLower(entry.Reason)
	if !strings.Contains(reason, "default") || !strings.Contains(reason, "optional") {
		t.Fatalf("ingressdefense rationale must record why default ingress security stays required when optional features are absent, got %q", entry.Reason)
	}
}

// TestCoreIngressDefensePackageIsDocumentedAndNeutral pins the package boundary:
// the kernel package must exist with a reviewable package doc, and it must not
// reach for HTTP, context, clock-free I/O, storage, public SDK contracts, the
// config package that compiles it, or the HTTP/auth driving adapters.
func TestCoreIngressDefensePackageIsDocumentedAndNeutral(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "ingressdefense")
	if !dirHasProductionGo(dir) {
		t.Fatal("internal/core/ingressdefense must exist and hold production Go policy code")
	}
	if _, err := os.Stat(filepath.Join(dir, "doc.go")); err != nil {
		t.Fatalf("internal/core/ingressdefense must document its boundary in doc.go: %v", err)
	}

	out, err := cachedGoList(t, "-json", "-test=false", "./internal/core/ingressdefense")
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	if !dec.More() {
		t.Fatal("go list: empty output")
	}
	var pkg goListPackage
	if err := dec.Decode(&pkg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	const wantPath = "github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	if pkg.ImportPath != wantPath {
		t.Fatalf("unexpected package: got %q want %q", pkg.ImportPath, wantPath)
	}
	forbidden := []forbiddenDep{
		{Substr: "net/http", ErrMsg: "core ingress defense must not depend on net/http; keep HTTP at stdhttp driving adapters"},
		{Substr: "net/url", ErrMsg: "core ingress defense must not depend on request-target parsing"},
		{Substr: "context", ErrMsg: "core ingress defense owns no cancellable I/O boundary"},
		{Substr: "os", ErrMsg: "core ingress defense owns no process or filesystem state"},
		{Substr: "database/sql", ErrMsg: "core ingress defense must not depend on database/sql; adaptive state is process-local"},
		{Substr: "uptrace/bun", ErrMsg: "core ingress defense must not depend on Bun; adaptive state is process-local"},
		{Substr: "github.com/aiproxer/aiproxer/pkg/", ErrMsg: "core ingress defense must not add a public contract (requirements 10.1/10.2)"},
		{Substr: "github.com/aiproxer/aiproxer/internal/core/config", ErrMsg: "core ingress defense must not depend on the config package that compiles it"},
		{Substr: "github.com/aiproxer/aiproxer/internal/stdhttp", ErrMsg: "core ingress defense must not depend on the stdhttp driving adapter"},
		{Substr: "github.com/aiproxer/aiproxer/internal/plugins", ErrMsg: "core ingress defense must not depend on concrete plugins"},
		{Substr: "github.com/aiproxer/aiproxer/internal/infra", ErrMsg: "core ingress defense must not depend on infrastructure composition"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "core ingress defense must not depend on a provider SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "core ingress defense must not depend on a provider SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "core ingress defense must not depend on a provider SDK"},
	}
	for _, imp := range pkg.Imports {
		for _, r := range forbidden {
			if strings.Contains(imp, r.Substr) {
				t.Fatalf("%s: %s", r.ErrMsg, imp)
			}
		}
	}
}

// TestCoreIngressDefenseCarriesNoAggregateNetworkState proves the exact-IP-only
// source-defense contract cannot regress into subnet, ASN, or country state.
// The only prefix surface is the read-only adaptive exemption allowlist on the
// policy value; no source-state type may be keyed or widened by prefix.
func TestCoreIngressDefenseCarriesNoAggregateNetworkState(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "ingressdefense")
	forbidden := []string{"netip.PrefixFrom", "ASN", "AutonomousSystem", "Country"}
	var found []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, term := range forbidden {
			if strings.Contains(string(src), term) {
				found = append(found, filepath.Base(path)+" ("+term+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("ingress defense must keep exact-address-only state and no ASN/country escalation:\n%s", strings.Join(found, "\n"))
	}
}

// TestCoreIngressDefenseKeysStateByExactAddressOnly seals requirement 5.6
// mechanically now that the package owns a mutable source-state table. The text
// scan above cannot ban a bare netip.Prefix because the read-only adaptive
// exemption allowlist legitimately uses one, so a prefix-keyed map such as
// map[netip.Prefix]*entry would slip through. This check parses the package and
// rejects any map whose key position holds a network prefix.
func TestCoreIngressDefenseKeysStateByExactAddressOnly(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "ingressdefense")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var parsed int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed++
		if offender, ok := prefixKeyedMapDecl(file); ok {
			t.Fatalf("%s declares prefix-keyed host state (%s); ingress defense must key adaptive state by normalized exact address only", name, offender)
		}
	}
	if parsed == 0 {
		t.Fatal("no production source parsed; the prefix-key guard is vacuous")
	}

	// Non-vacuity: the detector must flag every prefix-keyed shape the guard
	// claims to seal and leave the legitimate allowlist surface alone.
	for _, tc := range []struct {
		name    string
		src     string
		wantBad bool
	}{
		{name: "prefix map key", src: "package p\n\nvar aggregate = map[netip.Prefix]int{}\n", wantBad: true},
		{name: "pointer prefix map key", src: "package p\n\nvar aggregate = map[*netip.Prefix]string{}\n", wantBad: true},
		{name: "nested prefix map key", src: "package p\n\nvar aggregate = map[netip.Addr]map[netip.Prefix]int{}\n", wantBad: true},
		{name: "named prefix map type", src: "package p\n\ntype buckets map[netip.Prefix]int\n", wantBad: true},
		{name: "dot imported prefix map key", src: "package p\n\nvar aggregate = map[Prefix]int{}\n", wantBad: true},
		{name: "struct field prefix map", src: "package p\n\ntype state struct {\n\tbuckets map[netip.Prefix]int\n}\n", wantBad: true},
		{name: "exemption allowlist slice", src: "package p\n\ntype policy struct {\n\tExempt []netip.Prefix\n}\n", wantBad: false},
		{name: "exact address state", src: "package p\n\nvar entries = map[netip.Addr]int{}\n", wantBad: false},
		{name: "exact address to prefix map", src: "package p\n\nvar entries = map[netip.Addr]netip.Prefix{}\n", wantBad: false},
		{name: "reason counter", src: "package p\n\nvar counts = map[string]int{}\n", wantBad: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", tc.src, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, bad := prefixKeyedMapDecl(file)
			if bad != tc.wantBad {
				t.Fatalf("prefixKeyedMapDecl = %v, want %v", bad, tc.wantBad)
			}
		})
	}
}

// prefixKeyedMapDecl reports the first map type in file whose key position holds
// a network prefix, together with a printable description of the offending
// type. A prefix value is fine; only a prefix in key position widens one source
// address into aggregate network state.
func prefixKeyedMapDecl(file *ast.File) (string, bool) {
	var offender string
	ast.Inspect(file, func(node ast.Node) bool {
		if offender != "" {
			return false
		}
		decl, ok := node.(*ast.MapType)
		if !ok {
			return true
		}
		if prefixInKeyPosition(decl.Key) {
			offender = "map[" + types.ExprString(decl.Key) + "]" + types.ExprString(decl.Value)
		}
		return true
	})
	return offender, offender != ""
}

// prefixInKeyPosition reports whether expr places a netip.Prefix in a map key
// position, directly, behind a pointer or array, or as the key of a nested map.
func prefixInKeyPosition(expr ast.Expr) bool {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name == "Prefix"
	case *ast.SelectorExpr:
		pkg, ok := node.X.(*ast.Ident)
		return ok && pkg.Name == "netip" && node.Sel.Name == "Prefix"
	case *ast.StarExpr:
		return prefixInKeyPosition(node.X)
	case *ast.ArrayType:
		return prefixInKeyPosition(node.Elt)
	case *ast.MapType:
		return prefixInKeyPosition(node.Key)
	}
	return false
}
