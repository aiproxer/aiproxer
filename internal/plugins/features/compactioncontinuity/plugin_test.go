package compactioncontinuity

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/observability"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/resultmerge"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

type openParentFake struct {
	mu           sync.Mutex
	branch       ParentBranch
	state        ParentState
	order        []string
	capErr       error
	validateErr  error
	recordErr    error
	previewErr   error
	injectionErr error
	snapshotErr  error
}

func (f *openParentFake) Capture(context.Context, aipapi.Call, compaction.PreservationMeta) (ParentBranch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "capture")
	if f.capErr != nil {
		return ParentBranch{}, f.capErr
	}
	return f.branch, nil
}

func (f *openParentFake) CaptureMeta(context.Context, compaction.PreservationMeta) (ParentBranch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "capture-meta")
	if f.capErr != nil {
		return ParentBranch{}, f.capErr
	}
	return f.branch, nil
}

func (f *openParentFake) Snapshot(context.Context, ParentBranch) (ParentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "snapshot")
	if f.snapshotErr != nil {
		return ParentState{}, f.snapshotErr
	}
	return cloneOpenState(f.state), nil
}

func (f *openParentFake) CommitSource(_ context.Context, _ ParentBranch, revision uint64, data []byte, watermark string) (ParentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "source")
	if f.state.Revision != revision {
		return ParentState{}, errors.New("source revision mismatch")
	}
	f.state.SourceJSON = append([]byte(nil), data...)
	f.state.SourceHighWatermark = watermark
	return cloneOpenState(f.state), nil
}

func (f *openParentFake) CommitCapsule(_ context.Context, _ ParentBranch, revision uint64, data []byte, digest [32]byte, watermark string) (ParentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "capsule")
	if f.state.Revision != revision {
		return ParentState{}, errors.New("capsule revision mismatch")
	}
	f.state.Revision++
	f.state.CapsuleJSON = append([]byte(nil), data...)
	f.state.CapsuleDigest = digest
	f.state.SourceHighWatermark = watermark
	return cloneOpenState(f.state), nil
}

func (f *openParentFake) RecordPendingJob(_ context.Context, _ ParentBranch, id auxiliary.JobID, revision uint64) (ParentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "record")
	if f.recordErr != nil {
		return ParentState{}, f.recordErr
	}
	if f.state.PendingJobID != "" && f.state.PendingJobID != id {
		return ParentState{}, errors.New("pending job mismatch")
	}
	f.state.PendingJobID = id
	f.state.PendingJobTargetRevision = revision
	f.state.PendingJobBranchBinding = f.branch.Binding
	if revision != f.state.Revision {
		return ParentState{}, errors.New("pending revision mismatch")
	}
	return cloneOpenState(f.state), nil
}

func (f *openParentFake) CommitCapsuleForJob(_ context.Context, _ ParentBranch, id auxiliary.JobID, resultBinding string, revision uint64, data []byte, digest [32]byte, watermark string) (ParentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "capsule-job")
	if f.state.PendingJobID != id || f.state.PendingJobBranchBinding != f.branch.Binding || resultBinding != f.branch.Binding || f.state.Revision != revision || f.state.PendingJobTargetRevision != revision {
		return ParentState{}, errors.New("pending capsule mismatch")
	}
	f.state.Revision++
	f.state.CapsuleJSON = append([]byte(nil), data...)
	f.state.CapsuleDigest = digest
	f.state.SourceHighWatermark = watermark
	f.state.PendingJobID = ""
	f.state.PendingJobTargetRevision = 0
	f.state.PendingJobBranchBinding = ""
	return cloneOpenState(f.state), nil
}

func (f *openParentFake) ValidatePendingJob(context.Context, ParentBranch, auxiliary.JobID) (ParentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "validate")
	if f.validateErr != nil {
		return ParentState{}, f.validateErr
	}
	return cloneOpenState(f.state), nil
}

func (f *openParentFake) RecordPreviewIntent(_ context.Context, _ ParentBranch, intent PreviewIntent) (ParentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "preview-record")
	if f.previewErr != nil {
		return ParentState{}, f.previewErr
	}
	f.state.PendingPreviewIntent = &intent
	return cloneOpenState(f.state), nil
}

