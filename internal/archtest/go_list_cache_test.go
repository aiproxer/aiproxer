package archtest

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

type goListCacheEntry struct {
	done chan struct{}
	out  []byte
	err  error
}

type packageJSONBlock struct {
	importPath string
	relDir     string
	deps       []string
	raw        []byte
}

var (
	goListCacheMu   sync.Mutex
	goListCache     = make(map[string]*goListCacheEntry)
	moduleListOnce  sync.Once
	modulePackages  []packageJSONBlock
	modulePkgByPath map[string]packageJSONBlock
	moduleListErr   error
	moduleRootPath  string
)

// goListCacheKey canonicalizes equivalent `go list` argument lists into a
// single key. The leading flags used by these tests are order-independent, so
// they are sorted while positional package/query arguments remain untouched.
func goListCacheKey(args []string) string {
	firstPositional := 0
	for firstPositional < len(args) && strings.HasPrefix(args[firstPositional], "-") {
		firstPositional++
	}
	flags := append([]string(nil), args[:firstPositional]...)
	sort.Strings(flags)
	return strings.Join(append(flags, args[firstPositional:]...), "\x00")
}

func loadModulePackages(t *testing.T) ([]packageJSONBlock, map[string]packageJSONBlock, error) {
	t.Helper()
	moduleListOnce.Do(func() {
		moduleRootPath = repoRoot(t)
		cmd := exec.CommandContext(t.Context(), "go", "list", "-json", "-deps", "-test=false", "./...")
		cmd.Dir = moduleRootPath
		out, err := cmd.Output()
		if err != nil {
			moduleListErr = err
			return
		}
		modulePkgByPath = make(map[string]packageJSONBlock)
		dec := json.NewDecoder(bytes.NewReader(out))
		for dec.More() {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				moduleListErr = err
				return
			}
			var meta struct {
				ImportPath string   `json:"ImportPath"`
				Dir        string   `json:"Dir"`
				Deps       []string `json:"Deps"`
			}
			if err := json.Unmarshal(raw, &meta); err != nil {
				moduleListErr = err
				return
			}
			rel, err := filepath.Rel(moduleRootPath, meta.Dir)
			if err != nil {
				rel = meta.Dir
			}
			rel = filepath.ToSlash(rel)
			block := packageJSONBlock{
				importPath: meta.ImportPath,
				relDir:     rel,
				deps:       meta.Deps,
				raw:        raw,
			}
			modulePackages = append(modulePackages, block)
			modulePkgByPath[meta.ImportPath] = block
		}
	})
	return modulePackages, modulePkgByPath, moduleListErr
}

func matchesPattern(pkg packageJSONBlock, pattern string) bool {
	norm := filepath.ToSlash(strings.TrimPrefix(pattern, "./"))
	if norm == "..." || pattern == "./..." {
		return true
	}
	if prefix, ok := strings.CutSuffix(norm, "/..."); ok {
		if pkg.relDir == prefix || strings.HasPrefix(pkg.relDir, prefix+"/") {
			return true
		}
		if strings.HasSuffix(pkg.importPath, "/"+prefix) || strings.Contains(pkg.importPath, "/"+prefix+"/") {
			return true
		}
		return false
	}
	// Exact match by relative directory or import path.
	if pkg.relDir == norm {
		return true
	}
	if pkg.importPath == norm || strings.HasSuffix(pkg.importPath, "/"+norm) {
		return true
	}
	return false
}

func trySliceFromModuleCache(t *testing.T, args []string) ([]byte, bool, error) {
	t.Helper()
	firstPositional := 0
	hasDeps := false
	hasJSON := false
	for firstPositional < len(args) && strings.HasPrefix(args[firstPositional], "-") {
		f := args[firstPositional]
		if f == "-deps" {
			hasDeps = true
		}
		if f == "-json" {
			hasJSON = true
		}
		if f != "-json" && f != "-test=false" && f != "-e" && f != "-deps" {
			return nil, false, nil
		}
		firstPositional++
	}
	if !hasJSON {
		return nil, false, nil
	}

	positionals := args[firstPositional:]
	if len(positionals) == 0 {
		return nil, false, nil
	}

	for _, p := range positionals {
		if strings.HasPrefix(p, "-") {
			return nil, false, nil
		}
	}

	pkgs, pkgByPath, err := loadModulePackages(t)
	if err != nil {
		return nil, false, err
	}

	var buf bytes.Buffer
	seen := make(map[string]bool)
	for _, p := range positionals {
		for _, pkg := range pkgs {
			if matchesPattern(pkg, p) {
				if !seen[pkg.importPath] {
					seen[pkg.importPath] = true
					buf.Write(pkg.raw)
					buf.WriteByte('\n')
				}
				if hasDeps {
					for _, d := range pkg.deps {
						if !seen[d] {
							seen[d] = true
							if depBlock, ok := pkgByPath[d]; ok {
								buf.Write(depBlock.raw)
								buf.WriteByte('\n')
							} else {
								synthetic, _ := json.Marshal(map[string]any{
									"ImportPath": d,
									"Standard":   !strings.Contains(d, "."),
								})
								buf.Write(synthetic)
								buf.WriteByte('\n')
							}
						}
					}
				}
			}
		}
	}
	return buf.Bytes(), true, nil
}

