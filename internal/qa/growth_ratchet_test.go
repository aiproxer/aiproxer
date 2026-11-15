//go:build precommit

package qa

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/opensource"
)

// The test-cost growth ratchet.
//
// time.Sleep in a test file, a context.Context struct field in non-test code,
// and a hand-built file-backed SQLite DSN are each legitimate somewhere, and
// annotating every existing hit would be churn. The ratchet pins the current
// per-file count and fails only on growth; decreases and removals are
// reported and never fail.
//
// Measurement is syntactic, so a sleep behind a helper, a context field typed
// through an alias or a pointer, and an in-memory DSN literal stay review
// rules. Every tracked Go file is measured, including fixtures, so a fixture
// cannot hide a regression; a file absent from an exported tree surfaces as a
// removal. internal/qa/testdata is exported, so Enterprise counts are
// recorded under the export-ignored Enterprise tree instead: the public
// baseline never names a closed-source file.
const (
	growthBaselineRelPath = "internal/qa/testdata/growth-ratchet-baseline.json"
	// growthEnterpriseBaselineRelPath is export-ignored with the rest of the
	// Enterprise tree, which the public export omits entirely.
	growthEnterpriseBaselineRelPath = "enterprise/testdata/growth-ratchet-baseline.json"
	growthEnterprisePathPrefix      = "enterprise/"
	growthSchemaVersion             = 1
	growthUpdateCommand             = "go test -tags=precommit ./internal/qa -run TestGrowthRatchet -update-growth-baseline"

	measureTestSleep     = "test_time_sleep"
	measureContextField  = "non_test_context_field"
	measureSQLiteFileDSN = "test_sqlite_file_dsn"

	// sqliteFileScheme is assembled so this ratchet does not count itself.
	sqliteFileScheme = "fi" + "le:"
)

var updateGrowthBaseline = flag.Bool("update-growth-baseline", false,
	"rewrite the test-cost growth baselines from the measured counts")

// growthEntry pins one file's count for one measure.
type growthEntry struct {
	Path    string `json:"path"`
	Measure string `json:"measure"`
	Count   int    `json:"count"`
}

func (e growthEntry) key() string {
	return e.Path + "\x00" + e.Measure
}

// growthBaseline is the recorded set of nonzero per-file counts.
type growthBaseline struct {
	SchemaVersion int           `json:"schema_version"`
	Entries       []growthEntry `json:"entries"`
}

// growthMeasure is one recorded count. Every measure applies either only to
// test files or only to non-test files. markers are cheap source pre-filters:
// a file that lacks all of them cannot contain a counted construct, so it is
// never parsed.
type growthMeasure struct {
	name             string
	testFiles        bool
	skipPathPrefixes []string
	markers          []string
	count            func(goFileFacts) int
}

func growthMeasures() []growthMeasure {
	return []growthMeasure{
		{name: measureTestSleep, testFiles: true, markers: []string{"Sleep("}, count: countTimeSleepCalls},
		{name: measureContextField, markers: []string{"Context"}, count: countContextFields},
		{
			name:             measureSQLiteFileDSN,
			testFiles:        true,
			skipPathPrefixes: []string{"internal/testkit/sqlitefixture/"},
			markers:          []string{sqliteFileScheme, "SQLiteFileDSN"},
			count:            countSQLiteFileDSNs,
		},
	}
}

func (m growthMeasure) appliesTo(filePath string) bool {
	if m.testFiles != strings.HasSuffix(filePath, "_test.go") {
		return false
	}
	for _, prefix := range m.skipPathPrefixes {
		if strings.HasPrefix(filePath, prefix) {
			return false
		}
	}
	return true
}

func (m growthMeasure) candidate(src []byte) bool {
	for _, marker := range m.markers {
		if bytes.Contains(src, []byte(marker)) {
			return true
		}
	}
	return false
}

