package featurehost

// Task 10.2 false-positive certification.
//
// A false-positive suite is only worth its ability to fail, so this file is built
// around three instruments that make a vacuous PASS impossible:
//
//  1. Every fixture's evidence is observed FROM THE RUNTIME, not from the test's
//     own declaration. The submit hook receives the same working call the
//     classification stage read, so the recorded client User-Agent, tool names
//     and delivered user text are exactly what the production heuristic saw. The
//     completeness census then REJECTS a run in which two fixtures the suite
//     distinguishes were delivered the same evidence, and REJECTS a fixture
//     whose declared weak signal never reached the turn. Task 10.1 was rejected
//     for exactly that gap: six rows carried byte-identical generic filler while
//     logging PASS.
//
//  2. Every negative's reason for staying unknown is MEASURED, not inferred. The
//     production decision function is evaluated on the delivered evidence and
//     must report no promotion at all, and the same authoritative session is then
//     re-served with ONE independent decisive rule added to the very same turn
//     evidence. A negative that stayed unknown because nothing could ever promote
//     it would fail the pairing; a negative that stayed unknown for a wrong reason
//     fails the no-decisive-rule measurement.
//
//  3. "No durable negative classification" is proven at the TABLE, by raw SQL
//     over every row the feature wrote, and per session through an independent
//     store instance. A row with an empty or otherwise non-positive kind is
//     reported as a row that must not exist.
//
// Nothing here re-implements a classification rule, a tool category, a store, or
// a wire profile. Requirements 1.2, 3.2, 3.4-3.8, 12.2 and 12.3 are certified
// against the real composed surfaces: a real featurehost process, a real compiled
// generation whose classifier is the production one, the real generic-core
// executor on both the canonical and the large-payload wire lane, and the
// production durable SQLite store.
//
// The remote half of requirement 6.8 (a below-threshold or failed remote decision
// must not persist a negative classification) needs a hermetic decider, so it lives
// with the durable store it writes to:
// internal/standardplugins/featurehost/sessionclassification/negative_durable_certification_test.go.

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/uptrace/bun"
)

// ---------------------------------------------------------------------------
// Fixture table
// ---------------------------------------------------------------------------

// falsePositiveFillerPrompt is the generic prompt a naive suite would deliver to
// every row. No fixture that declares a technical-chat signal may receive it:
// delivering filler to a code-fence row means no classifier that promoted on the
// fence could ever be observed promoting.
const falsePositiveFillerPrompt = "summarize the diff"

// falsePositiveExclusionPrefixes is the operator-configured ignored identity
// prefix the exclusion-shaped fixtures run under (requirements 3.7, 8.1).
var falsePositiveExclusionPrefixes = []string{"codex_cli_rs/"}

// decisiveRule is the INDEPENDENT decisive rule requirement 12.2 requires a
// technical-chat fixture to be paired with. It is expressed as bounded evidence
// only: a client identity, canonical tool names, and resolved workspace markers.
// No fixture supplies a prompt to the pairing turn beyond its own, so the prompt
// that the negative carried is byte-identical on the positive turn.
type decisiveRule struct {
	Name     string
	Identity string
	Tools    []string
	Markers  []string
	Code     session.EvidenceCode
	Source   session.ClassificationSource
}

// falsePositiveCase is one technical-chat or ambiguous-client negative fixture.
//
// The frozen task-1.2 matrix is not edited by this task: these rows live in the
// acceptance suite because 10.2 needs two dimensions the matrix has no field for,
// namely the independent decisive rule a pairing turn must satisfy and the
// expected decisive source of the paired positive.
type falsePositiveCase struct {
	ID       string
	Shape    string
	Covers   string
	Identity string
	Tools    []string
	Markers  []string
	Prompt   string
	// Route is the request's route selector. It exists because requirement 3.6 names
	// a backend/route identity commonly used by coding agents as a weak signal, and
	// the classifier input deliberately carries no route member: the fixture proves
	// the absent member is why such a selector changes nothing.
	Route string
	// IgnoredPrefixes, when non-empty, serves the fixture under a reloaded
	// generation carrying exactly these configured exclusion prefixes.
	IgnoredPrefixes []string
	// Decisive is the independent decisive rule this fixture must become positive
	// under once it alone is added to the same turn evidence. It is the substance
	// of requirement 12.2's "unless an independent decisive rule is also
	// satisfied".
	Decisive *decisiveRule
}

// falsePositiveDefaultRoute is the ordinary selector every fixture but the route
// fixture uses, so the selector itself never distinguishes two fixtures by accident.
const falsePositiveDefaultRoute = "only:model"

// falsePositiveRoute returns the fixture's route selector.
func falsePositiveRoute(c falsePositiveCase) string {
	if c.Route != "" {
		return c.Route
	}
	return falsePositiveDefaultRoute
}

// falsePositivePromptFor is the single selector for which prompt a fixture
// delivers, so the delivery census and the served turn cannot disagree.
func falsePositivePromptFor(c falsePositiveCase) string {
	if c.Prompt != "" {
		return c.Prompt
	}
	return falsePositiveFillerPrompt
}

// falsePositiveSignature is exactly the evidence a fixture puts in front of the
// classifier. It is computed from the DECLARED fixture here and, separately, from
// the RUNTIME capture in the delivery census; the census compares the two, so a
// declaration that was never delivered fails loudly.
func falsePositiveSignature(c falsePositiveCase) string {
	return fmt.Sprintf("ua=%q ignored=%v route=%q markers=%v tools=%v prompt=%q",
		c.Identity, c.IgnoredPrefixes, falsePositiveRoute(c), c.Markers, c.Tools,
		falsePositivePromptFor(c))
}

// falsePositiveDistinctCluster and friends are the canonical tool-name sets used
// by the pairings. The names, not the categories, are what a turn carries, so the
// canonical classifier must derive the categories itself (requirements 3.9, 11.2).
func falsePositiveTools(names ...string) []string { return names }

var (
	// falsePositiveRuleBCluster satisfies design.md's Rule B on its own: canonical
	// file read/search plus a local mutation plus an OS command, with no workspace
	// marker and no client identity.
	falsePositiveRuleBCluster = falsePositiveTools("Read", "Grep", "Edit", "Bash")
	// falsePositiveWeakCluster is the weaker corroborated shape Rule C accepts: it
	// needs the recognized marker below before it can promote.
	falsePositiveWeakCluster = falsePositiveTools("Read", "Grep", "Edit")
	// falsePositiveRuleCMarker is a recognized build marker (design.md Rule C).
	falsePositiveRuleCMarker = []string{"go.mod"}
)