func (f *openParentFake) BindPreviewIntent(_ context.Context, _ ParentBranch, key, transaction string) (ParentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "preview-bind")
	if f.previewErr != nil || f.state.PendingPreviewIntent == nil || f.state.PendingPreviewIntent.Key != key || strings.TrimSpace(transaction) == "" {
		return ParentState{}, errors.New("preview mismatch")
	}
	f.state.PendingPreviewIntent = nil
	f.state.LastCompactionTransaction = transaction
	return cloneOpenState(f.state), nil
}

func (f *openParentFake) SetPendingInjection(_ context.Context, _ ParentBranch, target InjectionTarget) (ParentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "injection-set")
	if f.injectionErr != nil {
		return ParentState{}, f.injectionErr
	}
	f.state.PendingInjection = &target
	return cloneOpenState(f.state), nil
}

func (f *openParentFake) ValidateInjection(_ context.Context, _ ParentBranch, target InjectionTarget) (ParentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "injection-validate")
	if f.injectionErr != nil || f.state.PendingInjection == nil || *f.state.PendingInjection != target {
		return ParentState{}, errors.New("injection mismatch")
	}
	return cloneOpenState(f.state), nil
}

func (f *openParentFake) CommitReleasedInjection(_ context.Context, _ ParentBranch, watermark InjectionWatermark) (ParentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.order = append(f.order, "injection-commit")
	if f.state.PendingInjection == nil || f.state.PendingInjection.BoundaryKey != watermark.BoundaryKey || f.state.PendingInjection.CapsuleRevision != watermark.CapsuleRevision {
		return ParentState{}, errors.New("release mismatch")
	}
	f.state.LastReleasedInjection = &watermark
	f.state.PendingInjection = nil
	return cloneOpenState(f.state), nil
}

func cloneOpenState(in ParentState) ParentState {
	in.CapsuleJSON = append([]byte(nil), in.CapsuleJSON...)
	in.SourceJSON = append([]byte(nil), in.SourceJSON...)
	if in.PendingPreviewIntent != nil {
		intent := *in.PendingPreviewIntent
		in.PendingPreviewIntent = &intent
	}
	if in.PendingInjection != nil {
		target := *in.PendingInjection
		in.PendingInjection = &target
	}
	if in.LastReleasedInjection != nil {
		watermark := *in.LastReleasedInjection
		in.LastReleasedInjection = &watermark
	}
	return in
}

type openBackgroundFake struct {
	mu            sync.Mutex
	submits       []auxiliary.Request
	options       []auxiliary.SubmitOptions
	forget        []auxiliary.JobID
	job           auxiliary.JobID
	submitErr     error
	awaitResult   aipapi.Collected
	awaitErr      error
	awaited       bool
	awaitDeadline bool
}

func (f *openBackgroundFake) SubmitCollect(_ context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits = append(f.submits, req)
	f.options = append(f.options, opts)
	if f.submitErr != nil {
		return "", f.submitErr
	}
	if f.job == "" {
		f.job = "job-1"
	}
	return f.job, nil
}

func (f *openBackgroundFake) Await(ctx context.Context, _ auxiliary.JobID) (aipapi.Collected, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.awaited = true
	_, f.awaitDeadline = ctx.Deadline()
	return f.awaitResult, f.awaitErr
}

func (f *openBackgroundFake) Forget(id auxiliary.JobID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forget = append(f.forget, id)
}

func openConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := (Config{
		Extractor: ExtractorConfig{Enabled: true, Route: "extractor/model", Timeout: 137 * time.Millisecond, MaxInputTokens: 12_000, MaxOutputTokens: 77},
		Capsule:   CapsuleConfig{MaxBytes: 64 * 1024},
		Source:    SourceConfig{MaxBytes: 64 * 1024},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func openFixture(t *testing.T) (*Plugin, *openParentFake, *openBackgroundFake) {
	t.Helper()
	parent := &openParentFake{branch: ParentBranch{
		Binding: "sha256:" + strings.Repeat("a", 64), TraceID: "parent-trace", ALegID: "parent-a", BLegID: "parent-b",
	}}
	background := &openBackgroundFake{}
	plugin, err := New(openConfig(t), parent)
	if err != nil {
		t.Fatal(err)
	}
	return plugin, parent, background
}

func openCall() aipapi.Call {
	return aipapi.Call{Messages: []aipapi.Message{
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("Plan: retain the bounded continuity state before the next turn.")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("I choose the bounded mode and require continuity.")}},
	}}
}

func openEvent() []compaction.Event {
	return []compaction.Event{{Phase: compaction.PhaseStarted, TransactionID: "tx-committed", BLegID: "primary-b"}}
}

