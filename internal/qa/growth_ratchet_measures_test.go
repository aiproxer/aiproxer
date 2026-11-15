//go:build precommit

package qa

import (
	"go/parser"
	"go/token"
	"testing"
)

// TestGrowthRatchet_MeasuresCountSyntacticConstructs pins what each measure
// counts and what it deliberately leaves to review, so a later simplification
// cannot silently widen or narrow the ratchet.
func TestGrowthRatchet_MeasuresCountSyntacticConstructs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		measure string
		src     string
		want    int
	}{
		{
			name:    "time sleep call",
			measure: measureTestSleep,
			src:     "package fixture\nimport \"time\"\nfunc f() { time.Sleep(1) }",
			want:    1,
		},
		{
			name:    "aliased time import",
			measure: measureTestSleep,
			src:     "package fixture\nimport clock \"time\"\nfunc f() { clock.Sleep(1) }",
			want:    1,
		},
		{
			name:    "dot imported time",
			measure: measureTestSleep,
			src:     "package fixture\nimport . \"time\"\nfunc f() { Sleep(1) }",
			want:    1,
		},
		{
			name:    "sleep word in a comment or string is not a call",
			measure: measureTestSleep,
			src:     "package fixture\n// time.Sleep(1)\nvar s = \"time.Sleep(1)\"",
			want:    0,
		},
		{
			name:    "sleep on another value is not time.Sleep",
			measure: measureTestSleep,
			src:     "package fixture\nfunc f(clock sleeper) { clock.Sleep(1) }",
			want:    0,
		},
		{
			name:    "named context field",
			measure: measureContextField,
			src:     "package fixture\nimport \"context\"\ntype holder struct { ctx context.Context }",
			want:    1,
		},
		{
			name:    "embedded context field",
			measure: measureContextField,
			src:     "package fixture\nimport \"context\"\ntype holder struct { context.Context }",
			want:    1,
		},
		{
			name:    "aliased context import",
			measure: measureContextField,
			src:     "package fixture\nimport stdctx \"context\"\ntype holder struct { ctx stdctx.Context }",
			want:    1,
		},
		{
			name:    "context parameter split over lines is not a struct field",
			measure: measureContextField,
			src:     "package fixture\nimport \"context\"\nfunc f(\n\tctx context.Context,\n) {}",
			want:    0,
		},
		{
			name:    "pointer context field is not the context.Context type",
			measure: measureContextField,
			src:     "package fixture\nimport \"context\"\ntype holder struct { ctx *context.Context }",
			want:    0,
		},
		{
			name:    "file backed dsn literal",
			measure: measureSQLiteFileDSN,
			src:     "package fixture\nvar dsn = \"file:test.db?_pragma=busy_timeout(5000)\"",
			want:    1,
		},
		{
			name:    "shared cache in-memory dsn literal",
			measure: measureSQLiteFileDSN,
			src:     "package fixture\nvar dsn = \"file:shared?mode=memory&cache=shared\"",
			want:    0,
		},
		{
			name:    "named in-memory dsn literal",
			measure: measureSQLiteFileDSN,
			src:     "package fixture\nvar dsn = \"file::memory:?cache=shared\"",
			want:    0,
		},
		{
			name:    "SQLiteFileDSN call",
			measure: measureSQLiteFileDSN,
			src:     "package fixture\nfunc f(db handle) { db.SQLiteFileDSN(\"x\") }",
			want:    1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			facts := parseGrowthFixture(t, tc.src)
			measure := growthMeasureByName(t, tc.measure)
			if got := measure.count(facts); got != tc.want {
				t.Fatalf("%s counted %d occurrences, want %d", tc.measure, got, tc.want)
			}
		})
	}
}

// TestGrowthRatchet_MeasuresApplyToScope pins the file scope each measure
// covers.
func TestGrowthRatchet_MeasuresApplyToScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		measure string
		path    string
		want    bool
	}{
		{name: "sleep in a test file", measure: measureTestSleep, path: "internal/qa/example_test.go", want: true},
		{name: "sleep in production code is out of scope", measure: measureTestSleep, path: "internal/qa/example.go", want: false},
		{name: "context field in a test file is out of scope", measure: measureContextField, path: "internal/qa/example_test.go", want: false},
		{name: "context field in production code", measure: measureContextField, path: "internal/qa/example.go", want: true},
		{name: "dsn in a test file", measure: measureSQLiteFileDSN, path: "internal/qa/example_test.go", want: true},
		{name: "dsn in production code is out of scope", measure: measureSQLiteFileDSN, path: "internal/qa/example.go", want: false},
		{name: "the sqlite fixture helper owns its own dsn construction", measure: measureSQLiteFileDSN, path: "internal/testkit/sqlitefixture/file_test.go", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := growthMeasureByName(t, tc.measure).appliesTo(tc.path); got != tc.want {
				t.Fatalf("%s applies to %s = %v, want %v", tc.measure, tc.path, got, tc.want)
			}
		})
	}
}

// TestGrowthRatchet_DiffSeparatesGrowthFromDecreases pins that only growth
// fails the ratchet.
func TestGrowthRatchet_DiffSeparatesGrowthFromDecreases(t *testing.T) {
	t.Parallel()

	recorded := []growthEntry{
		{Path: "a_test.go", Measure: measureTestSleep, Count: 2},
		{Path: "b_test.go", Measure: measureSQLiteFileDSN, Count: 3},
		{Path: "c.go", Measure: measureContextField, Count: 4},
	}
	measured := []growthEntry{
		{Path: "a_test.go", Measure: measureTestSleep, Count: 3},
		{Path: "b_test.go", Measure: measureSQLiteFileDSN, Count: 1},
		{Path: "d_test.go", Measure: measureTestSleep, Count: 5},
	}
	diff := diffGrowth(measured, recorded)
	if len(diff.grown) != 1 || diff.grown[0].entry.Path != "a_test.go" || diff.grown[0].from != 2 {
		t.Fatalf("grown = %+v, want the a_test.go increase from 2", diff.grown)
	}
	if len(diff.added) != 1 || diff.added[0].Path != "d_test.go" {
		t.Fatalf("added = %+v, want d_test.go", diff.added)
	}
	if len(diff.shrunk) != 1 || diff.shrunk[0].entry.Path != "b_test.go" {
		t.Fatalf("shrunk = %+v, want b_test.go", diff.shrunk)
	}
	if len(diff.removed) != 1 || diff.removed[0].Path != "c.go" {
		t.Fatalf("removed = %+v, want c.go", diff.removed)
	}

	decreaseOnly := diffGrowth(measured[1:2], recorded[1:2])
	if decreaseOnly.growth() || len(decreaseOnly.shrunk) != 1 {
		t.Fatalf("decrease-only diff reported growth: %+v", decreaseOnly)
	}
}

func growthMeasureByName(t *testing.T, name string) growthMeasure {
	t.Helper()

	for _, measure := range growthMeasures() {
		if measure.name == name {
			return measure
		}
	}
	t.Fatalf("unknown growth measure %q", name)
	return growthMeasure{}
}

func parseGrowthFixture(t *testing.T, src string) goFileFacts {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return newGoFileFacts(file)
}