// falsePositiveCases is the frozen 10.2 fixture table. Every row is a negative on
// the stock heuristic generation, and every row carries the independent decisive
// rule that turns it positive.
//
// Requirements covered per row are in Covers; the shapes are chosen so the union
// covers 3.2 (generic/shared/ambiguous identity), 3.4 (one generic tool), 3.5
// (filename, fence, language, technical prose), 3.6 (model name), 3.3's second
// half (weak cluster needing a recognized marker), 3.7 (ignored identity
// prefixes), and 12.2/12.3.
func falsePositiveCases() []falsePositiveCase {
	// ruleC pairs a fixture with the weaker corroborated cluster: read/search plus
	// a local mutation plus a recognized build marker. It is the decisive rule for
	// fixtures whose own evidence is a weak cluster, because the recognized marker
	// is exactly the corroboration requirement 3.3 demands.
	ruleC := func(name string) *decisiveRule {
		return &decisiveRule{
			Name:    name,
			Tools:   falsePositiveWeakCluster,
			Markers: falsePositiveRuleCMarker,
			Code:    featurestate.EvidenceCodeProjectMarkerCluster,
			Source:  session.SourceLocalTooling,
		}
	}
	// markerOnly pairs a fixture whose OWN tool cluster already satisfies Rule C's
	// cluster requirement with nothing but the recognized marker. It is the sharpest
	// form of the pairing: the recognized marker is the single fact requirement 3.3
	// says is missing.
	markerOnly := func(name string) *decisiveRule {
		return &decisiveRule{
			Name:    name,
			Markers: falsePositiveRuleCMarker,
			Code:    featurestate.EvidenceCodeProjectMarkerCluster,
			Source:  session.SourceLocalTooling,
		}
	}
	ruleB := func(name string) *decisiveRule {
		return &decisiveRule{
			Name:   name,
			Tools:  falsePositiveRuleBCluster,
			Code:   featurestate.EvidenceCodeDistinctCluster,
			Source: session.SourceLocalTooling,
		}
	}
	return []falsePositiveCase{
		{
			ID:     "neg_technical_prose_words",
			Shape:  "programming_prose",
			Covers: "3.5, 12.2",
			Prompt: "Please debug the Go build and explain the bug in this repository; the code stopped compiling after the refactor.",
			// Paired through Rule A rather than Rule B, so the pairing proves
			// 12.2's exception for BOTH decisive paths and not only the tool one.
			Decisive: &decisiveRule{
				Name:     "a recognized roo-code client identity",
				Identity: "roo-code/1.2.3",
				Code:     featurestate.EvidenceCodeRoo,
				Source:   session.SourceLocalIdentity,
			},
		},
		{
			ID:       "neg_source_filenames",
			Shape:    "source_filename",
			Covers:   "3.5, 12.2",
			Prompt:   "Please review main.go and parser.py and explain why the build fails on the second package.",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:     "neg_markdown_code_fence",
			Shape:  "markdown_code_fence",
			Covers: "3.5, 12.2",
			Prompt: "Can you explain this snippet?\n```go\nfunc main() { fmt.Println(\"hi\") }\n```\n" +
				"What does the compiler object to?",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_language_names",
			Shape:    "programming_language_name",
			Covers:   "3.5, 12.2",
			Prompt:   "I am learning Rust ownership and trait bounds, then comparing them with Python dataclasses and Java generics.",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:     "neg_model_name_in_prose",
			Shape:  "model_name",
			Covers: "3.6, 12.2",
			// The canonical classifier input is deliberately model-free, so the model
			// name is delivered in the turn content exactly as the frozen matrix's
			// model_name row does.
			Prompt:   "Would gpt-5-codex be suitable for this code question, or is that model only for agentic edits?",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_compaction_prompt_marker",
			Shape:    "prompt_compaction_marker",
			Covers:   "3.5, 3.8, 12.2",
			Prompt:   "[CONTEXT SUMMARY]: the earlier conversation was compacted. Continue the current task from this summary.",
			Decisive: ruleC("read/search/edit plus go.mod"),
		},
		{
			ID:       "neg_single_shell_tool",
			Shape:    "one_shell_tool",
			Covers:   "3.4, 12.2",
			Tools:    falsePositiveTools("bash"),
			Prompt:   "Run `go test ./...` in this workspace and show me the failure output.",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_single_web_search_tool",
			Shape:    "one_web_tool",
			Covers:   "3.4, 12.2",
			Tools:    falsePositiveTools("web_search"),
			Prompt:   "Search the web for the latest Go release notes and summarize them.",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_single_browser_tool",
			Shape:    "one_browser_tool",
			Covers:   "3.4, 12.2",
			Tools:    falsePositiveTools("browser_navigate"),
			Prompt:   "Open the dashboard page in the browser and screenshot the chart.",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_single_read_tool",
			Shape:    "one_read_tool",
			Covers:   "3.4, 12.2",
			Tools:    falsePositiveTools("read_file"),
			Prompt:   "Show me what is written in notes.txt.",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_single_search_tool",
			Shape:    "one_search_tool",
			Covers:   "3.4, 12.2",
			Tools:    falsePositiveTools("grep"),
			Prompt:   "Grep the tree for the string LEGACY_FLAG and list the matches.",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:     "neg_code_fence_with_single_shell_tool",
			Shape:  "code_fence_plus_one_tool",
			Covers: "3.4, 3.5, 12.2",
			Tools:  falsePositiveTools("bash"),
			Prompt: "This build log arrived inside a fence:\n```\nmake: *** [all] Error 2\n```\n" +
				"Which target in the Makefile should I look at first?",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_model_name_as_tool_name",
			Shape:    "model_name_as_tool_name",
			Covers:   "3.6, 12.2",
			Tools:    falsePositiveTools("gpt-5-codex"),
			Prompt:   "Compare this repository's build script against the vendor's published one.",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_coding_agent_route_selector",
			Shape:    "model_name_in_route_selector",
			Covers:   "3.6",
			Route:    "only:gpt-5-codex",
			Prompt:   "Route this request at the coding-agent model and tell me which backend served it.",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_anthropic_sdk_ua",
			Shape:    "ambiguous_sdk_ua",
			Covers:   "3.2, 12.3",
			Identity: "Anthropic/JS 0.20.3",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_openai_sdk_ua",
			Shape:    "ambiguous_sdk_ua",
			Covers:   "3.2, 12.3",
			Identity: "OpenAI/JS 4.70.0",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_chrome_browser_ua",
			Shape:    "generic_browser_ua",
			Covers:   "3.2",
			Identity: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_python_requests_ua",
			Shape:    "generic_api_sdk_ua",
			Covers:   "3.2",
			Identity: "python-requests/2.32.3",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_axios_ua",
			Shape:    "generic_api_sdk_ua",
			Covers:   "3.2",
			Identity: "axios/1.7.2",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_curl_ua",
			Shape:    "generic_api_sdk_ua",
			Covers:   "3.2",
			Identity: "curl/8.4.0",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_cline_sdk_ua_with_weak_cluster",
			Shape:    "ambiguous_sdk_ua_plus_weak_cluster",
			Covers:   "3.2, 3.3, 3.4, 12.3",
			Identity: "Anthropic/JS 0.20.3",
			Tools:    falsePositiveTools("Read", "Edit"),
			Prompt:   "Read the failing test file and make the smallest edit that fixes it.",
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_sdk_ua_with_web_and_search",
			Shape:    "ambiguous_sdk_ua_plus_web_cluster",
			Covers:   "3.2, 3.4",
			Identity: "OpenAI/JS 4.70.0",
			Tools:    falsePositiveTools("web_search", "Grep"),
			Decisive: ruleB("read/search/edit/command cluster"),
		},
		{
			ID:       "neg_read_search_edit_without_marker",
			Shape:    "weak_cluster_without_project_marker",
			Covers:   "3.3, 3.4",
			Tools:    falsePositiveTools("Read", "Grep", "Edit"),
			Decisive: markerOnly("the recognized go.mod marker"),
		},
		{
			ID:       "neg_read_search_command_without_marker",
			Shape:    "weak_cluster_without_project_marker",
			Covers:   "3.3, 3.4",
			Tools:    falsePositiveTools("Read", "Grep", "Bash"),
			Decisive: markerOnly("the recognized go.mod marker"),
		},
		{
			ID:       "neg_search_remove_without_marker",
			Shape:    "weak_cluster_without_project_marker",
			Covers:   "3.3, 3.4",
			Tools:    falsePositiveTools("Grep", "delete_file"),
			Decisive: markerOnly("the recognized go.mod marker"),
		},
		{
			ID:       "neg_read_edit_with_generic_git_marker",
			Shape:    "weak_cluster_with_nondecisive_marker",
			Covers:   "3.3, 3.4",
			Tools:    falsePositiveTools("Read", "Edit"),
			Markers:  []string{".git"},
			Decisive: markerOnly("the recognized go.mod marker"),
		},
		{
			ID:       "neg_read_search_edit_with_unrecognized_markers",
			Shape:    "weak_cluster_with_nondecisive_marker",
			Covers:   "3.3, 3.4",
			Tools:    falsePositiveTools("Read", "Grep", "Edit"),
			Markers:  []string{"Makefile", "README.md"},
			Decisive: markerOnly("the recognized go.mod marker"),
		},
		{
			ID:       "neg_read_search_web_without_marker",
			Shape:    "weak_cluster_without_project_marker",
			Covers:   "3.3, 3.4",
			Tools:    falsePositiveTools("Read", "Grep", "web_search"),
			Decisive: ruleC("read/search/edit plus go.mod"),
		},
		{
			ID:              "neg_excluded_prefix_identity_only",
			Shape:           "ignored_identity_prefix",
			Covers:          "3.7",
			Identity:        "codex_cli_rs/1.2.3",
			IgnoredPrefixes: falsePositiveExclusionPrefixes,
			Decisive:        ruleB("read/search/edit/command cluster"),
		},
		{
			ID:              "neg_excluded_prefix_with_code_fence",
			Shape:           "ignored_identity_prefix_plus_prose",
			Covers:          "3.5, 3.7",
			Identity:        "codex_cli_rs/1.2.3",
			IgnoredPrefixes: falsePositiveExclusionPrefixes,
			Prompt:          "Explain this Go fence:\n```go\npackage main\n\nfunc main() {}\n```\nWhy does it not build?",
			Decisive:        ruleB("read/search/edit/command cluster"),
		},
		{
			ID:              "neg_excluded_prefix_with_weak_cluster",
			Shape:           "ignored_identity_prefix_plus_weak_cluster",
			Covers:          "3.3, 3.7",
			Identity:        "codex_cli_rs/1.2.3",
			IgnoredPrefixes: falsePositiveExclusionPrefixes,
			Tools:           falsePositiveTools("Read", "Edit"),
			Decisive:        ruleB("read/search/edit/command cluster"),
		},
		{
			ID:              "neg_excluded_prefix_case_variant",
			Shape:           "ignored_identity_prefix",
			Covers:          "3.7",
			Identity:        "CODEX_CLI_RS/1.9.0",
			IgnoredPrefixes: falsePositiveExclusionPrefixes,
			Decisive:        ruleB("read/search/edit/command cluster"),
		},
	}
}

// falsePositiveCasesByID resolves one fixture, failing when the table does not
// carry it. Every test reads its fixtures through this function so a renamed or
// deleted row cannot silently shrink a suite.
func falsePositiveCasesByID(tb testing.TB, ids ...string) []falsePositiveCase {
	tb.Helper()
	all := falsePositiveCases()
	byID := make(map[string]falsePositiveCase, len(all))
	for _, c := range all {
		byID[c.ID] = c
	}
	out := make([]falsePositiveCase, 0, len(ids))
	for _, id := range ids {
		c, ok := byID[id]
		if !ok {
			tb.Fatalf("false-positive fixture %q is not in the table", id)
		}
		out = append(out, c)
	}
	return out
}