func openMeta() compaction.PreservationMeta {
	return compaction.PreservationMeta{TraceID: "meta-trace", SessionID: "parent-session", ALegID: "parent-a", BLegID: "primary-b"}
}

func TestRequestOpened_failedOrUncommittedEventDoesNotCaptureOrSubmit(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	ctx := context.Background()
	if err := plugin.BeforeRequest(ctx, nil, compaction.RequestPreview{TransactionID: "preview"}, openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatal(err)
	}
	_ = plugin.RequestOpened(ctx, openCall(), nil, openMeta(), compaction.Services{BackgroundAux: background})
	_ = plugin.RequestOpened(ctx, openCall(), []compaction.Event{{Phase: compaction.PhaseStarted}}, openMeta(), compaction.Services{BackgroundAux: background})
	_ = plugin.RequestOpened(ctx, openCall(), []compaction.Event{{Phase: compaction.PhaseStarted, TransactionID: " "}}, openMeta(), compaction.Services{BackgroundAux: background})
	if len(parent.order) != 0 || len(background.submits) != 0 {
		t.Fatalf("uncommitted event did work: parent=%v submits=%d", parent.order, len(background.submits))
	}
}

func TestRequestOpened_commitsParentBeforeSubmitAndUsesParentIdentity(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	_ = plugin.RequestOpened(context.Background(), openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
	if got, want := strings.Join(parent.order, ","), "capture,snapshot,source,capsule,record"; got != want {
		t.Fatalf("parent order=%q want %q", got, want)
	}
	if len(background.submits) != 1 || len(background.options) != 1 {
		t.Fatalf("submit count=%d options=%d", len(background.submits), len(background.options))
	}
	req := background.submits[0]
	if req.ParentBranchBinding != parent.branch.Binding || req.ParentALegID != parent.branch.ALegID || req.ParentBLegID != parent.branch.BLegID {
		t.Fatalf("request lost parent identity: %+v", req)
	}
	if req.SessionMode != auxiliary.SessionModeDetached || req.Role != "compaction_continuity_extractor" || req.Visibility != "private" {
		t.Fatalf("request lineage=%+v", req)
	}
	if req.Call == nil || req.Call.Route.Selector != "extractor/model" || len(req.Call.Tools) != 0 || req.Call.ToolChoice.Mode != aipapi.ToolChoiceNone {
		t.Fatalf("child call policy=%+v", req.Call)
	}
	if req.Call.Options.MaxOutputTokens == nil || *req.Call.Options.MaxOutputTokens != 77 {
		t.Fatalf("output bound=%v", req.Call.Options.MaxOutputTokens)
	}
	if got := background.options[0].Timeout; got != 137*time.Millisecond {
		t.Fatalf("timeout=%v", got)
	}
	if text := req.Call.Messages[1].Parts[0].Text; strings.Contains(text, parent.branch.Binding) || strings.Contains(text, parent.branch.ALegID) || strings.Contains(text, "parent-session") {
		t.Fatalf("raw parent identity in extractor prompt: %q", text)
	}
}

func TestRequestOpened_deterministicCarrierDoesNotSubmit(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	planOnly := plugin.cfg
	planOnly.Preserve = PreserveConfig{Plan: true}
	var err error
	plugin, err = New(planOnly, parent)
	if err != nil {
		t.Fatal(err)
	}
	call := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, ID: "u1", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "ordinary request"}}},
		{Kind: aipapi.ItemKindToolCall, ID: "tc1", ToolCall: &aipapi.ToolCallItem{Name: "update_plan", CallID: "call-1", Arguments: []byte(`{"plan":[{"step":"inspect bounded state","status":"in_progress"}]}`)}},
	}}
	_ = plugin.RequestOpened(context.Background(), call, openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
	if len(background.submits) != 0 {
		t.Fatal("deterministic carrier started semantic extraction")
	}
	if parent.state.Revision == 0 || len(parent.state.CapsuleJSON) == 0 {
		t.Fatalf("deterministic capsule was not committed: %+v", parent.state)
	}
	merged, err := capsule.Decode(parent.state.CapsuleJSON)
	if err != nil || len(merged.Plan.Steps) != 1 {
		t.Fatalf("deterministic plan was not merged: capsule=%+v err=%v", merged, err)
	}
}

