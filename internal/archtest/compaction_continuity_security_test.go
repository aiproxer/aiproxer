package archtest

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	compactiondetect "github.com/aiproxer/aiproxer/internal/infra/compactiondetect"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/augmentation"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/extractor"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/injection"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/observability"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/source"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

const compactionModule = "github.com/aiproxer/aiproxer/"

// TestCompactionContinuitySecurity_NoProviderOrWireDependencies keeps the
// detector, coordinator-facing feature, and extractor on canonical contracts.
// The dependency check is transitive so a provider DTO or client cannot arrive
// through a helper package.
func TestCompactionContinuitySecurity_NoProviderOrWireDependencies(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{
		"./internal/core/auxreq/...",
		"./internal/infra/compactiondetect/...",
		"./internal/plugins/features/compactioncontinuity/...",
	}, []forbiddenDep{
		{Substr: "/internal/plugins/frontends/", ErrMsg: "compaction continuity must not import frontend DTOs"},
		{Substr: "/internal/plugins/backends/", ErrMsg: "compaction continuity must not import backend adapters"},
		{Substr: "/internal/plugins/protocols/", ErrMsg: "compaction continuity must not import protocol DTOs"},
		{Substr: "github.com/openai/openai-go", ErrMsg: "compaction continuity must not import the OpenAI client"},
		{Substr: "github.com/anthropics/anthropic-sdk-go", ErrMsg: "compaction continuity must not import the Anthropic client"},
		{Substr: "google.golang.org/genai", ErrMsg: "compaction continuity must not import the GenAI client"},
		{Substr: "github.com/aws/aws-sdk-go-v2", ErrMsg: "compaction continuity must not import AWS clients"},
	})
}

// TestCompactionContinuitySecurity_NoFeatureOwnedPersistenceOrMoneyPath
// prevents the feature from acquiring a second transcript store, ledger, or
// durable job framework. Existing continuity, billing, and scheduler seams are
// deliberately consumed through ports instead.
func TestCompactionContinuitySecurity_NoFeatureOwnedPersistenceOrMoneyPath(t *testing.T) {
	t.Parallel()
	assertDepsExcludeForbidden(t, []string{"./internal/plugins/features/compactioncontinuity/..."}, []forbiddenDep{
		{Substr: "/internal/core/continuity", ErrMsg: "feature must not own a second transcript store"},
		{Substr: "/internal/core/securesession", ErrMsg: "feature must use the secure-session seam, not its store"},
		{Substr: "/internal/core/billing", ErrMsg: "feature must not own a money ledger"},
		{Substr: "/enterprise/internal/infra/billingstore", ErrMsg: "feature must not write billing persistence"},
		{Substr: "/internal/core/tokenaccounting", ErrMsg: "feature must not use the legacy token ledger"},
		{Substr: "database/sql", ErrMsg: "feature must not open a transcript database"},
		{Substr: "github.com/uptrace/bun", ErrMsg: "feature must not add a durable ORM store"},
	})

	assertExactFields(t, "auxiliary.BackgroundClient", reflect.TypeFor[auxiliary.BackgroundClient](), "Await", "Forget", "SubmitCollect")
	assertExactFields(t, "compaction.Services", reflect.TypeFor[compaction.Services](), "BackgroundAux", "State")
	// OnCoalesced is additive optional content-free callback on SubmitOptions; it is not a BackgroundClient method and preserves three-method fixture.
	// MaxOutputBytes is additive per-job pre-collection byte bound (zero=existing scheduler/default behavior).
	// Deadline is an optional queue-inclusive bound; zero retains detached lifetime.
	assertExactFields(t, "auxiliary.SubmitOptions", reflect.TypeFor[auxiliary.SubmitOptions](), "CoalesceKey", "Deadline", "MaxOutputBytes", "OnCoalesced", "Timeout")
}