// falsePositiveTableIsDistinguishable is the pre-flight guard: the suite
// distinguishes its fixtures, so two of them may never declare the same evidence.
// Without this a census failure could never fire, because every row would
// already share one signature.
func falsePositiveTableIsDistinguishable(tb testing.TB) {
	tb.Helper()
	cases := falsePositiveCases()
	bySignature := map[string][]string{}
	for _, c := range cases {
		bySignature[falsePositiveSignature(c)] = append(bySignature[falsePositiveSignature(c)], c.ID)
		if c.Decisive == nil {
			tb.Errorf("false-positive fixture %q declares no independent decisive rule; "+
				"requirement 12.2 cannot be certified for it", c.ID)
		}
		if c.Covers == "" {
			tb.Errorf("false-positive fixture %q records no covered requirement", c.ID)
		}
	}
	shapes := map[string]int{}
	for _, c := range cases {
		shapes[c.Shape]++
	}
	for _, shape := range []string{
		"programming_prose", "source_filename", "markdown_code_fence",
		"programming_language_name", "model_name", "one_shell_tool", "one_web_tool",
		"one_browser_tool", "one_read_tool", "one_search_tool", "ambiguous_sdk_ua",
		"generic_browser_ua", "generic_api_sdk_ua", "weak_cluster_without_project_marker",
		"ignored_identity_prefix",
	} {
		if shapes[shape] == 0 {
			tb.Errorf("false-positive table carries no %q row; task 10.2 requires that shape", shape)
		}
	}
	problems := make([]string, 0, len(bySignature))
	signatures := make([]string, 0, len(bySignature))
	for signature := range bySignature {
		signatures = append(signatures, signature)
	}
	sort.Strings(signatures)
	for _, signature := range signatures {
		if ids := bySignature[signature]; len(ids) > 1 {
			slices.Sort(ids)
			problems = append(problems, fmt.Sprintf("%v declare identical evidence (%s)", ids, signature))
		}
	}
	if len(problems) > 0 {
		tb.Fatalf("false-positive table declares indistinguishable fixtures: %s", strings.Join(problems, "; "))
	}
}

// ---------------------------------------------------------------------------
// Runtime delivery capture
// ---------------------------------------------------------------------------

// falsePositiveDelivery is what the real runtime handed the classification
// stage. It is captured at the submit hook, which receives the SAME working call
// the stage read, so these values are the production evidence rather than a
// restatement of the fixture declaration.
type falsePositiveDelivery struct {
	UserAgent string
	Operation aipapi.Operation
	ToolNames []string
	UserText  []string
	Route     string
	// Markers are the workspace markers the RUNTIME resolved for this turn. They
	// are read from the submit-stage decision evidence rather than from the
	// fixture, so a resolved-marker difference between two fixtures is visible
	// here instead of collapsing two rows onto one signature.
	Markers []string
	// EvidencePresent reports that the submit stage ran with the stage's own
	// decision evidence attached. Without it the marker capture would be silently
	// empty and every marker-shaped fixture would look identical.
	EvidencePresent bool
}

// signature renders the delivered evidence as one comparable value.
func (d falsePositiveDelivery) signature() string {
	return fmt.Sprintf("ua=%q op=%q route=%q markers=%v tools=%v usertext=%q",
		d.UserAgent, d.Operation, d.Route, d.Markers, d.ToolNames, d.UserText)
}

// deliveredPrompt is the client prompt the turn actually carried. It is compared
// for exact equality against the fixture's own prompt, which is how a code-fence
// row delivered without its fence is detected: that row would otherwise certify
// nothing, exactly the task-10.1 rejection.
func (d falsePositiveDelivery) deliveredPrompt() string {
	return strings.Join(d.UserText, "\x00")
}

// falsePositiveDeliveryObserver is a submit-stage recorder. It exists only in this
// test file: it observes what the runtime passed on, and it mutates nothing.
type falsePositiveDeliveryObserver struct {
	id string

	mu    sync.Mutex
	turns []falsePositiveDelivery
}

var _ sdkhooks.SubmitHook = (*falsePositiveDeliveryObserver)(nil)

func newFalsePositiveDeliveryObserver(id string) *falsePositiveDeliveryObserver {
	return &falsePositiveDeliveryObserver{id: id}
}

func (o *falsePositiveDeliveryObserver) ID() string                        { return o.id }
func (o *falsePositiveDeliveryObserver) Order() int                        { return 0 }
func (o *falsePositiveDeliveryObserver) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (o *falsePositiveDeliveryObserver) Handle(
	ctx context.Context,
	call *aipapi.Call,
	_ *sdkhooks.SubmitMeta,
) (sdkhooks.SubmitDecision, error) {
	if call == nil {
		return sdkhooks.SubmitDecision{}, nil
	}
	delivery := falsePositiveDelivery{
		UserAgent: call.Invocation.ClientUserAgent,
		Operation: call.Invocation.Operation,
		Route:     call.Route.Selector,
	}
	// The resolved workspace reaches this stage on the same decision evidence the
	// submit barrier uses, so the captured markers are what the heuristic read.
	if evidence := extensions.DecisionEvidenceFromContext(ctx); evidence != nil {
		delivery.Markers = append([]string(nil), evidence.Views.Workspace.Markers...)
		delivery.EvidencePresent = true
	}
	for _, tool := range call.Tools {
		delivery.ToolNames = append(delivery.ToolNames, tool.Name)
	}
	for _, message := range call.Messages {
		if message.Role != aipapi.RoleUser {
			continue
		}
		for _, part := range message.Parts {
			if part.Kind == aipapi.PartText && part.Text != "" {
				delivery.UserText = append(delivery.UserText, part.Text)
			}
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.turns = append(o.turns, delivery)
	return sdkhooks.SubmitDecision{}, nil
}

// since returns and forgets every delivery recorded after the given mark, so a
// two-turn pairing reads each turn's own evidence instead of the last one.
func (o *falsePositiveDeliveryObserver) since(mark int) []falsePositiveDelivery {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := append([]falsePositiveDelivery(nil), o.turns[mark:]...)
	return out
}

func (o *falsePositiveDeliveryObserver) mark() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.turns)
}

// ---------------------------------------------------------------------------
// Downstream consumer, gate probe and stream recorder
// ---------------------------------------------------------------------------

// falsePositiveGateProbe is a test-only downstream consumer. It records the
// classification a real consumer received and whether a consumer that gates on
// IsCodingAgent would have engaged for that turn.
//
// It is NOT a production consumer: session classification gates no bundled
// feature until a feature opts in (requirements 1.8, 10.3). Its only job here is
// to make "no feature gate engaged" observable on an unknown turn instead of
// inferred from the absence of an assertion.
type falsePositiveGateProbe struct {
	id string

	mu      sync.Mutex
	runs    int
	byTurn  map[string][]session.Classification
	engaged int
}

var _ prerequest.Handler = (*falsePositiveGateProbe)(nil)

func newFalsePositiveGateProbe(id string) *falsePositiveGateProbe {
	return &falsePositiveGateProbe{id: id, byTurn: map[string][]session.Classification{}}
}

func (p *falsePositiveGateProbe) ID() string                        { return p.id }
func (p *falsePositiveGateProbe) Order() int                        { return 0 }
func (p *falsePositiveGateProbe) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (p *falsePositiveGateProbe) Handle(
	_ context.Context,
	_ *aipapi.Call,
	meta prerequest.Meta,
	_ prerequest.Services,
) (prerequest.Decision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sessionID := meta.Session.AuthoritativeSessionID
	p.byTurn[sessionID] = append(p.byTurn[sessionID], meta.Session.Classification)
	p.runs++
	if meta.Session.Classification.IsCodingAgent() {
		p.engaged++
	}
	return prerequest.Allow(), nil
}

// observed returns every classification the probe saw for one authoritative
// session, in turn order.
func (p *falsePositiveGateProbe) observed(sessionID string) []session.Classification {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]session.Classification(nil), p.byTurn[sessionID]...)
}

func (p *falsePositiveGateProbe) totals() (runs, engaged int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runs, p.engaged
}

// falsePositiveStreamRecorder wraps the executor's own event stream so the suite
// can assert that an unknown turn really streamed a normal response to completion
// instead of being short-circuited by the classification stage.
type falsePositiveStreamRecorder struct {
	inner aipapi.EventStream
	kinds []aipapi.EventKind
}

func (r *falsePositiveStreamRecorder) Recv(ctx context.Context) (aipapi.Event, error) {
	event, err := r.inner.Recv(ctx)
	if err == nil {
		r.kinds = append(r.kinds, event.Kind)
	}
	return event, err
}

func (r *falsePositiveStreamRecorder) Close() error { return r.inner.Close() }

// served reports whether the client received a normal, complete streaming
// response: a start event and the terminal finish event, in that order.
func (r *falsePositiveStreamRecorder) served() bool {
	return slices.Equal(r.kinds, []aipapi.EventKind{aipapi.EventResponseStarted, aipapi.EventResponseFinished})
}

// ---------------------------------------------------------------------------
// Durable row census
// ---------------------------------------------------------------------------

// falsePositiveRow is one physical row of the feature-owned classification table,
// read with raw SQL so no store-level guard can hide what was written.
type falsePositiveRow struct {
	ScopeKind string `bun:"scope_kind"`
	ScopeID   string `bun:"scope_id"`
	Kind      string `bun:"kind"`
	Source    string `bun:"source"`
	Evidence  string `bun:"evidence_code"`
	Revision  int64  `bun:"classification_revision"`
}

