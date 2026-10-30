package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRetiredPackagesAbsent(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	got, err := ScanRetiredPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 0 {
		t.Fatalf("retired packages resurrected (%d): %v", len(got), got)
	}
	for _, dir := range RetiredPackageDirs {
		p := filepath.Join(root, filepath.FromSlash(dir))
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("retired package directory %s must not exist", dir)
		}
	}
}

func TestRetiredPackages_RenamedOrNestedBypassRejected(t *testing.T) {
	t.Parallel()
	// Test that any production Go file located under a retired package directory,
	// even if renamed or deeply nested, is deterministically rejected.
	cases := []struct {
		rel  string
		want bool
	}{
		{"internal/core/toolcallrepair/engine.go", true},
		{"internal/core/toolcallrepair/renamed_engine.go", true},
		{"internal/core/toolcallrepair/nested/sub/bypass.go", true},
		{"internal/core/toolcallrepair/repair/jsonshape/deep.go", true},
		{"internal/plugins/features/toolcallrepair/repair/engine.go", false},
		{"internal/core/runtime/tool_call_assembler.go", false},
		{"internal/core/secretguard/catalog.go", true},
		{"internal/core/secretguard/renamed_catalog.go", true},
		{"internal/core/secretguard/nested/sub/bypass.go", true},
		{"internal/core/secretguard/engine/deep.go", true},
		{"internal/plugins/features/secretguard/engine/catalog.go", false},
		{"internal/standardplugins/featurehost/secretguard/compose.go", false},
		{"internal/core/compactiondetect/detector.go", true},
		{"internal/core/compactiondetect/renamed_detector.go", true},
		{"internal/core/compactiondetect/nested/sub/bypass.go", true},
		{"internal/infra/compactiondetect/detector.go", false},
		{"internal/core/compactioncontinuity/branch_coordinator.go", true},
		{"internal/core/compactioncontinuity/renamed_coordinator.go", true},
		{"internal/core/compactioncontinuity/nested/sub/bypass.go", true},
		{"internal/plugins/features/compactioncontinuity/state/branch_coordinator.go", false},
		{"internal/core/conversationview/store.go", true},
		{"internal/core/conversationview/renamed_store.go", true},
		{"internal/core/conversationview/nested/sub/bypass.go", true},
		{"internal/infra/conversationview/store.go", false},
		{"internal/core/conversationprojection/projection.go", false},
		{"internal/core/terminaldecisionpolicy/store.go", true},
		{"internal/core/terminaldecisionpolicy/renamed_store.go", true},
		{"internal/core/terminaldecisionpolicy/nested/sub/bypass.go", true},
		{"internal/standardplugins/featurehost/sessionpolicy/store.go", false},
		{"internal/reasoningreplay/eligible.go", true},
		{"internal/infra/reasoningcompose/generation.go", true},
		{"internal/infra/secretguardcompose/compose.go", true},
		{"internal/infra/secretaudit/logger.go", true},
		{"internal/infra/compactioncompose/scheduler.go", true},
	}
	for _, tc := range cases {
		f := ScanFileRetiredPackage(tc.rel)
		if tc.want && f == nil {
			t.Errorf("ScanFileRetiredPackage(%q) = nil, want finding", tc.rel)
		}
		if !tc.want && f != nil {
			t.Errorf("ScanFileRetiredPackage(%q) = %v, want nil", tc.rel, f)
		}
	}
}

