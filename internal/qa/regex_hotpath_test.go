package qa

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRegexHotpathAllowlist_parsesCommentsBlankLinesAndMatchesForwardSlashPaths(t *testing.T) {
	t.Parallel()

	contents := strings.Join([]string{
		"# full-line comment",
		"",
		" internal/plugins/frontends/example/handler.go  # justification",
		`internal\core\runtime\safe.go`,
	}, "\n")
	allowlist := parseRegexHotpathAllowlist(contents)

	want := regexHotpathAllowlist{
		"internal/plugins/frontends/example/handler.go": {},
		"internal/core/runtime/safe.go":                 {},
	}
	if !reflect.DeepEqual(allowlist, want) {
		t.Fatalf("allowlist = %#v, want %#v", allowlist, want)
	}
	if !allowlist.allowed("internal/plugins/frontends/example/handler.go") {
		t.Fatal("forward-slash repo-relative path was not allowed")
	}
	if !allowlist.allowed(`internal\core\runtime\safe.go`) {
		t.Fatal("backslash path was not normalized before matching")
	}
	if allowlist.allowed("internal/plugins/frontends/example/other.go") {
		t.Fatal("unexpected allowlist match for different file")
	}
}

func TestFrontendAndRuntimeRequestPathsDoNotCompileRegexps(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	allowlist := loadRegexHotpathAllowlist(t, root)
	var violations []string
	for _, dir := range []string{
		"internal/plugins/frontends",
		"internal/core/runtime",
	} {
		violations = append(violations, regexpCompileViolations(t, root, dir, allowlist)...)
	}
	if len(violations) > 0 {
		t.Fatalf("regexp.Compile/MustCompile in frontend/runtime request paths; use constant package-level regexps or allowlist with justification:\n%s", strings.Join(violations, "\n"))
	}
}

type regexHotpathAllowlist map[string]struct{}

func loadRegexHotpathAllowlist(t *testing.T, root string) regexHotpathAllowlist {
	t.Helper()

	path := filepath.Join(root, "scripts", "regex-hotpath-allowlist.txt")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return regexHotpathAllowlist{}
		}
		t.Fatalf("read regex hotpath allowlist: %v", err)
	}
	return parseRegexHotpathAllowlist(string(b))
}

func parseRegexHotpathAllowlist(contents string) regexHotpathAllowlist {
	allowlist := regexHotpathAllowlist{}
	for line := range strings.SplitSeq(contents, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" {
			continue
		}
		allowlist[strings.ReplaceAll(line, `\`, "/")] = struct{}{}
	}
	return allowlist
}

func (a regexHotpathAllowlist) allowed(relPath string) bool {
	relPath = strings.ReplaceAll(relPath, `\`, "/")
	_, ok := a[relPath]
	return ok
}

func regexpCompileViolations(t *testing.T, root, relDir string, allowlist regexHotpathAllowlist) []string {
	t.Helper()

	var violations []string
	absDir := filepath.Join(root, filepath.FromSlash(relDir))
	err := filepath.WalkDir(absDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if allowlist.allowed(relPath) {
			return nil
		}
		fileViolations, err := regexpCompileCalls(path)
		if err != nil {
			return err
		}
		violations = append(violations, fileViolations...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", relDir, err)
	}
	return violations
}

func regexpCompileCalls(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}

	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "regexp" {
			return true
		}
		if sel.Sel.Name != "Compile" && sel.Sel.Name != "MustCompile" {
			return true
		}
		violations = append(violations, fset.Position(call.Pos()).String())
		return true
	})
	return violations, nil
}

func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root")
		}
		dir = parent
	}
}
