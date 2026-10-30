package sessionclassification_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/agentfacts"
	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"gopkg.in/yaml.v3"
)

func TestEvaluateLocalWeakEvidenceDoesNotAccumulateAcrossSameHint(t *testing.T) {
	t.Parallel()

	sessionView := session.SessionView{ClientSessionHint: "reused-client-hint"}
	weakTurns := []sdkclassification.Input{
		toolEvidenceInput("read_file"),
		toolEvidenceInput("search_files"),
		toolEvidenceInput("write_file"),
		toolEvidenceInput("execute_command"),
		toolEvidenceInput("read_file", "write_file"),
	}
	for i := range weakTurns {
		weakTurns[i].Session = sessionView
	}

	cfg := sessionclassification.Config{Mode: sessionclassification.ModeHybrid}
	for repeat := range 128 {
		for _, input := range weakTurns {
			got := sessionclassification.EvaluateLocal(cfg, input)
			if got != (sessionclassification.LocalDecision{}) {
				t.Fatalf("weak turn %d/%d accumulated or promoted evidence: %+v", repeat, input.Evidence.ToolCategories, got)
			}
		}
	}

	// A complete cluster on one current turn remains decisive; only cross-turn
	// accumulation of the separate weak signals is forbidden.
	got := sessionclassification.EvaluateLocal(cfg, toolEvidenceInput("read_file", "write_file", "execute_command"))
	if !got.Promotes || got.EvidenceCode != "tooling.distinct_coding_cluster" {
		t.Fatalf("same-turn distinctive cluster = %+v, want positive local evidence", got)
	}
}

func TestRejectedClientUserAgentCaptureCannotBecomeIdentityEvidence(t *testing.T) {
	t.Parallel()

	inputs := []string{
		strings.Repeat("x", identity.MaxUserAgentBytes+1),
		"codex_cli_rs/\x001.2.3",
		"codex_cli_rs/1.2.3\u0085noise",
	}
	for _, raw := range inputs {
		accepted, ok := identity.AcceptClientUserAgent(raw)
		if ok || accepted != "" {
			t.Fatalf("AcceptClientUserAgent(%q) = (%q, %t), want rejected empty evidence", raw, accepted, ok)
		}
		got := sessionclassification.EvaluateLocal(
			sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
			sdkclassification.Input{Evidence: sdkclassification.Evidence{ClientUserAgent: accepted}},
		)
		if got.Promotes || got.EvidenceCode != "" {
			t.Fatalf("rejected identity capture produced a local match: %+v", got)
		}
	}
}

func TestToolNameExplosionKeepsFixedCategoriesAndUnknownAliasesWeak(t *testing.T) {
	t.Parallel()

	var unknown sdkclassification.ToolCategorySet
	for _, alias := range []string{"readFile", "searchFile", "write-file", "executeCommand", "shell", "codex"} {
		category, _ := aipapi.ClassifyToolName(alias)
		if category != aipapi.ToolCategoryUnknown {
			t.Fatalf("alias %q is no longer unknown in the canonical taxonomy: %q", alias, category)
		}
		unknown = unknown.AddToolName(alias)
	}
	for i := range 10_000 {
		unknown = unknown.AddToolName(fmt.Sprintf("vendor-tool-%d", i))
	}
	if unknown != sdkclassification.ToolCategoryUnknownSeen {
		t.Fatalf("10,000 unknown names produced categories %#x, want only unknown-seen", unknown)
	}
	got := sessionclassification.EvaluateLocal(
		sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
		sdkclassification.Input{Evidence: sdkclassification.Evidence{ToolCategories: unknown}},
	)
	if got != (sessionclassification.LocalDecision{}) {
		t.Fatalf("unknown tool-name explosion became coding evidence: %+v", got)
	}

	// Unknown aliases remain noise when a real canonical cluster is present.
	cluster := unknown
	for _, name := range []string{"read_file", "search_files", "write_file", "execute_command"} {
		cluster = cluster.AddToolName(name)
	}
	got = sessionclassification.EvaluateLocal(
		sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
		sdkclassification.Input{Evidence: sdkclassification.Evidence{ToolCategories: cluster}},
	)
	if !got.Promotes || got.EvidenceCode != "tooling.distinct_coding_cluster" {
		t.Fatalf("known cluster with unknown-name noise = %+v, want the canonical positive", got)
	}
}

