package testfixtures

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestEvidenceMatrixIntegrity(t *testing.T) {
	t.Parallel()
	matrix, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if matrix.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d, want 1", matrix.SchemaVersion)
	}
	if len(matrix.Sources) == 0 || len(matrix.HarnessIdentitySupport) == 0 || len(matrix.Fixtures) == 0 {
		t.Fatal("matrix must contain sources, harness identity support, and fixtures")
	}

	root := repositoryRoot(t)
	sources := make(map[string]Source, len(matrix.Sources))
	for _, source := range matrix.Sources {
		if source.ID == "" || source.Reference == "" || source.Description == "" {
			t.Fatalf("source entry is incomplete: %+v", source)
		}
		if _, duplicate := sources[source.ID]; duplicate {
			t.Fatalf("duplicate source ID %q", source.ID)
		}
		sources[source.ID] = source
		if err := validateSourceReference(root, source.Reference); err != nil {
			t.Errorf("source %q: %v", source.ID, err)
		}
	}

	supportByHarness := make(map[string]HarnessIdentity, len(matrix.HarnessIdentitySupport))
	for _, support := range matrix.HarnessIdentitySupport {
		if support.Harness == "" || support.Reason == "" || len(support.SourceRefs) == 0 {
			t.Errorf("harness support entry is incomplete: %+v", support)
		}
		if _, duplicate := supportByHarness[support.Harness]; duplicate {
			t.Errorf("duplicate harness support entry %q", support.Harness)
		}
		supportByHarness[support.Harness] = support
		if support.Status != IdentityHighConfidence && support.Status != IdentityAmbiguous && support.Status != IdentityUnsupportedByUserAgentAlone {
			t.Errorf("harness %q has unsupported identity status %q", support.Harness, support.Status)
		}
		if support.Status == IdentityHighConfidence && (support.IdentityField == "" || len(support.Examples) == 0) {
			t.Errorf("high-confidence harness %q needs an identity field and evidence examples", support.Harness)
		}
		if support.Status == IdentityHighConfidence && support.IdentityField != "user_agent" {
			t.Errorf("high-confidence harness %q must use the accepted user_agent input field, got %q", support.Harness, support.IdentityField)
		}
		if support.Status == IdentityUnsupportedByUserAgentAlone && support.IdentityField != "user_agent" {
			t.Errorf("unsupported-by-UA harness %q has identity field %q", support.Harness, support.IdentityField)
		}
		validateSourceRefs(t, sources, "harness "+support.Harness, support.SourceRefs)
	}

	fixturesByID := make(map[string]EvidenceFixture, len(matrix.Fixtures))
	for _, fixture := range matrix.Fixtures {
		if fixture.ID == "" || fixture.Reason == "" || fixture.ExpectedClassification == "" || fixture.ExpectedEvidenceCodes == nil {
			t.Errorf("fixture is incomplete: %+v", fixture)
		}
		if _, duplicate := fixturesByID[fixture.ID]; duplicate {
			t.Errorf("duplicate fixture ID %q", fixture.ID)
		}
		fixturesByID[fixture.ID] = fixture
		if fixture.ExpectedClassification != ClassificationUnknown && fixture.ExpectedClassification != ClassificationCodingAgent {
			t.Errorf("fixture %q has invalid expected classification %q", fixture.ID, fixture.ExpectedClassification)
		}
		validateSourceRefs(t, sources, "fixture "+fixture.ID, fixture.SourceRefs)
		for _, signal := range fixture.WeakSignals {
			if !isKnownWeakSignal(signal) {
				t.Errorf("fixture %q has unknown weak-signal label %q", fixture.ID, signal)
			}
		}
		if len(fixture.WeakSignals) > 0 && fixture.WeakPrompt == "" {
			t.Errorf("fixture %q labels weak signals without a replayable synthetic prompt", fixture.ID)
		}
		if contains(fixture.WeakSignals, "model_name") && fixture.ModelName == "" {
			t.Errorf("fixture %q labels a model-name signal without a modelName value", fixture.ID)
		}
		if len(fixture.WeakPrompt) > 500 || len(fixture.ModelName) > 128 {
			t.Errorf("fixture %q exceeds the bounded weak-input fixture size", fixture.ID)
		}
		if fixture.IdentityField != "" && fixture.IdentityField != "user_agent" {
			t.Errorf("fixture %q uses unsupported classification identity field %q", fixture.ID, fixture.IdentityField)
		}

		categories := make(map[aipapi.ToolCategory]struct{})
		for _, tool := range fixture.ToolEvidence {
			if tool.Name == "" || tool.ExpectedCategory == "" {
				t.Errorf("fixture %q has incomplete tool evidence: %+v", fixture.ID, tool)
				continue
			}
			category, _ := aipapi.ClassifyToolName(tool.Name)
			if string(category) != tool.ExpectedCategory {
				t.Errorf("fixture %q tool %q category = %q, want canonical category %q", fixture.ID, tool.Name, category, tool.ExpectedCategory)
			}
			categories[category] = struct{}{}
		}
		validateExpectedEvidenceShape(t, fixture, categories)

		if fixture.Harness != "" {
			support, ok := supportByHarness[fixture.Harness]
			if !ok {
				t.Errorf("fixture %q names harness %q without a support row", fixture.ID, fixture.Harness)
			} else if fixture.IdentityValue != "" && !contains(support.Examples, fixture.IdentityValue) {
				t.Errorf("fixture %q identity value %q is absent from harness %q support examples", fixture.ID, fixture.IdentityValue, fixture.Harness)
			} else if fixture.IdentityField != "" && support.IdentityField != fixture.IdentityField {
				t.Errorf("fixture %q identity field %q does not match harness %q field %q", fixture.ID, fixture.IdentityField, fixture.Harness, support.IdentityField)
			}
		}
	}

	for harness, support := range supportByHarness {
		if support.Status != IdentityHighConfidence {
			continue
		}
		foundPositive := false
		for _, fixture := range fixturesByID {
			if fixture.Harness == harness && fixture.ExpectedClassification == ClassificationCodingAgent {
				foundPositive = true
				break
			}
		}
		if !foundPositive {
			t.Errorf("high-confidence harness %q has no positive fixture", harness)
		}
	}

	for _, requiredID := range requiredFixtureIDs {
		if _, ok := fixturesByID[requiredID]; !ok {
			t.Errorf("required matrix fixture %q is missing", requiredID)
		}
	}
}