// TestCompactionContinuitySecurity_DetachedModeIsNotWireSettable verifies the
// trusted detached control remains an auxiliary execution field. Frontends
// and wire codecs have no direct import edge to that control or its context.
func TestCompactionContinuitySecurity_DetachedModeIsNotWireSettable(t *testing.T) {
	t.Parallel()
	assertProductionDirectImportsExclude(t, []string{"internal/plugins/frontends", "internal/plugins/protocols"}, []string{
		compactionModule + "pkg/aipsdk/auxiliary",
		compactionModule + "internal/core/execctx",
		compactionModule + "pkg/aipsdk/compaction",
		compactionModule + "internal/plugins/features/compactioncontinuity",
	})

	for _, typ := range []reflect.Type{reflect.TypeFor[aipapi.Call](), reflect.TypeFor[aipapi.Event](), reflect.TypeFor[aipapi.Invocation]()} {
		assertTypeKeysAbsent(t, typ, map[string]struct{}{
			"sessionmode": {}, "detached": {}, "parentbranchbinding": {},
			"parentalegid": {}, "parentblegid": {}, "parenttraceid": {},
			"branchkey": {},
		})
	}
	assertExactFields(t, "auxiliary.Request", reflect.TypeFor[auxiliary.Request](), "Call", "DisablePlugins", "ParentALegID", "ParentBLegID", "ParentBranchBinding", "ParentTraceID", "Role", "SessionMode", "Visibility")
}

// TestCompactionContinuitySecurity_ContentFreePublicSurfaces freezes the
// metadata-only observer and accounting/report shapes. Raw source, capsule,
// result, and BranchKey values have no ordinary log or money-record slot.
func TestCompactionContinuitySecurity_ContentFreePublicSurfaces(t *testing.T) {
	t.Parallel()
	assertExactFields(t, "compaction.Event", reflect.TypeFor[compaction.Event](), "ALegID", "AttemptSeq", "BLegID", "Evidence", "OccurredAt", "Phase", "RuleID", "SessionID", "TraceID", "TransactionID")
	assertExactFields(t, "compaction.PreservationMeta", reflect.TypeFor[compaction.PreservationMeta](), "ALegID", "AttemptSeq", "BLegID", "Evidence", "RuleID", "SessionID", "TraceID", "TransactionID")
	assertExactFields(t, "compaction.RequestPreview", reflect.TypeFor[compaction.RequestPreview](), "BoundaryFingerprint", "Evidence", "Kind", "RuleID", "TransactionID")
	assertExactFields(t, "compaction.ResponsePreview", reflect.TypeFor[compaction.ResponsePreview](), "Evidence", "Kind", "RuleID", "TransactionID")
	assertExactFields(t, "observability.Observation", reflect.TypeFor[observability.Observation](), "CorrelationHash", "Count", "Duration", "Evidence", "FactCount", "Outcome", "Phase", "Revision", "RuleID", "SizeBytes", "Stage")
	assertExactFields(t, "usageevidence.WorkloadIdentity", reflect.TypeFor[usageevidence.WorkloadIdentity](), "Class", "Role")
	assertExactFields(t, "usageevidence.CallUsageRecord", reflect.TypeFor[usageevidence.CallUsageRecord](), "ALegID", "AccountID", "CallID", "ChargePolicyRef", "CustomerPricingRef", "ExpectedBLegIDs", "Fingerprint", "FinishedAt", "Key", "Outcome", "SchemaVersion", "SessionID", "StartedAt", "SubmissionID", "Workload")
	assertExactFields(t, "usageevidence.CallLegUsageRecord", reflect.TypeFor[usageevidence.CallLegUsageRecord](), "ALegID", "AttemptSeq", "BackendID", "BLegID", "CallID", "EconomicDispositions", "EconomicEvidenceVersion", "Evidence", "EvidenceConflicts", "EvidenceProjection", "EvidenceVersion", "Fingerprint", "FinishedAt", "Key", "ModelID", "ObservationRefs", "Observations", "OperatorRateRef", "Outcome", "ProviderID", "StartedAt", "SubmissionID", "Surfaced", "Workload")

	for _, value := range []any{
		compaction.Event{},
		compaction.PreservationMeta{},
		compaction.RequestPreview{},
		compaction.ResponsePreview{},
		observability.Observation{},
		usageevidence.WorkloadIdentity{},
	} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal %T: %v", value, err)
		}
		lower := strings.ToLower(string(raw))
		for _, forbidden := range []string{"capsulejson", "sourcejson", "rawresult", "branchkey", "prompt", "completion"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("content-bearing key %q entered %T JSON: %s", forbidden, value, raw)
			}
		}
	}
}