// goFileFacts is what the counts need beyond the AST: the local name each
// import gives its package, so an aliased or dot import still resolves.
type goFileFacts struct {
	file    *ast.File
	imports map[string]string
}

func newGoFileFacts(file *ast.File) goFileFacts {
	facts := goFileFacts{file: file, imports: make(map[string]string, len(file.Imports))}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		local := path.Base(importPath)
		if spec.Name != nil {
			local = spec.Name.Name
		}
		if local != "_" {
			facts.imports[importPath] = local
		}
	}
	return facts
}

func (f goFileFacts) selectorRefersTo(selector *ast.SelectorExpr, importPath, name string) bool {
	pkg, ok := selector.X.(*ast.Ident)
	return ok && selector.Sel.Name == name && f.imports[importPath] == pkg.Name
}

func (f goFileFacts) dotImported(importPath string) bool {
	return f.imports[importPath] == "."
}

func countTimeSleepCalls(facts goFileFacts) int {
	count := 0
	ast.Inspect(facts.file, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				if facts.selectorRefersTo(fun, "time", "Sleep") {
					count++
				}
			case *ast.Ident:
				if facts.dotImported("time") && fun.Name == "Sleep" {
					count++
				}
			}
		}
		return true
	})
	return count
}

func countContextFields(facts goFileFacts) int {
	count := 0
	ast.Inspect(facts.file, func(node ast.Node) bool {
		structType, ok := node.(*ast.StructType)
		if !ok || structType.Fields == nil {
			return true
		}
		for _, field := range structType.Fields.List {
			switch typed := field.Type.(type) {
			case *ast.SelectorExpr:
				if facts.selectorRefersTo(typed, "context", "Context") {
					count++
				}
			case *ast.Ident:
				if facts.dotImported("context") && typed.Name == "Context" {
					count++
				}
			}
		}
		return true
	})
	return count
}

// countSQLiteFileDSNs counts hand-built file-backed SQLite DSNs: string
// literals that begin with the file: scheme and do not state an in-memory
// database, plus calls to SQLiteFileDSN. The sanctioned helper
// (sqlitefixture.FileDSN) is outside the measure.
func countSQLiteFileDSNs(facts goFileFacts) int {
	count := 0
	ast.Inspect(facts.file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.BasicLit:
			if typed.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(typed.Value)
			if err != nil {
				return true
			}
			if strings.HasPrefix(value, sqliteFileScheme) && !strings.Contains(value, ":memory:") && !strings.Contains(value, "mode=memory") {
				count++
			}
		case *ast.CallExpr:
			switch fun := typed.Fun.(type) {
			case *ast.SelectorExpr:
				if fun.Sel.Name == "SQLiteFileDSN" {
					count++
				}
			case *ast.Ident:
				if fun.Name == "SQLiteFileDSN" {
					count++
				}
			}
		}
		return true
	})
	return count
}

// measureGrowth returns every nonzero per-file count, measuring only files
// whose source contains a marker of an applicable measure. A tracked file
// deleted from the working tree is skipped; the baseline diff reports it.
func measureGrowth(t *testing.T, root string, measures []growthMeasure) ([]growthEntry, error) {
	t.Helper()

	entries := map[string]growthEntry{}
	for _, rel := range trackedGoFiles(t, root) {
		var applicable []growthMeasure
		for _, measure := range measures {
			if measure.appliesTo(rel) {
				applicable = append(applicable, measure)
			}
		}
		if len(applicable) == 0 {
			continue
		}
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", rel, err)
		}
		var facts goFileFacts
		parsed := false
		for _, measure := range applicable {
			if !measure.candidate(src) {
				continue
			}
			if !parsed {
				file, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.SkipObjectResolution)
				if err != nil {
					return nil, fmt.Errorf("parse %s: %w", rel, err)
				}
				facts = newGoFileFacts(file)
				parsed = true
			}
			if count := measure.count(facts); count > 0 {
				entry := growthEntry{Path: rel, Measure: measure.name, Count: count}
				entries[entry.key()] = entry
			}
		}
	}
	out := make([]growthEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	slices.SortFunc(out, func(a, b growthEntry) int {
		if a.Path != b.Path {
			return strings.Compare(a.Path, b.Path)
		}
		return strings.Compare(a.Measure, b.Measure)
	})
	return out, nil
}

