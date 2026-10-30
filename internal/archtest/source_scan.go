package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ProductionScanRoots are the top-level trees walked for production guardrails.
func ProductionScanRoots() []string {
	return []string{"cmd", "internal", "pkg"}
}

// FindRepoRoot walks upward from dir until go.mod is found.
func FindRepoRoot(dir string) (string, error) {
	cur := dir
	for range 16 {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return "", os.ErrNotExist
}

// SlashPath normalizes a path to forward slashes.
func SlashPath(p string) string {
	return filepath.ToSlash(p)
}

// PackageDirFromRel returns the repo-relative package directory for a .go file
// (parent of the file, forward slashes).
func PackageDirFromRel(rel string) string {
	rel = SlashPath(rel)
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "." {
		return ""
	}
	return dir
}

type sourceFileEntry struct {
	rel string
	abs string
	src []byte
}

type sourceScanCacheKey struct {
	canonicalRoot string
	includeTests  bool
}

type sourceScanCacheEntry struct {
	done    chan struct{}
	entries []sourceFileEntry
	err     error
}

var (
	sourceScanCacheMu sync.Mutex
	sourceScanCache   = make(map[sourceScanCacheKey]*sourceScanCacheEntry)
)

// canonicalScanPathKey normalizes an absolute filesystem path for use as a cache key.
// On Windows, filesystem paths are case-insensitive, so the key is lowercased.
// Note: Symlinks are intentionally not evaluated with filepath.EvalSymlinks to avoid
// errors or missing files on virtual or non-existent temporary test roots.
func canonicalScanPathKey(absPath string) string {
	clean := filepath.Clean(absPath)
	if runtime.GOOS == "windows" {
		clean = strings.ToLower(clean)
	}
	return clean
}

func canonicalSourceScanRoot(root string) (string, string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	abs = filepath.Clean(abs)
	key := canonicalScanPathKey(abs)
	return abs, key, nil
}

func loadCachedSourceFiles(key sourceScanCacheKey, loader func() ([]sourceFileEntry, error)) ([]sourceFileEntry, error) {
	return loadCachedSourceFilesWithObserver(key, loader, nil)
}

func loadCachedSourceFilesWithObserver(
	key sourceScanCacheKey,
	loader func() ([]sourceFileEntry, error),
	onWait func(),
) ([]sourceFileEntry, error) {
	sourceScanCacheMu.Lock()
	entry, ok := sourceScanCache[key]
	if ok {
		sourceScanCacheMu.Unlock()
		if onWait != nil {
			onWait()
		}
		<-entry.done
		return entry.entries, entry.err
	}

	entry = &sourceScanCacheEntry{done: make(chan struct{})}
	sourceScanCache[key] = entry
	sourceScanCacheMu.Unlock()

	var (
		loaded   []sourceFileEntry
		loadErr  error
		finished bool
	)

	finalize := func(panicked bool, panicVal any) {
		if finished {
			return
		}
		finished = true

		if panicked {
			if entry.err == nil {
				entry.err = fmt.Errorf("source scan panicked: %v", panicVal)
			}
		} else {
			entry.entries = loaded
			entry.err = loadErr
		}

		sourceScanCacheMu.Lock()
		if panicked || loadErr != nil {
			if current, exists := sourceScanCache[key]; exists && current == entry {
				delete(sourceScanCache, key)
			}
		}
		close(entry.done)
		sourceScanCacheMu.Unlock()
	}

	defer func() {
		if r := recover(); r != nil {
			finalize(true, r)
			panic(r)
		}
		finalize(false, nil)
	}()

	loaded, loadErr = loader()
	return loaded, loadErr
}

func loadSourceFilesFromDisk(absRoot string, includeTests bool) ([]sourceFileEntry, error) {
	var loaded []sourceFileEntry
	for _, top := range ProductionScanRoots() {
		base := filepath.Join(absRoot, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				name := info.Name()
				if name == "vendor" || name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if !includeTests && strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(absRoot, path)
			if err != nil {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			loaded = append(loaded, sourceFileEntry{
				rel: SlashPath(rel),
				abs: path,
				src: src,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return loaded, nil
}

func loadSourceFilesWithCanonicalizer(
	root string,
	includeTests bool,
	loader func(absRoot string, includeTests bool) ([]sourceFileEntry, error),
	canonicalize func(string) (string, string, error),
) ([]sourceFileEntry, error) {
	if canonicalize == nil {
		canonicalize = canonicalSourceScanRoot
	}
	absRoot, canonicalKey, err := canonicalize(root)
	if err != nil {
		return nil, err
	}
	key := sourceScanCacheKey{
		canonicalRoot: canonicalKey,
		includeTests:  includeTests,
	}
	return loadCachedSourceFiles(key, func() ([]sourceFileEntry, error) {
		return loader(absRoot, includeTests)
	})
}

func loadSourceFilesWith(root string, includeTests bool, loader func(absRoot string, includeTests bool) ([]sourceFileEntry, error)) ([]sourceFileEntry, error) {
	return loadSourceFilesWithCanonicalizer(root, includeTests, loader, canonicalSourceScanRoot)
}

func loadSourceFiles(root string, includeTests bool) ([]sourceFileEntry, error) {
	return loadSourceFilesWith(root, includeTests, loadSourceFilesFromDisk)
}

func walkSourceFiles(root string, includeTests bool, fn func(rel, abs string, src []byte) error) error {
	entries, err := loadSourceFiles(root, includeTests)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcCopy := append([]byte(nil), entry.src...)
		if err := fn(entry.rel, entry.abs, srcCopy); err != nil {
			return err
		}
	}
	return nil
}

// WalkProductionGoFiles walks non-test .go files under ProductionScanRoots.
// For performance during multi-suite test runs, files for each root are loaded
// into an in-memory snapshot on first access. The callback receives repo-relative
// slash path, absolute path, and an isolated copy of file bytes.
func WalkProductionGoFiles(root string, fn func(rel, abs string, src []byte) error) error {
	return walkSourceFiles(root, false, fn)
}

// WalkGoFiles walks all .go files (including _test.go) under ProductionScanRoots.
// For performance during multi-suite test runs, files for each root are loaded
// into an in-memory snapshot on first access. The callback receives repo-relative
// slash path, absolute path, and an isolated copy of file bytes.
func WalkGoFiles(root string, fn func(rel, abs string, src []byte) error) error {
	return walkSourceFiles(root, true, fn)
}

// WalkAllGoFiles is an alias for WalkGoFiles.
func WalkAllGoFiles(root string, fn func(rel, abs string, src []byte) error) error {
	return WalkGoFiles(root, fn)
}

// ParseGoSource parses src with SkipObjectResolution and ParseComments.
func ParseGoSource(filename string, src []byte) (*token.FileSet, *ast.File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, err
	}
	return fset, f, nil
}

// FileImportPaths returns the import paths declared in f.
func FileImportPaths(f *ast.File) []string {
	var out []string
	for _, imp := range f.Imports {
		if imp.Path == nil {
			continue
		}
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	return out
}

// MatchPathPrefix reports whether path equals prefix or is under prefix/.
func MatchPathPrefix(path, prefix string) bool {
	path = SlashPath(path)
	prefix = SlashPath(prefix)
	if prefix == "" || prefix == "*" {
		return true
	}
	if before, ok := strings.CutSuffix(prefix, "/**"); ok {
		base := before
		return path == base || strings.HasPrefix(path, base+"/")
	}
	if before, ok := strings.CutSuffix(prefix, "/*"); ok {
		base := before
		if path == base {
			return false
		}
		rest := strings.TrimPrefix(path, base+"/")
		return strings.HasPrefix(path, base+"/") && !strings.Contains(rest, "/")
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// MatchImportPattern reports whether importPath matches a simple pattern.
// Patterns may be exact paths, suffix "/name", or contain a substring marker.
func MatchImportPattern(importPath, pattern string) bool {
	if pattern == "" {
		return false
	}
	if strings.HasPrefix(pattern, "*/") {
		return strings.HasSuffix(importPath, pattern[1:]) || strings.Contains(importPath, pattern[1:])
	}
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		return strings.Contains(importPath, strings.Trim(pattern, "*"))
	}
	if after, ok := strings.CutPrefix(pattern, "*"); ok {
		return strings.HasSuffix(importPath, after)
	}
	if before, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(importPath, before)
	}
	return importPath == pattern || strings.HasSuffix(importPath, "/"+pattern) || strings.Contains(importPath, pattern)
}

// Unexported aliases keep existing structural tests compiling against the shared scanner.
func productionScanRoots() []string { return ProductionScanRoots() }

func walkProductionGoFiles(root string, fn func(rel, abs string, src []byte) error) error {
	return WalkProductionGoFiles(root, fn)
}
func slashPath(p string) string { return SlashPath(p) }
func parseGoSource(filename string, src string) (*token.FileSet, *ast.File, error) {
	return ParseGoSource(filename, []byte(src))
}
func countFileLines(path string) (int, error)     { return CountFileLines(path) }
func countNonTestGoLines(dir string) (int, error) { return CountNonTestGoLines(dir) }