// TestCompactionContinuitySecurity_ExtractorEgressIsSanitizedAndContentBounded
// checks both sides of the egress seam: sanitized source is allowed in the
// child prompt, while parent lineage, BranchKey material, and capsule digest
// stay out of the canonical child call and model-facing projection.
func TestCompactionContinuitySecurity_ExtractorEgressIsSanitizedAndContentBounded(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("parent-session", "parent-a", "principal")
	if err != nil {
		t.Fatal(err)
	}
	trace, parentA, parentB := "trace-parent-only", "parent-a-leg-only", "parent-b-leg-only"
	req, err := extractor.BuildRequest(extractor.Input{
		Route:               "route:extractor",
		ParentBranchBinding: branch,
		ParentTraceID:       trace,
		ParentALegID:        parentA,
		ParentBLegID:        parentB,
		SourceHighWatermark: "wm-1",
		SanitizedDelta: []source.Entry{{
			Kind: source.EntryUserDecision, ItemID: "item-1", Text: "retain only this sanitized decision",
		}},
		MaxInputTokens: 100_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.SessionMode != auxiliary.SessionModeDetached || req.Call == nil || req.Call.ToolChoice.Mode != aipapi.ToolChoiceNone {
		t.Fatalf("child request lost detached/no-tools policy: %+v", req)
	}
	if len(req.Call.Messages) != 2 {
		t.Fatalf("extractor must build one fixed system prompt plus one data prompt, got %d messages", len(req.Call.Messages))
	}
	rawCall, err := json.Marshal(req.Call)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{branch, trace, parentA, parentB} {
		if bytes.Contains(rawCall, []byte(secret)) {
			t.Fatalf("parent lineage %q leaked into canonical extractor call: %s", secret, rawCall)
		}
	}
	if !bytes.Contains(rawCall, []byte("retain only this sanitized decision")) {
		t.Fatalf("sanitized source did not reach the fixed child prompt: %s", rawCall)
	}

	e, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	block, err := injection.SerializeBlock(e, branch, injection.ProjectionLimits{MaxBytes: 16_384, MaxTokens: 16_384})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{branch, e.ContentDigest} {
		if bytes.Contains(block, []byte(secret)) {
			t.Fatalf("integrity metadata %q leaked into model-facing projection: %s", secret, block)
		}
	}
}

// TestCompactionContinuitySecurity_DetectorOwnsSignatureAndBoundaryIdentity
// checks that the detector's preview and committed event share the same rule
// identity. The feature receives this metadata through the SDK and cannot
// recreate a second signature catalog in its own package.
func TestCompactionContinuitySecurity_DetectorOwnsSignatureAndBoundaryIdentity(t *testing.T) {
	t.Parallel()
	d := compactiondetect.New(compactiondetect.Config{})
	meta := compaction.PreservationMeta{TraceID: "detector-trace", ALegID: "detector-a-leg"}
	call := aipapi.Call{Messages: []aipapi.Message{{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart("context checkpoint compaction")},
	}}}
	preview := d.PreviewRequest(meta, call)
	if preview.Kind != compaction.PreviewStartCandidate || preview.RuleID == "" {
		t.Fatalf("detector preview=%+v", preview)
	}
	if events := d.RequestOpened(meta, call); len(events) != 1 || events[0].RuleID != preview.RuleID {
		t.Fatalf("committed detector identity diverged: preview=%+v events=%v", preview, events)
	}
	if preview.TransactionID != "" || preview.BoundaryFingerprint != "" {
		t.Fatalf("strict pre-open preview exposed a committed identity: %+v", preview)
	}

	d = compactiondetect.New(compactiondetect.Config{})
	historyMeta := compaction.PreservationMeta{TraceID: "history-current", ALegID: "history-a-leg"}
	prior := detectorItemCall(strings.Repeat("prior ", 7_000), strings.Repeat("tail-a ", 1_500), strings.Repeat("tail-b ", 1_500))
	current := detectorItemCall(strings.Repeat("current ", 700), strings.Repeat("tail-a ", 1_500), strings.Repeat("tail-b ", 1_500))
	if events := d.RequestOpened(compaction.PreservationMeta{TraceID: "history-prior", ALegID: historyMeta.ALegID}, prior); len(events) != 0 {
		t.Fatalf("history setup emitted events: %v", events)
	}
	boundary := d.PreviewRequest(historyMeta, current)
	if boundary.Kind != compaction.PreviewCompletionCandidate || boundary.BoundaryFingerprint == "" {
		t.Fatalf("completion preview lost detector-owned boundary identity: %+v", boundary)
	}
	if again := d.PreviewRequest(historyMeta, current); again.BoundaryFingerprint != boundary.BoundaryFingerprint {
		t.Fatalf("detector boundary identity changed across pure previews: first=%+v again=%+v", boundary, again)
	}
}

// TestCompactionContinuitySecurity_ObserverAndPreserverCannotAuthorizeRetry
// freezes the one-way extension seams. Observers return only errors and
// preservers have no retry/router/failover method; callback failures therefore
// remain fail-open diagnostics rather than new attempt authority.
func TestCompactionContinuitySecurity_ObserverAndPreserverCannotAuthorizeRetry(t *testing.T) {
	t.Parallel()
	assertExactMethods(t, "compaction.Observer", reflect.TypeFor[compaction.Observer](), "OnCompaction")
	assertExactMethods(t, "compaction.Preserver", reflect.TypeFor[compaction.Preserver](), "BeforeRequest", "BeforeResponseRelease", "ID", "RequestOpened")
	assertExactMethods(t, "auxiliary.BackgroundClient", reflect.TypeFor[auxiliary.BackgroundClient](), "Await", "Forget", "SubmitCollect")

	observer := &recordingCompactionObserver{}
	compaction.Dispatch(context.Background(), []compaction.Observer{observer}, []compaction.Event{{Phase: compaction.PhaseStarted}})
	if observer.calls != 1 || observer.last.Phase != compaction.PhaseStarted {
		t.Fatalf("Dispatch did not deliver the observer event: calls=%d last=%+v", observer.calls, observer.last)
	}
	if aipapi.OutputCommitted(aipapi.Event{Kind: aipapi.EventResponseStarted}) {
		t.Fatal("response lifecycle frame committed output")
	}
	if !aipapi.OutputCommitted(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "visible"}) {
		t.Fatal("visible content stopped committing the active attempt")
	}
	if !terminal.CommandGateReplacement.IsRetryOrReplacement() || terminal.CommandNormalFinish.IsRetryOrReplacement() {
		t.Fatal("terminal retry/replacement authority changed")
	}
}

