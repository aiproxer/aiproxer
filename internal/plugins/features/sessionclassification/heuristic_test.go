package sessionclassification_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/agentfacts"
	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification/testfixtures"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestEvaluateLocalEvidenceMatrix(t *testing.T) {
	t.Parallel()

	matrix, err := testfixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(matrix.Fixtures) != 28 {
		t.Fatalf("fixture matrix has %d rows, want frozen 28-row acceptance matrix", len(matrix.Fixtures))
	}
	for _, fixture := range matrix.Fixtures {
		t.Run(fixture.ID, func(t *testing.T) {
			t.Parallel()
			input := fixtureInput(fixture)
			cfg := sessionclassification.Config{
				Mode: sessionclassification.ModeHeuristic,
				Heuristic: sessionclassification.HeuristicConfig{
					IgnoredUserAgentPrefixes: append([]string(nil), fixture.IgnoredUserAgentPrefixes...),
				},
			}
			got := sessionclassification.EvaluateLocal(cfg, input)
			wantPositive := fixture.ExpectedClassification == testfixtures.ClassificationCodingAgent
			gotPositive := got.Promotes || got.Preserves
			if gotPositive != wantPositive {
				t.Fatalf("local result promotes=%t preserves=%t, want positive=%t; evidence=%q", got.Promotes, got.Preserves, wantPositive, got.EvidenceCode)
			}
			if got.Promotes && got.Preserves {
				t.Fatalf("local result both promotes and preserves: %+v", got)
			}
			if len(fixture.ExpectedEvidenceCodes) > 1 {
				t.Fatalf("fixture %q has %d expected codes; task 3.2 expects one decisive code at most", fixture.ID, len(fixture.ExpectedEvidenceCodes))
			}
			var wantCode session.EvidenceCode
			if len(fixture.ExpectedEvidenceCodes) == 1 {
				wantCode = session.EvidenceCode(fixture.ExpectedEvidenceCodes[0])
			}
			if got.EvidenceCode != wantCode {
				t.Fatalf("evidence code = %q, want %q", got.EvidenceCode, wantCode)
			}
			switch {
			case strings.HasPrefix(string(wantCode), "client_family.") && got.Source != session.SourceLocalIdentity:
				t.Fatalf("identity source = %q, want %q", got.Source, session.SourceLocalIdentity)
			case strings.HasPrefix(string(wantCode), "tooling.") && got.Source != session.SourceLocalTooling:
				t.Fatalf("tooling source = %q, want %q", got.Source, session.SourceLocalTooling)
			}
		})
	}
}

func TestEvaluateLocalModePromotionPolicy(t *testing.T) {
	t.Parallel()

	input := sdkclassification.Input{Evidence: sdkclassification.Evidence{ClientUserAgent: "codex_cli_rs/1.2.3"}}
	cases := []struct {
		name        string
		mode        sessionclassification.Mode
		wantPromote bool
	}{
		{name: "heuristic may promote", mode: sessionclassification.ModeHeuristic, wantPromote: true},
		{name: "jev only constructs evidence", mode: sessionclassification.ModeJev, wantPromote: false},
		{name: "hybrid may promote locally", mode: sessionclassification.ModeHybrid, wantPromote: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := decodeConfig(t, configForMode(tc.mode))
			got := sessionclassification.EvaluateLocal(cfg, input)
			if got.Promotes != tc.wantPromote {
				t.Fatalf("Promotes = %t, want %t", got.Promotes, tc.wantPromote)
			}
			if got.EvidenceCode != "client_family.codex" || got.ClientFamily != agentfacts.FamilyCodex {
				t.Fatalf("bounded identity evidence = (%q, %q), want codex family evidence", got.EvidenceCode, got.ClientFamily)
			}
			if got.Source != session.SourceLocalIdentity {
				t.Fatalf("identity source = %q, want %q", got.Source, session.SourceLocalIdentity)
			}
		})
	}
}

