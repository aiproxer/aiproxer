package archtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"github.com/aiproxer/aiproxer/internal/testkit/opensource"
)

// TestConversationViewCoreImportsExcludeProvidersAndFrontends enforces that internal/core/conversationprojection
// and internal/infra/conversationview do not import provider SDKs or concrete frontend/backend plugins.
func TestConversationViewCoreImportsExcludeProvidersAndFrontends(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/core/conversationprojection/..."}, []forbiddenDep{
		{Substr: "github.com/openai/openai-go", ErrMsg: "conversationprojection must not import OpenAI SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "conversationprojection must not import Anthropic SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "conversationprojection must not import Gemini SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "conversationprojection must not import AWS SDK"},
		{Substr: "/internal/plugins/frontends", ErrMsg: "conversationprojection must not import frontend plugins"},
		{Substr: "/internal/plugins/backends", ErrMsg: "conversationprojection must not import backend plugins"},
		{Substr: "uptrace/bun", ErrMsg: "conversationprojection core must not import Bun"},
		{Substr: "/internal/infra", ErrMsg: "conversationprojection core must not import infra"},
	})
	assertDepsExcludeForbidden(t, []string{"./internal/infra/conversationview/..."}, []forbiddenDep{
		{Substr: "github.com/openai/openai-go", ErrMsg: "infra conversationview must not import OpenAI SDK"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "infra conversationview must not import Anthropic SDK"},
		{Substr: "google.golang.org/genai", ErrMsg: "infra conversationview must not import Gemini SDK"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "infra conversationview must not import AWS SDK"},
		{Substr: "/internal/plugins/frontends", ErrMsg: "infra conversationview must not import frontend plugins"},
		{Substr: "/internal/plugins/backends", ErrMsg: "infra conversationview must not import backend plugins"},
	})
	// assertDepsExcludeForbidden skips standard-library packages, so a rule
	// against database/sql can never fire there; enforce that boundary on the
	// core zone's direct imports instead. The infra adapter owns persistence and
	// may import database/sql.
	assertPackageDirectImportsExclude(t,
		[]string{"./internal/core/conversationprojection/..."},
		map[string]string{"database/sql": "conversationprojection core must not import database/sql"},
	)
}

// assertPackageDirectImportsExclude checks the direct imports of every package
// matching pattern, including standard-library imports that
// assertDepsExcludeForbidden cannot see because it skips pkg.Standard packages.
func assertPackageDirectImportsExclude(t *testing.T, patterns []string, forbidden map[string]string) {
	t.Helper()
	for _, pattern := range patterns {
		out, err := cachedGoList(t, "-json", "-test=false", pattern)
		if err != nil {
			t.Fatalf("go list %s: %v", pattern, err)
		}
		dec := json.NewDecoder(bytes.NewReader(out))
		for dec.More() {
			var pkg goListPackage
			if err := dec.Decode(&pkg); err != nil {
				t.Fatalf("decode %s: %v", pattern, err)
			}
			for _, imp := range pkg.Imports {
				if msg, ok := forbidden[imp]; ok {
					t.Fatalf("%s: %s imports %s", msg, pkg.ImportPath, imp)
				}
			}
		}
	}
}

// TestConversationViewCoreHasNoPromptCacheKey proves PromptCacheKey / provider cache policy
// was not moved into conversationprojection core (Req 10.10). The core must not reference
// PromptCacheKey, cache_control, or PromptCacheProfile/Observation.
func TestConversationViewCoreHasNoPromptCacheKey(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "conversationprojection")
	forbidden := []string{"PromptCacheKey", "PromptCacheProfile", "PromptCacheObservation", "cache_control", "CacheControl"}
	var bad []string
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
		s := string(src)
		for _, term := range forbidden {
			if strings.Contains(s, term) {
				bad = append(bad, path+": contains "+term)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("conversationprojection core must not reference provider cache policy:\n%s", strings.Join(bad, "\n"))
	}
}