// TestCompactionContinuitySecurity_OpaquePayloadAndIndependentBillingRemainStable
// is a small cross-boundary regression: preservation may append a canonical
// projection, but it cannot rewrite opaque/encrypted provider bytes, and an
// independently accounted leg still requires a positive attempt sequence and
// evidence identity.
func TestCompactionContinuitySecurity_OpaquePayloadAndIndependentBillingRemainStable(t *testing.T) {
	t.Parallel()
	branch, err := capsule.NewBranchBinding("opaque-session", "opaque-a", "principal")
	if err != nil {
		t.Fatal(err)
	}
	e, err := capsule.New(branch)
	if err != nil {
		t.Fatal(err)
	}
	opaque := []byte(`{"provider":"opaque","bytes":[1,2,3]}`)
	call := aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindCompaction, Compaction: &aipapi.CompactionItem{EncryptedContent: "ciphertext==", Opaque: opaque}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "continue"}}},
	}}
	out, err := injection.Inject(injection.Input{Call: call, Capsule: e, ExpectedBranchBinding: branch, BoundaryKey: "boundary"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Call.Items[0].Compaction.Opaque, opaque) || out.Call.Items[0].Compaction.EncryptedContent != "ciphertext==" {
		t.Fatalf("opaque/encrypted payload changed: %+v", out.Call.Items[0].Compaction)
	}
	if caps := augmentation.Capabilities(); len(caps) != 0 {
		t.Fatalf("unverified plaintext augmentation capabilities appeared: %#v", caps)
	}
	if _, ok := augmentation.Match(&aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
		Kind: aipapi.ItemKindCompaction, Compaction: &aipapi.CompactionItem{EncryptedContent: "ciphertext==", Opaque: opaque},
	}}); ok {
		t.Fatal("opaque/encrypted compaction item was classified as mutable plaintext")
	}

	identity, err := usageevidence.WorkloadIdentityFromAuxiliaryRole(usageevidence.WorkloadRoleCompactionContinuityExtractor)
	if err != nil {
		t.Fatal(err)
	}
	base := usageevidence.CallLegUsageRecord{
		CallID: "bc_0123456789abcdef0123456789abcdef", ALegID: "child-a", BLegID: "child-b",
		BackendID: "backend", ProviderID: "provider", ModelID: "model", StartedAt: time.Unix(1, 0), FinishedAt: time.Unix(2, 0),
		Outcome: usageevidence.LegOutcomeWinner, Surfaced: usageevidence.SurfacedYes, Workload: identity,
		Evidence: usageevidence.FinalBillingEvidence{
			InputTokens: usageevidence.Quantity{Value: 1, Present: true}, OutputTokens: usageevidence.Quantity{Value: 1, Present: true},
			Source: usageevidence.EvidenceSourceProviderReported, Authority: usageevidence.EvidenceAuthorityAuthoritative, DedupeKey: "provider-key",
		},
	}
	legacy, err := base.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := usageevidence.ValidateIndependentLeg(legacy); err == nil {
		t.Fatal("independent leg with unknown attempt sequence was accepted")
	}
	base.AttemptSeq = 1
	sealed, err := base.Seal()
	if err != nil {
		t.Fatal(err)
	}
	if err := usageevidence.ValidateIndependentLeg(sealed); err != nil {
		t.Fatalf("positive independent attempt sequence rejected: %v", err)
	}
}