func TestEvaluateLocalPreservesOnlyValidPriorPositive(t *testing.T) {
	t.Parallel()

	input := sdkclassification.Input{
		Session: session.SessionView{Classification: session.Classification{
			Kind:       session.KindCodingAgent,
			Source:     session.SourceLocalIdentity,
			Confidence: session.ConfidenceHigh,
			Evidence:   "client_family.codex",
			Revision:   1,
		}},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "codex_cli_rs/1.2.3"},
	}
	for _, mode := range []sessionclassification.Mode{sessionclassification.ModeHeuristic, sessionclassification.ModeJev, sessionclassification.ModeHybrid} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := sessionclassification.Config{
				Mode:      mode,
				Heuristic: sessionclassification.HeuristicConfig{IgnoredUserAgentPrefixes: []string{"codex_cli_rs/"}},
			}
			got := sessionclassification.EvaluateLocal(cfg, input)
			if !got.Preserves || got.Promotes {
				t.Fatalf("valid prior positive result = %+v, want preserved without new promotion", got)
			}
			if got.EvidenceCode != "client_family.codex" || got.Source != session.SourceLocalIdentity {
				t.Fatalf("preserved result = %+v, want prior bounded evidence/source", got)
			}
		})
	}

	input.Session.Classification.Revision = 0
	got := sessionclassification.EvaluateLocal(sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, input)
	if got.Preserves || !got.Promotes || got.EvidenceCode != "client_family.codex" {
		t.Fatalf("malformed prior state did not fall back to current evidence: %+v", got)
	}
	input.Evidence.ClientUserAgent = ""
	for _, mode := range []sessionclassification.Mode{sessionclassification.ModeHeuristic, sessionclassification.ModeJev, sessionclassification.ModeHybrid} {
		got = sessionclassification.EvaluateLocal(sessionclassification.Config{Mode: mode}, input)
		if got.Preserves || got.Promotes || got.EvidenceCode != "" {
			t.Fatalf("invalid prior positive was accepted in %q mode: %+v", mode, got)
		}
	}
}

func TestEvaluateLocalRuleCUsesBoundedBasenameAndSuffixMarkers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		marker string
		want   bool
	}{
		{name: "go module exact basename", marker: "go.mod", want: true},
		{name: "case insensitive project file", marker: "MYAPP.CSPROJ", want: true},
		{name: "generic git marker", marker: ".git", want: false},
		{name: "path is not a marker", marker: "/repo/go.mod", want: false},
		{name: "parent path is not a marker", marker: "../go.mod", want: false},
		{name: "embedded filename is not a marker", marker: "repo-go.mod-backup", want: false},
		{name: "suffix requires complete basename", marker: ".csproj", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := toolEvidenceInput("read_file", "search_files", "write_file")
			input.Workspace.Markers = []string{tc.marker}
			got := sessionclassification.EvaluateLocal(sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, input)
			if got.Promotes != tc.want {
				t.Fatalf("Promotes = %t, want %t (evidence %q)", got.Promotes, tc.want, got.EvidenceCode)
			}
			if tc.want && (got.EvidenceCode != "tooling.project_marker_cluster" || got.Source != session.SourceLocalTooling) {
				t.Fatalf("positive marker result = %+v, want local tooling evidence", got)
			}
		})
	}
}

func TestEvaluateLocalRuleCIncludesOSCommandAsMutation(t *testing.T) {
	t.Parallel()

	input := toolEvidenceInput("read_file", "execute_command")
	input.Workspace.Markers = []string{"package.json"}
	got := sessionclassification.EvaluateLocal(sessionclassification.Config{Mode: sessionclassification.ModeHybrid}, input)
	if !got.Promotes || got.EvidenceCode != "tooling.project_marker_cluster" || got.Source != session.SourceLocalTooling {
		t.Fatalf("read plus command and project marker = %+v, want Rule C positive", got)
	}
}

