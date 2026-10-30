//go:build precommit

package qa

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/uudashr/gocognit"
	"gopkg.in/yaml.v3"
)

// The cognitive complexity ratchet.
//
// The advisory report (make lint-advisory) lists every function whose
// cognitive complexity is above the threshold configured in .golangci.yml.
// This check compares that measurement with the functions recorded in
// testdata/cognitive-complexity-baseline.json and fails only on growth: a
// recorded function whose complexity increased, or a new function above the
// threshold. Improvements (a recorded function measured below its record)
// and removals (a recorded function gone from the tree) are reported for
// human review but never fail; the record is refreshed opportunistically.
//
// The threshold and the excluded paths are read from .golangci.yml, so the
// advisory report and this ratchet always measure the same code. The metric
// comes from the same library golangci-lint's gocognit linter uses, so the
// pinned values match the reported numbers exactly.

const (
	cognitiveBaselineRelPath = "testdata/cognitive-complexity-baseline.json"
	cognitiveSchemaVersion   = 1
	cognitiveLinterName      = "gocognit"
)

var updateCognitiveBaseline = flag.Bool("update-cognitive-baseline", false,
	"rewrite the cognitive complexity baseline from the measured values")

// cognitiveEntry pins one function's measured cognitive complexity.
type cognitiveEntry struct {
	Path       string `json:"path"`
	Function   string `json:"function"`
	Complexity int    `json:"cognitive_complexity"`
}

// key identifies a function independently of its source line, so an unrelated
// edit above the function does not look like a change.
func (e cognitiveEntry) key() string {
	return e.Path + "\x00" + e.Function
}

// cognitiveBaseline is the recorded set of functions above the threshold.
type cognitiveBaseline struct {
	SchemaVersion int              `json:"schema_version"`
	Functions     []cognitiveEntry `json:"functions"`
}

// golangciConfig is the subset of .golangci.yml this check reads.
type golangciConfig struct {
	Linters struct {
		Settings struct {
			Gocognit struct {
				MinComplexity int `yaml:"min-complexity"`
			} `yaml:"gocognit"`
		} `yaml:"settings"`
	} `yaml:"linters"`
}

// golangciExcludeRule mirrors one golangci-lint exclusion rule.
type golangciExcludeRule struct {
	Path       string   `yaml:"path"`
	PathExcept string   `yaml:"path-except"`
	Text       string   `yaml:"text"`
	Linters    []string `yaml:"linters"`
}

// cognitivePolicy is the measured scope: the threshold and the rules that
// take a function out of scope.
type cognitivePolicy struct {
	threshold int
	compiled  []cognitiveExclusion
}

// cognitiveExclusion is one compiled exclusion rule. A rule without a path
// and without text matches every function, exactly like golangci-lint.
type cognitiveExclusion struct {
	path       *regexp.Regexp
	pathExcept *regexp.Regexp
	text       *regexp.Regexp
}

// excludes reports whether the rule excludes the function. The path rules
// are evaluated against the module-relative path (what golangci-lint matches)
// and the repository-relative path (what the baseline records), so a rule
// written for either form excludes the same function in both tools. The text
// rule is evaluated against the message the advisory report shows.
func (e cognitiveExclusion) excludes(paths []string, issueText string) bool {
	if e.text != nil && !e.text.MatchString(issueText) {
		return false
	}
	for _, path := range paths {
		if e.path != nil && !e.path.MatchString(path) {
			continue
		}
		if e.pathExcept != nil && e.pathExcept.MatchString(path) {
			continue
		}
		return true
	}
	return false
}

// cognitiveIssueText renders the message golangci-lint's gocognit linter
// produces, so a text-scoped exclusion rule applies to the same message the
// advisory report shows.
func cognitiveIssueText(entry cognitiveEntry, threshold int) string {
	return fmt.Sprintf("cognitive complexity %d of func `%s` is high (> %d)", entry.Complexity, entry.Function, threshold)
}

// excluded reports whether the rules take the function out of scope.
func (p cognitivePolicy) excluded(paths []string, issueText string) bool {
	for _, exclusion := range p.compiled {
		if exclusion.excludes(paths, issueText) {
			return true
		}
	}
	return false
}

// excludesFile reports whether a whole file is out of scope. Only rules that
// carry no text condition can exclude a file, because a text condition
// selects individual issues rather than files.
func (p cognitivePolicy) excludesFile(paths []string) bool {
	for _, exclusion := range p.compiled {
		if exclusion.text != nil {
			continue
		}
		if exclusion.excludes(paths, "") {
			return true
		}
	}
	return false
}