// growthChange is a recorded count that moved.
type growthChange struct {
	entry growthEntry
	from  int
}

// growthDiff reports how the measured counts differ from the record.
type growthDiff struct {
	grown   []growthChange
	added   []growthEntry
	shrunk  []growthChange
	removed []growthEntry
}

func (d growthDiff) growth() bool {
	return len(d.grown) > 0 || len(d.added) > 0
}

func (d growthDiff) String() string {
	var report strings.Builder
	for _, change := range d.grown {
		fmt.Fprintf(&report, "  grown:   %s %s %d -> %d\n", change.entry.Path, change.entry.Measure, change.from, change.entry.Count)
	}
	for _, entry := range d.added {
		fmt.Fprintf(&report, "  added:   %s %s %d\n", entry.Path, entry.Measure, entry.Count)
	}
	for _, change := range d.shrunk {
		fmt.Fprintf(&report, "  shrunk:  %s %s %d -> %d\n", change.entry.Path, change.entry.Measure, change.from, change.entry.Count)
	}
	for _, entry := range d.removed {
		fmt.Fprintf(&report, "  removed: %s %s %d\n", entry.Path, entry.Measure, entry.Count)
	}
	return report.String()
}

func diffGrowth(measured, recorded []growthEntry) growthDiff {
	measuredByKey := make(map[string]growthEntry, len(measured))
	for _, entry := range measured {
		measuredByKey[entry.key()] = entry
	}
	recordedByKey := make(map[string]growthEntry, len(recorded))
	for _, entry := range recorded {
		recordedByKey[entry.key()] = entry
	}
	var diff growthDiff
	for _, entry := range measured {
		previous, ok := recordedByKey[entry.key()]
		switch {
		case !ok:
			diff.added = append(diff.added, entry)
		case entry.Count > previous.Count:
			diff.grown = append(diff.grown, growthChange{entry: entry, from: previous.Count})
		case entry.Count < previous.Count:
			diff.shrunk = append(diff.shrunk, growthChange{entry: entry, from: previous.Count})
		}
	}
	for _, entry := range recorded {
		if _, ok := measuredByKey[entry.key()]; !ok {
			diff.removed = append(diff.removed, entry)
		}
	}
	return diff
}

func growthBaselinePath(root, relPath string) string {
	return filepath.Join(root, filepath.FromSlash(relPath))
}

func isEnterprisePath(filePath string) bool {
	return strings.HasPrefix(filePath, growthEnterprisePathPrefix)
}

// splitGrowthEntries routes counts by path prefix. The public record is
// exported, so an Enterprise path may only reach the Enterprise record.
func splitGrowthEntries(entries []growthEntry) (public, enterprise []growthEntry) {
	for _, entry := range entries {
		if isEnterprisePath(entry.Path) {
			enterprise = append(enterprise, entry)
			continue
		}
		public = append(public, entry)
	}
	return public, enterprise
}

// loadGrowthBaselines reads both records. A missing Enterprise record stays
// empty: the public export has no Enterprise tree to measure, so no count is
// silently unratcheted there, while a private tree that measures one sees the
// count as growth. An Enterprise path in the public record is an error rather
// than a tolerated leak.
func loadGrowthBaselines(root string) (growthBaseline, growthBaseline, error) {
	public, err := readGrowthBaseline(growthBaselinePath(root, growthBaselineRelPath))
	if err != nil {
		return growthBaseline{}, growthBaseline{}, err
	}
	for _, entry := range public.Entries {
		if isEnterprisePath(entry.Path) {
			return growthBaseline{}, growthBaseline{}, fmt.Errorf("%s records %s: Enterprise measurements belong in %s", growthBaselineRelPath, entry.Path, growthEnterpriseBaselineRelPath)
		}
	}
	enterprise, err := readGrowthBaseline(growthBaselinePath(root, growthEnterpriseBaselineRelPath))
	if err != nil {
		return growthBaseline{}, growthBaseline{}, err
	}
	return public, enterprise, nil
}