func TestEvaluateLocalJevNeverPromotesToolEvidence(t *testing.T) {
	t.Parallel()

	cfg := decodeConfig(t, validRemoteConfig(sessionclassification.ModeJev))
	input := toolEvidenceInput("read_file", "search_files", "write_file", "execute_command")
	got := sessionclassification.EvaluateLocal(cfg, input)
	if got.Promotes || got.Preserves || got.EvidenceCode != "tooling.distinct_coding_cluster" {
		t.Fatalf("jev local evaluation = %+v, want evidence without promotion", got)
	}
}

func TestEvaluateLocalBoundsWorkspaceMarkerInspection(t *testing.T) {
	t.Parallel()

	markers := make([]string, sessionclassification.MaxWorkspaceMarkers+1)
	for i := range markers {
		markers[i] = "unrecognized-marker"
	}
	markers[len(markers)-1] = "go.mod"
	input := toolEvidenceInput("read_file", "write_file")
	input.Workspace.Markers = markers
	got := sessionclassification.EvaluateLocal(sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, input)
	if got.Promotes {
		t.Fatalf("marker after the bounded inspection window promoted: %+v", got)
	}

	input.Workspace.Markers = []string{strings.Repeat("a", sessionclassification.MaxWorkspaceMarkerBytes) + ".csproj"}
	got = sessionclassification.EvaluateLocal(sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, input)
	if got.Promotes {
		t.Fatalf("oversized marker promoted: %+v", got)
	}
}

func TestEvaluateLocalExclusionIsLiteralAndCaseInsensitive(t *testing.T) {
	t.Parallel()

	input := sdkclassification.Input{Evidence: sdkclassification.Evidence{ClientUserAgent: "CoDeX_Cli_Rs/1.2.3"}}
	cfg := decodeConfig(t, "heuristic:\n  ignored_user_agent_prefixes: ['codex_cli_rs/']\n")
	if got := sessionclassification.EvaluateLocal(cfg, input); got.Promotes || got.EvidenceCode != "" {
		t.Fatalf("case-folded exclusion did not block Rule A: %+v", got)
	}

	cfg = decodeConfig(t, "heuristic:\n  ignored_user_agent_prefixes: ['codex.*']\n")
	if got := sessionclassification.EvaluateLocal(cfg, input); !got.Promotes || got.EvidenceCode != "client_family.codex" {
		t.Fatalf("exclusion was not treated as a literal prefix: %+v", got)
	}
}

func fixtureInput(fixture testfixtures.EvidenceFixture) sdkclassification.Input {
	input := sdkclassification.Input{
		Evidence:  sdkclassification.Evidence{ClientUserAgent: fixture.IdentityValue},
		Workspace: workspace.WorkspaceView{Markers: append([]string(nil), fixture.ProjectMarkers...)},
	}
	for _, tool := range fixture.ToolEvidence {
		input.Evidence.ToolCategories = input.Evidence.ToolCategories.AddToolName(tool.Name)
	}
	if fixture.PriorClassification == testfixtures.ClassificationCodingAgent {
		input.Session.Classification = session.Classification{
			Kind:       session.KindCodingAgent,
			Source:     session.SourceLocalIdentity,
			Confidence: session.ConfidenceHigh,
			Evidence:   "client_family.codex",
			Revision:   1,
		}
	}
	return input
}

func toolEvidenceInput(names ...string) sdkclassification.Input {
	var categories sdkclassification.ToolCategorySet
	for _, name := range names {
		categories = categories.AddToolName(name)
	}
	return sdkclassification.Input{Evidence: sdkclassification.Evidence{ToolCategories: categories}}
}

func configForMode(mode sessionclassification.Mode) string {
	if mode == sessionclassification.ModeHeuristic {
		return "mode: heuristic\n"
	}
	return validRemoteConfig(mode)
}