func TestSemanticExtractionEligibility_planCarrierOnlyVsRequestedSemanticCategories(t *testing.T) {
	t.Parallel()
	base := Config{Extractor: ExtractorConfig{Enabled: true}}
	tests := []struct {
		name          string
		preserve      PreserveConfig
		candidate     bool
		deterministic bool
		wantEligible  bool
	}{
		{name: "plan-only deterministic state is sufficient", preserve: PreserveConfig{Plan: true}, candidate: true, deterministic: true, wantEligible: false},
		{name: "decision category remains eligible", preserve: PreserveConfig{Plan: true, UserDecisions: true}, candidate: true, deterministic: true, wantEligible: true},
		{name: "semantic category without carrier is eligible", preserve: PreserveConfig{UserDecisions: true}, candidate: true, deterministic: false, wantEligible: true},
		{name: "no requested category is not eligible", preserve: PreserveConfig{}, candidate: true, deterministic: false, wantEligible: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			cfg.Preserve = tt.preserve
			if got := semanticExtractionEligible(cfg, tt.candidate, tt.deterministic); got != tt.wantEligible {
				t.Fatalf("eligible=%v want %v", got, tt.wantEligible)
			}
		})
	}
}

func TestRequestOpened_planCarrierDoesNotSuppressRequestedSemanticCategories(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	cfg := plugin.cfg
	cfg.Preserve = PreserveConfig{Plan: true, UserDecisions: true}
	var err error
	plugin, err = New(cfg, parent)
	if err != nil {
		t.Fatal(err)
	}
	call := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, ID: "tc1", ToolCall: &aipapi.ToolCallItem{Name: "update_plan", CallID: "call-1", Arguments: []byte(`{"plan":[{"step":"inspect bounded state","status":"in_progress"}]}`)}},
		{Kind: aipapi.ItemKindMessage, ID: "choice", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "I choose the bounded mode."}}},
	}}
	_ = plugin.RequestOpened(context.Background(), call, openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
	if len(background.submits) != 1 {
		t.Fatalf("semantic category was suppressed by plan carrier: submits=%d", len(background.submits))
	}
}

func TestRequestOpened_duplicateOpenUsesParentPendingJobAsCoalesceGuard(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	ctx := context.Background()
	_ = plugin.RequestOpened(ctx, openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
	_ = plugin.RequestOpened(ctx, openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
	if got := len(background.submits); got != 1 {
		t.Fatalf("submit count=%d want 1", got)
	}
	if parent.state.PendingJobID == "" {
		t.Fatal("pending job was not bound to parent")
	}
	if key := background.options[0].CoalesceKey; strings.Contains(key, "parent-session") || strings.Contains(key, "tx-committed") || !strings.HasPrefix(key, "sha256:") {
		t.Fatalf("coalesce key leaked identity or was not hashed: %q", key)
	}
	if background.awaited {
		t.Fatal("RequestOpened awaited a late result")
	}
}

func TestRequestOpened_sourceOmitsUnrelatedToolDumpAndRawParentIdentifiers(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	call := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindToolCall, ID: "tool-call", ToolCall: &aipapi.ToolCallItem{Name: "shell", CallID: "shell-1", Arguments: []byte(`{"command":"cat secrets"}`)}},
		{Kind: aipapi.ItemKindToolResult, ID: "tool-result", ToolResult: &aipapi.ToolResultItem{Name: "shell", CallID: "shell-1", Output: `{"rows":["raw-dump-secret"]}`}},
		{Kind: aipapi.ItemKindMessage, ID: "user-choice", Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "I choose the bounded mode."}}},
	}}
	_ = plugin.RequestOpened(context.Background(), call, openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
	if len(background.submits) != 1 {
		t.Fatalf("submits=%d want 1", len(background.submits))
	}
	prompt := background.submits[0].Call.Messages[1].Parts[0].Text
	for _, forbidden := range []string{"raw-dump-secret", "cat secrets", parent.branch.Binding, parent.branch.ALegID, "parent-session"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt contains forbidden %q: %s", forbidden, prompt)
		}
	}
}