// strictGocognitExcludeRules returns the golangci-lint exclusion rules that
// apply to the gocognit linter. The rules are decoded with unknown fields
// rejected, so a rule key this check does not model (for example `source`)
// cannot make the report and the ratchet disagree without anyone noticing.
func strictGocognitExcludeRules(raw []byte) ([]golangciExcludeRule, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	linters, _ := doc["linters"].(map[string]any)
	exclusions, _ := linters["exclusions"].(map[string]any)
	allRules, _ := exclusions["rules"].([]any)
	selected := make([]any, 0, len(allRules))
	for _, rule := range allRules {
		entry, ok := rule.(map[string]any)
		if !ok {
			continue
		}
		names, _ := entry["linters"].([]any)
		for _, name := range names {
			if text, _ := name.(string); text == cognitiveLinterName {
				selected = append(selected, entry)
				break
			}
		}
	}
	if len(selected) == 0 {
		return nil, nil
	}
	encoded, err := yaml.Marshal(selected)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	var rules []golangciExcludeRule
	if err := decoder.Decode(&rules); err != nil {
		return nil, fmt.Errorf("unsupported gocognit exclusion rule: %w", err)
	}
	return rules, nil
}

// loadCognitivePolicy reads the threshold and the exclusions from the
// repository's golangci-lint configuration. It fails closed when the
// configuration no longer states a policy, so the ratchet can never silently
// measure nothing.
func loadCognitivePolicy(path string) (cognitivePolicy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return cognitivePolicy{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg golangciConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cognitivePolicy{}, fmt.Errorf("parse %s: %w", path, err)
	}
	policy := cognitivePolicy{threshold: cfg.Linters.Settings.Gocognit.MinComplexity}
	if policy.threshold <= 0 {
		return cognitivePolicy{}, fmt.Errorf("%s: linters.settings.gocognit.min-complexity must be set for the complexity ratchet", path)
	}
	rules, err := strictGocognitExcludeRules(raw)
	if err != nil {
		return cognitivePolicy{}, fmt.Errorf("%s: %w", path, err)
	}
	for _, rule := range rules {
		if !slices.Contains(rule.Linters, cognitiveLinterName) {
			continue
		}
		exclusion := cognitiveExclusion{}
		if rule.Path != "" {
			re, err := regexp.Compile(rule.Path)
			if err != nil {
				return cognitivePolicy{}, fmt.Errorf("%s: exclusion path %q: %w", path, rule.Path, err)
			}
			exclusion.path = re
		}
		if rule.PathExcept != "" {
			re, err := regexp.Compile(rule.PathExcept)
			if err != nil {
				return cognitivePolicy{}, fmt.Errorf("%s: exclusion path-except %q: %w", path, rule.PathExcept, err)
			}
			exclusion.pathExcept = re
		}
		if rule.Text != "" {
			re, err := regexp.Compile(rule.Text)
			if err != nil {
				return cognitivePolicy{}, fmt.Errorf("%s: exclusion text %q: %w", path, rule.Text, err)
			}
			exclusion.text = re
		}
		policy.compiled = append(policy.compiled, exclusion)
	}
	return policy, nil
}

// moduleRoots returns every directory under root that holds a go.mod file.
// The lint gate lints exactly these modules, and the ratchet measures the
// same set.
func moduleRoots(root string) ([]string, error) {
	var roots []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root && skipScannedDir(entry.Name()) {
			return fs.SkipDir
		}
		if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
			roots = append(roots, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return roots, nil
}

// skipScannedDir reports whether a directory is never scanned. Dotted
// directories hold VCS and tool state; vendor and node_modules hold
// third-party code that the repository does not author.
func skipScannedDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules"
}

// nearestModuleRoot returns the closest ancestor of dir that holds a
// go.mod file, which is the root of the module the directory belongs to.
func nearestModuleRoot(dir string, roots []string) string {
	best := ""
	for _, root := range roots {
		if root == dir || strings.HasPrefix(dir, root+string(filepath.Separator)) {
			if len(root) > len(best) {
				best = root
			}
		}
	}
	return best
}

// cognitiveReleasePlatforms are the platforms the release ships
// (.goreleaser.yaml). The measured file set is the union over these targets,
// so a platform-specific file is measured on every host.
var cognitiveReleasePlatforms = []struct {
	GOOS   string
	GOARCH string
}{
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "arm64"},
	{GOOS: "darwin", GOARCH: "amd64"},
	{GOOS: "darwin", GOARCH: "arm64"},
	{GOOS: "windows", GOARCH: "amd64"},
	{GOOS: "windows", GOARCH: "arm64"},
}

