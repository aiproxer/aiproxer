package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// nodeToolNames is the closed set of Node-family entry points the host build
// surface must not be able to invoke. It covers the package managers and
// version managers that can materialise a runtime on demand, because a lane that
// only blocked `node` would still let `npx`/`corepack` fetch one.
var nodeToolNames = []string{
	"bun", "corepack", "deno", "fnm", "node", "nodejs", "nodenv",
	"npm", "npx", "nvm", "pnpm", "volta", "yarn",
}

// nodeToolNameSet is the lookup form of nodeToolNames.
var nodeToolNameSet = func() map[string]bool {
	set := make(map[string]bool, len(nodeToolNames))
	for _, name := range nodeToolNames {
		set[name] = true
	}
	return set
}()

// The sweep is scoped, not exhaustive, and the scope is recorded in the report
// so the evidence never overstates what was checked.
//
// Reasoning: a build command can only start a Node entry point it can name.
// Bare names come from PATH; absolute paths come from configuration, the
// repository, or the conventional install locations below. Recursively scanning
// every home directory would add unbounded cost for paths no build could ever
// construct - and on a Windows-subsystem host it crossed a million-entry
// Windows tree, taking minutes and finding nothing a build could invoke.
//
// depthBudget is the per-root recursion cap; entryBudget bounds a single root.
// A root that exceeds either is reported as incomplete and fails the lane,
// because an unbounded scan must never be mistaken for a clean result.
const (
	depthBudget  = 6
	entryBudget  = 200000
	repoDepthCap = 12
)

// systemRoots are the system-wide locations that hold Node toolchains.
var systemRoots = []string{
	"/usr/bin", "/usr/local/bin", "/usr/sbin", "/usr/local/sbin",
	"/usr/lib/nodejs", "/usr/local/lib/nodejs", "/usr/share/nodejs",
}

// deepRoots are small system trees that hold version-manager installs and need a
// bounded recursive scan rather than a single level.
var deepRoots = []string{"/opt", "/snap"}

// homeRoots are expanded per home directory: distro tarball installs, version
// managers, and the single-level bin directories they add.
var homeRoots = []string{
	".nvm", ".fnm", ".volta", ".asdf", ".bun",
	".local/bin", ".local/share/fnm", ".local/share/fnm_multishells",
	".cache/ms-playwright-go", ".cursor-server/bin",
}

// prunedPrefixes are never walked: pseudo filesystems would loop or report
// kernel noise, and /__e is the GitHub Actions private action runtime, a
// container-job mount of the runner's own Node that is neither part of the image
// nor on PATH, so counting it would make container evidence unprovable while
// proving nothing about the build environment.
//
// /tmp is deliberately absent. A runtime staged there is still reachable, and
// this run's own workdir is excluded precisely through skipDir rather than by
// pruning a whole temporary tree.
var prunedPrefixes = []string{"/proc", "/sys", "/dev", "/var/run", "/__e"}

// sweepScope records exactly what a probe observed, so the report cannot be read
// as a stronger claim than it is.
type sweepScope struct {
	// Shallow are the directories checked one level deep (PATH entries and the
	// single-level bin directories).
	Shallow []string `json:"shallow"`
	// Deep are the directories walked recursively under depthBudget/entryBudget.
	Deep []string `json:"deep"`
	// Repo is the repository root walked under repoDepthCap.
	Repo string `json:"repo"`
	// Incomplete names roots that hit a budget and were therefore not fully
	// observed. Any entry here fails the lane.
	Incomplete []string `json:"incomplete"`
	// EntriesVisited is the total number of filesystem entries examined.
	EntriesVisited int `json:"entries_visited"`
}

// probeResult is the evidence that a single observation of Node availability
// produced. It is deliberately comparable before and after isolation.
type probeResult struct {
	// ResolvedByName are executable entry points reachable through PATH
	// resolution.
	ResolvedByName []string `json:"resolved_by_name"`
	// Swept are executable entry points found in the conventional locations, in
	// the repository, or on PATH.
	Swept []string `json:"swept"`
	// Scope bounds the claim.
	Scope sweepScope `json:"scope"`
}

// Paths returns every distinct discovered entry point.
func (p probeResult) Paths() []string {
	return sortedUnique(slices.Concat(p.ResolvedByName, p.Swept))
}

// Empty reports whether the Node toolchain is unreachable at every probed path.
func (p probeResult) Empty() bool { return len(p.Paths()) == 0 }

func (p probeResult) String() string {
	if p.Empty() {
		return "none"
	}
	return strings.Join(p.Paths(), ", ")
}