// falsePositiveCensusRows reads every durable classification row directly. The
// store's own Load path validates the row, so a persisted negative kind could be
// reported as an error rather than as a value; the raw census is what makes such
// a row visible at all.
func falsePositiveCensusRows(tb testing.TB, database *bun.DB) []falsePositiveRow {
	tb.Helper()
	var rows []falsePositiveRow
	err := database.NewSelect().
		Table("session_classification").
		Column("scope_kind", "scope_id", "kind", "source", "evidence_code", "classification_revision").
		Order("scope_kind", "scope_id").
		Scan(context.Background(), &rows)
	if err != nil {
		tb.Fatalf("census the durable classification table: %v", err)
	}
	return rows
}

// falsePositiveAssertNoDurableNegative fails when the durable table holds any row
// that is not a plain positive. Requirement 1.5 makes the V1 vocabulary
// unknown/coding_agent only, so an empty kind is the only legal non-positive row,
// and it must be absent for a purely local negative.
func falsePositiveAssertNoDurableNegative(tb testing.TB, database *bun.DB) []falsePositiveRow {
	tb.Helper()
	rows := falsePositiveCensusRows(tb, database)
	for _, row := range rows {
		switch row.Kind {
		case string(session.KindUnknown), string(session.KindCodingAgent):
		default:
			tb.Errorf("durable classification row (%s/%s) holds kind %q; the V1 vocabulary has no negative classification "+
				"(source=%q evidence=%q)", row.ScopeKind, row.ScopeID, row.Kind, row.Source, row.Evidence)
		}
		if row.Kind == string(session.KindCodingAgent) && row.Revision != 1 {
			tb.Errorf("durable positive row (%s/%s) has revision %d, want the store-assigned first revision 1",
				row.ScopeKind, row.ScopeID, row.Revision)
		}
	}
	return rows
}

// falsePositiveAssertRowAbsent fails when the authoritative session owns any
// durable row at all, read back through an INDEPENDENT store instance so a process
// cache cannot stand in for committed state.
func falsePositiveAssertRowAbsent(tb testing.TB, database *bun.DB, sessionID, why string) {
	tb.Helper()
	record, found := certificationLoadRow(tb, database, sessionID)
	if found {
		tb.Errorf("%s: authoritative session %q owns a durable row %+v; an unknown turn must create no "+
			"durable classification of any kind", why, sessionID, record)
	}
	for _, row := range falsePositiveCensusRows(tb, database) {
		if row.ScopeKind == string(featurestate.ScopeSecureSession) && row.ScopeID == sessionID {
			tb.Errorf("%s: the durable table physically holds a row for %q: %+v", why, sessionID, row)
		}
	}
}

// ---------------------------------------------------------------------------
// Composed harness
// ---------------------------------------------------------------------------

// falsePositiveRig is one compiled generation's serving surfaces: the canonical
// executor (with the observing consumer and the delivery recorder) plus the wire
// executor, both bound to the same durable database and the same process-owned
// classification state.
type falsePositiveRig struct {
	database  *bun.DB
	workspace *crossHarnessWorkspaceResolver
	consumer  *falsePositiveGateProbe
	delivery  *falsePositiveDeliveryObserver

	canonical     *runtime.Executor
	canonicalOpen *atomic.Int32
	wire          *runtime.Executor
	wireOpen      *atomic.Int32
}

// falsePositiveHarness is a real featurehost process with TWO started compiled
// generations over one shared process state: the stock heuristic generation and a
// generation carrying the configured ignored identity prefix. Both are started,
// which is the overlapping-generation posture the holder supports.
type falsePositiveHarness struct {
	database  *bun.DB
	registry  *prometheus.Registry
	workspace *crossHarnessWorkspaceResolver
	consumer  *falsePositiveGateProbe
	delivery  *falsePositiveDeliveryObserver

	stock   *falsePositiveRig
	exclude *falsePositiveRig
}

func newFalsePositiveHarness(tb testing.TB) *falsePositiveHarness {
	tb.Helper()

	database := certificationOpenSQLite(tb,
		certificationSQLiteDSN(filepath.Join(tb.TempDir(), "false-positive.db")))

	registry := prometheus.NewRegistry()
	process, err := NewProcess(context.Background(), ProcessInput{
		Logger: slog.Default(), BunDB: database, MetricsRegistry: registry,
	})
	if err != nil {
		tb.Fatalf("NewProcess: %v", err)
	}
	tb.Cleanup(func() {
		if err := process.Close(); err != nil {
			tb.Errorf("close false-positive process: %v", err)
		}
	})

	workspace := &crossHarnessWorkspaceResolver{}
	consumer := newFalsePositiveGateProbe("false-positive-gate-probe")
	delivery := newFalsePositiveDeliveryObserver("false-positive-delivery")

	return &falsePositiveHarness{
		database:  database,
		registry:  registry,
		workspace: workspace,
		consumer:  consumer,
		delivery:  delivery,
		stock: newFalsePositiveRig(tb, process, database, workspace, consumer, delivery,
			certificationCompile(tb, process, true, "")),
		exclude: newFalsePositiveRig(tb, process, database, workspace, consumer, delivery,
			certificationCompile(tb, process, true, crossHarnessExclusionYAML(tb, falsePositiveExclusionPrefixes))),
	}
}

func newFalsePositiveRig(
	tb testing.TB,
	process *Runtime,
	database *bun.DB,
	workspace *crossHarnessWorkspaceResolver,
	consumer *falsePositiveGateProbe,
	delivery *falsePositiveDeliveryObserver,
	generation certificationGeneration,
) *falsePositiveRig {
	tb.Helper()
	if generation.classifier == nil {
		tb.Fatal("enabled generation published no classifier")
	}
	generation.start(tb)
	canonical, canonicalOpen := certificationExecutorWithWorkspace(tb, database,
		certificationPlanesWithConsumer(tb, generation.planes, consumer), workspace, delivery)
	// The wire lane gets the unmodified plane set: adding the observing consumer
	// would occupy a canonical-required plane and statically block the wire lane,
	// which is the condition task 7.3 recorded.
	wire, wireOpen := certificationExecutorWithWorkspace(tb, database, generation.planes, workspace)
	return &falsePositiveRig{
		database:      database,
		workspace:     workspace,
		consumer:      consumer,
		delivery:      delivery,
		canonical:     canonical,
		canonicalOpen: canonicalOpen,
		wire:          wire,
		wireOpen:      wireOpen,
	}
}

// rigFor selects the generation whose policy owns a fixture. A fixture carrying
// configured ignored prefixes is served by the reloaded exclusion generation.
func (h *falsePositiveHarness) rigFor(c falsePositiveCase) *falsePositiveRig {
	if len(c.IgnoredPrefixes) > 0 {
		return h.exclude
	}
	return h.stock
}

// ---------------------------------------------------------------------------
// Serving one turn
// ---------------------------------------------------------------------------

// falsePositiveToolDefs builds the canonical request's tool definitions. Names
// only: requirement 11.2 requires the classifier to work from the exact name, so
// adding a description here would let a fixture pass for the wrong reason.
func falsePositiveToolDefs(c falsePositiveCase, decisive *decisiveRule) []aipapi.ToolDef {
	names := append([]string(nil), c.Tools...)
	if decisive != nil {
		names = append(names, decisive.Tools...)
	}
	defs := make([]aipapi.ToolDef, 0, len(names))
	for _, name := range names {
		defs = append(defs, aipapi.ToolDef{Name: name})
	}
	return defs
}

// falsePositiveIdentity resolves the accepted client identity a turn carries. An
// independent decisive rule may supply one, which is how an ambiguous-UA fixture
// is paired with a Rule A positive.
func falsePositiveIdentity(c falsePositiveCase, decisive *decisiveRule) string {
	if decisive != nil && decisive.Identity != "" {
		return decisive.Identity
	}
	return c.Identity
}

// falsePositiveMarkers resolves the workspace markers a turn is served with.
func falsePositiveMarkers(c falsePositiveCase, decisive *decisiveRule) []string {
	if decisive != nil && len(decisive.Markers) > 0 {
		return append([]string(nil), decisive.Markers...)
	}
	return append([]string(nil), c.Markers...)
}

// falsePositiveCall builds the canonical client turn. The client prompt is always
// the fixture's own, so a pairing turn carries byte-identical prompt material and
// only the decisive evidence differs.
func falsePositiveCall(
	clientSessionID, resumeToken string,
	c falsePositiveCase,
	decisive *decisiveRule,
	history int,
) *aipapi.Call {
	messages := make([]aipapi.Message, 0, history+1)
	// The accumulated history exists to prove requirement 3.8: an unknown turn
	// whose transcript is large must be evaluated from bounded current-turn
	// metadata, so the outcome cannot depend on the history.
	for i := range history {
		role := aipapi.RoleUser
		if i%2 == 1 {
			role = aipapi.RoleAssistant
		}
		messages = append(messages, aipapi.Message{
			Role:  role,
			Parts: []aipapi.Part{aipapi.TextPart(fmt.Sprintf("turn %d of earlier conversation", i))},
		})
	}
	messages = append(messages, aipapi.Message{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart(falsePositivePromptFor(c))},
	})
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: falsePositiveRoute(c)},
		Session: aipapi.SessionRef{
			ClientSessionID: clientSessionID,
			ResumeToken:     resumeToken,
		},
		Messages: messages,
		Invocation: aipapi.Invocation{
			Operation:       aipapi.OperationOpenAIResponses,
			ClientUserAgent: falsePositiveIdentity(c, decisive),
		},
	}
	call.Tools = falsePositiveToolDefs(c, decisive)
	return call
}