// cognitiveFileIsBuilt reports whether any released platform builds the file.
func cognitiveFileIsBuilt(dir, name string) bool {
	for _, platform := range cognitiveReleasePlatforms {
		ctx := build.Default
		ctx.GOOS = platform.GOOS
		ctx.GOARCH = platform.GOARCH
		matched, err := ctx.MatchFile(dir, name)
		if err == nil && matched {
			return true
		}
	}
	return false
}

// measureCognitiveComplexity returns every in-scope function above the
// threshold, together with its measured cognitive complexity.
func measureCognitiveComplexity(root string, policy cognitivePolicy) ([]cognitiveEntry, error) {
	roots, err := moduleRoots(root)
	if err != nil {
		return nil, fmt.Errorf("scan module roots: %w", err)
	}
	var entries []cognitiveEntry
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && skipScannedDir(entry.Name()) {
				return fs.SkipDir
			}
			if entry.Name() == "testdata" {
				// The repository-root testdata directory holds the fixture
				// modules the lint gate lints. Nested testdata directories
				// hold fixtures, which go list skips.
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil || filepath.ToSlash(rel) != "testdata" {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		repoPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		repoPath = filepath.ToSlash(repoPath)
		moduleRoot := nearestModuleRoot(filepath.Dir(path), roots)
		modulePath := repoPath
		if moduleRoot != "" {
			rel, err := filepath.Rel(moduleRoot, path)
			if err != nil {
				return err
			}
			modulePath = filepath.ToSlash(rel)
		}
		paths := []string{modulePath, repoPath}
		if policy.excludesFile(paths) {
			return nil
		}
		// A file is measured when at least one released platform builds it, so
		// the recorded set does not depend on the host the check runs on. A
		// file no platform builds (a //go:build ignore generator) is never
		// measured.
		if !cognitiveFileIsBuilt(filepath.Dir(path), filepath.Base(path)) {
			return nil
		}
		fset := token.NewFileSet()
		// Comments are required: gocognit reads the //gocognit:ignore
		// directive from the declaration's doc comment.
		parsed, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", repoPath, err)
		}
		for _, stat := range gocognit.ComplexityStats(parsed, fset, nil) {
			if stat.Complexity <= policy.threshold {
				continue
			}
			entry := cognitiveEntry{
				Path:       repoPath,
				Function:   stat.FuncName,
				Complexity: stat.Complexity,
			}
			if policy.excluded(paths, cognitiveIssueText(entry, policy.threshold)) {
				continue
			}
			entries = append(entries, entry)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortCognitiveEntries(entries)
	return entries, nil
}

func sortCognitiveEntries(entries []cognitiveEntry) {
	slices.SortFunc(entries, func(a, b cognitiveEntry) int {
		if a.Path != b.Path {
			return strings.Compare(a.Path, b.Path)
		}
		return strings.Compare(a.Function, b.Function)
	})
}

// cognitiveChange is a recorded function whose measured value changed.
type cognitiveChange struct {
	entry cognitiveEntry
	from  int
}

// cognitiveDiff reports how the measured set differs from the record.
type cognitiveDiff struct {
	grown   []cognitiveChange
	added   []cognitiveEntry
	shrunk  []cognitiveChange
	removed []cognitiveEntry
}

func (d cognitiveDiff) empty() bool {
	return len(d.grown) == 0 && len(d.added) == 0 && len(d.shrunk) == 0 && len(d.removed) == 0
}

// diffCognitiveEntries compares the measured functions with the record.
// Growth (higher complexity, or a new function above the threshold) fails
// the check; an improvement is worth recording so it cannot silently
// regrow, and a removal is worth recording so the record stays current.
func diffCognitiveEntries(measured, recorded []cognitiveEntry) cognitiveDiff {
	measuredByKey := make(map[string]cognitiveEntry, len(measured))
	for _, entry := range measured {
		measuredByKey[entry.key()] = entry
	}
	recordedByKey := make(map[string]cognitiveEntry, len(recorded))
	for _, entry := range recorded {
		recordedByKey[entry.key()] = entry
	}
	var diff cognitiveDiff
	for _, entry := range measured {
		previous, ok := recordedByKey[entry.key()]
		if !ok {
			diff.added = append(diff.added, entry)
			continue
		}
		switch {
		case entry.Complexity > previous.Complexity:
			diff.grown = append(diff.grown, cognitiveChange{entry: entry, from: previous.Complexity})
		case entry.Complexity < previous.Complexity:
			diff.shrunk = append(diff.shrunk, cognitiveChange{entry: entry, from: previous.Complexity})
		}
	}
	for _, entry := range recorded {
		if _, ok := measuredByKey[entry.key()]; !ok {
			diff.removed = append(diff.removed, entry)
		}
	}
	return diff
}

// readCognitiveBaseline loads the recorded functions.
func readCognitiveBaseline(path string) (cognitiveBaseline, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cognitiveBaseline{SchemaVersion: cognitiveSchemaVersion}, nil
		}
		return cognitiveBaseline{}, fmt.Errorf("read %s: %w", path, err)
	}
	var baseline cognitiveBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return cognitiveBaseline{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if baseline.SchemaVersion != cognitiveSchemaVersion {
		return cognitiveBaseline{}, fmt.Errorf("%s: schema version %d is not supported (want %d)", path, baseline.SchemaVersion, cognitiveSchemaVersion)
	}
	return baseline, nil
}

// writeCognitiveBaseline records the measured functions.
func writeCognitiveBaseline(path string, entries []cognitiveEntry) error {
	baseline := cognitiveBaseline{SchemaVersion: cognitiveSchemaVersion, Functions: entries}
	if baseline.Functions == nil {
		baseline.Functions = []cognitiveEntry{}
	}
	raw, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// TestCognitiveComplexityRatchet fails on complexity growth: a recorded
// function measured above its record, or a new function above the
// threshold. Improvements and removals are logged for human review but
// never fail.
func TestCognitiveComplexityRatchet(t *testing.T) {
	t.Parallel()

	root, err := projectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	policy, err := loadCognitivePolicy(filepath.Join(root, ".golangci.yml"))
	if err != nil {
		t.Fatalf("load complexity policy: %v", err)
	}
	measured, err := measureCognitiveComplexity(root, policy)
	if err != nil {
		t.Fatalf("measure cognitive complexity: %v", err)
	}
	baselinePath := filepath.Join(root, "internal", "qa", filepath.FromSlash(cognitiveBaselineRelPath))
	baseline, err := readCognitiveBaseline(baselinePath)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}

	if *updateCognitiveBaseline {
		if err := writeCognitiveBaseline(baselinePath, measured); err != nil {
			t.Fatalf("update baseline: %v", err)
		}
		t.Logf("wrote %d recorded functions to %s", len(measured), cognitiveBaselineRelPath)
		return
	}

	diff := diffCognitiveEntries(measured, baseline.Functions)
	if len(diff.grown) == 0 && len(diff.added) == 0 {
		if !diff.empty() {
			var report strings.Builder
			fmt.Fprintf(&report, "cognitive complexity improvements (threshold %d), baseline refresh is optional:\n", policy.threshold)
			for _, change := range diff.shrunk {
				fmt.Fprintf(&report, "  shrunk:  %s %s %d -> %d\n", change.entry.Path, change.entry.Function, change.from, change.entry.Complexity)
			}
			for _, entry := range diff.removed {
				fmt.Fprintf(&report, "  removed: %s %s %d\n", entry.Path, entry.Function, entry.Complexity)
			}
			fmt.Fprintf(&report, "refresh the recorded values with: go test -tags=precommit ./internal/qa -run TestCognitiveComplexityRatchet -update-cognitive-baseline")
			t.Logf("%s", report.String())
		}
		return
	}
	var report strings.Builder
	fmt.Fprintf(&report, "cognitive complexity growth (threshold %d):\n", policy.threshold)
	for _, change := range diff.grown {
		fmt.Fprintf(&report, "  grown:   %s %s %d -> %d\n", change.entry.Path, change.entry.Function, change.from, change.entry.Complexity)
	}
	for _, entry := range diff.added {
		fmt.Fprintf(&report, "  added:   %s %s %d\n", entry.Path, entry.Function, entry.Complexity)
	}
	for _, change := range diff.shrunk {
		fmt.Fprintf(&report, "  shrunk:  %s %s %d -> %d\n", change.entry.Path, change.entry.Function, change.from, change.entry.Complexity)
	}
	for _, entry := range diff.removed {
		fmt.Fprintf(&report, "  removed: %s %s %d\n", entry.Path, entry.Function, entry.Complexity)
	}
	fmt.Fprintf(&report, "record the measured values with: go test -tags=precommit ./internal/qa -run TestCognitiveComplexityRatchet -update-cognitive-baseline")
	t.Fatalf("%s", report.String())
}
