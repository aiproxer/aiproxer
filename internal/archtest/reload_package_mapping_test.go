package archtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ReloadPackageMapping records the approved package/import ownership map for
// versioned-runtime-reloadable-proxy-configuration Phase 1 (task 1.1).
// Later phases add packages under these owners without moving core boundaries.
var ReloadPackageMapping = []struct {
	Package string
	Owner   string
	Role    string
}{
	{Package: "internal/core/config", Owner: "core", Role: "typed config decode; no signal/watcher driving adapters"},
	{Package: "internal/core/configreload", Owner: "core", Role: "reloadability policy (future); no filesystem/signal imports"},
	{Package: "internal/infra/configsource", Owner: "infra", Role: "filesystem stable-source adapter (future)"},
	{Package: "internal/infra/runtimebundle", Owner: "composition", Role: "process services + generation compile root"},
	{Package: "internal/infra/runtimehost", Owner: "composition", Role: "generation manager/dispatcher/coordinator (future)"},
	{Package: "internal/stdhttp", Owner: "driving", Role: "data-plane HTTP; management adapter (future)"},
	{Package: "cmd/aipstd", Owner: "composition", Role: "CLI + signal adapter (future)"},
	{Package: "pkg/aipruntime", Owner: "public", Role: "stable facade; no internal types exposed"},
}

// reloadCoreForbiddenImportSubstrs is the mechanical core import deny-list for
// reload driving adapters, watchers, and signal/filesystem wrappers.
// Paths checked via go/list Deps must be module-path unique; syscall and
// golang.org/x/sys/unix are enforced as direct imports only (they appear
// transitively via unrelated dependencies).
var reloadCoreForbiddenImportSubstrs = []string{
	"/internal/infra/runtimebundle",
	"/internal/infra/configsource",
	"/internal/infra/runtimehost",
	"/internal/stdhttp",
	"/internal/stdhttp/management",
	"/internal/plugins/",
	"os/signal",
	"fsnotify",
	"github.com/rjeczalik/notify",
}

var reloadCoreForbiddenDirectImports = []string{
	"os/signal",
	"fsnotify",
	"syscall",
	"golang.org/x/sys/unix",
	"rjeczalik/notify",
	"configsource",
	"runtimehost",
}

// reloadCoreWrapperPathMarkers are path segments that identify signal/watcher/
// filesystem/reload-driving wrapper adapters. Live core import scans must apply
// these via coreImportForbiddenByReloadMap (not static-list-only checks).
var reloadCoreWrapperPathMarkers = []string{
	"fswrap",
	"signalwrap",
	"filesignal",
	"watchutil",
}