// TestConversationViewHasNoWatcherOrBackgroundGoroutine enforces Req 13.2: no polling loop,
// watcher, background cleanup goroutine, or global mutable service locator in conversationview core
// or runtime conversation-view seam. Checks for forbidden patterns in production code.
func TestConversationViewHasNoWatcherOrBackgroundGoroutine(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	// The core inventory is the package directory: a new conversationprojection
	// file joins the gate without anyone remembering to extend a hand list. An
	// older hand list still named filter.go and types.go, which no longer exist,
	// and silently skipped them; the read below fails closed instead.
	coreFiles, err := filepath.Glob(filepath.Join(root, "internal", "core", "conversationprojection", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var filesToScan []string
	for _, path := range coreFiles {
		if !strings.HasSuffix(path, "_test.go") {
			filesToScan = append(filesToScan, path)
		}
	}
	if len(filesToScan) == 0 {
		t.Fatal("conversationprojection core has no production files to scan")
	}
	// The infrastructure adapter and the runtime seam stay explicit: a rename
	// must update this inventory instead of silently removing a file from the
	// gate.
	filesToScan = append(filesToScan,
		filepath.Join(root, "internal", "infra", "conversationview", "bun_store.go"),
		filepath.Join(root, "internal", "infra", "conversationview", "observer.go"),
		filepath.Join(root, "internal", "infra", "conversationview", "reference_store.go"),
		filepath.Join(root, "internal", "infra", "conversationview", "store.go"),
		filepath.Join(root, "internal", "infra", "conversationview", "sdkadapter", "writer.go"),
		filepath.Join(root, "internal", "infra", "conversationview", "sdkadapter", "services.go"),
		filepath.Join(root, "internal", "infra", "conversationview", "sdkadapter", "registrar.go"),
		filepath.Join(root, "internal", "core", "runtime", "conversation_view.go"),
		filepath.Join(root, "internal", "core", "runtime", "conversation_view_seam.go"),
		filepath.Join(root, "internal", "core", "runtime", "local_turn.go"),
	)
	forbiddenSubstrs := []string{
		"time.NewTicker",
		"time.Ticker",
		"Watcher",
		"ServiceLocator",
		"service locator",
	}
	var bad []string
	for _, path := range filesToScan {
		src, err := os.ReadFile(path)
		if err != nil {
			// Fail closed: an inventory file that cannot be read means this gate
			// no longer checks what it claims to check.
			t.Fatal(err)
		}
		s := string(src)
		// A go statement is the background-goroutine shape; match the AST so an
		// indented `go worker()` is seen, not just the column-zero or func
		// literal forms a substring scan happens to catch.
		fset, file, err := ParseGoSource(path, src)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if stmt, ok := node.(*ast.GoStmt); ok {
				bad = append(bad, fmt.Sprintf("%s:%d: background goroutine (go statement) forbidden for conversationview",
					filepath.Base(path), fset.Position(stmt.Pos()).Line))
			}
			return true
		})
		for _, tok := range forbiddenSubstrs {
			if strings.Contains(s, tok) {
				bad = append(bad, path+": contains "+tok)
			}
		}
		// Package-level sync primitives as global locator
		if strings.Contains(s, "sync.Once") || strings.Contains(s, "sync.WaitGroup") {
			for line := range strings.SplitSeq(s, "\n") {
				trim := strings.TrimSpace(line)
				if strings.HasPrefix(trim, "var ") && (strings.Contains(trim, "Once") || strings.Contains(trim, "WaitGroup")) {
					bad = append(bad, path+": package-level sync primitive forbidden: "+trim)
				}
			}
		}
		// Forbid global mutable map/slice holding Store
		if strings.Contains(s, "var ") && (strings.Contains(s, "Store") || strings.Contains(s, "conversationview.Store")) && strings.Contains(s, "map[") {
			for line := range strings.SplitSeq(s, "\n") {
				trim := strings.TrimSpace(line)
				if strings.HasPrefix(trim, "var ") && strings.Contains(trim, "Store") {
					bad = append(bad, path+": global Store var forbidden: "+trim)
				}
			}
		}
	}
	if len(bad) != 0 {
		t.Fatalf("conversationview must not introduce watcher/background goroutine/service-locator:\n%s", strings.Join(bad, "\n"))
	}
}

