package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFrozenFactsNoFallbackRatchet strengthens the post-freeze boundary:
// after facts freeze, typed recvTurnFacts/requestFacts are authoritative.
// No post-freeze attempt-open / routing code may resurrect business facts
// from context. Context projection is write-only at the boundary.
// This ratchet forbids meteringHolderFrom, requestAuthorityFrom,
// execctx.FromContext, and NativeModelResolverFromContext reads in the
// post-freeze pipeline, and forbids fallback comments.
func TestFrozenFactsNoFallbackRatchet(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	// Post-freeze pipeline files: attempt-open, routing, parallel race,
	// open loop, and the post-freeze viewsFor accessor. Assembly files
	// (executor_prepare_request.go, recv_turn_facts capture) are excluded
	// because they are the single freeze boundary where context -> typed
	// is allowed (documented in captureBoundModelViews).
	postFreezeFiles := []string{
		"executor_open_attempt.go",
		"executor_route_plan.go",
		"parallel_race.go",
		"executor_open_loop.go",
		"recovery_controller.go",
		"turn_terminal.go",
		"executor_settlement.go",
		// The pre-open guard reads the captured auxiliary role and the
		// captured registry publication. Both are typed frozen facts; a
		// context read here would resurrect a removable fact, so the guard
		// file is part of the post-freeze pipeline.
		"executor_guard.go",
		// The round's guard-decision barrier is part of the same
		// post-freeze pipeline: it orders the operation-level guard
		// decision against the round's publication and must not read a
		// business fact from a context.
		"guard_decision_barrier.go",
	}

	forbiddenIdents := []string{
		"meteringHolderFrom",
		"requestAuthorityFrom",
		"FromContext", // covers execctx.FromContext and routing.NativeModelResolverFromContext via ident
		"NativeModelResolverFromContext",
		// The auxiliary role is captured once, at the assembly boundary.
		// Reading it from the context in the post-freeze path would make the
		// label removable between the request and the provider open.
		"AuxiliaryRoleFromContext",
	}
	forbiddenCommentSubstrings := []string{
		"backward-compatibility",
		"backward compatibility",
		"fallback",
		"resurrect",
		"solely because tests bypass",
	}

	t.Run("no_post_freeze_business_fact_reads", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		for _, name := range postFreezeFiles {
			path := filepath.Join(runtimeDir, name)
			content, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				t.Fatalf("read %s: %v", name, err)
			}
			text := string(content)
			// Single AST pass per file: collect all violations.
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			var violations []string
			ast.Inspect(file, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
						if sel.Sel.Name == "FromContext" {
							// Only forbid execctx.FromContext (business views). Other FromContext
							// like SecureSessionTurnFromContext are allowlisted as they are not the frozen business view.
							if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "execctx" {
								pos := fset.Position(x.Pos())
								violations = append(violations, filepath.Base(path)+":"+itoa(pos.Line)+": forbidden execctx.FromContext (post-freeze typed facts are authoritative)")
							}
						}
						if sel.Sel.Name == "AuxiliaryRoleFromContext" {
							if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "execctx" {
								pos := fset.Position(x.Pos())
								violations = append(violations, filepath.Base(path)+":"+itoa(pos.Line)+": forbidden execctx.AuxiliaryRoleFromContext (the auxiliary role is a captured typed fact)")
							}
						}
						if sel.Sel.Name == "NativeModelResolverFromContext" {
							pos := fset.Position(x.Pos())
							violations = append(violations, filepath.Base(path)+":"+itoa(pos.Line)+": forbidden NativeModelResolverFromContext (post-freeze typed facts are authoritative)")
						}
					}
				case *ast.Ident:
					if x.Name == "meteringHolderFrom" || x.Name == "requestAuthorityFrom" {
						// Check if it's a call (parent is CallExpr) to avoid flagging definition
						pos := fset.Position(x.Pos())
						violations = append(violations, filepath.Base(path)+":"+itoa(pos.Line)+": forbidden "+x.Name+" (post-freeze)")
					}
				}
				return true
			})
			if len(violations) > 0 {
				// Filter duplicates and ensure at least one forbidden ident actually present in text
				hasForbidden := false
				for _, ident := range forbiddenIdents {
					if strings.Contains(text, ident) {
						hasForbidden = true
						break
					}
				}
				if hasForbidden {
					t.Fatalf("post-freeze fallback read in %s:\n%s", name, strings.Join(violations, "\n"))
				} else if len(violations) > 0 {
					t.Fatalf("post-freeze fallback read in %s (via AST):\n%s", name, strings.Join(violations, "\n"))
				}
			}
		}
	})

	t.Run("no_fallback_comments_in_post_freeze", func(t *testing.T) {
		t.Parallel()
		runtimeDir := filepath.Join(root, "internal", "core", "runtime")
		for _, name := range postFreezeFiles {
			path := filepath.Join(runtimeDir, name)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				t.Fatalf("read/parse %s: %v", name, err)
			}
			for _, cg := range file.Comments {
				for _, c := range cg.List {
					lower := strings.ToLower(c.Text)
					for _, substr := range forbiddenCommentSubstrings {
						if strings.Contains(lower, strings.ToLower(substr)) {
							t.Fatalf("post-freeze file %s contains forbidden comment pattern %q in comment %q (facts freeze is authoritative, no fallback)", name, substr, c.Text)
						}
					}
				}
			}
		}
	})

	t.Run("viewsFor_is_write_only_no_context_fallback", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(root, "internal", "core", "runtime", "recv_turn_facts.go")
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse recv_turn_facts.go: %v", err)
		}
		found := false
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "viewsFor" {
				return true
			}
			found = true
			body := nodeText(fn.Body)
			if strings.Contains(body, "FromContext") {
				t.Fatalf("viewsFor must not contain FromContext fallback; frozen facts are authoritative, missing is supported nil, never resurrected")
			}
			return false
		})
		if !found {
			t.Fatalf("viewsFor not found")
		}
	})

	t.Run("projectContext_is_write_only", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(root, "internal", "core", "runtime", "recv_turn_facts.go")
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse recv_turn_facts.go: %v", err)
		}
		var projectCtx *ast.FuncDecl
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "projectContext" {
				return true
			}
			projectCtx = fn
			return false
		})
		if projectCtx == nil {
			t.Fatalf("projectContext not found")
		}
		body := nodeText(projectCtx.Body)
		// projectContext must not read business facts from context; it only writes.
		// Allow reads only for diagnostics/tracing, not for meteringHolderFrom etc.
		forbidden := []string{"meteringHolderFrom", "requestAuthorityFrom", "FromContext", "NativeModelResolverFromContext"}
		for _, f := range forbidden {
			if strings.Contains(body, f) {
				t.Fatalf("projectContext must be write-only, found forbidden read %q", f)
			}
		}
	})

	t.Run("buildRoutePlan_uses_typed_resolver", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(root, "internal", "core", "runtime", "executor_route_plan.go")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read executor_route_plan.go: %v", err)
		}
		text := string(content)
		if strings.Contains(text, "NativeModelResolverFromContext") {
			t.Fatalf("buildRoutePlan must use typed prep.nativeResolver, not NativeModelResolverFromContext (resolver is frozen at preparation and projected via projectContext)")
		}
		if !strings.Contains(text, "prep.nativeResolver") && !strings.Contains(text, "prep.recvTurnFacts.nativeResolver") {
			t.Fatalf("buildRoutePlan must read resolver from typed facts (prep.nativeResolver)")
		}
	})
}