func TestReloadPackageMapping_ForbiddenListCoversDrivingAdapters(t *testing.T) {
	t.Parallel()
	requiredDeps := []string{
		"/internal/infra/configsource",
		"/internal/infra/runtimehost",
		"/internal/stdhttp/management",
		"github.com/rjeczalik/notify",
	}
	requiredDirect := []string{
		"syscall",
		"golang.org/x/sys/unix",
	}
	haveDeps := map[string]bool{}
	for _, s := range reloadCoreForbiddenImportSubstrs {
		haveDeps[s] = true
	}
	haveDirect := map[string]bool{}
	for _, s := range reloadCoreForbiddenDirectImports {
		haveDirect[s] = true
	}
	var missing []string
	for _, r := range requiredDeps {
		if !haveDeps[r] {
			missing = append(missing, r)
		}
	}
	for _, r := range requiredDirect {
		if !haveDirect[r] {
			missing = append(missing, r+" (direct)")
		}
	}
	if len(missing) != 0 {
		t.Fatalf("reload core forbidden import list missing driving adapters: %v", missing)
	}
	// Wrapper markers must be enforced by the centralized predicate (Finding A).
	for _, marker := range reloadCoreWrapperPathMarkers {
		imp := "github.com/aiproxer/aiproxer/internal/infra/" + marker
		if !coreImportForbiddenByReloadMap(imp, nil) {
			missing = append(missing, "predicate:"+marker)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("reload core forbidden predicate missing wrapper markers: %v", missing)
	}
}

func TestReloadPackageMapping_RejectsDrivingAdapterImports(t *testing.T) {
	t.Parallel()
	forbidden := append([]string{}, reloadCoreForbiddenImportSubstrs...)
	forbidden = append(forbidden, reloadCoreForbiddenDirectImports...)
	simulatedCoreImports := []string{
		"github.com/aiproxer/aiproxer/internal/infra/configsource",
		"github.com/aiproxer/aiproxer/internal/infra/runtimehost",
		"github.com/aiproxer/aiproxer/internal/stdhttp/management",
		"github.com/aiproxer/aiproxer/internal/infra/fswrap",
		"github.com/aiproxer/aiproxer/internal/infra/signalwrap",
		"github.com/aiproxer/aiproxer/internal/infra/filesignal",
		"github.com/aiproxer/aiproxer/internal/infra/watchutil",
		"github.com/fsnotify/fsnotify",
		"github.com/rjeczalik/notify",
		"syscall",
		"golang.org/x/sys/unix",
		"os/signal",
	}
	var hits []string
	for _, imp := range simulatedCoreImports {
		if coreImportForbiddenByReloadMap(imp, forbidden) {
			hits = append(hits, imp)
		}
	}
	if len(hits) != len(simulatedCoreImports) {
		t.Fatalf("expected all wrapper/signal/filesystem/reload adapter imports rejected, got %d/%d hits=%v",
			len(hits), len(simulatedCoreImports), hits)
	}
}

// coreImportForbiddenByReloadMap is the centralized reload forbidden-dependency
// predicate. Live core-tree import walks and fixture proofs must call this exact
// function (Finding A): static substring lists alone miss wrapper-shaped paths.
func coreImportForbiddenByReloadMap(importPath string, forbiddenSubstr []string) bool {
	for _, sub := range forbiddenSubstr {
		if sub != "" && strings.Contains(importPath, sub) {
			return true
		}
	}
	if isReloadDrivingWrapperImport(importPath) {
		return true
	}
	if strings.Contains(importPath, "/internal/stdhttp") &&
		(strings.Contains(importPath, "/management") || strings.Contains(importPath, "/reload") ||
			importPath == "github.com/aiproxer/aiproxer/internal/stdhttp" ||
			strings.HasSuffix(importPath, "/internal/stdhttp")) {
		return true
	}
	return false
}

// isReloadDrivingWrapperImport reports internal wrapper/adapter paths whose
// semantic or path segments indicate filesystem, signal, watcher, notify, or
// reload-driving behavior.
func isReloadDrivingWrapperImport(importPath string) bool {
	if importPath == "" {
		return false
	}
	if !strings.Contains(importPath, "/internal/") && !strings.HasPrefix(importPath, "internal/") {
		// Still catch well-known external watcher libraries by path segment.
		if strings.Contains(importPath, "fsnotify") || strings.Contains(importPath, "rjeczalik/notify") {
			return true
		}
		return false
	}
	lower := strings.ToLower(importPath)
	for _, marker := range reloadCoreWrapperPathMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	// Reasonable variants: path segments for watcher/notify/signal/fs driving adapters.
	segs := strings.SplitSeq(lower, "/")
	for seg := range segs {
		switch seg {
		case "watcher", "watchers", "notify", "filesignal", "fswatch", "sighup", "signalwrap", "fswrap", "watchutil":
			return true
		}
		if strings.Contains(seg, "watcher") || strings.Contains(seg, "fswrap") ||
			strings.Contains(seg, "signalwrap") || strings.Contains(seg, "filesignal") ||
			strings.Contains(seg, "watchutil") {
			return true
		}
	}
	if strings.Contains(lower, "/internal/infra/") {
		for _, needle := range []string{"filesystem", "filesignal", "fswatch", "sighandler", "reloaddrive"} {
			if strings.Contains(lower, needle) {
				return true
			}
		}
	}
	return false
}

// filterForbiddenCoreImports applies coreImportForbiddenByReloadMap to each import.
// direct controls whether direct-only substrings (syscall, x/sys/unix) are included.
func filterForbiddenCoreImports(imports []string, direct bool) []string {
	forbidden := append([]string{}, reloadCoreForbiddenImportSubstrs...)
	if direct {
		forbidden = append(forbidden, reloadCoreForbiddenDirectImports...)
	}
	var hits []string
	for _, imp := range imports {
		if coreImportForbiddenByReloadMap(imp, forbidden) {
			hits = append(hits, imp)
		}
	}
	return hits
}

func TestReloadPackageMapping_WrapperFixtureUsesSamePredicateAsLiveScan(t *testing.T) {
	t.Parallel()
	// Prove static substring lists alone miss wrapper-shaped imports (the live-scan hole).
	staticOnly := append([]string{}, reloadCoreForbiddenImportSubstrs...)
	staticOnly = append(staticOnly, reloadCoreForbiddenDirectImports...)
	wrapper := "github.com/aiproxer/aiproxer/internal/infra/fswrap"
	staticHit := false
	for _, sub := range staticOnly {
		if strings.Contains(wrapper, sub) {
			staticHit = true
			break
		}
	}
	if staticHit {
		t.Fatal("fixture assumption broken: static forbidden lists unexpectedly cover fswrap")
	}
	hits := filterForbiddenCoreImports([]string{wrapper}, true)
	if len(hits) != 1 || hits[0] != wrapper {
		t.Fatalf("central predicate used by live scan must reject wrapper-shaped import %q, got hits=%v", wrapper, hits)
	}
}

func TestReloadPackageMapping_LiveCoreImportWalkAppliesForbiddenPredicate(t *testing.T) {
	t.Parallel()
	assertLiveCoreImportsPassReloadForbiddenPredicate(t)
}

func assertLiveCoreImportsPassReloadForbiddenPredicate(t *testing.T) {
	t.Helper()
	live := enumerateLiveCoreImports(t)
	if len(live.Direct) == 0 {
		t.Fatal("live core direct import enumeration returned empty set")
	}
	if len(live.Transitive) == 0 {
		t.Fatal("live core transitive import enumeration returned empty set")
	}

	var bad []string
	directForbidden := append(append([]string{}, reloadCoreForbiddenImportSubstrs...), reloadCoreForbiddenDirectImports...)
	for imp, importers := range live.Direct {
		if !coreImportForbiddenByReloadMap(imp, directForbidden) {
			continue
		}
		bad = append(bad, fmt.Sprintf("direct %s (from %s)", imp, strings.Join(importers, ", ")))
	}
	// Transitive walk uses the same predicate without direct-only substrings
	// (syscall / golang.org/x/sys/unix appear via unrelated stdlib dependencies).
	for dep := range live.Transitive {
		if coreImportForbiddenByReloadMap(dep, reloadCoreForbiddenImportSubstrs) {
			bad = append(bad, "transitive "+dep)
		}
	}
	if len(bad) != 0 {
		t.Fatalf("internal/core live import walk found reload-forbidden dependencies:\n%s", strings.Join(bad, "\n"))
	}

	// Brownfield filesystem imports (os/path/filepath/embed) must not be falsely
	// rejected by the reload predicate; they remain gated by the exact allowlist
	// in TestProcessService_CoreFilesystemImportAllowlist.
	for _, fsImp := range []string{"os", "path/filepath", "embed", "io/fs"} {
		if coreImportForbiddenByReloadMap(fsImp, directForbidden) {
			t.Fatalf("reload predicate must not reject brownfield filesystem import %q", fsImp)
		}
	}
}

type liveCoreImportSet struct {
	Direct     map[string][]string // import path -> core packages that import it directly
	Transitive map[string]bool     // dependency ImportPath set from go list -deps
}

func enumerateLiveCoreImports(t *testing.T) liveCoreImportSet {
	t.Helper()
	out := liveCoreImportSet{
		Direct:     map[string][]string{},
		Transitive: map[string]bool{},
	}

	directOut, err := cachedGoList(t, "-test=false", "-json", "./internal/core/...")
	if err != nil {
		t.Fatalf("go list direct core imports: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(directOut))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode direct go list: %v", err)
		}
		for _, imp := range pkg.Imports {
			out.Direct[imp] = append(out.Direct[imp], pkg.ImportPath)
		}
	}

	depsOut, err := cachedGoList(t, "-deps", "-test=false", "-json", "./internal/core/...")
	if err != nil {
		t.Fatalf("go list transitive core deps: %v", err)
	}
	dec = json.NewDecoder(bytes.NewReader(depsOut))
	for dec.More() {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			t.Fatalf("decode deps go list: %v", err)
		}
		if pkg.ImportPath == "" {
			continue
		}
		// Mirror assertDepsExcludeForbidden: stdlib paths (os/signal, syscall, …)
		// are enforced on direct imports only; they appear transitively via unrelated deps.
		if pkg.Standard {
			continue
		}
		out.Transitive[pkg.ImportPath] = true
	}
	return out
}

