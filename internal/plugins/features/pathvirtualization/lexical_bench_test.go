package pathvirtualization_test

// Spec: b-leg-path-virtualization Task 11.1, requirements.md 9.2, 9.3, 9.4 and 9.6,
// against design.md "Testing Strategy / Benchmarks" and "Observability".
//
// This file measures the lexical core in isolation: flavor recognition (requirement
// 9.4's "path parsing"), mapping derivation including the SHA-256 workspace-tag
// derivation every outbound and inbound pass pays for, and the two prefix
// operations the whole feature rests on.
//
// Three rules govern everything below.
//
// Fixtures are synthetic and built before the timed region. Every root here is
// spelled under `/synthetic/` or `C:\synthetic\`, and no benchmark prints a root,
// an alias, or a workspace tag: the only names that appear in benchmark output are
// the fixed labels in benchRootForms. That is requirement 7.7's content-freedom
// rule applied to measurement output, not only to production observability.
//
// The roots are LONG on purpose. A short root is a SUPPORTED root whose alias is
// correctly not strictly shorter than the root itself, which leaves virtualization
// inactive by requirement 1.4, so a short fixture would measure the inactive path
// and prove nothing. The Windows drive root is 220 bytes, so an ordinary source-file
// path under it reaches 256 bytes, four under MAX_PATH; the POSIX root is a deep
// monorepo worktree path of 151 bytes, reaching 187 with the same suffix.
//
// No benchmark asserts on elapsed time. These are measurements; the stable guard
// against a reintroduced super-linear cost lives in rewrite/occurrence_scaling_test.go
// and is expressed in allocations, which are host-independent.

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// benchPOSIXWorktreeRoot is the long synthetic POSIX monorepo worktree root of
// requirement 9.6: 151 bytes, a deep monorepo worktree path.
const benchPOSIXWorktreeRoot = "/synthetic/build-agent/workspaces/aiproxer-example-project/monorepo" +
	"/services/interactive-proxy/.worktrees/b-leg-path-virtualization-deep-posix-worktree"

// benchWindowsWorktreeRoot is the long synthetic Windows drive worktree root of
// requirement 9.6: 220 bytes, so a source-file path under it is 256 bytes and the
// fixture is under real MAX_PATH pressure rather than merely long.
const benchWindowsWorktreeRoot = `C:\synthetic\build-agent\source\repos\aiproxer-example-project` +
	`\.worktrees\b-leg-path-virtualization-windows-long-worktree\connector-support\backends` +
	`\interactive-proxy-provider\internal\plugins\features\pathvirtualization`

// benchUNCWorktreeRoot is a long synthetic Windows UNC worktree root. UNC roots are
// the flavor whose volume prefix is a complete root rather than a drive letter, so
// the tag's canonical-identity trim differs there; benchmarking it is what keeps the
// flavor sweep honest.
const benchUNCWorktreeRoot = `\\synthetic-fileserver\build-artifacts\aiproxer-example-project` +
	`\.worktrees\b-leg-path-virtualization-unc-worktree\internal\plugins\features\pathvirtualization`

// benchExtendedDriveWorktreeRoot is a long synthetic Windows extended-drive worktree
// root, whose spelled volume is `\\?\C:` and whose separator handling is the
// anchor-mangled spelling.
const benchExtendedDriveWorktreeRoot = `\\?\C:\synthetic\build-agent\source\repos\aiproxer-example-project` +
	`\.worktrees\b-leg-path-virtualization-extended-drive-worktree\internal\plugins\features\pathvirtualization`

// benchExtendedUNCWorktreeRoot is a long synthetic Windows extended-UNC worktree root.
const benchExtendedUNCWorktreeRoot = `\\?\UNC\synthetic-fileserver\build-artifacts\aiproxer-example-project` +
	`\.worktrees\b-leg-path-virtualization-extended-unc-worktree\internal\plugins\features\pathvirtualization`