func TestRequestOpened_inheritUsesPrimaryRouteWithoutSharingChildTools(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	cfg := plugin.cfg
	cfg.Extractor.Route = ""
	cfg.Extractor.Inherit = true
	plugin, err := New(cfg, parent)
	if err != nil {
		t.Fatal(err)
	}
	call := openCall()
	call.Route.Selector = "primary/model"
	_ = plugin.RequestOpened(context.Background(), call, openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
	if len(background.submits) != 1 {
		t.Fatalf("submits=%d want 1", len(background.submits))
	}
	if got := background.submits[0].Call.Route.Selector; got != "primary/model" {
		t.Fatalf("inherited route=%q", got)
	}
	if len(background.submits[0].Call.Tools) != 0 || background.submits[0].Call.ToolChoice.Mode != aipapi.ToolChoiceNone {
		t.Fatal("detached extractor inherited primary tools")
	}
}

func pendingResponseFixture(t *testing.T) (*Plugin, *openParentFake, *openBackgroundFake) {
	t.Helper()
	plugin, parent, background := openFixture(t)
	if err := plugin.RequestOpened(context.Background(), openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatal(err)
	}
	if parent.state.PendingJobID == "" {
		t.Fatal("fixture did not create pending job")
	}
	return plugin, parent, background
}

func completionPreview() compaction.ResponsePreview {
	return compaction.ResponsePreview{Kind: compaction.PreviewCompletionCandidate, TransactionID: "tx-committed", RuleID: "protocol.context_compaction.v1"}
}

func semanticResult() aipapi.Collected {
	var result aipapi.Collected
	result.Text.WriteString(`{"schema_version":1,"base_revision":1,"facts":[],"plan_updates":[],"decision_updates":[{"id":"decision-ready","conflict_key":"runtime.mode","supersedes":[],"statement":"Use the bounded mode.","status":"active","rationale":"explicit","source_ref":"msg-1"}],"remove_or_supersede":[]}`)
	return result
}

func TestBeforeResponseRelease_readyResultMergesAtBoundedBarrierWithoutSubmitOrEventMutation(t *testing.T) {
	t.Parallel()
	plugin, parent, background := pendingResponseFixture(t)
	background.awaitResult = semanticResult()
	ev := aipapi.Event{Kind: aipapi.EventItem, Opaque: []byte(`{"opaque":"exact"}`)}
	before := ev
	_ = plugin.BeforeResponseRelease(context.Background(), &ev, completionPreview(), openMeta(), compaction.Services{BackgroundAux: background})
	if !background.awaited || !background.awaitDeadline {
		t.Fatalf("await did not use bounded context: awaited=%v deadline=%v", background.awaited, background.awaitDeadline)
	}
	if len(background.submits) != 1 {
		t.Fatalf("response barrier submitted new work: submits=%d", len(background.submits))
	}
	if parent.state.PendingJobID != "" || parent.state.Revision != 2 {
		t.Fatalf("ready result was not atomically merged: state=%+v", parent.state)
	}
	merged, err := capsule.Decode(parent.state.CapsuleJSON)
	if err != nil || len(merged.Decisions) != 1 || merged.Decisions[0].ID != "decision-ready" {
		t.Fatalf("merged capsule=%+v err=%v", merged, err)
	}
	if !reflect.DeepEqual(ev, before) {
		t.Fatalf("unsupported/opaque event mutated: before=%+v after=%+v", before, ev)
	}
}

func TestBeforeResponseRelease_timeoutRetainsPendingRawResult(t *testing.T) {
	t.Parallel()
	plugin, parent, background := pendingResponseFixture(t)
	background.awaitErr = context.DeadlineExceeded
	ev := aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "stop"}
	_ = plugin.BeforeResponseRelease(context.Background(), &ev, completionPreview(), openMeta(), compaction.Services{BackgroundAux: background})
	if !background.awaited || len(background.forget) != 0 || parent.state.PendingJobID == "" {
		t.Fatalf("timeout did not retain pending result: awaited=%v forgotten=%v state=%+v", background.awaited, background.forget, parent.state)
	}
}

func TestBeforeResponseRelease_invalidOrWrongParentFailsOpen(t *testing.T) {
	t.Parallel()
	plugin, parent, background := pendingResponseFixture(t)
	background.awaitResult.Text.WriteString(`not-json`)
	ev := aipapi.Event{Kind: aipapi.EventResponseFinished, Opaque: []byte("opaque")}
	before := ev
	_ = plugin.BeforeResponseRelease(context.Background(), &ev, completionPreview(), openMeta(), compaction.Services{BackgroundAux: background})
	if len(background.forget) != 1 || background.forget[0] != "job-1" || parent.state.PendingJobID == "" || !reflect.DeepEqual(ev, before) {
		t.Fatalf("invalid result handling: forgotten=%v state=%+v event=%+v", background.forget, parent.state, ev)
	}

	plugin, parent, background = pendingResponseFixture(t)
	parent.validateErr = errors.New("wrong parent")
	_ = plugin.BeforeResponseRelease(context.Background(), &ev, completionPreview(), openMeta(), compaction.Services{BackgroundAux: background})
	if background.awaited || len(background.forget) != 0 || parent.state.PendingJobID == "" {
		t.Fatalf("wrong parent crossed await boundary: awaited=%v forgotten=%v state=%+v", background.awaited, background.forget, parent.state)
	}
}