func TestForbiddenImports_ToolCallRepairRenamedOrNestedBypassRejected(t *testing.T) {
	t.Parallel()
	// Prove that files in nested subpackages of toolcallrepair or with renamed filenames
	// cannot bypass the recursive import boundary.
	adversarialImports := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "feature root imports core runtime",
			relPath:    "internal/plugins/features/toolcallrepair/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/runtime",
			wantForbid: true,
		},
		{
			name:       "repair subpackage imports core routing",
			relPath:    "internal/plugins/features/toolcallrepair/repair/renamed_repair.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/routing",
			wantForbid: true,
		},
		{
			name:       "deeply nested jsonshape subpackage imports runtimebundle",
			relPath:    "internal/plugins/features/toolcallrepair/repair/jsonshape/nested/deep.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/runtimebundle",
			wantForbid: true,
		},
		{
			name:       "nested subpackage imports frontends",
			relPath:    "internal/plugins/features/toolcallrepair/nested/sub/helper.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses",
			wantForbid: true,
		},
		{
			name:       "nested subpackage imports backends",
			relPath:    "internal/plugins/features/toolcallrepair/nested/sub/helper.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses",
			wantForbid: true,
		},
		{
			name:       "nested subpackage imports sibling feature",
			relPath:    "internal/plugins/features/toolcallrepair/repair/nested/helper.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard",
			wantForbid: true,
		},
		{
			name:       "core runtime imports retired toolcallrepair",
			relPath:    "internal/core/runtime/renamed_assembler.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/toolcallrepair",
			wantForbid: true,
		},
		{
			name:       "nested core imports retired nested toolcallrepair",
			relPath:    "internal/core/runtime/nested/deep.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/toolcallrepair/nested",
			wantForbid: true,
		},
		{
			name:       "feature root imports own repair subpackage allowed",
			relPath:    "internal/plugins/features/toolcallrepair/bundle.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair",
			wantForbid: false,
		},
		{
			name:       "repair imports own jsonshape subpackage allowed",
			relPath:    "internal/plugins/features/toolcallrepair/repair/shape_guard.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair/repair/jsonshape",
			wantForbid: false,
		},
		{
			name:       "feature imports aipapi allowed",
			relPath:    "internal/plugins/features/toolcallrepair/repair/engine.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipapi",
			wantForbid: false,
		},
		{
			name:       "feature imports aipsdk/toolcall allowed",
			relPath:    "internal/plugins/features/toolcallrepair/repair/engine.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall",
			wantForbid: false,
		},
	}
	for _, tc := range adversarialImports {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := fmt.Sprintf("package dummy\nimport _ %q\n", tc.importPath)
			findings, err := ScanFileForbiddenImports(tc.relPath, tc.relPath, []byte(src))
			if err != nil {
				t.Fatalf("ScanFileForbiddenImports error: %v", err)
			}
			if tc.wantForbid && len(findings) == 0 {
				t.Errorf("%s: expected forbidden import finding for %s importing %s, got none", tc.name, tc.relPath, tc.importPath)
			}
			if !tc.wantForbid && len(findings) > 0 {
				t.Errorf("%s: unexpected forbidden import finding for %s importing %s: %v", tc.name, tc.relPath, tc.importPath, findings)
			}
		})
	}
}

func TestForbiddenImports_SecretGuardRenamedOrNestedBypassRejected(t *testing.T) {
	t.Parallel()
	// Prove that files in nested subpackages of secretguard or with renamed filenames
	// cannot bypass the recursive import boundary.
	adversarialImports := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "feature root imports core runtime",
			relPath:    "internal/plugins/features/secretguard/renamed.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/runtime",
			wantForbid: true,
		},
		{
			name:       "engine subpackage imports core routing",
			relPath:    "internal/plugins/features/secretguard/engine/renamed_matcher.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/routing",
			wantForbid: true,
		},
		{
			name:       "deeply nested subpackage imports runtimebundle",
			relPath:    "internal/plugins/features/secretguard/engine/nested/deep.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/runtimebundle",
			wantForbid: true,
		},
		{
			name:       "nested subpackage imports frontends",
			relPath:    "internal/plugins/features/secretguard/nested/sub/helper.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses",
			wantForbid: true,
		},
		{
			name:       "nested subpackage imports backends",
			relPath:    "internal/plugins/features/secretguard/nested/sub/helper.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses",
			wantForbid: true,
		},
		{
			name:       "nested subpackage imports sibling feature",
			relPath:    "internal/plugins/features/secretguard/engine/nested/helper.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/toolcallrepair",
			wantForbid: true,
		},
		{
			name:       "core runtime imports retired secretguard",
			relPath:    "internal/core/runtime/renamed_guard.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/secretguard",
			wantForbid: true,
		},
		{
			name:       "nested core imports retired nested secretguard",
			relPath:    "internal/core/runtime/nested/deep.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/secretguard/nested",
			wantForbid: true,
		},
		{
			name:       "feature root imports own engine subpackage allowed",
			relPath:    "internal/plugins/features/secretguard/guard.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine",
			wantForbid: false,
		},
		{
			name:       "feature imports aipapi allowed",
			relPath:    "internal/plugins/features/secretguard/guard.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipapi",
			wantForbid: false,
		},
		{
			name:       "feature imports aipsdk/secretguard allowed",
			relPath:    "internal/plugins/features/secretguard/engine/matcher.go",
			importPath: "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard",
			wantForbid: false,
		},
	}
	for _, tc := range adversarialImports {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := fmt.Sprintf("package dummy\nimport _ %q\n", tc.importPath)
			findings, err := ScanFileForbiddenImports(tc.relPath, tc.relPath, []byte(src))
			if err != nil {
				t.Fatalf("ScanFileForbiddenImports error: %v", err)
			}
			if tc.wantForbid && len(findings) == 0 {
				t.Errorf("%s: expected forbidden import finding for %s importing %s, got none", tc.name, tc.relPath, tc.importPath)
			}
			if !tc.wantForbid && len(findings) > 0 {
				t.Errorf("%s: unexpected forbidden import finding for %s importing %s: %v", tc.name, tc.relPath, tc.importPath, findings)
			}
		})
	}
}