func TestGoListCacheKeyCanonicalizesLeadingFlagsOnly(t *testing.T) {
	t.Parallel()

	first := goListCacheKey([]string{"-json", "-test=false", "./internal/core/..."})
	reorderedFlags := goListCacheKey([]string{"-test=false", "-json", "./internal/core/..."})
	if first != reorderedFlags {
		t.Fatalf("equivalent leading flags produced different keys: %q != %q", first, reorderedFlags)
	}

	queriesAB := goListCacheKey([]string{"-json", "./internal/core/...", "./internal/plugins/..."})
	queriesBA := goListCacheKey([]string{"-json", "./internal/plugins/...", "./internal/core/..."})
	if queriesAB == queriesBA {
		t.Fatalf("positional query order was lost: %q", queriesAB)
	}

	flagBeforeQuery := goListCacheKey([]string{"-json", "-test=false", "./internal/core/..."})
	flagAfterQuery := goListCacheKey([]string{"-json", "./internal/core/...", "-test=false"})
	if flagBeforeQuery == flagAfterQuery {
		t.Fatalf("argument after a positional query was incorrectly canonicalized: %q", flagBeforeQuery)
	}
}

func TestCachedGoListSlicesModuleScan(t *testing.T) {
	t.Parallel()

	out, err := cachedGoList(t, "-json", "-test=false", "./pkg/aipsdk/auth")
	if err != nil {
		t.Fatalf("cachedGoList error: %v", err)
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	var pkgs []string
	for dec.More() {
		var pkg struct {
			ImportPath string `json:"ImportPath"`
		}
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		pkgs = append(pkgs, pkg.ImportPath)
	}
	if len(pkgs) != 1 || !strings.Contains(pkgs[0], "pkg/aipsdk/auth") {
		t.Fatalf("expected single auth package, got: %v", pkgs)
	}
}

// cachedGoList coalesces identical package-graph queries made by parallel
// architecture tests. The go command cache does not avoid subprocess startup
// and graph loading, which dominate this package's runtime on Windows.
func cachedGoList(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()

	if out, ok, err := trySliceFromModuleCache(t, args); ok {
		return out, err
	}

	key := goListCacheKey(args)

	goListCacheMu.Lock()
	if entry, ok := goListCache[key]; ok {
		goListCacheMu.Unlock()
		select {
		case <-entry.done:
			return entry.out, entry.err
		case <-t.Context().Done():
			return nil, t.Context().Err()
		}
	}
	entry := &goListCacheEntry{done: make(chan struct{})}
	goListCache[key] = entry
	// Release waiters even if the producer below terminates early (t.Fatal,
	// Goexit, or a panic); otherwise same-key callers would block forever.
	defer close(entry.done)
	goListCacheMu.Unlock()

	cmd := exec.CommandContext(t.Context(), "go", append([]string{"list"}, args...)...)
	cmd.Dir = repoRoot(t)
	entry.out, entry.err = cmd.Output()
	return entry.out, entry.err
}

func TestCachedGoListWithDepsReturnsDependencies(t *testing.T) {
	t.Parallel()

	out, err := cachedGoList(t, "-deps", "-test=false", "-json", "./pkg/aipsdk/auth")
	if err != nil {
		t.Fatalf("cachedGoList error: %v", err)
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	foundAuth := false
	foundContext := false
	for dec.More() {
		var pkg struct {
			ImportPath string `json:"ImportPath"`
			DepOnly    bool   `json:"DepOnly"`
		}
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if strings.Contains(pkg.ImportPath, "pkg/aipsdk/auth") {
			foundAuth = true
			if pkg.DepOnly {
				t.Errorf("primary target %s must have DepOnly=false", pkg.ImportPath)
			}
		}
		if pkg.ImportPath == "context" {
			foundContext = true
			if !pkg.DepOnly {
				t.Errorf("dependency context must have DepOnly=true")
			}
		}
	}
	if !foundAuth {
		t.Fatalf("expected pkg/aipsdk/auth in output")
	}
	if !foundContext {
		t.Fatalf("expected context dependency in output")
	}
}

func TestWalkProductionGoFiles_MutationIsolated(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)

	var firstBytes []byte
	var targetRel string
	err := WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if targetRel == "" && len(src) > 5 {
			targetRel = rel
			firstBytes = append([]byte(nil), src...)
			// Mutate callback slice in place
			src[0] = 0xFF
			src[1] = 0xFE
		}
		return nil
	})
	if err != nil {
		t.Fatalf("first walk: %v", err)
	}

	var secondBytes []byte
	err = WalkProductionGoFiles(root, func(rel, abs string, src []byte) error {
		if rel == targetRel {
			secondBytes = append([]byte(nil), src...)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("second walk: %v", err)
	}

	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("WalkProductionGoFiles cache was mutated across runs for %s", targetRel)
	}
}