// benchRootForms is the flavor sweep: all five supported absolute forms of
// requirement 1.2, each spelled as a realistic long root.
//
// The name is the ONLY fixture-derived value any benchmark below can print.
var benchRootForms = []struct {
	name   string
	root   string
	flavor pathvirtualization.PathFlavor
	sep    string
}{
	{"posix_deep_monorepo_worktree", benchPOSIXWorktreeRoot, pathvirtualization.FlavorPOSIX, "/"},
	{"windows_drive_long_worktree", benchWindowsWorktreeRoot, pathvirtualization.FlavorWindowsDrive, `\`},
	{"windows_unc_long_worktree", benchUNCWorktreeRoot, pathvirtualization.FlavorWindowsUNC, `\`},
	{
		"windows_extended_drive_long_worktree",
		benchExtendedDriveWorktreeRoot,
		pathvirtualization.FlavorWindowsExtendedDrive,
		`\`,
	},
	{
		"windows_extended_unc_long_worktree",
		benchExtendedUNCWorktreeRoot,
		pathvirtualization.FlavorWindowsExtendedUNC,
		`\`,
	},
}

// benchmark sinks. Each benchmark body assigns its result to exactly one of these so
// the measured call cannot be eliminated as dead code; nothing reads them, and no
// fixture content reaches them beyond what the measured call returned.
var (
	benchSinkParsed  pathvirtualization.ParsedPath
	benchSinkMapping pathvirtualization.Mapping
	benchSinkReason  pathvirtualization.SkipReason
	benchSinkValue   string
	benchSinkMatch   bool
	benchSinkResult  pathvirtualization.ExpandResult
)

// BenchmarkClassifyPathLongRoots is requirement 9.4's "path parsing" benchmark: the
// flavor recognition every derived mapping and every alias decision begins with.
//
// It sweeps all five supported absolute forms, because recognition is flavor
// dependent: a POSIX root is one byte test, a UNC root splits a volume out of the
// body, and the extended forms add an anchor-mangled branch that must not be slower
// than the plain drive branch.
func BenchmarkClassifyPathLongRoots(b *testing.B) {
	for _, form := range benchRootForms {
		b.Run(form.name, func(b *testing.B) {
			if parsed, reason := pathvirtualization.ClassifyPath(form.root); reason != pathvirtualization.SkipReasonNone {
				b.Fatalf("fixture %q is not a supported absolute form: %v", form.name, reason)
			} else if parsed.Flavor != form.flavor {
				b.Fatalf("fixture %q classified as %v, want %v", form.name, parsed.Flavor, form.flavor)
			}
			b.ReportAllocs()
			for b.Loop() {
				benchSinkParsed, benchSinkReason = pathvirtualization.ClassifyPath(form.root)
			}
		})
	}
}

// BenchmarkDeriveMappingWorkspaceTag is requirement 9.4's "mapping" benchmark and it
// deliberately includes the workspace-tag derivation: DeriveMapping always runs the
// SHA-256 tag over the canonical root identity, so a mapping derivation that skipped
// it would not describe what any outbound pass or completed-call expansion pays.
//
// Comparing this benchmark against BenchmarkClassifyPathLongRoots on the same fixture
// isolates the tag and alias construction from the lexical parse, which is the only
// way to tell a future regression in the tag derivation from one in the parser.
func BenchmarkDeriveMappingWorkspaceTag(b *testing.B) {
	for _, form := range benchRootForms {
		b.Run(form.name, func(b *testing.B) {
			mapping := benchActiveMapping(b, form.name, form.root)
			// The frozen V1 tag is 20 base32 characters, so a fixture that stopped
			// deriving one would silently measure a shorter pipeline.
			if len(mapping.WorkspaceTag) != 20 {
				b.Fatalf("fixture %q derived a %d-character tag, want the frozen 20",
					form.name, len(mapping.WorkspaceTag))
			}
			b.ReportAllocs()
			for b.Loop() {
				benchSinkMapping, benchSinkReason = pathvirtualization.DeriveMapping(form.root)
			}
		})
	}
}

// BenchmarkPathPrefixMatch is requirement 9.4's "prefix matching" benchmark: the two
// byte comparisons the feature is built on, with no JSON, no selector layer, and no
// call walk around them.
//
// It runs over the two roots requirement 9.6 names and over both directions, because
// they are not the same work: virtualizing compares a real-root prefix and
// concatenates the alias with the untouched suffix, while expanding first runs
// reserved-alias recognition over the whole candidate before it compares anything.
func BenchmarkPathPrefixMatch(b *testing.B) {
	for _, form := range benchRootForms[:2] {
		mapping := benchActiveMapping(b, form.name, form.root)
		suffix := "src" + form.sep + "synthetic_service" + form.sep + "handler.go"
		realPath := form.root + form.sep + suffix
		virtualPath := mapping.VirtualRoot + suffix

		if _, matched := mapping.VirtualizePath(realPath); !matched {
			b.Fatalf("fixture %q does not virtualize its own suffix", form.name)
		}
		// The sibling-root refusal is the segment-boundary rule of requirement 1.7, and
		// it is what a longer prefix match must not be measured without: a value that
		// continues the root's last segment shares a byte prefix with the root itself.
		if _, matched := mapping.VirtualizePath(form.root + "-sibling" + form.sep + suffix); matched {
			b.Fatalf("fixture %q virtualizes a sibling root", form.name)
		}

		b.Run(form.name+"/virtualize_real_path", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchSinkValue, benchSinkMatch = mapping.VirtualizePath(realPath)
			}
		})
		b.Run(form.name+"/expand_alias_path", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				benchSinkValue, benchSinkResult = mapping.ExpandPath(virtualPath)
			}
		})
	}
}

// benchActiveMapping derives the mapping of one fixture root and fails the benchmark
// unless the fixture really is an ACTIVE mapping. The check runs before the timed
// region, so it costs the measurement nothing.
func benchActiveMapping(b *testing.B, name, root string) pathvirtualization.Mapping {
	b.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(root)
	if reason != pathvirtualization.SkipReasonNone {
		b.Fatalf("fixture %q is not a usable project root: %v", name, reason)
	}
	if mapping.VirtualRoot == "" {
		b.Fatalf("fixture %q derives no active alias: a short root is not a defect "+
			"(requirement 1.4), so it cannot benchmark the active path", name)
	}
	return mapping
}