// falsePositiveTurn is one served turn's complete observation.
type falsePositiveTurn struct {
	Fixture    falsePositiveCase
	Decisive   string
	Delivered  falsePositiveDelivery
	Projected  session.Classification
	Durable    session.Classification
	DurableSet bool
	Opener     int32
	Streamed   bool
}

// kind names the classification a turn projected, for the result tables.
func (turn falsePositiveTurn) kind() string {
	if turn.Projected.IsCodingAgent() {
		return "coding_agent"
	}
	return "unknown"
}

// verdict is the per-turn PASS/FAIL summary against the turn's own expectation. A
// negative turn passes only when it projected unknown AND left no durable row
// behind; the row check is part of the verdict because a negative that wrote a row
// has certified the opposite.
func (turn falsePositiveTurn) verdict(wantPositive bool) string {
	if wantPositive {
		if turn.Projected.IsCodingAgent() && turn.DurableSet && turn.Durable.IsCodingAgent() {
			return "PASS"
		}
		return "FAIL: expected a persisted coding_agent"
	}
	if turn.Projected.IsCodingAgent() {
		return "FAIL: " + crossHarnessKind(turn.Projected) + "/" + string(turn.Projected.Evidence)
	}
	if turn.DurableSet {
		return "FAIL: negative turn persisted a durable row"
	}
	return "PASS"
}

// serveFalsePositiveTurn serves one canonical turn for a fixture and asserts the
// generic-behaviour invariants that hold for EVERY outcome, positive or negative:
// the request routed to a backend exactly once, the client received a complete
// streaming response, and the downstream consumer ran.
func (rig *falsePositiveRig) serveFalsePositiveTurn(
	t *testing.T,
	clientSessionID, sessionID, resumeToken string,
	c falsePositiveCase,
	decisive *decisiveRule,
	history int,
) falsePositiveTurn {
	t.Helper()
	rig.workspace.set(falsePositiveMarkers(c, decisive))

	mark := rig.delivery.mark()
	before := rig.canonicalOpen.Load()
	call := falsePositiveCall(clientSessionID, resumeToken, c, decisive, history)
	stream, err := rig.canonical.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("canonical turn for %q: Execute: %v", c.ID, err)
	}
	recorder := &falsePositiveStreamRecorder{inner: stream}
	if _, err := aipapi.Collect(context.Background(), recorder); err != nil {
		t.Fatalf("canonical turn for %q: collect: %v", c.ID, err)
	}

	turn := falsePositiveTurn{Fixture: c, Opener: rig.canonicalOpen.Load() - before, Streamed: recorder.served()}
	if turn.Opener != 1 {
		t.Errorf("fixture %q opened the backend %d times, want exactly 1: an unknown turn must keep serving "+
			"generic traffic", c.ID, turn.Opener)
	}
	if !turn.Streamed {
		t.Errorf("fixture %q delivered event kinds %v, want a normal started/finished stream", c.ID, recorder.kinds)
	}
	delivered := rig.delivery.since(mark)
	if len(delivered) != 1 {
		t.Fatalf("fixture %q recorded %d submit-stage deliveries, want exactly 1", c.ID, len(delivered))
	}
	turn.Delivered = delivered[0]
	observed := rig.consumer.observed(sessionID)
	if len(observed) == 0 {
		t.Fatalf("fixture %q: the downstream consumer never ran for authoritative session %q", c.ID, sessionID)
	}
	turn.Projected = observed[len(observed)-1]
	record, found := certificationLoadRow(t, rig.database, sessionID)
	turn.Durable, turn.DurableSet = record.Classification, found
	return turn
}

// serveFalsePositiveWire runs one wire turn carrying the fixture's bounded proof
// evidence. The wire proof deliberately carries no prompt material
// (requirements 5.2, 5.5), so a prompt-only fixture's wire parity is about the
// absent evidence staying unknown rather than about the prompt.
func (rig *falsePositiveRig) serveFalsePositiveWire(
	t *testing.T,
	clientSessionID string,
	c falsePositiveCase,
	decisive *decisiveRule,
) crossHarnessOutcome {
	t.Helper()
	rig.workspace.set(falsePositiveMarkers(c, decisive))
	categories := sdkclassification.ToolCategorySet(0)
	for _, tool := range falsePositiveToolDefs(c, decisive) {
		categories = categories.AddToolName(tool.Name)
	}
	evidence := sdkclassification.Evidence{
		Operation:       aipapi.OperationOpenAIChatCompletions,
		ClientUserAgent: falsePositiveIdentity(c, decisive),
		ToolCategories:  categories,
	}
	source := certificationNewWireSource(t, crossHarnessWireBody)
	assessment := certificationWireAssessment(t, source)
	proof := largebody.Proof{
		ProfileID:              "openai-chat",
		Operation:              aipapi.OperationOpenAIChatCompletions,
		Delivery:               aipapi.DeliveryModeStreaming,
		Identity:               largebody.NewIdentityDigest([32]byte{0x70, 0x70, 0x70, 0x70}),
		Session:                certificationNewWireSession(clientSessionID),
		ClassificationEvidence: evidence,
	}
	ctx := largebody.ContextWithWireProof(context.Background(), proof, "req-"+clientSessionID)
	result, err := rig.wire.ExecuteLargeBody(ctx, assessment, source)
	if err != nil {
		t.Fatalf("wire turn for %q: ExecuteLargeBody: %v", c.ID, err)
	}
	if _, err := aipapi.Collect(context.Background(), result.Stream); err != nil {
		t.Fatalf("wire turn for %q: collect: %v", c.ID, err)
	}
	if rig.wireOpen.Load() == 0 {
		t.Fatalf("wire turn for %q never opened the backend", c.ID)
	}
	record, found := certificationLoadRow(t, rig.database, result.Session.AuthoritativeSessionID)
	return crossHarnessOutcome{Durable: record.Classification, DurableSet: found}
}

// ---------------------------------------------------------------------------
// Delivered-evidence census
// ---------------------------------------------------------------------------