func validateExpectedEvidenceShape(t *testing.T, fixture EvidenceFixture, categories map[aipapi.ToolCategory]struct{}) {
	t.Helper()
	has := func(category aipapi.ToolCategory) bool {
		_, ok := categories[category]
		return ok
	}
	for _, code := range fixture.ExpectedEvidenceCodes {
		switch code {
		case "tooling.distinct_coding_cluster":
			if (!has(aipapi.ToolCategoryFileRead) && !has(aipapi.ToolCategoryFileSearch)) ||
				(!has(aipapi.ToolCategoryFileEdit) && !has(aipapi.ToolCategoryFileRemove)) ||
				!has(aipapi.ToolCategoryOSCommand) {
				t.Errorf("fixture %q claims tooling.distinct_coding_cluster without canonical read/search, mutation, and OS-command categories", fixture.ID)
			}
		case "tooling.project_marker_cluster":
			if (!has(aipapi.ToolCategoryFileRead) && !has(aipapi.ToolCategoryFileSearch)) ||
				(!has(aipapi.ToolCategoryFileEdit) && !has(aipapi.ToolCategoryFileRemove) && !has(aipapi.ToolCategoryOSCommand)) {
				t.Errorf("fixture %q claims tooling.project_marker_cluster without canonical read/search and mutation categories", fixture.ID)
			}
			if len(fixture.ProjectMarkers) == 0 || contains(fixture.ProjectMarkers, ".git") {
				t.Errorf("fixture %q claims tooling.project_marker_cluster without a decisive project marker", fixture.ID)
			}
		case "client_family.codex", "client_family.roo", "client_family.opencode", "client_family.pi", "client_family.droid", "client_family.hermes":
			if fixture.ExpectedClassification != ClassificationCodingAgent {
				t.Errorf("fixture %q has positive identity evidence but expected classification %q", fixture.ID, fixture.ExpectedClassification)
			}
		default:
			t.Errorf("fixture %q has unknown expected evidence code %q", fixture.ID, code)
		}
	}
}

func validateSourceRefs(t *testing.T, sources map[string]Source, owner string, refs []string) {
	t.Helper()
	if len(refs) == 0 {
		t.Errorf("%s has no source references", owner)
		return
	}
	for _, ref := range refs {
		if _, ok := sources[ref]; !ok {
			t.Errorf("%s references unknown source ID %q", owner, ref)
		}
	}
}

func validateSourceReference(root, reference string) error {
	parsed, err := url.Parse(reference)
	if err == nil && parsed.Scheme != "" {
		if parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("external reference %q must be an HTTPS URL", reference)
		}
		return nil
	}
	path, _, _ := strings.Cut(reference, "#")
	if path == "" {
		return fmt.Errorf("local reference %q has no path", reference)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
		return fmt.Errorf("local reference %q does not resolve: %w", reference, err)
	}
	return nil
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for current := directory; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatalf("could not locate repository go.mod from %q", directory)
		}
	}
}

func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}

func isKnownWeakSignal(value string) bool {
	switch value {
	case "technical_prose", "technical_words", "source_filename", "markdown_code_fence", "programming_language_name", "model_name", "prompt_compaction_marker", "repository_discussion":
		return true
	default:
		return false
	}
}

var requiredFixtureIDs = []string{
	"codex_stable_identity_positive",
	"roo_stable_identity_positive",
	"opencode_stable_identity_positive",
	"pi_stable_identity_positive",
	"droid_stable_identity_positive",
	"hermes_stable_identity_positive",
	"cline_anthropic_sdk_ua_unknown",
	"cline_openai_sdk_ua_unknown",
	"generic_sdk_with_distinctive_tools_positive",
	"absent_ua_with_distinctive_tools_positive",
	"read_search_edit_without_marker_unknown",
	"git_marker_is_not_decisive_unknown",
	"project_marker_cluster_positive",
	"single_bash_unknown",
	"single_web_search_unknown",
	"single_read_file_unknown",
	"single_grep_unknown",
	"technical_prose_unknown",
	"source_filename_unknown",
	"code_fence_unknown",
	"programming_language_name_unknown",
	"model_name_unknown",
	"prompt_compaction_marker_unknown",
	"cline_attempt_completion_unknown",
	"assistant_action_description_names_unknown",
	"excluded_identity_prospective_unknown",
	"exclusion_preserves_prior_positive",
	"aider_no_stable_ua_unknown",
}