func TestBeforeResponseRelease_requiresActualCompletionPreview(t *testing.T) {
	t.Parallel()
	plugin, parent, background := pendingResponseFixture(t)
	_ = plugin.BeforeResponseRelease(context.Background(), &aipapi.Event{}, compaction.ResponsePreview{Kind: compaction.PreviewNone, TransactionID: "tx-committed"}, openMeta(), compaction.Services{BackgroundAux: background})
	_ = plugin.BeforeResponseRelease(context.Background(), &aipapi.Event{}, compaction.ResponsePreview{Kind: compaction.PreviewCompletionCandidate}, openMeta(), compaction.Services{BackgroundAux: background})
	if background.awaited || len(background.submits) != 1 || parent.state.PendingJobID == "" {
		t.Fatalf("non-boundary preview did work: awaited=%v submits=%d state=%+v", background.awaited, len(background.submits), parent.state)
	}
}

func TestRequestOpened_submitOrRecordFailureIsFailOpen(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	background.submitErr = errors.New("queue full")
	_ = plugin.RequestOpened(context.Background(), openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
	if len(parent.order) != 4 || len(background.submits) != 1 {
		t.Fatalf("submit failure altered primary path: parent=%v submits=%d", parent.order, len(background.submits))
	}
	plugin, parent, background = openFixture(t)
	background.job = "job-record-fails"
	parent.recordErr = errors.New("state unavailable")
	_ = plugin.RequestOpened(context.Background(), openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
	if len(background.forget) != 1 || background.forget[0] != background.job {
		t.Fatalf("record failure did not forget unusable raw result: %#v", background.forget)
	}
}

func TestFeatureBundleWithPort_requiresExplicitPort(t *testing.T) {
	t.Parallel()
	if _, err := FeatureBundleWithPort(openConfig(t), nil); err == nil {
		t.Fatal("nil parent port accepted")
	}
	plugin, parent, _ := openFixture(t)
	bundle, err := FeatureBundleWithPort(plugin.cfg, parent)
	if err != nil || len(aipfeature.Get(bundle.PlaneSet, aipfeature.PlaneCompactionPreservers)) != 1 {
		t.Fatalf("bundle=%+v err=%v", bundle, err)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestBeforeRequest_completionPreviewStoresIntentInjectsWithoutSubmit(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	call := openCall()
	before := aipapi.CloneCall(call)
	err := plugin.BeforeRequest(context.Background(), &call, compaction.RequestPreview{
		Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "preview-boundary-1",
	}, openMeta(), compaction.Services{BackgroundAux: background})
	if err != nil {
		t.Fatalf("BeforeRequest: %v", err)
	}
	if len(background.submits) != 0 {
		t.Fatalf("pre-open preview submitted work: %d", len(background.submits))
	}
	if parent.state.PendingPreviewIntent == nil || parent.state.PendingPreviewIntent.Key == "" {
		t.Fatalf("preview intent not recorded: %+v", parent.state)
	}
	if parent.state.PendingInjection == nil || parent.state.PendingInjection.BoundaryKey != "preview-boundary-1" {
		t.Fatalf("pending injection not prepared: %+v", parent.state)
	}
	if reflect.DeepEqual(call, before) || len(call.Instructions) != 1 {
		t.Fatalf("deterministic capsule was not injected: before=%+v after=%+v", before, call)
	}
	_ = plugin.BeforeRequest(context.Background(), &call, compaction.RequestPreview{
		Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "preview-boundary-1",
	}, openMeta(), compaction.Services{BackgroundAux: background})
	if len(call.Instructions) != 1 {
		t.Fatalf("retry/failover duplicated call-local projection: %d", len(call.Instructions))
	}
}

func TestRequestOpened_bindsPreviewIntentAndSubmitsOnlyAfterOpen(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	call := openCall()
	_ = plugin.BeforeRequest(context.Background(), &call, compaction.RequestPreview{
		Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "preview-boundary-2",
	}, openMeta(), compaction.Services{BackgroundAux: background})
	if parent.state.PendingPreviewIntent == nil {
		t.Fatal("missing preview intent")
	}
	_ = plugin.RequestOpened(context.Background(), call, openEvent(), openMeta(), compaction.Services{BackgroundAux: background})
	if parent.state.PendingPreviewIntent != nil || parent.state.LastCompactionTransaction != "tx-committed" {
		t.Fatalf("preview intent was not bound: %+v", parent.state)
	}
	if parent.state.PendingInjection == nil || parent.state.PendingInjection.BoundaryKey != "tx-committed" {
		t.Fatalf("preview injection boundary was not rebound: %+v", parent.state)
	}
	if len(background.submits) != 1 {
		t.Fatalf("successful open did not permit exactly one submit: %d", len(background.submits))
	}
}

func TestAfterResponseRelease_commitsOnlyTerminalMatchingPreparedInjection(t *testing.T) {
	t.Parallel()
	plugin, parent, background := pendingResponseFixture(t)
	parent.state.PendingInjection = &InjectionTarget{BoundaryKey: "tx-committed", CapsuleRevision: parent.state.Revision}
	meta := openMeta()
	meta.TransactionID = "tx-committed"
	plugin.recordPreparedMarker(meta, *parent.state.PendingInjection)
	_ = plugin.AfterResponseRelease(context.Background(), aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "partial"}, meta, compaction.Services{BackgroundAux: background})
	if parent.state.PendingInjection == nil {
		t.Fatal("non-terminal event committed release watermark")
	}
	_ = plugin.AfterResponseRelease(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "stop"}, meta, compaction.Services{BackgroundAux: background})
	if parent.state.PendingInjection != nil || parent.state.LastReleasedInjection == nil || parent.state.LastReleasedInjection.BoundaryKey != "tx-committed" {
		t.Fatalf("terminal release did not commit watermark: %+v", parent.state)
	}
}

func TestAfterResponseRelease_terminalWithoutPreparedMarkerRetainsPending(t *testing.T) {
	t.Parallel()
	plugin, parent, background := pendingResponseFixture(t)
	parent.state.PendingInjection = &InjectionTarget{BoundaryKey: "tx-committed", CapsuleRevision: parent.state.Revision}
	meta := openMeta()
	meta.TransactionID = "tx-committed"
	_ = plugin.AfterResponseRelease(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished}, meta, compaction.Services{BackgroundAux: background})
	if parent.state.PendingInjection == nil || parent.state.LastReleasedInjection != nil {
		t.Fatalf("unprepared terminal cleared pending state: %+v", parent.state)
	}
}

func TestBeforeRequest_validationFailureRestoresCallAndPendingState(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	parent.state.PendingInjection = &InjectionTarget{BoundaryKey: "old-boundary", CapsuleRevision: 1}
	parent.injectionErr = errors.New("validation failed")
	call := openCall()
	before := aipapi.CloneCall(call)
	_ = plugin.BeforeRequest(context.Background(), &call, compaction.RequestPreview{Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "new-boundary"}, openMeta(), compaction.Services{BackgroundAux: background})
	if !reflect.DeepEqual(call, before) || parent.state.PendingInjection == nil || parent.state.PendingInjection.BoundaryKey != "old-boundary" {
		t.Fatalf("validation failure changed call/pending state: call=%+v state=%+v", call, parent.state)
	}
}

func TestBeforeRequest_setPendingInjectionFailureIsFeatureLocal(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	parent.injectionErr = errors.New("injection state unavailable")
	call := openCall()
	before := aipapi.CloneCall(call)
	err := plugin.BeforeRequest(context.Background(), &call, compaction.RequestPreview{
		Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "set-pending-failure",
	}, openMeta(), compaction.Services{BackgroundAux: background})
	if err != nil {
		t.Fatalf("feature callback error escaped: %v", err)
	}
	if !reflect.DeepEqual(call, before) || parent.state.PendingInjection != nil || len(background.submits) != 0 {
		t.Fatalf("set-pending failure changed primary flow: call=%#v state=%#v submits=%d", call, parent.state, len(background.submits))
	}
}

func TestAfterResponseRelease_distinguishesSnapshotErrorFromNoPendingInjection(t *testing.T) {
	t.Parallel()
	plugin, parent, _ := pendingResponseFixture(t)
	recorder := observability.NewRecorder(64)
	plugin.obs = recorder
	parent.snapshotErr = errors.New("state unavailable")
	meta := openMeta()
	meta.TransactionID = "watermark-snapshot-error"
	_ = plugin.AfterResponseRelease(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished}, meta, compaction.Services{})
	if !hasObservation(recorder.Snapshot(), observability.StageWatermark, observability.OutcomeFailed) {
		t.Fatalf("snapshot error was not recorded as failure: %#v", recorder.Snapshot())
	}

	parent.snapshotErr = nil
	_ = plugin.AfterResponseRelease(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished}, meta, compaction.Services{})
	if !hasObservation(recorder.Snapshot(), observability.StageWatermark, observability.OutcomeSkipped) {
		t.Fatalf("missing pending injection was not recorded as skip: %#v", recorder.Snapshot())
	}

	parent.branch.Binding = ""
	_ = plugin.AfterResponseRelease(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished}, meta, compaction.Services{})
	if !hasObservation(recorder.Snapshot(), observability.StageWatermark, observability.OutcomeSkipped) {
		t.Fatalf("invalid parent was not recorded as skip: %#v", recorder.Snapshot())
	}
}