// falsePositiveDeliveredEvidenceError is the completeness check over the evidence
// the runtime actually received. It rejects two distinct failures:
//
//   - a fixture the suite distinguishes was delivered evidence identical to
//     another fixture's, which means at least one declared signal was never
//     delivered and that row's result proves nothing; and
//   - a fixture's own declared evidence (identity, tools, markers, prompt) was
//     not what the turn carried.
//
// Both comparisons are made against the RUNTIME capture, never against the
// declaration alone, so a delivery regression is visible instead of invisible.
func falsePositiveDeliveredEvidenceError(
	delivered map[string]falsePositiveDelivery,
	cases []falsePositiveCase,
) error {
	bySignature := map[string][]string{}
	var problems []string
	for _, c := range cases {
		got, ok := delivered[c.ID]
		if !ok {
			problems = append(problems, fmt.Sprintf("fixture %q recorded no delivered evidence", c.ID))
			continue
		}
		bySignature[got.signature()] = append(bySignature[got.signature()], c.ID)
		if !got.EvidencePresent {
			problems = append(problems, fmt.Sprintf(
				"fixture %q ran its submit stage without the stage decision evidence, so the captured resolved "+
					"workspace markers are unreliable", c.ID))
		}
		if got.Route != falsePositiveRoute(c) {
			problems = append(problems, fmt.Sprintf(
				"fixture %q delivered route selector %q, want the declared %q",
				c.ID, got.Route, falsePositiveRoute(c)))
		}
		if got.UserAgent != c.Identity {
			problems = append(problems, fmt.Sprintf(
				"fixture %q delivered identity %q, want the declared %q", c.ID, got.UserAgent, c.Identity))
		}
		if !slices.Equal(got.ToolNames, c.Tools) {
			problems = append(problems, fmt.Sprintf(
				"fixture %q delivered tool names %v, want the declared %v", c.ID, got.ToolNames, c.Tools))
		}
		if !slices.Equal(got.Markers, c.Markers) {
			problems = append(problems, fmt.Sprintf(
				"fixture %q resolved workspace markers %v, want the declared %v", c.ID, got.Markers, c.Markers))
		}
		if got.deliveredPrompt() != falsePositivePromptFor(c) {
			problems = append(problems, fmt.Sprintf(
				"fixture %q delivered client prompt %q, want %q; the row's declared signal was dropped",
				c.ID, got.deliveredPrompt(), falsePositivePromptFor(c)))
		}
	}
	signatures := make([]string, 0, len(bySignature))
	for signature := range bySignature {
		signatures = append(signatures, signature)
	}
	sort.Strings(signatures)
	for _, signature := range signatures {
		ids := bySignature[signature]
		if len(ids) < 2 {
			continue
		}
		slices.Sort(ids)
		problems = append(problems, fmt.Sprintf(
			"fixtures %v were delivered identical evidence (%s); this suite distinguishes them, so at least one "+
				"row certified nothing", ids, signature))
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("delivered evidence census failed: %s", strings.Join(problems, "; "))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Local-decision measurement
// ---------------------------------------------------------------------------

// falsePositiveEvaluateLocal runs the PRODUCTION local decision function over the
// evidence a turn actually delivered. It is how a negative's reason for staying
// unknown is measured rather than inferred: the function must report no promotion
// and no preserved prior, which is the statement "no decisive rule fired".
//
// The input is built from the runtime capture, and the Workspace markers come from
// the fixture, so the probe mirrors the composed turn rather than a second
// interpretation of it.
func falsePositiveEvaluateLocal(
	c falsePositiveCase,
	delivered falsePositiveDelivery,
	markers []string,
) featurestate.LocalDecision {
	categories := sdkclassification.ToolCategorySet(0)
	for _, name := range delivered.ToolNames {
		categories = categories.AddToolName(name)
	}
	cfg := featurestate.Config{
		Mode: featurestate.ModeHeuristic,
		Heuristic: featurestate.HeuristicConfig{
			IgnoredUserAgentPrefixes: append([]string(nil), c.IgnoredPrefixes...),
		},
	}
	return featurestate.EvaluateLocal(cfg, sdkclassification.Input{
		Session: session.SessionView{AuthoritativeSessionID: "false-positive-probe"},
		Workspace: aipworkspace.WorkspaceView{
			ID:      certificationWorkspaceID,
			Markers: append([]string(nil), markers...),
		},
		Evidence: sdkclassification.Evidence{
			Operation:       delivered.Operation,
			ClientUserAgent: delivered.UserAgent,
			ToolCategories:  categories,
		},
	})
}

// falsePositiveAssertNoDecisiveRuleFired requires the production decision function
// to report the empty decision for a delivered negative.
func falsePositiveAssertNoDecisiveRuleFired(tb testing.TB, decision featurestate.LocalDecision, id string) {
	tb.Helper()
	if decision.Promotes || decision.Preserves || decision.EvidenceCode != "" ||
		decision.Source != "" || decision.ClientFamily != "" {
		tb.Errorf("fixture %q: the local heuristic decided %+v on the delivered evidence; a technical-chat or "+
			"ambiguous-client negative must reach NO decisive rule", id, decision)
	}
}

// ---------------------------------------------------------------------------
// Certification test 1: the false-positive matrix
// ---------------------------------------------------------------------------

// TestFalsePositiveMatrixKeepsTechnicalChatAndAmbiguousClientsUnknown is the
// load-bearing 10.2 suite. Every fixture is served through the real canonical
// stage and the real wire stage, and each is required to:
//
//   - project the exact zero-value unknown to a downstream consumer;
//   - create NO durable row at all, verified per session through an independent
//     store instance and globally through a raw census of the table;
//   - keep generic behavior: one backend open and one complete stream;
//   - reach no decisive rule in the production local decision function; and
//   - have delivered its own declared evidence, proven by the runtime census.
func TestFalsePositiveMatrixKeepsTechnicalChatAndAmbiguousClientsUnknown(t *testing.T) {
	t.Parallel()

	falsePositiveTableIsDistinguishable(t)
	cases := falsePositiveCases()
	harness := newFalsePositiveHarness(t)

	delivered := map[string]falsePositiveDelivery{}
	for _, c := range cases {
		rig := harness.rigFor(c)
		clientSessionID := "fp-" + c.ID
		sessionID, resumeToken := certificationResumableSession(t, rig.canonical, clientSessionID)

		turn := rig.serveFalsePositiveTurn(t, clientSessionID, sessionID, resumeToken, c, nil, 0)
		delivered[c.ID] = turn.Delivered

		if turn.Projected != (session.Classification{}) {
			t.Errorf("fixture %q [%s/%s] projected %+v, want the zero-value unknown",
				c.ID, c.Shape, c.Covers, turn.Projected)
		}
		if turn.DurableSet {
			t.Errorf("fixture %q [%s/%s] persisted a durable classification %+v",
				c.ID, c.Shape, c.Covers, turn.Durable)
		}
		falsePositiveAssertNoDecisiveRuleFired(t,
			falsePositiveEvaluateLocal(c, turn.Delivered, falsePositiveMarkers(c, nil)), c.ID)
		falsePositiveAssertRowAbsent(t, rig.database, sessionID, fmt.Sprintf("fixture %q", c.ID))

		// Wire parity: the same bounded evidence must leave the wire lane unknown
		// with no committed row either (requirements 5.3, 5.4, 12.7).
		wire := rig.serveFalsePositiveWire(t, clientSessionID+"-wire", c, nil)
		if wire.DurableSet || wire.Durable.IsCodingAgent() {
			t.Errorf("fixture %q: wire lane committed %+v (set=%t), want unknown with no row",
				c.ID, wire.Durable, wire.DurableSet)
		}

		t.Logf("  %-42s %-36s %-12s ua=%-46q tools=%-28v markers=%v -> %s/%s durable=%t streamed=%t %s",
			c.ID, c.Shape, c.Covers, turn.Delivered.UserAgent, turn.Delivered.ToolNames,
			falsePositiveMarkers(c, nil), turn.kind(), string(turn.Projected.Evidence),
			turn.DurableSet, turn.Streamed, turn.verdict(false))
	}

	// Requirement 1.5 / 6.9: not one durable row may exist at all, so a negative
	// cannot have been recorded as a row carrying an empty or unknown kind.
	rows := falsePositiveAssertNoDurableNegative(t, harness.database)
	if len(rows) != 0 {
		t.Errorf("the durable classification table holds %d rows after %d negative turns; an unknown session must "+
			"leave no durable trace whatsoever", len(rows), len(cases))
	}

	if err := falsePositiveDeliveredEvidenceError(delivered, cases); err != nil {
		t.Fatal(err)
	}

	// The gate probe is the "no feature gate engaged" evidence: it ran once per
	// fixture and never saw a positive it could have gated on.
	runs, engaged := harness.consumer.totals()
	if runs != len(cases) || engaged != 0 {
		t.Errorf("downstream consumer ran %d times with %d gates engaged over %d negative fixtures; want %d runs and 0 gates",
			runs, engaged, len(cases), len(cases))
	}
	if got := len(harness.delivery.since(0)); got != len(cases) {
		t.Errorf("recorded %d submit-stage deliveries for %d fixtures", got, len(cases))
	}
}

// ---------------------------------------------------------------------------
// Certification test 2: requirement 12.2's independent decisive rule
// ---------------------------------------------------------------------------

// TestTechnicalChatStaysUnknownUntilAnIndependentDecisiveRuleIsSatisfied is
// requirement 12.2's substance: each technical-chat fixture stays unknown on its
// own evidence and becomes positive the moment ONE independent decisive rule is
// added, on the SAME authoritative session and with the SAME client prompt.
//
// Running the pairing in the same logical session matters. It proves the unknown
// turn left no durable state that could mask or pre-empt the later promotion, and
// it proves the prompt is not what blocks classification: the prompt is delivered
// identically on both turns.
func TestTechnicalChatStaysUnknownUntilAnIndependentDecisiveRuleIsSatisfied(t *testing.T) {
	t.Parallel()

	falsePositiveTableIsDistinguishable(t)
	harness := newFalsePositiveHarness(t)
	wantRows := 0

	for _, c := range falsePositiveCases() {
		rig := harness.rigFor(c)
		clientSessionID := "fp-pair-" + c.ID
		sessionID, resumeToken := certificationResumableSession(t, rig.canonical, clientSessionID)

		turn := rig.serveFalsePositiveTurn(t, clientSessionID, sessionID, resumeToken, c, nil, 0)
		if turn.Projected != (session.Classification{}) {
			t.Errorf("fixture %q: the weak-evidence turn projected %+v, want unknown", c.ID, turn.Projected)
		}
		falsePositiveAssertNoDecisiveRuleFired(t,
			falsePositiveEvaluateLocal(c, turn.Delivered, falsePositiveMarkers(c, nil)), c.ID)
		falsePositiveAssertRowAbsent(t, rig.database, sessionID, fmt.Sprintf("fixture %q weak turn", c.ID))

		// Requirement 12.2's exception: the same prompt, plus exactly one
		// independent decisive rule, is positive.
		paired := rig.serveFalsePositiveTurn(t, clientSessionID, sessionID, resumeToken, c, c.Decisive, 0)
		if !paired.Projected.IsCodingAgent() {
			t.Errorf("fixture %q: the paired turn with the independent decisive rule %q projected %+v, want coding_agent",
				c.ID, c.Decisive.Name, paired.Projected)
		}
		if paired.Projected.Evidence != c.Decisive.Code || paired.Projected.Source != c.Decisive.Source {
			t.Errorf("fixture %q: paired decisive evidence = %q from %q, want %q from %q",
				c.ID, paired.Projected.Evidence, paired.Projected.Source, c.Decisive.Code, c.Decisive.Source)
		}
		if !paired.DurableSet || !paired.Durable.IsCodingAgent() {
			t.Errorf("fixture %q: paired turn durable row = %+v (set=%t), want a persisted coding_agent",
				c.ID, paired.Durable, paired.DurableSet)
		}
		if paired.Durable != paired.Projected {
			t.Errorf("fixture %q: durable row %+v differs from the projected %+v", c.ID, paired.Durable, paired.Projected)
		}
		// The prompt really was the same on both turns, so the positive cannot be
		// attributed to a different client prompt.
		if !slices.Equal(turn.Delivered.UserText, paired.Delivered.UserText) {
			t.Errorf("fixture %q: the paired turn delivered different client text %v, want the negative turn's %v",
				c.ID, paired.Delivered.UserText, turn.Delivered.UserText)
		}
		// And the production decision function agrees on the paired evidence.
		decision := falsePositiveEvaluateLocal(c, paired.Delivered, falsePositiveMarkers(c, c.Decisive))
		if !decision.Promotes || decision.EvidenceCode != c.Decisive.Code || decision.Source != c.Decisive.Source {
			t.Errorf("fixture %q: the local heuristic decided %+v on the paired evidence, want promotion on %q from %q",
				c.ID, decision, c.Decisive.Code, c.Decisive.Source)
		}
		wantRows++
		t.Logf("  %-42s weak-turn=%s/%s durable=%t -> paired %-34s = %s/%s %s",
			c.ID, turn.kind(), string(turn.Projected.Evidence), turn.DurableSet,
			c.Decisive.Name, paired.kind(), paired.Projected.Evidence, paired.verdict(true))
	}

	rows := falsePositiveAssertNoDurableNegative(t, harness.database)
	if len(rows) != wantRows {
		t.Errorf("the durable table holds %d rows, want exactly %d positive promotions", len(rows), wantRows)
	}
	runs, engaged := harness.consumer.totals()
	if runs != 2*wantRows || engaged != wantRows {
		t.Errorf("downstream consumer ran %d times with %d gates engaged; want %d runs and %d gates",
			runs, engaged, 2*wantRows, wantRows)
	}
}

// ---------------------------------------------------------------------------
// Certification test 3: requirement 3.7's ignored identity prefixes
// ---------------------------------------------------------------------------

// TestIgnoredIdentityPrefixWithdrawsOnlyTheIdentityRule pins the exclusion
// semantics requirement 3.7 and design.md's "Exclusions" section state: a
// configured prefix is checked BEFORE Rule A, so it withdraws a prospective
// identity match and nothing else.
//
// Three facts are certified separately, because a single assertion could not tell
// them apart:
//
//   - the excluded identity with no tool evidence, with technical prose, with a
//     weak tool cluster, and in a different letter case is unknown with no row;
//   - the SAME excluded identity plus one decisive TOOL cluster is positive on the
//     tooling rule, because design.md scopes the exclusion to Rule A; and
//   - the SAME identity under the STOCK generation, which carries no exclusion at
//     all, is positive on Rule A. Without this control the previous result would
//     be indistinguishable from an identity that simply does not match.
func TestIgnoredIdentityPrefixWithdrawsOnlyTheIdentityRule(t *testing.T) {
	t.Parallel()

	negatives := falsePositiveCasesByID(t,
		"neg_excluded_prefix_identity_only",
		"neg_excluded_prefix_with_code_fence",
		"neg_excluded_prefix_with_weak_cluster",
		"neg_excluded_prefix_case_variant",
	)
	harness := newFalsePositiveHarness(t)

	for _, c := range negatives {
		rig := harness.rigFor(c)
		clientSessionID := "fp-excl-" + c.ID
		sessionID, resumeToken := certificationResumableSession(t, rig.canonical, clientSessionID)
		turn := rig.serveFalsePositiveTurn(t, clientSessionID, sessionID, resumeToken, c, nil, 0)
		if turn.Projected != (session.Classification{}) {
			t.Errorf("fixture %q: an ignored-prefixed identity projected %+v, want unknown", c.ID, turn.Projected)
		}
		falsePositiveAssertNoDecisiveRuleFired(t,
			falsePositiveEvaluateLocal(c, turn.Delivered, falsePositiveMarkers(c, nil)), c.ID)
		falsePositiveAssertRowAbsent(t, rig.database, sessionID, fmt.Sprintf("fixture %q", c.ID))
	}

	// The decisive TOOL cluster on the very same ignored identity. This is the
	// design's stated scope, so it must be positive and must be attributed to the
	// tooling rule rather than to the withdrawn identity rule.
	toolingPair := &decisiveRule{
		Name:   "read/search/edit/command cluster",
		Tools:  falsePositiveRuleBCluster,
		Code:   featurestate.EvidenceCodeDistinctCluster,
		Source: session.SourceLocalTooling,
	}
	excluded := falsePositiveCase{
		ID:              "excluded_identity_with_distinctive_tools",
		Shape:           "ignored_identity_prefix_plus_distinctive_cluster",
		Covers:          "3.3, 3.7",
		Identity:        "codex_cli_rs/1.2.3",
		IgnoredPrefixes: falsePositiveExclusionPrefixes,
	}
	excludedSession, excludedToken := certificationResumableSession(t, harness.exclude.canonical, "fp-excl-tooling")
	tooling := harness.exclude.serveFalsePositiveTurn(t, "fp-excl-tooling", excludedSession, excludedToken, excluded, toolingPair, 0)
	if !tooling.Projected.IsCodingAgent() {
		t.Fatalf("the ignored-prefixed identity with a decisive tool cluster projected %+v, want coding_agent: "+
			"design.md scopes the exclusion to Rule A", tooling.Projected)
	}
	if tooling.Projected.Evidence != toolingPair.Code || tooling.Projected.Source != session.SourceLocalTooling {
		t.Errorf("excluded identity with decisive tools = %q from %q, want %q from %q: the exclusion must not "+
			"reclassify the positive as an identity one",
			tooling.Projected.Evidence, tooling.Projected.Source, toolingPair.Code, session.SourceLocalTooling)
	}

	// Control: the identical identity on the STOCK generation, which declares no
	// exclusion prefix, is a Rule A positive. Without this the previous result
	// could not be distinguished from an identity that matches nothing.
	stock := falsePositiveCase{ID: "stock_identity_control", Shape: "identity_control", Covers: "3.1, 3.7", Identity: "codex_cli_rs/1.2.3"}
	stockSession, stockToken := certificationResumableSession(t, harness.stock.canonical, "fp-excl-stock")
	identity := harness.stock.serveFalsePositiveTurn(t, "fp-excl-stock", stockSession, stockToken, stock, nil, 0)
	if !identity.Projected.IsCodingAgent() || identity.Projected.Evidence != featurestate.EvidenceCodeCodex ||
		identity.Projected.Source != session.SourceLocalIdentity {
		t.Fatalf("the unexcluded identity control = %+v, want a Rule A positive on %q", identity.Projected, featurestate.EvidenceCodeCodex)
	}

	rows := falsePositiveAssertNoDurableNegative(t, harness.database)
	if len(rows) != 2 {
		t.Errorf("the durable table holds %d rows, want exactly the two positives served by this test: %+v", len(rows), rows)
	}
	t.Logf("ignored-prefix negatives=4 unknown with no row; excluded+distinctive cluster=%q; unexcluded control=%q",
		tooling.Projected.Evidence, identity.Projected.Evidence)
}

// ---------------------------------------------------------------------------
// Certification test 4: requirement 3.8 and the accumulated transcript
// ---------------------------------------------------------------------------

// TestFalsePositiveUnknownTurnDoesNotRescanAccumulatedTranscript certifies
// requirement 3.8 at the composed level.
//
// The falsifiable claim is not "the test passed" but "the outcome cannot depend on
// the transcript": a turn carrying four hundred accumulated messages is classified
// from the same bounded current-turn metadata as a two-message turn with the same
// prompt, and the durable store is touched exactly once per turn with no promotion
// and no remote claim at all. The structural half is checked against the REAL SDK
// types, because a locally declared mirror of Input would prove nothing.
func TestFalsePositiveUnknownTurnDoesNotRescanAccumulatedTranscript(t *testing.T) {
	t.Parallel()

	const historyMessages = 400
	harness := newFalsePositiveHarness(t)
	c := falsePositiveCasesByID(t, "neg_code_fence_with_single_shell_tool")[0]
	rig := harness.rigFor(c)

	longSession, longToken := certificationResumableSession(t, rig.canonical, "fp-transcript-long")
	long := rig.serveFalsePositiveTurn(t, "fp-transcript-long", longSession, longToken, c, nil, historyMessages)
	if long.Projected != (session.Classification{}) {
		t.Errorf("a %d-message technical-chat turn projected %+v, want unknown", historyMessages, long.Projected)
	}
	falsePositiveAssertRowAbsent(t, rig.database, longSession, "large-transcript negative")

	shortSession, shortToken := certificationResumableSession(t, rig.canonical, "fp-transcript-short")
	short := rig.serveFalsePositiveTurn(t, "fp-transcript-short", shortSession, shortToken, c, nil, 0)
	if short.Projected != long.Projected {
		t.Errorf("transcript size changed the classification: %d messages = %+v, 0 extra messages = %+v",
			historyMessages, long.Projected, short.Projected)
	}
	// Exactly one bounded store read per unknown turn, and no promotion and no
	// remote claim anywhere: a transcript scan, or an accumulating weak-evidence
	// history, would show up here.
	operations := certificationStoreOperations(t, harness.registry)
	for operation, count := range operations {
		if !strings.HasPrefix(operation, "load/") {
			t.Errorf("durable store operation %q ran %v times on unknown turns; an unknown turn must not write "+
				"or lease state", operation, count)
		}
	}
	if got := operations["load/miss"]; got != 2 {
		t.Errorf("durable load observations = %v, want exactly one miss per unknown turn (2)", operations)
	}
	if rows := falsePositiveAssertNoDurableNegative(t, rig.database); len(rows) != 0 {
		t.Errorf("the durable table holds %d rows after two unknown turns, want 0", len(rows))
	}
	// The accumulated transcript really was delivered: the submit stage saw the
	// earlier messages, so the size-invariance result is not a delivery failure.
	if len(long.Delivered.UserText) < historyMessages/4 {
		t.Errorf("the long turn delivered only %d user texts for a %d-message history; the accumulated transcript "+
			"was not actually sent", len(long.Delivered.UserText), historyMessages)
	}

	// Structural half over the REAL SDK types: the classifier input has no
	// transcript-shaped member, so no transcript scan is even expressible. A
	// locally declared mirror of Input would prove nothing here, so the real
	// types are reflected.
	inputType := reflect.TypeFor[sdkclassification.Input]()
	wantInputFields := []string{"TraceID", "Session", "Workspace", "Evidence"}
	if inputType.NumField() != len(wantInputFields) {
		t.Fatalf("sdkclassification.Input has %d fields, want exactly %d bounded fields", inputType.NumField(), len(wantInputFields))
	}
	for i, want := range wantInputFields {
		if got := inputType.Field(i).Name; got != want {
			t.Fatalf("sdkclassification.Input field %d = %q, want %q", i, got, want)
		}
	}
	evidenceType := reflect.TypeFor[sdkclassification.Evidence]()
	wantEvidenceFields := []string{"Operation", "ClientUserAgent", "ToolCategories"}
	if evidenceType.NumField() != len(wantEvidenceFields) {
		t.Fatalf("sdkclassification.Evidence has %d fields, want exactly %d bounded fields", evidenceType.NumField(), len(wantEvidenceFields))
	}
	for i, want := range wantEvidenceFields {
		if got := evidenceType.Field(i).Name; got != want {
			t.Fatalf("sdkclassification.Evidence field %d = %q, want %q", i, got, want)
		}
	}
	t.Logf("a %d-message technical-chat turn (%d user texts) and a %d-message turn agree on %q with one store read each",
		historyMessages, len(long.Delivered.UserText), len(short.Delivered.UserText), crossHarnessKind(long.Projected))
}

// ---------------------------------------------------------------------------
// Certification test 5: census controls
// ---------------------------------------------------------------------------

// TestFalsePositiveDeliveredEvidenceCensusRejectsCollapsedDelivery is the
// non-vacuity control for the census every assertion above depends on. It feeds
// the census hand-built delivery records that reproduce the exact failure this
// suite exists to prevent: a fixture whose declared signal was never delivered,
// and two fixtures the suite distinguishes served the same evidence.
func TestFalsePositiveDeliveredEvidenceCensusRejectsCollapsedDelivery(t *testing.T) {
	t.Parallel()

	cases := falsePositiveCasesByID(t,
		"neg_markdown_code_fence", "neg_source_filenames", "neg_read_edit_with_generic_git_marker",
		"neg_anthropic_sdk_ua")

	// A complete, honest run is accepted.
	honest := map[string]falsePositiveDelivery{}
	for _, c := range cases {
		honest[c.ID] = falsePositiveDelivery{
			UserAgent:       c.Identity,
			Operation:       aipapi.OperationOpenAIResponses,
			Route:           falsePositiveRoute(c),
			ToolNames:       append([]string(nil), c.Tools...),
			UserText:        []string{falsePositivePromptFor(c)},
			Markers:         append([]string(nil), c.Markers...),
			EvidencePresent: true,
		}
	}
	if err := falsePositiveDeliveredEvidenceError(honest, cases); err != nil {
		t.Fatalf("an honest complete delivery census was rejected: %v", err)
	}

	// The task-10.1 failure mode: the declared prompt is replaced with generic
	// filler, so the weak signal never reaches the turn.
	filler := map[string]falsePositiveDelivery{}
	for id, delivery := range honest {
		delivery.UserText = []string{falsePositiveFillerPrompt}
		filler[id] = delivery
	}
	err := falsePositiveDeliveredEvidenceError(filler, cases)
	if err == nil {
		t.Fatal("the census accepted a run that replaced every declared prompt with generic filler")
	}
	for _, id := range []string{"neg_markdown_code_fence", "neg_source_filenames"} {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("census failure does not name the fixture whose prompt was dropped (%q): %v", id, err)
		}
	}

	// A dropped client identity must also be reported, or a generic-UA row could
	// silently be served with no identity at all.
	noIdentity := map[string]falsePositiveDelivery{}
	for id, delivery := range honest {
		if id == "neg_anthropic_sdk_ua" {
			delivery.UserAgent = ""
		}
		noIdentity[id] = delivery
	}
	err = falsePositiveDeliveredEvidenceError(noIdentity, cases)
	if err == nil {
		t.Fatal("the census accepted a run that dropped a declared client identity")
	}
	if !strings.Contains(err.Error(), "neg_anthropic_sdk_ua") {
		t.Fatalf("census failure does not name the fixture whose identity was dropped: %v", err)
	}

	// A dropped resolved marker must be reported too. This is the case that
	// distinguishes two weak-cluster fixtures which differ ONLY in their markers,
	// so a census blind to markers would silently merge them.
	noMarkers := map[string]falsePositiveDelivery{}
	for id, delivery := range honest {
		if id == "neg_read_edit_with_generic_git_marker" {
			delivery.Markers = nil
		}
		noMarkers[id] = delivery
	}
	err = falsePositiveDeliveredEvidenceError(noMarkers, cases)
	if err == nil {
		t.Fatal("the census accepted a run whose declared project markers were never resolved")
	}
	if !strings.Contains(err.Error(), "neg_read_edit_with_generic_git_marker") {
		t.Fatalf("census failure does not name the fixture whose markers were dropped: %v", err)
	}

	// A missing submit-stage evidence snapshot is reported, so an empty marker
	// capture can never be mistaken for "no markers".
	noEvidence := map[string]falsePositiveDelivery{}
	for id, delivery := range honest {
		delivery.EvidencePresent = false
		noEvidence[id] = delivery
	}
	err = falsePositiveDeliveredEvidenceError(noEvidence, cases)
	if err == nil {
		t.Fatal("the census accepted a run with no submit-stage decision evidence")
	}

	// A missing fixture must be reported rather than treated as "no evidence, so
	// nothing to certify".
	missing := map[string]falsePositiveDelivery{}
	for id, delivery := range honest {
		if id != "neg_markdown_code_fence" {
			missing[id] = delivery
		}
	}
	err = falsePositiveDeliveredEvidenceError(missing, cases)
	if err == nil {
		t.Fatal("the census accepted a run that never executed one fixture")
	}
	if !strings.Contains(err.Error(), "neg_markdown_code_fence") {
		t.Fatalf("census failure does not name the unexecuted fixture: %v", err)
	}

	// Two fixtures the suite distinguishes delivered byte-identical evidence.
	collapsed := map[string]falsePositiveDelivery{}
	for _, c := range cases {
		collapsed[c.ID] = falsePositiveDelivery{
			UserAgent:       "",
			Operation:       aipapi.OperationOpenAIResponses,
			Route:           falsePositiveDefaultRoute,
			ToolNames:       nil,
			UserText:        []string{falsePositiveFillerPrompt},
			EvidencePresent: true,
		}
	}
	err = falsePositiveDeliveredEvidenceError(collapsed, cases)
	if err == nil {
		t.Fatal("the census accepted a run in which distinct fixtures received identical evidence")
	}
	if !strings.Contains(err.Error(), "identical evidence") {
		t.Fatalf("census failure does not report the collapsed delivery: %v", err)
	}
	t.Logf("census rejected filler delivery, dropped identity, dropped markers, missing evidence, "+
		"missing fixture and collapsed delivery: %v", err)
}

// TestFalsePositiveStreamAndGateInstrumentsRejectAnEngagedGateAndABrokenStream
// keeps the two generic-behavior instruments honest: the stream recorder must
// reject a turn that produced no response, and the gate probe must be able to
// observe an engaged gate at all, so "zero gates engaged" above is a measurement
// rather than a broken observer.
func TestFalsePositiveInstrumentsCanObserveAFailingTurn(t *testing.T) {
	t.Parallel()

	good := &falsePositiveStreamRecorder{inner: aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished},
	})}
	if _, err := aipapi.Collect(context.Background(), good); err != nil {
		t.Fatalf("collect the control stream: %v", err)
	}
	if !good.served() {
		t.Fatal("the recorder rejected a normal started/finished stream")
	}
	// A stream that never reached its terminal event is refused by the collector
	// itself, and the recorder must also refuse to call that a served turn.
	truncated := &falsePositiveStreamRecorder{inner: aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
	})}
	if _, err := aipapi.Collect(context.Background(), truncated); err == nil {
		t.Fatal("the collector accepted a stream with no terminal event")
	}
	if truncated.served() {
		t.Error("the recorder accepted a stream that never finished; a broken turn would pass the generic-behavior check")
	}

	probe := newFalsePositiveGateProbe("instrument-control")
	positive := session.Classification{
		Kind: session.KindCodingAgent, Source: session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh, Evidence: featurestate.EvidenceCodeCodex, Revision: 1,
	}
	if !positive.IsCodingAgent() {
		t.Fatal("the control positive classification is not recognized as a positive")
	}
	if _, err := probe.Handle(context.Background(), nil, prerequest.Meta{
		Session: session.SessionView{AuthoritativeSessionID: "instrument", Classification: positive},
	}, prerequest.Services{}); err != nil {
		t.Fatalf("drive the control consumer: %v", err)
	}
	runs, engaged := probe.totals()
	if runs != 1 || engaged != 1 {
		t.Fatalf("gate probe totals = (%d runs, %d engaged), want (1, 1): a probe that cannot see an engaged gate "+
			"proves nothing when it reports zero", runs, engaged)
	}
	if got := probe.observed("instrument"); len(got) != 1 || got[0] != positive {
		t.Fatalf("gate probe observation = %+v, want the control positive", got)
	}
}