type contentFreeObserver struct{}

func (contentFreeObserver) OnCompaction(context.Context, compaction.Event) error { return nil }

type recordingCompactionObserver struct {
	calls int
	last  compaction.Event
}

func (o *recordingCompactionObserver) OnCompaction(_ context.Context, event compaction.Event) error {
	o.calls++
	o.last = event
	return nil
}

func detectorItemCall(prefix, tailA, tailB string) aipapi.Call {
	return aipapi.Call{Items: []aipapi.Item{
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Status: aipapi.ItemStatusCompleted, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: prefix}}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleAssistant, Status: aipapi.ItemStatusCompleted, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: tailA}}},
		{Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Status: aipapi.ItemStatusCompleted, Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: tailB}}},
	}}
}

func assertExactFields(t *testing.T, label string, typ reflect.Type, want ...string) {
	t.Helper()
	if typ.Kind() != reflect.Struct {
		assertExactMethods(t, label, typ, want...)
		return
	}
	got := make([]string, typ.NumField())
	for i := range got {
		got[i] = typ.Field(i).Name
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s fields=%v want=%v", label, got, want)
	}
}

func assertExactMethods(t *testing.T, label string, typ reflect.Type, want ...string) {
	t.Helper()
	got := make([]string, typ.NumMethod())
	for i := range got {
		got[i] = typ.Method(i).Name
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s methods=%v want=%v", label, got, want)
	}
}

func assertTypeKeysAbsent(t *testing.T, typ reflect.Type, forbidden map[string]struct{}) {
	t.Helper()
	if field := forbiddenTypeKey(typ, forbidden); field != "" {
		t.Fatalf("%s exposes forbidden wire/control field %s", typ, field)
	}
}

func forbiddenTypeKey(typ reflect.Type, forbidden map[string]struct{}) string {
	seen := make(map[reflect.Type]bool)
	var visit func(reflect.Type) string
	visit = func(current reflect.Type) string {
		for current.Kind() == reflect.Pointer || current.Kind() == reflect.Slice || current.Kind() == reflect.Array || current.Kind() == reflect.Map {
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct || seen[current] {
			return ""
		}
		seen[current] = true
		for field := range current.Fields() {
			key := strings.ToLower(field.Name)
			if tag, _, _ := strings.Cut(field.Tag.Get("json"), ","); tag != "" && tag != "-" {
				key = strings.ToLower(tag)
			}
			if _, blocked := forbidden[strings.ReplaceAll(key, "_", "")]; blocked {
				return field.Name
			}
			if nested := visit(field.Type); nested != "" {
				return nested
			}
		}
		return ""
	}
	return visit(typ)
}

func TestCompactionContinuitySecurity_TypeWalkerTraversesMapValueStructs(t *testing.T) {
	t.Parallel()
	type mapValue struct{ SessionMode string }
	type envelope struct{ Values map[string]mapValue }

	if got := forbiddenTypeKey(reflect.TypeFor[envelope](), map[string]struct{}{"sessionmode": {}}); got != "SessionMode" {
		t.Fatalf("map-value walker field=%q, want SessionMode", got)
	}
}

func assertProductionDirectImportsExclude(t *testing.T, sourcePrefixes, forbidden []string) {
	t.Helper()
	root := repoRoot(t)
	err := WalkProductionGoFiles(root, func(rel, _ string, src []byte) error {
		pkg := PackageDirFromRel(rel)
		matched := false
		for _, prefix := range sourcePrefixes {
			if MatchPathPrefix(pkg, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
		_, file, err := ParseGoSource(rel, src)
		if err != nil {
			return err
		}
		for _, imp := range FileImportPaths(file) {
			for _, blocked := range forbidden {
				if imp == blocked || strings.HasPrefix(imp, blocked+"/") {
					t.Fatalf("%s directly imports forbidden %q", rel, imp)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