func FuzzAcceptedClientUserAgentEvaluationIsBounded(f *testing.F) {
	f.Add("codex_cli_rs/1.2.3")
	f.Add("  CoDeX_Cli_Rs/1.2.3  ")
	f.Add(strings.Repeat("x", identity.MaxUserAgentBytes+1) + "codex_cli_rs/1.2.3")
	f.Add("codex_cli_rs/\x001.2.3")
	f.Add("codex_cli_rs/1.2.3\u0085noise")
	f.Add("écodex_cli_rs/1.2.3")
	f.Add("Anthropic/JS")

	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 2048 {
			t.Skip()
		}
		accepted, ok := identity.AcceptClientUserAgent(raw)
		if !ok {
			accepted = ""
		}
		if len(accepted) > identity.MaxUserAgentBytes {
			t.Fatalf("accepted User-Agent is %d bytes, over the capture bound", len(accepted))
		}

		got := sessionclassification.EvaluateLocal(
			sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
			sdkclassification.Input{Evidence: sdkclassification.Evidence{ClientUserAgent: accepted}},
		)
		if !ok && (got.Promotes || got.EvidenceCode != "") {
			t.Fatalf("rejected User-Agent %q became evidence after capture: %+v", raw, got)
		}
		if got.Promotes {
			match, matched := agentfacts.MatchIdentity(accepted)
			if !matched || got.Source != session.SourceLocalIdentity || got.ClientFamily != match.Family || got.EvidenceCode == "" {
				t.Fatalf("accepted User-Agent produced evidence outside the bounded identity catalog: input=%q decision=%+v match=%+v matched=%t", accepted, got, match, matched)
			}
		}
		if got.Preserves {
			t.Fatalf("empty prior state was unexpectedly preserved: %+v", got)
		}
	})
}

func FuzzDecodeIgnoredUserAgentPrefixBounds(f *testing.F) {
	f.Add("  CoDeX_CLi_Rs/  ", 1)
	f.Add(strings.Repeat("x", sessionclassification.MaxIgnoredUserAgentPrefixBytes), 1)
	f.Add(strings.Repeat("x", sessionclassification.MaxIgnoredUserAgentPrefixBytes+1), 1)
	f.Add(strings.Repeat("\u023a", sessionclassification.MaxIgnoredUserAgentPrefixBytes/2), 1)
	f.Add("\x00", 1)
	f.Add("codex_cli_rs/", sessionclassification.MaxIgnoredUserAgentPrefixes+1)
	f.Add("é", 1)

	f.Fuzz(func(t *testing.T, prefix string, count int) {
		if len(prefix) > sessionclassification.MaxIgnoredUserAgentPrefixBytes*4 || count < 0 || count > sessionclassification.MaxIgnoredUserAgentPrefixes+1 {
			return
		}
		prefixes := make([]string, count)
		for i := range prefixes {
			prefixes[i] = prefix
		}
		encoded, err := yaml.Marshal(map[string]any{
			"heuristic": map[string]any{"ignored_user_agent_prefixes": prefixes},
		})
		if err != nil {
			return
		}
		var node yaml.Node
		if err := yaml.Unmarshal(encoded, &node); err != nil {
			t.Fatalf("yaml.Unmarshal of encoded fuzz input: %v", err)
		}
		cfg, err := sessionclassification.DecodeConfig(node)
		if count > sessionclassification.MaxIgnoredUserAgentPrefixes && err == nil {
			t.Fatalf("DecodeConfig accepted %d ignored prefixes, over the count bound", count)
		}
		if err != nil {
			return
		}
		if len(cfg.Heuristic.IgnoredUserAgentPrefixes) > sessionclassification.MaxIgnoredUserAgentPrefixes {
			t.Fatalf("decoded %d ignored prefixes, over the count bound", len(cfg.Heuristic.IgnoredUserAgentPrefixes))
		}
		for _, decoded := range cfg.Heuristic.IgnoredUserAgentPrefixes {
			if decoded == "" || len(decoded) > sessionclassification.MaxIgnoredUserAgentPrefixBytes || !utf8.ValidString(decoded) || strings.TrimSpace(decoded) != decoded || strings.ToLower(decoded) != decoded {
				t.Fatalf("decoded exclusion is not normalized and bounded: %q (%d bytes)", decoded, len(decoded))
			}
			for _, r := range decoded {
				if unicode.IsControl(r) {
					t.Fatalf("decoded exclusion contains control rune %U", r)
				}
			}
		}
	})
}

