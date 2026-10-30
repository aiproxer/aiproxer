package extractor

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/source"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func TestBuildRequestDetachedNoToolsIndependentRouteAndNoBranchExposure(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("session-parent", "a-parent", "acct")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	request, err := BuildRequest(Input{
		Route:               "extractor:model-small",
		ParentBranchBinding: branch,
		ParentTraceID:       "trace-parent",
		ParentALegID:        "a-parent",
		ParentBLegID:        "b-parent",
		Previous:            previous,
		DeterministicPlan:   &previous.Plan,
		SanitizedDelta: []source.Entry{{
			Kind: source.EntryUserDecision, Role: aipapi.RoleUser, Text: "Use the bounded adapter.", New: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Role != Role || request.Visibility != Visibility || request.SessionMode != auxiliary.SessionModeDetached {
		t.Fatalf("request policy = %#v", request)
	}
	if request.ParentBranchBinding != branch || request.ParentTraceID != "trace-parent" || request.ParentALegID != "a-parent" || request.ParentBLegID != "b-parent" {
		t.Fatalf("lineage = %#v", request)
	}
	if len(request.DisablePlugins) != 1 || request.DisablePlugins[0] != PluginID {
		t.Fatalf("disabled plugins = %#v", request.DisablePlugins)
	}
	if request.Call == nil {
		t.Fatal("nil child call")
	}
	if request.Call.Route.Selector != "extractor:model-small" {
		t.Fatalf("route = %q", request.Call.Route.Selector)
	}
	if len(request.Call.Tools) != 0 || request.Call.ToolChoice.Mode != aipapi.ToolChoiceNone {
		t.Fatalf("child tools = %#v choice=%#v", request.Call.Tools, request.Call.ToolChoice)
	}
	if !reflect.DeepEqual(request.Call.Session, aipapi.SessionRef{}) {
		t.Fatalf("child copied primary session authority: %#v", request.Call.Session)
	}
	if err := request.Call.Validate(); err != nil {
		t.Fatalf("child call invalid: %v", err)
	}
	if request.Call.Invocation.Operation == aipapi.OperationContextCompaction {
		t.Fatalf("extractor child must not identify as a compaction transaction: %#v", request.Call.Invocation)
	}
	if request.Call.Invocation.DeliveryMode != aipapi.DeliveryModeNonStreaming {
		t.Fatalf("extractor child delivery mode = %q", request.Call.Invocation.DeliveryMode)
	}
	prompt := request.Call.Messages[0].Parts[0].Text + request.Call.Messages[1].Parts[0].Text
	if strings.Contains(prompt, branch) || strings.Contains(prompt, "session-parent") || strings.Contains(prompt, "a-parent") {
		t.Fatalf("raw branch/lineage leaked into prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "schema_version") || !strings.Contains(prompt, "sanitized_delta") {
		t.Fatalf("fixed schema/input missing from prompt: %q", prompt)
	}
}

func TestBuildRequestCapturesInputTokenBoundAndOutputBudget(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("session", "parent-a", "account")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	request, err := BuildRequest(Input{
		Route:               "extractor:model-small",
		ParentBranchBinding: branch,
		Previous:            previous,
		MaxInputTokens:      12_000,
		MaxOutputTokens:     37,
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Call.Options.MaxOutputTokens == nil || *request.Call.Options.MaxOutputTokens != 37 {
		t.Fatalf("max output tokens = %#v, want 37", request.Call.Options.MaxOutputTokens)
	}
}

func TestSubmitRejectsPromptOverMaxInputTokensBeforeSubmission(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("session", "parent-a", "account")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeBackgroundClient{}
	if _, err := Submit(context.Background(), client, Input{
		Route:               "extractor:model-small",
		ParentBranchBinding: branch,
		Previous:            previous,
		MaxInputTokens:      1,
	}, "committed-key"); err == nil {
		t.Fatal("prompt exceeding MaxInputTokens was submitted")
	}
	if client.Calls != 0 {
		t.Fatalf("background submissions = %d, want 0", client.Calls)
	}
}

func TestParseResultStrictlyValidatesSchemaBaseAndReferences(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("session", "parent-a", "account")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	previous.Decisions = []capsule.Decision{{
		ID: "old", ConflictKey: "architecture.billing.mode", Statement: "Use the journal seam.",
		Status: capsule.DecisionActive, Authority: capsule.AuthorityUserExplicit, SourceRef: "item-user-1",
	}}
	if err := previous.Seal(); err != nil {
		t.Fatal(err)
	}
	raw := `{"schema_version":1,"base_revision":1,"facts":[{"kind":"constraint","id":"constraint-1","statement":"No provider SDK in core.","status":"active","source_ref":"item-user-1"}],"plan_updates":[],"decision_updates":[{"id":"new","conflict_key":"architecture.billing.mode","supersedes":["old"],"statement":"Use the adapter seam.","status":"active","source_ref":"item-user-1"}],"remove_or_supersede":[]}`
	result, err := ParseResult([]byte(raw), ParseOptions{Previous: previous, ExpectedBranch: branch, AllowedSourceRefs: []string{"item-user-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.BaseRevision != previous.Revision || len(result.Decisions) != 1 || len(result.Facts) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Decisions[0].Authority != capsule.AuthoritySemantic || result.Decisions[0].Source != capsule.SourceSemantic {
		t.Fatalf("semantic authority/source = %#v", result.Decisions[0])
	}

	for name, invalid := range map[string]string{
		"trailing JSON":      raw + `{}`,
		"duplicate field":    strings.Replace(raw, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"unknown field":      strings.Replace(raw, `"facts":`, `"authority":"user_explicit","facts":`, 1),
		"unknown supersedes": strings.Replace(raw, `"old"`, `"other-branch"`, 1),
		"wrong base":         strings.Replace(raw, `"base_revision":1`, `"base_revision":2`, 1),
		"bad conflict key":   strings.Replace(raw, `"architecture.billing.mode"`, `"Architecture Billing Mode"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseResult([]byte(invalid), ParseOptions{Previous: previous, ExpectedBranch: branch, AllowedSourceRefs: []string{"item-user-1"}}); err == nil {
				t.Fatalf("invalid result accepted: %s", invalid)
			}
		})
	}
}

func TestParseResultRejectsDepthAndOversizedOutput(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("session", "parent-a", "account")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"schema_version":1,"base_revision":1,"facts":[],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[]}`
	if _, err := ParseResult([]byte(raw), ParseOptions{Previous: previous, ExpectedBranch: branch, Limits: Limits{MaxBytes: 8}}); err == nil {
		t.Fatal("oversized result accepted")
	}
	deep := `{"schema_version":1,"base_revision":1,"facts":[{"kind":"constraint","id":"c","statement":"x","status":"active","source_ref":"item","nested":{"x":1}}],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[]}`
	if _, err := ParseResult([]byte(deep), ParseOptions{Previous: previous, ExpectedBranch: branch}); err == nil {
		t.Fatal("unknown nested field accepted")
	}
}

func TestParseResultSourceRefsRequireExactAllowlistMembership(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("session", "parent-a", "account")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"schema_version":1,"base_revision":1,"facts":[{"kind":"constraint","id":"c","statement":"Keep accounting policy explicit.","status":"active","source_ref":"accounting-policy"}],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[]}`
	if _, err := ParseResult([]byte(raw), ParseOptions{Previous: previous, ExpectedBranch: branch, AllowedSourceRefs: []string{"accounting-policy"}}); err != nil {
		t.Fatalf("allowlisted accounting ref rejected: %v", err)
	}
	if _, err := ParseResult([]byte(raw), ParseOptions{Previous: previous, ExpectedBranch: branch}); err == nil {
		t.Fatal("source ref accepted without a sanitized allowlist")
	}
}

func TestParseResultAllowsExactSemanticRetryButRejectsChangedDecision(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("session", "parent-a", "account")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	previous.Decisions = []capsule.Decision{{
		ID: "semantic-id", ConflictKey: "product.mode", Statement: "Use the bounded mode.",
		Status: capsule.DecisionActive, Authority: capsule.AuthoritySemantic, Rationale: "Accepted by the user.", SourceRef: "item-1",
	}}
	if err := previous.Seal(); err != nil {
		t.Fatal(err)
	}
	exact := `{"schema_version":1,"base_revision":1,"facts":[],"plan_updates":[],"decision_updates":[{"id":"semantic-id","conflict_key":"product.mode","supersedes":[],"statement":"Use the bounded mode.","status":"active","rationale":"Accepted by the user.","source_ref":"item-1"}],"remove_or_supersede":[]}`
	if _, err := ParseResult([]byte(exact), ParseOptions{Previous: previous, ExpectedBranch: branch, AllowedSourceRefs: []string{"item-1"}}); err != nil {
		t.Fatalf("exact semantic retry rejected: %v", err)
	}
	changed := strings.Replace(exact, "Use the bounded mode.", "Use the unbounded mode.", 1)
	if _, err := ParseResult([]byte(changed), ParseOptions{Previous: previous, ExpectedBranch: branch, AllowedSourceRefs: []string{"item-1"}}); err == nil {
		t.Fatal("changed same-ID semantic decision accepted")
	}
	for name, changed := range map[string]string{
		"status":    strings.Replace(exact, `"status":"active"`, `"status":"rejected"`, 1),
		"conflict":  strings.Replace(exact, `"conflict_key":"product.mode"`, `"conflict_key":"product.other"`, 1),
		"authority": strings.Replace(exact, `"source_ref":"item-1"`, `"authority":"user_explicit","source_ref":"item-1"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseResult([]byte(changed), ParseOptions{Previous: previous, ExpectedBranch: branch, AllowedSourceRefs: []string{"item-1"}}); err == nil {
				t.Fatalf("changed same-ID semantic decision accepted: %s", changed)
			}
		})
	}
}

func TestParseResultRejectsDuplicateRemovalAndCrossCollectionDecisionIDs(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("session", "parent-a", "account")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	previous.Decisions = []capsule.Decision{{
		ID: "semantic-id", ConflictKey: "product.mode", Statement: "Use the bounded mode.",
		Status: capsule.DecisionActive, Authority: capsule.AuthoritySemantic, SourceRef: "item-1",
	}}
	if err := previous.Seal(); err != nil {
		t.Fatal(err)
	}
	duplicateRemoval := `{"schema_version":1,"base_revision":1,"facts":[],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[{"id":"semantic-id","status":"superseded","source_ref":"item-1"},{"id":"semantic-id","status":"superseded","source_ref":"item-1"}]}`
	if _, err := ParseResult([]byte(duplicateRemoval), ParseOptions{Previous: previous, ExpectedBranch: branch, AllowedSourceRefs: []string{"item-1"}}); err == nil {
		t.Fatal("duplicate removal IDs accepted")
	}
	crossCollection := `{"schema_version":1,"base_revision":1,"facts":[],"plan_updates":[],"decision_updates":[{"id":"semantic-id","conflict_key":"product.mode","supersedes":[],"statement":"Use the bounded mode.","status":"active","source_ref":"item-1"}],"remove_or_supersede":[{"id":"semantic-id","status":"superseded","source_ref":"item-1"}]}`
	if _, err := ParseResult([]byte(crossCollection), ParseOptions{Previous: previous, ExpectedBranch: branch, AllowedSourceRefs: []string{"item-1"}}); err == nil {
		t.Fatal("decision/removal cross-collection duplicate accepted")
	}
}

func TestResultDeltaPreservesValidatedDecisionTransitions(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("session", "parent-a", "account")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	previous.Decisions = []capsule.Decision{{
		ID: "semantic-id", ConflictKey: "product.mode", Statement: "Use the bounded mode.",
		Status: capsule.DecisionActive, Authority: capsule.AuthoritySemantic, SourceRef: "item-1",
	}}
	if err := previous.Seal(); err != nil {
		t.Fatal(err)
	}
	raw := `{"schema_version":1,"base_revision":1,"facts":[],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[{"id":"semantic-id","status":"superseded","source_ref":"item-2"}]}`
	result, err := ParseResult([]byte(raw), ParseOptions{Previous: previous, ExpectedBranch: branch, AllowedSourceRefs: []string{"item-2"}})
	if err != nil {
		t.Fatal(err)
	}
	delta := result.Delta(branch, "item-2")
	if len(delta.DecisionTransitions) != 1 {
		t.Fatalf("decision transitions = %#v, want one transition", delta.DecisionTransitions)
	}
	transition := delta.DecisionTransitions[0]
	if transition.ID != "semantic-id" || transition.Status != capsule.DecisionSuperseded || transition.Authority != capsule.AuthoritySemantic || transition.SourceRef != "item-2" {
		t.Fatalf("decision transition = %#v", transition)
	}
}

func TestParseResultRejectsRemovalOfProtectedDecisionAuthorities(t *testing.T) {
	t.Parallel()
	protected := []capsule.Authority{
		capsule.AuthorityUserExplicit,
		capsule.AuthorityUserAcceptance,
		capsule.AuthorityStructured,
	}
	for _, authority := range protected {
		t.Run(string(authority), func(t *testing.T) {
			t.Parallel()
			branch, err := capsule.NewBranchBinding("session", "parent-a", "account")
			if err != nil {
				t.Fatal(err)
			}
			previous, err := capsule.New(branch)
			if err != nil {
				t.Fatal(err)
			}
			previous.Decisions = []capsule.Decision{{
				ID: "protected-id", ConflictKey: "product.mode", Statement: "Keep the protected mode.",
				Status: capsule.DecisionActive, Authority: authority, SourceRef: "item-1",
			}}
			if err := previous.Seal(); err != nil {
				t.Fatal(err)
			}
			raw := `{"schema_version":1,"base_revision":1,"facts":[],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[{"id":"protected-id","status":"superseded","source_ref":"item-1"}]}`
			if _, err := ParseResult([]byte(raw), ParseOptions{Previous: previous, ExpectedBranch: branch, AllowedSourceRefs: []string{"item-1"}}); err == nil {
				t.Fatalf("removal of %s decision authority accepted", authority)
			}
		})
	}
}

type fakeClient struct {
	Calls int
	Text  string
}

type fakeBackgroundClient struct {
	Calls int
}

func (f *fakeBackgroundClient) SubmitCollect(context.Context, auxiliary.Request, auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	f.Calls++
	return auxiliary.JobID("job"), nil
}

func (f *fakeBackgroundClient) Await(context.Context, auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}

func (f *fakeBackgroundClient) Forget(auxiliary.JobID) {}

func (f *fakeClient) Collect(context.Context, auxiliary.Request) (aipapi.Collected, error) {
	f.Calls++
	var out aipapi.Collected
	out.Text.WriteString(f.Text)
	return out, nil
}

func (f *fakeClient) Stream(context.Context, auxiliary.Request) (aipapi.EventStream, error) {
	return nil, nil
}

func TestCollectUsesExactlyOneChildCallAndNoSummaryRewrite(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("session", "parent-a", "account")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeClient{Text: `{"schema_version":1,"base_revision":1,"facts":[],"plan_updates":[],"decision_updates":[],"remove_or_supersede":[]}`}
	if _, err := Collect(context.Background(), fake, Input{Route: "extractor:model", ParentBranchBinding: branch, Previous: previous}); err != nil {
		t.Fatal(err)
	}
	if fake.Calls != 1 {
		t.Fatalf("child calls=%d want 1", fake.Calls)
	}
}