func TestReloadPackageMapping_CoreBoundaryCommitments(t *testing.T) {
	t.Parallel()
	if len(ReloadPackageMapping) == 0 {
		t.Fatal("ReloadPackageMapping must not be empty")
	}
	for _, e := range ReloadPackageMapping {
		if e.Package == "" || e.Owner == "" || e.Role == "" {
			t.Fatalf("incomplete ReloadPackageMapping entry: %+v", e)
		}
	}
	// Live production-tree walk applies the centralized predicate (not static-only).
	assertLiveCoreImportsPassReloadForbiddenPredicate(t)

	// Retain legacy go/list substring gates for the static deny-list entries.
	var rules []forbiddenDep
	for _, sub := range reloadCoreForbiddenImportSubstrs {
		switch {
		case strings.HasPrefix(sub, "/internal/") || strings.Contains(sub, "fsnotify") ||
			strings.Contains(sub, "notify") || sub == "os/signal" || sub == "syscall" ||
			strings.Contains(sub, "golang.org/x/sys/unix"):
			rules = append(rules, forbiddenDep{
				Substr: sub,
				ErrMsg: "internal/core must not import " + sub,
			})
		}
	}
	assertDepsExcludeForbidden(t, []string{"./internal/core/..."}, rules)
	for _, sub := range reloadCoreForbiddenDirectImports {
		assertDirectImportsExclude(t, "./internal/core/...", sub,
			"internal/core must not directly import "+sub)
	}
	// Filesystem imports are gated by TestProcessService_CoreFilesystemImportAllowlist
	// (exact temporary baseline for known production imports).
}