// TestConversationViewBaseContinuityStoresUnchanged proves base b2bua.Store and public
// pkg/aipsdk/continuity.Store remain narrow and unchanged (Req 2.11, 13.18).
// It asserts that Store interface does not contain conversation-view methods.
func TestConversationViewBaseContinuityStoresUnchanged(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	// Check internal/core/b2bua/store.go defines Store with exactly the base methods (no Snapshot etc.)
	path := filepath.Join(root, "internal", "core", "b2bua", "store.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Use ParseGoSource helper from archtest
	_, f, err := ParseGoSource(path, src)
	if err != nil {
		t.Fatal(err)
	}
	var storeMethods []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Store" {
			return true
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}
		for _, m := range iface.Methods.List {
			if len(m.Names) > 0 {
				for _, name := range m.Names {
					storeMethods = append(storeMethods, name.Name)
				}
			}
		}
		return false
	})
	expected := map[string]bool{
		"ResolveALeg": true, "CreateALeg": true, "FetchALeg": true, "SetWeightedFirstConsumed": true,
		"NextBLeg": true, "RecordAttempt": true, "LoadAttempts": true,
	}
	if len(storeMethods) != len(expected) {
		t.Fatalf("b2bua.Store method count %d want %d, got %v", len(storeMethods), len(expected), storeMethods)
	}
	for _, m := range storeMethods {
		if !expected[m] {
			t.Fatalf("b2bua.Store contains unexpected method %q (widening forbidden), methods %v", m, storeMethods)
		}
	}
	// Also check pkg/aipsdk/continuity/store.go remains narrow
	path2 := filepath.Join(root, "pkg", "aipsdk", "continuity", "store.go")
	src2, err := os.ReadFile(path2)
	if err != nil {
		t.Fatal(err)
	}
	_, f2, err := ParseGoSource(path2, src2)
	if err != nil {
		t.Fatal(err)
	}
	var pubMethods []string
	ast.Inspect(f2, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Store" {
			return true
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			return true
		}
		for _, m := range iface.Methods.List {
			if len(m.Names) > 0 {
				for _, name := range m.Names {
					pubMethods = append(pubMethods, name.Name)
				}
			}
		}
		return false
	})
	if len(pubMethods) != len(expected) {
		t.Fatalf("pkg/aipsdk/continuity.Store method count %d want %d, got %v", len(pubMethods), len(expected), pubMethods)
	}
	for _, m := range pubMethods {
		if !expected[m] {
			t.Fatalf("pkg/aipsdk/continuity.Store contains unexpected method %q", m)
		}
	}
	// Ensure b2bua does not implement or expose conversation-view capability
	b2buaDir := filepath.Join(root, "internal", "core", "b2bua")
	_ = filepath.Walk(b2buaDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "ConversationViewStore()") {
			t.Fatalf("b2bua must not expose ConversationViewStore; ownership belongs to featurehost")
		}
		return nil
	})
	// Ensure featurehost exposes ConversationStore()
	fhDir := filepath.Join(root, "internal", "standardplugins", "featurehost")
	hasFeatureHostAccessor := false
	_ = filepath.Walk(fhDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "ConversationStore()") {
			hasFeatureHostAccessor = true
		}
		return nil
	})
	if !hasFeatureHostAccessor {
		t.Fatalf("featurehost must expose ConversationStore() accessor")
	}
}

// TestConversationViewChangedGoFileCountUnderGate bounds committed Go changes
// against origin/main, or an explicit local stacked-slice predecessor (DEV_BASE).
func TestConversationViewChangedGoFileCountUnderGate(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	changed, err := countChangedGoFiles(root)
	if err != nil {
		// Keep explicit local-base policy errors and broken Git repositories
		// fail closed. A fresh public export may lack the default remote base.
		if _, explicit := os.LookupEnv("DEV_BASE"); !explicit && opensource.IsOpenSourceTree(root) {
			if _, headErr := runCmd(root, "git", "rev-parse", "--verify", "HEAD^{commit}"); headErr == nil {
				skipMissingPublicBaseline(t, root, "origin/main")
			}
		}
		t.Fatalf("git diff count unavailable: %v", err)
	}
	if changed > 100 && !largeChangeOverrideEnabled(root) {
		t.Fatalf("changed *.go files %d exceeds gate 100 (keep diff reviewable)", changed)
	}
}

