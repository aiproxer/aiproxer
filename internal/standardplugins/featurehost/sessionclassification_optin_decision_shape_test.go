package featurehost

// Task 10.3 structural half of requirement 1.6.
//
// Requirement 1.6 says a downstream feature must be able to decide from the
// immutable session classification snapshot "without parsing User-Agent,
// rescanning prompt content, re-running the classifier, or consulting a
// vendor-specific result". The composed test proves the capability at runtime;
// this file pins the consumer side, so the capability cannot be satisfied by a
// consumer that quietly re-derives the fact.
//
// The checks are pure functions over PARSED SOURCE, which is what lets the
// synthetic controls at the bottom feed them consumers the repository does not
// contain. Two properties make the assertions load-bearing:
//
//   - the decision function's parameter list and selector set are asserted for
//     EXACT equality, so adding an input or an extra read fails; and
//   - the handler's selector set is asserted for exact equality against a set
//     that contains no evidence-bearing field, so a consumer cannot smuggle a
//     User-Agent, prompt, transcript, tool catalog, route or classifier read into
//     the decision path.
//
// Without the controls, an allowlist of this shape could be satisfied by a
// checker that accepts everything, so each control isolates exactly one violation.
//
// INSTRUMENT NOTE for anyone mutating the consumer to prove this guard fails:
// parseOptInConsumerSource reads the file from disk with os.ReadFile at RUNTIME, so
// `go test -overlay` is a silent no-op here. A mutation delivered only through an
// overlay makes this test PASS. Back the real file up outside the repository, edit
// it, run, restore, and verify the restore by diff against the backup and by
// `git status --short` before believing a RED result.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const (
	// optInConsumerSourceFile is the test-only consumer's own file. The closed
	// import and selector assertions apply to it specifically, which is only
	// possible because the consumer lives alone in that file.
	optInConsumerSourceFile = "sessionclassification_optin_gate_consumer_test.go"
	// optInDecisionFunc is the consumer's entire decision function.
	optInDecisionFunc = "optInCodingAgentDecision"
	// optInHandleMethod is the consumer's handler method.
	optInHandleMethod = "Handle"
)

// optInDecisionAllowedSelectors is the EXACT selector set of the decision
// function: the contract predicate on its own parameter, and nothing else.
var optInDecisionAllowedSelectors = []string{"IsCodingAgent"}

// optInHandlerAllowedSelectors is the EXACT selector set of the handler.
//
// Session and Classification are the projection read; AuthoritativeSessionID is
// recorded only so the census can attribute an observation to a session and never
// reaches the decision; record, Deny and Allow are the handler's own plumbing.
// Any evidence-bearing name - ClientUserAgent, Messages, Parts, Items,
// Instructions, Tools, Route, Model, Text, ClassifyToolName, Evidence, Input,
// Classify - is outside the set and fails the assertion.
var optInHandlerAllowedSelectors = []string{
	"Allow", "AuthoritativeSessionID", "Classification", "Deny", "Session", "record",
}

// optInForbiddenImportFragments are import path fragments the consumer must not
// reach for. The classifier contract, the feature implementation and the
// featurehost binding are the three ways a consumer could obtain a second,
// independent decision for the same turn, which requirement 1.6 forbids. The
// shared client-family matcher is included because re-deriving a classification
// from a User-Agent through it is exactly the duplication requirement 1.6 names.
var optInForbiddenImportFragments = []string{
	"aipsdk/sessionclassification",
	"plugins/features/sessionclassification",
	"featurehost/sessionclassification",
	"agentfacts",
	"typesafe",
	"jev",
}

// TestOptInConsumerDecidesFromTheSnapshotAlone is the structural half of
// requirement 1.6, asserted over the consumer's real source.
func TestOptInConsumerDecidesFromTheSnapshotAlone(t *testing.T) {
	t.Parallel()

	file := parseOptInConsumerSource(t)

	params, results, ok := optInSignatureOf(file, optInDecisionFunc)
	if !ok {
		t.Fatalf("the opt-in consumer declares no %s function; the structural guard would be vacuous",
			optInDecisionFunc)
	}
	if want := []string{"Classification"}; !slices.Equal(params, want) {
		t.Errorf("%s parameters = %v, want exactly %v: requirement 1.6 lets a consumer decide from the "+
			"immutable snapshot alone, so any additional input is a capability it must not have",
			optInDecisionFunc, params, want)
	}
	if results != 1 {
		t.Errorf("%s returns %d values, want exactly 1", optInDecisionFunc, results)
	}

	decisionSelectors := optInSelectorNamesIn(t, file, optInDecisionFunc)
	if !slices.Equal(decisionSelectors, optInDecisionAllowedSelectors) {
		t.Errorf("%s selectors = %v, want exactly %v", optInDecisionFunc, decisionSelectors,
			optInDecisionAllowedSelectors)
	}
	handlerSelectors := optInSelectorNamesIn(t, file, optInHandleMethod)
	if !slices.Equal(handlerSelectors, optInHandlerAllowedSelectors) {
		t.Errorf("%s selectors = %v, want exactly %v; a consumer that reads the request, the identity or "+
			"the transcript is not deciding from the snapshot alone",
			optInHandleMethod, handlerSelectors, optInHandlerAllowedSelectors)
	}

	// The decision must be handed the projection, not a local the handler could
	// have built from the request.
	argument, ok := optInDecisionArgumentShape(file, optInDecisionFunc)
	if !ok {
		t.Fatalf("%s is never called, so the handler is not deciding through it", optInDecisionFunc)
	}
	if want := "meta.Session.Classification"; argument != want {
		t.Errorf("%s is called with %q, want %q: the decision must start from the projected snapshot, not "+
			"from a local the handler could have computed from the request", optInDecisionFunc, argument, want)
	}

	imports := optInImportsOf(file)
	for _, imported := range imports {
		for _, forbidden := range optInForbiddenImportFragments {
			if strings.Contains(strings.ToLower(imported), forbidden) {
				t.Errorf("the opt-in consumer imports %q; a consumer must not reach for the classifier "+
					"contract, the classifier implementation, the shared matcher catalog or a vendor adapter "+
					"(requirement 1.6)", imported)
			}
		}
	}
	t.Logf("consumer decision params=%v selectors=%v handler selectors=%v call argument=%q imports=%v",
		params, decisionSelectors, handlerSelectors, argument, imports)
}