func TestForbiddenImports_CompactionDetectRenamedOrNestedBypassRejected(t *testing.T) {
	t.Parallel()
	adversarialImports := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "core runtime imports retired compactiondetect",
			relPath:    "internal/core/runtime/renamed_detector.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/compactiondetect",
			wantForbid: true,
		},
		{
			name:       "nested core imports retired nested compactiondetect",
			relPath:    "internal/core/runtime/nested/deep.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/compactiondetect/nested",
			wantForbid: true,
		},
		{
			name:       "infra compactiondetect allowed",
			relPath:    "internal/infra/runtimebundle/background_aux_lifecycle.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/compactiondetect",
			wantForbid: false,
		},
	}
	for _, tc := range adversarialImports {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := fmt.Sprintf("package dummy\nimport _ %q\n", tc.importPath)
			findings, err := ScanFileForbiddenImports(tc.relPath, tc.relPath, []byte(src))
			if err != nil {
				t.Fatalf("ScanFileForbiddenImports error: %v", err)
			}
			if tc.wantForbid && len(findings) == 0 {
				t.Errorf("%s: expected forbidden import finding for %s importing %s, got none", tc.name, tc.relPath, tc.importPath)
			}
			if !tc.wantForbid && len(findings) > 0 {
				t.Errorf("%s: unexpected forbidden import finding for %s importing %s: %v", tc.name, tc.relPath, tc.importPath, findings)
			}
		})
	}
}

func TestForbiddenImports_CompactionContinuityRenamedOrNestedBypassRejected(t *testing.T) {
	t.Parallel()
	adversarialImports := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "core runtime imports retired compactioncontinuity",
			relPath:    "internal/core/runtime/renamed_coordinator.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/compactioncontinuity",
			wantForbid: true,
		},
		{
			name:       "nested core imports retired nested compactioncontinuity",
			relPath:    "internal/core/runtime/nested/deep.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/compactioncontinuity/nested",
			wantForbid: true,
		},
		{
			name:       "plugins features compactioncontinuity state allowed",
			relPath:    "internal/standardplugins/featurehost/process.go",
			importPath: "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/state",
			wantForbid: false,
		},
	}
	for _, tc := range adversarialImports {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := fmt.Sprintf("package dummy\nimport _ %q\n", tc.importPath)
			findings, err := ScanFileForbiddenImports(tc.relPath, tc.relPath, []byte(src))
			if err != nil {
				t.Fatalf("ScanFileForbiddenImports error: %v", err)
			}
			if tc.wantForbid && len(findings) == 0 {
				t.Errorf("%s: expected forbidden import finding for %s importing %s, got none", tc.name, tc.relPath, tc.importPath)
			}
			if !tc.wantForbid && len(findings) > 0 {
				t.Errorf("%s: unexpected forbidden import finding for %s importing %s: %v", tc.name, tc.relPath, tc.importPath, findings)
			}
		})
	}
}

func TestForbiddenImports_ConversationViewRenamedOrNestedBypassRejected(t *testing.T) {
	t.Parallel()
	adversarialImports := []struct {
		name       string
		relPath    string
		importPath string
		wantForbid bool
	}{
		{
			name:       "core runtime imports retired conversationview",
			relPath:    "internal/core/runtime/renamed_cv.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/conversationview",
			wantForbid: true,
		},
		{
			name:       "nested core imports retired nested conversationview",
			relPath:    "internal/core/runtime/nested/deep.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/conversationview/nested",
			wantForbid: true,
		},
		{
			name:       "conversationprojection core imports infra forbidden",
			relPath:    "internal/core/conversationprojection/projection.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/conversationview",
			wantForbid: true,
		},
		{
			name:       "infra conversationview allowed",
			relPath:    "internal/standardplugins/featurehost/conversation.go",
			importPath: "github.com/aiproxer/aiproxer/internal/infra/conversationview",
			wantForbid: false,
		},
		{
			name:       "core conversationprojection allowed",
			relPath:    "internal/core/runtime/conversation_view.go",
			importPath: "github.com/aiproxer/aiproxer/internal/core/conversationprojection",
			wantForbid: false,
		},
	}
	for _, tc := range adversarialImports {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := fmt.Sprintf("package dummy\nimport _ %q\n", tc.importPath)
			findings, err := ScanFileForbiddenImports(tc.relPath, tc.relPath, []byte(src))
			if err != nil {
				t.Fatalf("ScanFileForbiddenImports error: %v", err)
			}
			if tc.wantForbid && len(findings) == 0 {
				t.Errorf("%s: expected forbidden import finding for %s importing %s, got none", tc.name, tc.relPath, tc.importPath)
			}
			if !tc.wantForbid && len(findings) > 0 {
				t.Errorf("%s: unexpected forbidden import finding for %s importing %s: %v", tc.name, tc.relPath, tc.importPath, findings)
			}
		})
	}
}