func TestErrorOutcome_usesKnownSentinels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want observability.Outcome
	}{
		{name: "rejected", err: fmt.Errorf("wrapped: %w", resultmerge.ErrRejected), want: observability.OutcomeRejected},
		{name: "invalid branch", err: fmt.Errorf("wrapped: %w", capsule.ErrInvalidBranch), want: observability.OutcomeInvalid},
		{name: "invalid parent", err: fmt.Errorf("wrapped: %w", resultmerge.ErrInvalidParentState), want: observability.OutcomeInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := errorOutcome(tc.err); got != tc.want {
				t.Fatalf("errorOutcome(%v)=%q want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestRequestOpenFailed_clearsEphemeralMarkerButRetainsPendingInjection(t *testing.T) {
	t.Parallel()
	plugin, parent, background := pendingResponseFixture(t)
	parent.state.PendingInjection = &InjectionTarget{BoundaryKey: "tx-failed", CapsuleRevision: parent.state.Revision}
	meta := openMeta()
	meta.TransactionID = "tx-failed"
	plugin.recordPreparedMarker(meta, *parent.state.PendingInjection)
	if err := plugin.RequestOpenFailed(context.Background(), meta, compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatal(err)
	}
	_ = plugin.AfterResponseRelease(context.Background(), aipapi.Event{Kind: aipapi.EventResponseFinished}, meta, compaction.Services{BackgroundAux: background})
	if parent.state.PendingInjection == nil || parent.state.LastReleasedInjection != nil {
		t.Fatalf("failed-open marker affected durable pending state: %+v", parent.state)
	}
}

// TestFailedOpen_producesNoBillableJobAfterPreOpenPrep locks requirement 7.12
// and design §571 for the pre-open path: a completion-candidate preview may
// stage intent/capsule state before Open, but if the open then fails, no
// billable child job may be submitted.
func TestFailedOpen_producesNoBillableJobAfterPreOpenPrep(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	call := openCall()
	meta := openMeta()
	_ = plugin.BeforeRequest(context.Background(), &call, compaction.RequestPreview{
		Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "failed-open-boundary",
	}, meta, compaction.Services{BackgroundAux: background})
	if parent.state.PendingPreviewIntent == nil {
		t.Fatal("pre-open preview intent was not staged")
	}
	if err := plugin.RequestOpenFailed(context.Background(), meta, compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatal(err)
	}
	if len(background.submits) != 0 {
		t.Fatalf("failed open submitted billable work: %d", len(background.submits))
	}
}

func TestBeforeRequest_nearMissAndStartPreviewDoNotCreateCompletionIntent(t *testing.T) {
	t.Parallel()
	plugin, parent, background := openFixture(t)
	call := openCall()
	before := aipapi.CloneCall(call)
	for _, preview := range []compaction.RequestPreview{
		{Kind: compaction.PreviewNone, BoundaryFingerprint: "near-miss"},
		{Kind: compaction.PreviewStartCandidate, BoundaryFingerprint: "start"},
	} {
		_ = plugin.BeforeRequest(context.Background(), &call, preview, openMeta(), compaction.Services{BackgroundAux: background})
	}
	if !reflect.DeepEqual(call, before) || parent.state.PendingPreviewIntent != nil || len(background.submits) != 0 {
		t.Fatalf("near-miss/start preview acted: call=%+v state=%+v submits=%d", call, parent.state, len(background.submits))
	}
}

var (
	_ ParentPort                 = (*openParentFake)(nil)
	_ auxiliary.BackgroundClient = (*openBackgroundFake)(nil)
	_ compaction.Preserver       = (*Plugin)(nil)
)
