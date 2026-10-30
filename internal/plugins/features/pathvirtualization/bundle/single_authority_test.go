package bundle_test

// This file pins the ONE-authority rule at the strongest level available: the
// bundle's own construction site. Both outbound passes must consume the SAME
// compiled policy resolver, because a bundle that constructed a second resolver for
// it would let the early pass and the late pass disagree about which surface is
// path-bearing. Two workspace tags inside one request is exactly the shape
// requirements.md 5.6 forbids, and Task 5.2's composition constraint records it as
// the one disagreement this feature can still cause.
//
// The workspace half of that rule is now stronger than "one shared instance": this
// package holds NO workspace authority at all. The runtime pins one per logical turn
// and projects it onto both stages the two passes read, so the second half of the
// check is that nothing here manufactures, wraps, or chains one.
//
// The check is structural because identity is not observable from outside the
// package: the passes hold their policy resolver in unexported fields, and a
// behavioral test cannot distinguish "one shared instance" from "two equal
// instances" - two separately compiled resolvers would answer identically. Reading
// the construction site is therefore the only way to make the rule falsifiable, and
// it fails the moment a second construction, a second compile, or a locally
// manufactured workspace chain appears.

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// sharedConstructor is one component constructor this bundle must call exactly
// once, with the shared values at the named argument positions.
type sharedConstructor struct {
	// name is the constructor being checked.
	name string
	// want maps a zero-based argument position to the exact source expression that
	// argument must carry, so "resolved.Resolver" cannot silently become a fresh
	// compile and "mode" cannot silently become the engine's zero value.
	want map[int]string
}

// bundleConstructors is the complete set of component constructors the bundle
// builds. It is exhaustive on purpose: an unlisted fourth component, or a second
// call to a listed one, is the shape this file exists to refuse.
var bundleConstructors = []sharedConstructor{
	{name: "NewAttemptTransform", want: map[int]string{0: "mode", 1: "resolved.Resolver"}},
	{name: "NewRequestPartHook", want: map[int]string{0: "mode", 1: "resolved.Resolver"}},
	{name: "NewFinalizer", want: map[int]string{0: "resolved.Resolver", 1: "mode", 2: "resolved.ExpansionPolicy()"}},
}

// manufacturedAuthorities are the workspace-authority constructors. None may be
// called from this package: the authority is the runtime's per-turn pin, projected
// onto both stages the two passes read, and a chain built here would be a second,
// competing fact about the authoritative project root.
var manufacturedAuthorities = []string{
	"NewResolverChain",
	"NewStrictChain",
	"NewStaticResolver",
	"WithWorkspaceView",
}

// coreWorkspaceChain is the internal/core package that owns the workspace chains.
// A feature plugin may not depend on internal/core at all, so its absence here is
// the import-boundary form of the same rule.
const coreWorkspaceChain = "github.com/aiproxer/aiproxer/internal/core/workspace"

// parseBundle parses this package's own production source.
func parseBundle(t *testing.T) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "bundle.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse bundle.go: %v", err)
	}
	return fset, file
}

func TestEveryComponentIsBuiltOnceFromTheSameSharedAuthority(t *testing.T) {
	t.Parallel()
	fset, file := parseBundle(t)

	seen := make(map[string]bool, len(bundleConstructors))
	matched := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		spec, ok := lookupConstructor(sel.Sel.Name)
		if !ok {
			return true
		}
		matched++
		if seen[spec.name] {
			t.Errorf("%s is constructed more than once; a second construction could publish a second policy", spec.name)
		}
		seen[spec.name] = true
		assertSharedArguments(t, fset, spec, call)
		return true
	})
	for _, spec := range bundleConstructors {
		if !seen[spec.name] {
			t.Errorf("%s is never constructed; the bundle would contribute an incomplete generation", spec.name)
		}
	}
	if matched != len(bundleConstructors) {
		t.Fatalf("matched %d component constructor calls, want %d", matched, len(bundleConstructors))
	}
}

func TestTheBundleNeverManufacturesAWorkspaceAuthority(t *testing.T) {
	t.Parallel()
	_, file := parseBundle(t)
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		for _, forbidden := range manufacturedAuthorities {
			if sel.Sel.Name == forbidden {
				t.Errorf("the bundle calls %s; the workspace authority is injected, never manufactured here", forbidden)
			}
		}
		return true
	})
	for _, spec := range file.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("import path is not a string literal: %v", err)
		}
		if imported == coreWorkspaceChain {
			t.Errorf("the bundle imports %q; the injected authority is the only workspace dependency here", coreWorkspaceChain)
		}
	}
}

func assertSharedArguments(t *testing.T, fset *token.FileSet, spec sharedConstructor, call *ast.CallExpr) {
	t.Helper()
	highest := -1
	for index := range spec.want {
		if index > highest {
			highest = index
		}
	}
	if len(call.Args) <= highest {
		t.Errorf("%s called with %d args, want at least %d", spec.name, len(call.Args), highest+1)
		return
	}
	for index, want := range spec.want {
		if got := exprString(fset, call.Args[index]); got != want {
			t.Errorf("%s arg %d = %q, want %q", spec.name, index, got, want)
		}
	}
}

func lookupConstructor(name string) (sharedConstructor, bool) {
	for _, spec := range bundleConstructors {
		if spec.name == name {
			return spec, true
		}
	}
	return sharedConstructor{}, false
}

// exprString renders an argument expression from its own source text, which is what
// makes the check about the bundle's construction site rather than about a
// reconstructed approximation of it.
func exprString(fset *token.FileSet, expr ast.Expr) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, expr); err != nil {
		return ""
	}
	return buf.String()
}