func FuzzEvaluateLocalWorkspaceMarkerBounds(f *testing.F) {
	f.Add("unknown-marker", sessionclassification.MaxWorkspaceMarkers+1)
	f.Add(strings.Repeat("x", sessionclassification.MaxWorkspaceMarkerBytes+1)+".csproj", 1)
	f.Add("Go.MoD", 1)
	f.Add("name\x00.csproj", 1)
	f.Add("/workspace/go.mod", 1)
	f.Add("égo.mod", 1)

	f.Fuzz(func(t *testing.T, marker string, count int) {
		if len(marker) > sessionclassification.MaxWorkspaceMarkerBytes*4 || count < 0 || count > sessionclassification.MaxWorkspaceMarkers+1 {
			return
		}
		markers := make([]string, count)
		if count > sessionclassification.MaxWorkspaceMarkers {
			for i := range markers {
				markers[i] = fmt.Sprintf("unrecognized-marker-%d", i)
			}
			markers[sessionclassification.MaxWorkspaceMarkers] = "go.mod"
		} else if count > 0 {
			markers[0] = marker
		}

		input := toolEvidenceInput("read_file", "write_file")
		input.Workspace.Markers = markers
		got := sessionclassification.EvaluateLocal(
			sessionclassification.Config{Mode: sessionclassification.ModeHeuristic}, input,
		)
		if count > sessionclassification.MaxWorkspaceMarkers && got.Promotes {
			t.Fatalf("marker after the bounded inspection window promoted: %+v", got)
		}
		invalid := len(marker) > sessionclassification.MaxWorkspaceMarkerBytes || !utf8.ValidString(marker) || strings.ContainsAny(marker, "/\\") || hasControlRune(marker)
		if invalid && count > 0 && count <= sessionclassification.MaxWorkspaceMarkers && got.Promotes {
			t.Fatalf("invalid or oversized marker promoted: %q -> %+v", marker, got)
		}
		if len(got.EvidenceCode) > session.MaxEvidenceCodeBytes {
			t.Fatalf("decision retained an oversized evidence code: %q", got.EvidenceCode)
		}
	})
}

func FuzzUnknownToolAliasesStayUnknown(f *testing.F) {
	f.Add("readFile")
	f.Add("searchFile")
	f.Add("write-file")
	f.Add("shell")
	f.Add("codex")
	f.Add("unknown vendor tool")

	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 4096 {
			t.Skip()
		}
		category, _ := aipapi.ClassifyToolName(name)
		if category != aipapi.ToolCategoryUnknown {
			return
		}
		categories := sdkclassification.ToolCategorySet(0).AddToolName(name)
		if categories != sdkclassification.ToolCategoryUnknownSeen {
			t.Fatalf("canonical unknown alias %q accumulated category bits %#x", name, categories)
		}
		got := sessionclassification.EvaluateLocal(
			sessionclassification.Config{Mode: sessionclassification.ModeHeuristic},
			sdkclassification.Input{Evidence: sdkclassification.Evidence{ToolCategories: categories}},
		)
		if got != (sessionclassification.LocalDecision{}) {
			t.Fatalf("canonical unknown alias %q became coding evidence: %+v", name, got)
		}
	})
}

func hasControlRune(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