// probeNodeToolchain observes Node availability without changing anything.
// skipDir is this run's own workdir, which holds the PATH tripwires.
func probeNodeToolchain(pathEnv, skipDir, repoRoot string) (probeResult, error) {
	shallow := pathDirs(pathEnv)
	deep := slices.Clone(systemRoots)
	deep = append(deep, deepRoots...)
	for _, home := range homeDirs() {
		for _, rel := range homeRoots {
			deep = append(deep, filepath.Join(home, rel))
		}
	}
	// A PATH entry is where a bare name resolves; one level is exhaustive for
	// that purpose and keeps a mounted Windows tree out of the walk entirely.
	// Symlinked entries are resolved first: WalkDir does not descend through a
	// symlinked root, and a version-manager shim directory is routinely one, so
	// skipping resolution there would silently miss real entry points.
	shallow = sortedUnique(slices.Concat(shallow, homeLevelDirs()))
	shallow = resolveDirs(shallow)

	result := probeResult{}
	for _, name := range nodeToolNames {
		if resolved, ok := lookExecutableIn(name, shallow); ok {
			result.ResolvedByName = append(result.ResolvedByName, resolved)
		}
	}

	scope := sweepScope{Shallow: shallow, Deep: deep, Repo: repoRoot}
	seen := map[string]bool{}
	for _, path := range result.ResolvedByName {
		seen[path] = true
	}
	for _, root := range shallow {
		found, visited, incomplete := scanDir(root, skipDir, 1)
		scope.EntriesVisited += visited
		if incomplete != "" {
			scope.Incomplete = append(scope.Incomplete, incomplete)
		}
		for _, path := range found {
			if !seen[path] {
				seen[path] = true
				result.Swept = append(result.Swept, path)
			}
		}
	}
	for _, root := range deep {
		found, visited, incomplete := scanDir(root, skipDir, depthBudget)
		scope.EntriesVisited += visited
		if incomplete != "" {
			scope.Incomplete = append(scope.Incomplete, incomplete)
		}
		for _, path := range found {
			if !seen[path] {
				seen[path] = true
				result.Swept = append(result.Swept, path)
			}
		}
	}
	if repoRoot != "" {
		found, visited, incomplete := scanDir(repoRoot, skipDir, repoDepthCap)
		scope.EntriesVisited += visited
		if incomplete != "" {
			scope.Incomplete = append(scope.Incomplete, incomplete)
		}
		for _, path := range found {
			if !seen[path] {
				seen[path] = true
				result.Swept = append(result.Swept, path)
			}
		}
	}
	result.Swept = sortedUnique(result.Swept)
	sortStrings(scope.Incomplete)
	return result, nil
}

// scanDir walks root up to maxDepth looking for executable Node entry points. It
// returns the matches, the number of entries examined, and the root path when the
// walk hit a budget and therefore cannot claim full coverage.
func scanDir(root, skipDir string, maxDepth int) (found []string, visited int, incomplete string) {
	root = resolveDir(root)
	if root == "" || pruned(root, skipDir) {
		return nil, 0, ""
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, 0, ""
	}
	rootDevice := deviceOf(info)
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			if pruned(path, skipDir) {
				return filepath.SkipDir
			}
			// Never cross a filesystem boundary: a mounted Windows or network tree
			// under an install root is unbounded and is not the host's toolchain.
			if !sameDevice(rootDevice, path) {
				return filepath.SkipDir
			}
			if depthFrom(root, path) > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > entryBudget {
			incomplete = root
			return filepath.SkipAll
		}
		if !nodeToolNameSet[entry.Name()] {
			return nil
		}
		if isEntryPoint(path) {
			found = append(found, path)
		}
		return nil
	})
	if walkErr != nil && incomplete == "" {
		// An unreadable subtree is not evidence of an entry point; the PATH probe
		// still covers the names.
		return found, visited, ""
	}
	return found, visited, incomplete
}

// lookExecutableIn resolves name against an explicit directory list rather than
// the ambient process environment, so the lane probes the same PATH it later
// runs commands with.
func lookExecutableIn(name string, dirs []string) (string, bool) {
	for _, dir := range dirs {
		candidate := filepath.Join(dir, name)
		if isEntryPoint(candidate) {
			return candidate, true
		}
	}
	return "", false
}

// homeDirs returns the home directories that exist on this host.
func homeDirs() []string {
	var homes []string
	if _, err := os.Stat("/root"); err == nil {
		homes = append(homes, "/root")
	}
	entries, err := os.ReadDir("/home")
	if err != nil {
		return homes
	}
	for _, entry := range entries {
		if entry.IsDir() {
			homes = append(homes, filepath.Join("/home", entry.Name()))
		}
	}
	return homes
}

// homeLevelDirs returns the single-level bin directories a home may contribute
// to PATH resolution.
func homeLevelDirs() []string {
	var dirs []string
	for _, home := range homeDirs() {
		for _, rel := range []string{".local/bin", "bin"} {
			if info, err := os.Stat(filepath.Join(home, rel)); err == nil && info.IsDir() {
				dirs = append(dirs, filepath.Join(home, rel))
			}
		}
	}
	return dirs
}

// resolveDir returns the real directory behind a possibly symlinked path. The
// entry points it contains are reported under their real path because that is
// the absolute path a process would have to open.
func resolveDir(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}

func resolveDirs(dirs []string) []string {
	resolved := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		resolved = append(resolved, resolveDir(dir))
	}
	return sortedUnique(resolved)
}

func pathDirs(pathEnv string) []string {
	var dirs []string
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir != "" && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func depthFrom(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}

// pruned reports whether a path is outside the environment the lane may probe.
func pruned(path, skipDir string) bool {
	if skipDir != "" && (path == skipDir || strings.HasPrefix(path, skipDir+string(filepath.Separator))) {
		return true
	}
	clean := filepath.Clean(path)
	for _, prefix := range prunedPrefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	var unique []string
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	sortStrings(unique)
	return unique
}

func sortStrings(values []string) { slices.Sort(values) }