// parseOptInConsumerSource reads and parses the consumer's own source file. The
// path is resolved from this file's own location so the guard needs no working
// directory and no repository root discovery.
func parseOptInConsumerSource(t *testing.T) *ast.File {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve this test file's path")
	}
	path := filepath.Join(filepath.Dir(self), optInConsumerSourceFile)
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the opt-in consumer source %s: %v", path, err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		t.Fatalf("parse the opt-in consumer source: %v", err)
	}
	return file
}

// optInSelectorNamesIn returns every selector name appearing in the named
// function's body. It scans only that function's own body: the walk stops at the
// next top-level declaration, so a helper elsewhere in the file cannot make the
// result look clean.
func optInSelectorNamesIn(t *testing.T, file *ast.File, function string) []string {
	t.Helper()
	body := optInFunctionBody(t, file, function)
	names := map[string]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok {
			names[sel.Sel.Name] = true
		}
		return true
	})
	return sortedBoolKeys(names)
}

// optInDecisionArgumentShape renders the argument the decision function is called
// with as a dotted name relative to the handler's parameter, so a handler that
// passed a local variable built from the request instead of the projection is
// visible.
func optInDecisionArgumentShape(file *ast.File, decision string) (string, bool) {
	var (
		shape string
		found bool
	)
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != decision || len(call.Args) != 1 {
			return true
		}
		shape, found = optInDottedName(call.Args[0]), true
		return true
	})
	return shape, found
}

func optInDottedName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return optInDottedName(typed.X) + "." + typed.Sel.Name
	case *ast.ParenExpr:
		return optInDottedName(typed.X)
	case *ast.UnaryExpr:
		return optInDottedName(typed.X)
	}
	return "?"
}

func optInFunctionBody(t *testing.T, file *ast.File, function string) *ast.BlockStmt {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function || fn.Body == nil {
			continue
		}
		return fn.Body
	}
	t.Fatalf("function %q not found in the consumer source; the structural guard would be vacuous", function)
	return nil
}

// optInSignatureOf renders the parameter types and result count of the named
// function.
func optInSignatureOf(file *ast.File, function string) (params []string, results int, ok bool) {
	for _, decl := range file.Decls {
		fn, isFn := decl.(*ast.FuncDecl)
		if !isFn || fn.Name.Name != function {
			continue
		}
		for _, field := range fn.Type.Params.List {
			count := len(field.Names)
			if count == 0 {
				count = 1
			}
			for range count {
				params = append(params, optInTypeString(field.Type))
			}
		}
		if fn.Type.Results != nil {
			for _, field := range fn.Type.Results.List {
				count := len(field.Names)
				if count == 0 {
					count = 1
				}
				results += count
			}
		}
		return params, results, true
	}
	return nil, 0, false
}

func optInTypeString(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return "*" + optInTypeString(typed.X)
	case *ast.SelectorExpr:
		return typed.Sel.Name
	}
	return "?"
}

// optInImportsOf returns the sorted import paths of a parsed file.
func optInImportsOf(file *ast.File) []string {
	out := make([]string, 0, len(file.Imports))
	for _, imp := range file.Imports {
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	slices.Sort(out)
	return out
}

func sortedBoolKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

// optInRejectedImports returns the forbidden fragments a source's imports violate.
func optInRejectedImports(file *ast.File) []string {
	var rejected []string
	for _, imported := range optInImportsOf(file) {
		for _, forbidden := range optInForbiddenImportFragments {
			if strings.Contains(strings.ToLower(imported), forbidden) {
				rejected = append(rejected, imported)
			}
		}
	}
	return rejected
}