func largeChangeOverrideEnabled(root string) bool {
	if isTruthyOverride(os.Getenv("AIP_ALLOW_LARGE_CHANGE")) {
		return true
	}
	out, err := runCmd(root, "git", "config", "--bool", "--get", "aip.allowLargeChange")
	return err == nil && isTruthyOverride(string(out))
}

func isTruthyOverride(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func countChangedGoFiles(root string) (int, error) {
	return countChangedViaGit(root)
}

func countChangedViaGit(root string) (int, error) {
	base := "origin/main"
	if localBase, explicit := os.LookupEnv("DEV_BASE"); explicit {
		// This is local slice evidence only. Public CI retains its aggregate
		// comparison and the separate PR preflight's actual PR-base check.
		if os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "" {
			return 0, fmt.Errorf("DEV_BASE is local-only; unset it in CI")
		}
		if strings.TrimSpace(localBase) == "" {
			return 0, fmt.Errorf("DEV_BASE must name a commit; unset it for origin/main")
		}
		out, err := runCmd(root, "git", "rev-parse", "--verify", "--end-of-options", localBase+"^{commit}")
		if err != nil {
			return 0, fmt.Errorf("resolve DEV_BASE %q: %w", localBase, err)
		}
		base = strings.TrimSpace(string(out))
		if _, err := runCmd(root, "git", "merge-base", "--is-ancestor", base, "HEAD"); err != nil {
			return 0, fmt.Errorf("DEV_BASE %q must be an ancestor of HEAD: %w", localBase, err)
		}
	}
	out, err := runCmd(root, "git", "diff", "--name-only", "-z", base+"...HEAD", "--", "*.go")
	if err != nil {
		return 0, err
	}
	paths := strings.Split(string(out), "\x00")
	cnt := 0
	for _, path := range paths {
		if strings.HasSuffix(path, ".go") {
			cnt++
		}
	}
	return cnt, nil
}

func runCmd(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	// dir selects the repository to query. Git exports GIT_DIR to every hook it
	// runs, so an inherited GIT_DIR would answer for the ambient repository.
	cmd.Env = gitscope.Environ()
	return cmd.Output()
}

// TestConversationProjectionKernelPurity_ExcludedSymbols enforces Requirement 4.1-4.2 and Design §7:
// TagRequest, TagResult, StoredPlacement, SteeringOverlay, steering bounds/default policies,
// and Observer.OnSteeringMutation must NOT remain in internal/core/conversationprojection.
func TestConversationProjectionKernelPurity_ExcludedSymbols(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "core", "conversationprojection")

	forbiddenSymbols := []string{
		"TagRequest",
		"TagResult",
		"StoredPlacement",
		"SteeringOverlay",
		"MaxNeverBackendTags",
		"MaxActiveOverlays",
		"MaxSteeringTextBytes",
		"MaxTotalSteeringBytes",
		"MaxReasonCodeBytes",
		"MaxOverlayIDBytes",
		"MaxALegIDBytes",
		"OnSteeringMutation",
	}

	var findings []string
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
		_, f, err := ParseGoSource(path, src)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.TypeSpec:
				if node.Name != nil {
					for _, sym := range forbiddenSymbols {
						if node.Name.Name == sym {
							findings = append(findings, fmt.Sprintf("%s: forbidden type declaration %s", filepath.Base(path), sym))
						}
					}
				}
			case *ast.ValueSpec:
				for _, id := range node.Names {
					if id != nil {
						for _, sym := range forbiddenSymbols {
							if id.Name == sym {
								findings = append(findings, fmt.Sprintf("%s: forbidden const/var declaration %s", filepath.Base(path), sym))
							}
						}
					}
				}
			case *ast.Field: // interface method in Observer
				for _, id := range node.Names {
					if id != nil {
						for _, sym := range forbiddenSymbols {
							if id.Name == sym {
								findings = append(findings, fmt.Sprintf("%s: forbidden interface method %s", filepath.Base(path), sym))
							}
						}
					}
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	if len(findings) > 0 {
		t.Fatalf("conversationprojection contains forbidden symbols (must move to infra/conversationview):\n%s", strings.Join(findings, "\n"))
	}
}