// writeGrowthBaselines records measured counts in both files. A public export
// has no Enterprise tree and measures none of its files, so it writes only the
// public record and never creates an Enterprise one.
func writeGrowthBaselines(root string, measured []growthEntry) error {
	publicEntries, enterpriseEntries := splitGrowthEntries(measured)
	if opensource.IsOpenSourceTree(root) {
		if len(enterpriseEntries) > 0 {
			return fmt.Errorf("%s is absent but %d Enterprise counts were measured", growthEnterpriseBaselineRelPath, len(enterpriseEntries))
		}
		return writeGrowthBaseline(growthBaselinePath(root, growthBaselineRelPath), publicEntries)
	}
	if err := writeGrowthBaseline(growthBaselinePath(root, growthBaselineRelPath), publicEntries); err != nil {
		return err
	}
	return writeGrowthBaseline(growthBaselinePath(root, growthEnterpriseBaselineRelPath), enterpriseEntries)
}

func readGrowthBaseline(baselinePath string) (growthBaseline, error) {
	raw, err := os.ReadFile(baselinePath)
	if err != nil {
		if os.IsNotExist(err) {
			return growthBaseline{SchemaVersion: growthSchemaVersion}, nil
		}
		return growthBaseline{}, fmt.Errorf("read %s: %w", baselinePath, err)
	}
	var baseline growthBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return growthBaseline{}, fmt.Errorf("parse %s: %w", baselinePath, err)
	}
	if baseline.SchemaVersion != growthSchemaVersion {
		return growthBaseline{}, fmt.Errorf("%s: schema version %d is not supported (want %d)", baselinePath, baseline.SchemaVersion, growthSchemaVersion)
	}
	return baseline, nil
}

func writeGrowthBaseline(baselinePath string, entries []growthEntry) error {
	baseline := growthBaseline{SchemaVersion: growthSchemaVersion, Entries: entries}
	if baseline.Entries == nil {
		baseline.Entries = []growthEntry{}
	}
	raw, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", baselinePath, err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(baselinePath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(baselinePath), err)
	}
	if err := os.WriteFile(baselinePath, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", baselinePath, err)
	}
	return nil
}

// TestGrowthRatchet fails when a recorded test-cost count grows or a new file
// starts with a nonzero count. Decreases and removed files are logged for
// human review but never fail.
func TestGrowthRatchet(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	start := time.Now()
	measured, err := measureGrowth(t, root, growthMeasures())
	if err != nil {
		t.Fatalf("measure test-cost growth: %v", err)
	}

	if *updateGrowthBaseline {
		if err := writeGrowthBaselines(root, measured); err != nil {
			t.Fatalf("update baseline: %v", err)
		}
		t.Logf("wrote %d recorded counts in %s", len(measured), time.Since(start))
		return
	}

	public, enterprise, err := loadGrowthBaselines(root)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	diff := diffGrowth(measured, slices.Concat(public.Entries, enterprise.Entries))
	if !diff.growth() {
		if len(diff.shrunk) > 0 || len(diff.removed) > 0 {
			t.Logf("test-cost decreases, baseline refresh is optional:\n%srefresh the recorded counts with: %s", diff, growthUpdateCommand)
		}
		return
	}
	t.Fatalf("test-cost growth:\n%sremove the added usage, or record a reviewed increase with: %s", diff, growthUpdateCommand)
}
