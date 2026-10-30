package feature_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

type stubLife struct{}

func (stubLife) Start(context.Context) error { return nil }
func (stubLife) Stop(context.Context) error  { return nil }

type stubSubmit struct {
	id  string
	ord int
}

func (s stubSubmit) ID() string                        { return s.id }
func (s stubSubmit) Order() int                        { return s.ord }
func (s stubSubmit) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s stubSubmit) Handle(context.Context, *aipapi.Call, *sdkhooks.SubmitMeta) (sdkhooks.SubmitDecision, error) {
	return sdkhooks.SubmitDecision{}, nil
}

type stubTool struct {
	id  string
	ord int
}

func (s stubTool) ID() string { return s.id }
func (s stubTool) Order() int { return s.ord }
func (s stubTool) HandleToolEvent(context.Context, aipapi.ToolEvent, sdkhooks.ToolMeta) (sdkhooks.ToolDecision, aipapi.ToolEvent, error) {
	return sdkhooks.ToolPass, aipapi.ToolEvent{}, nil
}

type stubPreserver struct{ id string }

func (p stubPreserver) ID() string { return p.id }

func (stubPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (stubPreserver) RequestOpened(context.Context, aipapi.Call, []compaction.Event, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (stubPreserver) BeforeResponseRelease(context.Context, *aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

type terminalDecisionProvider struct{ id string }

func (p terminalDecisionProvider) ID() string { return p.id }

func (terminalDecisionProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{Kind: terminaldecision.DecisionAllowStop, ReasonCode: "complete"}, nil
}

type stubAttemptTransform struct {
	id  string
	ord int
}

func (s stubAttemptTransform) ID() string                        { return s.id }
func (s stubAttemptTransform) Order() int                        { return s.ord }
func (s stubAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s stubAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type stubStreamObserverFactory struct {
	id  string
	ord int
}

func (s stubStreamObserverFactory) ID() string                        { return s.id }
func (s stubStreamObserverFactory) Order() int                        { return s.ord }
func (s stubStreamObserverFactory) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }
func (s stubStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return stubStreamObserver{}, nil
}

type stubStreamObserver struct{}

func (stubStreamObserver) Observe(context.Context, aipapi.Event) error          { return nil }
func (stubStreamObserver) Finish(context.Context, response.StreamOutcome) error { return nil }

func TestFeatureBundle_FinalFields(t *testing.T) {
	t.Parallel()
	bundleType := reflect.TypeFor[feature.FeatureBundle]()
	var actualFields []string
	for f := range bundleType.Fields() {
		actualFields = append(actualFields, f.Name)
	}
	expectedFields := []string{"SchemaVersion", "PlaneSet", "Lifecycles"}
	assert.Equal(t, expectedFields, actualFields)
}

func TestBundleFromPlanes_isolation(t *testing.T) {
	t.Parallel()

	// 1. SchemaVersion is SchemaVersionV1
	cs := feature.NewContributionSet()
	submitHook := stubSubmit{id: "hook-1", ord: 10}
	if err := feature.Contribute(cs, feature.PlaneSubmitHooks, "feat-1", []sdkhooks.SubmitHook{submitHook}); err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	frozen := cs.Freeze()

	life1 := stubLife{}
	inputLifecycles := []aipplugin.Lifecycle{life1}
	bundle := feature.BundleFromPlanes(frozen, inputLifecycles)

	if bundle.SchemaVersion != feature.SchemaVersionV1 {
		t.Fatalf("SchemaVersion = %d, want %d", bundle.SchemaVersion, feature.SchemaVersionV1)
	}

	// 2. Lifecycle isolation and nil vs empty preservation
	if len(bundle.Lifecycles) != 1 {
		t.Fatalf("Lifecycles len = %d, want 1", len(bundle.Lifecycles))
	}
	inputLifecycles[0] = nil
	if bundle.Lifecycles[0] == nil {
		t.Fatal("mutating source lifecycles affected bundle.Lifecycles")
	}

	nilLifeBundle := feature.BundleFromPlanes(frozen, nil)
	if nilLifeBundle.Lifecycles != nil {
		t.Fatal("expected nil Lifecycles preserved")
	}

	emptyLifeBundle := feature.BundleFromPlanes(frozen, []aipplugin.Lifecycle{})
	if emptyLifeBundle.Lifecycles == nil {
		t.Fatal("expected non-nil empty Lifecycles preserved")
	}
	if len(emptyLifeBundle.Lifecycles) != 0 {
		t.Fatalf("expected 0 lifecycles, got %d", len(emptyLifeBundle.Lifecycles))
	}

	// 3. FrozenPlaneSet isolation
	hooksFromBundle := feature.Get(bundle.PlaneSet, feature.PlaneSubmitHooks)
	if len(hooksFromBundle) != 1 || hooksFromBundle[0].ID() != "hook-1" {
		t.Fatalf("unexpected hooks from bundle: %v", hooksFromBundle)
	}
}

func TestFeatureBundle_Validate_EmptyAndPlanes(t *testing.T) {
	t.Parallel()

	// Empty bundle: SchemaVersion 0 and 1 are valid, other is rejected
	if err := (feature.FeatureBundle{}).Validate(); err != nil {
		t.Fatalf("empty bundle with SchemaVersion 0 rejected: %v", err)
	}
	if err := (feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1}).Validate(); err != nil {
		t.Fatalf("empty bundle with SchemaVersionV1 rejected: %v", err)
	}
	err := (feature.FeatureBundle{SchemaVersion: 2}).Validate()
	require.EqualError(t, err, "feature: FeatureBundle: invalid schema version 2 for empty bundle")

	// Lifecycle-only bundle: requires SchemaVersionV1
	lifeBad := feature.FeatureBundle{Lifecycles: []aipplugin.Lifecycle{stubLife{}}}
	require.EqualError(t, lifeBad.Validate(), "feature: FeatureBundle: schema version want 1 got 0")
	lifeOk := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		Lifecycles:    []aipplugin.Lifecycle{stubLife{}},
	}
	if err := lifeOk.Validate(); err != nil {
		t.Fatalf("lifecycle-only bundle with SchemaVersionV1 rejected: %v", err)
	}

	// PlaneSet bundle: requires SchemaVersionV1
	cs := feature.NewContributionSet()
	if err := feature.Contribute(cs, feature.PlaneSubmitHooks, "feat", []sdkhooks.SubmitHook{stubSubmit{id: "h", ord: 1}}); err != nil {
		t.Fatalf("Contribute: %v", err)
	}
	frozen := cs.Freeze()

	newBad := feature.FeatureBundle{
		PlaneSet: frozen,
	}
	require.EqualError(t, newBad.Validate(), "feature: FeatureBundle: schema version want 1 got 0")
	newOk := feature.FeatureBundle{
		SchemaVersion: feature.SchemaVersionV1,
		PlaneSet:      frozen,
	}
	if err := newOk.Validate(); err != nil {
		t.Fatalf("new-only bundle with SchemaVersionV1 rejected: %v", err)
	}
}

type callCountingTerminalProvider struct {
	id    string
	calls *int
}

func (c callCountingTerminalProvider) ID() string {
	if c.calls != nil {
		*c.calls++
	}
	return c.id
}

func (callCountingTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{}, nil
}

type panicAfterFreezeTerminalProvider struct {
	id          string
	shouldPanic *bool
}

func (p panicAfterFreezeTerminalProvider) ID() string {
	if p.shouldPanic != nil && *p.shouldPanic {
		panic("provider ID() invoked unexpectedly after freeze")
	}
	return p.id
}

func (panicAfterFreezeTerminalProvider) Decide(context.Context, terminaldecision.Input) (terminaldecision.Decision, error) {
	return terminaldecision.Decision{}, nil
}

func TestFeatureBundle_Validate_FrozenExclusiveIdentity(t *testing.T) {
	t.Parallel()

	t.Run("provider ID call count unchanged during Validate", func(t *testing.T) {
		t.Parallel()
		calls := 0
		var prov terminaldecision.Provider = callCountingTerminalProvider{id: "prov.callcount", calls: &calls}

		cs := feature.NewContributionSet()
		if err := feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "prov.callcount", prov); err != nil {
			t.Fatalf("Contribute: %v", err)
		}
		frozen := cs.Freeze()
		callsAfterFreeze := calls
		if callsAfterFreeze == 0 {
			t.Fatal("expected provider ID to be called during contribution/freeze")
		}

		b := feature.FeatureBundle{
			SchemaVersion: feature.SchemaVersionV1,
			PlaneSet:      frozen,
		}
		if err := b.Validate(); err != nil {
			t.Fatalf("b.Validate() failed: %v", err)
		}
		if calls != callsAfterFreeze {
			t.Fatalf("provider ID calls changed during b.Validate(): before=%d, after=%d", callsAfterFreeze, calls)
		}

		if err := frozen.Validate(); err != nil {
			t.Fatalf("frozen.Validate() failed: %v", err)
		}
		if calls != callsAfterFreeze {
			t.Fatalf("provider ID calls changed during frozen.Validate(): before=%d, after=%d", callsAfterFreeze, calls)
		}
	})

	t.Run("provider can panic after freeze and bundle validates from cache", func(t *testing.T) {
		t.Parallel()
		shouldPanic := false
		var prov terminaldecision.Provider = panicAfterFreezeTerminalProvider{id: "prov.panic", shouldPanic: &shouldPanic}

		cs := feature.NewContributionSet()
		if err := feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "prov.panic", prov); err != nil {
			t.Fatalf("Contribute: %v", err)
		}
		frozen := cs.Freeze()

		// Enable panics
		shouldPanic = true

		b := feature.FeatureBundle{
			SchemaVersion: feature.SchemaVersionV1,
			PlaneSet:      frozen,
		}
		if err := b.Validate(); err != nil {
			t.Fatalf("b.Validate() failed with panicking provider: %v", err)
		}
		if err := frozen.Validate(); err != nil {
			t.Fatalf("frozen.Validate() failed with panicking provider: %v", err)
		}
	})

	t.Run("missing cached identity fails", func(t *testing.T) {
		t.Parallel()
		prov := terminalDecisionProvider{id: "term.missing"}

		// Missing identity (hasID false, id empty)
		f1 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(prov, "", false)
		b1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f1}
		err1 := b1.Validate()
		if err1 == nil {
			t.Fatal("b.Validate() accepted missing cached identity (hasID=false, id=\"\")")
		}
		if !strings.Contains(err1.Error(), "missing cached identity") {
			t.Fatalf("error = %q, want substring 'missing cached identity'", err1.Error())
		}

		// Present ID but hasID false
		f2 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(prov, "term.missing", false)
		b2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f2}
		err2 := b2.Validate()
		if err2 == nil {
			t.Fatal("b.Validate() accepted missing cached identity (hasID=false, id non-empty)")
		}
		if !strings.Contains(err2.Error(), "missing cached identity") {
			t.Fatalf("error = %q, want substring 'missing cached identity'", err2.Error())
		}

		// hasID true but ID empty
		f3 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(prov, "", true)
		b3 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f3}
		err3 := b3.Validate()
		if err3 == nil {
			t.Fatal("b.Validate() accepted missing cached identity (hasID=true, id=\"\")")
		}
		if !strings.Contains(err3.Error(), "missing cached identity") {
			t.Fatalf("error = %q, want substring 'missing cached identity'", err3.Error())
		}
	})

	t.Run("invalid cached ID fails exact terminal sentinel", func(t *testing.T) {
		t.Parallel()
		prov := terminalDecisionProvider{id: "term.invalid"}

		invalidIDs := map[string]string{
			"blank id":     "   ",
			"invalid utf8": string([]byte{0xff}),
			"oversized id": strings.Repeat("x", terminaldecision.MaxProviderIDBytes+1),
		}

		for name, invID := range invalidIDs {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				f := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(prov, invID, true)
				b := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f}
				err := b.Validate()
				if err == nil {
					t.Fatalf("b.Validate() accepted invalid cached ID %q", invID)
				}
				if !errors.Is(err, terminaldecision.ErrInvalidProvider) {
					t.Fatalf("b.Validate() error = %v, want ErrInvalidProvider", err)
				}
				if errFrozen := f.Validate(); !errors.Is(errFrozen, terminaldecision.ErrInvalidProvider) {
					t.Fatalf("f.Validate() error = %v, want ErrInvalidProvider", errFrozen)
				}
			})
		}
	})

	t.Run("cached identity with absent provider fails", func(t *testing.T) {
		t.Parallel()

		// Provider nil, but hasID true and ID set
		f1 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(nil, "valid.provider.id", true)
		b1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f1}
		err1 := b1.Validate()
		if err1 == nil {
			t.Fatal("b.Validate() accepted cached identity with nil provider")
		}
		if !strings.Contains(err1.Error(), "malformed metadata without value") {
			t.Fatalf("error = %q, want substring 'malformed metadata without value'", err1.Error())
		}

		// Provider nil, but hasID true and ID empty
		f2 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(nil, "", true)
		b2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f2}
		err2 := b2.Validate()
		if err2 == nil {
			t.Fatal("b.Validate() accepted hasID=true with nil provider")
		}
		if !strings.Contains(err2.Error(), "malformed metadata without value") {
			t.Fatalf("error = %q, want substring 'malformed metadata without value'", err2.Error())
		}

		// Provider nil, but hasID false and ID non-empty
		f3 := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(nil, "valid.provider.id", false)
		b3 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f3}
		err3 := b3.Validate()
		if err3 == nil {
			t.Fatal("b.Validate() accepted ID non-empty with nil provider")
		}
		if !strings.Contains(err3.Error(), "malformed metadata without value") {
			t.Fatalf("error = %q, want substring 'malformed metadata without value'", err3.Error())
		}
	})

	t.Run("valid cached identity succeeds", func(t *testing.T) {
		t.Parallel()
		prov := terminalDecisionProvider{id: "provider.valid.cached"}
		f := feature.NewMalformedGeneratedFrozenTerminalDecisionForTest(prov, "provider.valid.cached", true)
		b := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: f}
		if err := b.Validate(); err != nil {
			t.Fatalf("b.Validate() failed for valid cached identity: %v", err)
		}
		if err := f.Validate(); err != nil {
			t.Fatalf("f.Validate() failed for valid cached identity: %v", err)
		}
	})

	t.Run("no mutation during Validate", func(t *testing.T) {
		t.Parallel()
		cs := feature.NewContributionSet()
		var prov terminaldecision.Provider = terminalDecisionProvider{id: "term.nomutate"}
		if err := feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "term.nomutate", prov); err != nil {
			t.Fatalf("Contribute: %v", err)
		}
		frozen := cs.Freeze()

		b := feature.FeatureBundle{
			SchemaVersion: feature.SchemaVersionV1,
			PlaneSet:      frozen,
		}
		if err := b.Validate(); err != nil {
			t.Fatalf("b.Validate(): %v", err)
		}

		gotProv := feature.Get(b.PlaneSet, feature.PlaneTerminalDecisionProvider)
		if gotProv == nil || gotProv.ID() != "term.nomutate" {
			t.Fatalf("PlaneTerminalDecisionProvider mutated: %v", gotProv)
		}
		id, ok := feature.FrozenIdentity(b.PlaneSet, feature.PlaneTerminalDecisionProvider)
		if !ok || id != "term.nomutate" {
			t.Fatalf("FrozenIdentity mutated: (%q, %v)", id, ok)
		}
	})
}

func TestFrozenPlaneSet_ReplayTo_allPlanesAndAtomicFailure(t *testing.T) {
	t.Parallel()

	// 1. Build a populated FrozenPlaneSet with candidate, non-candidate, scalar, exclusive, and explicit-empty
	cs := feature.NewContributionSet()
	submitHook := stubSubmit{id: "submit-1", ord: 1}
	if err := feature.Contribute(cs, feature.PlaneSubmitHooks, "feat-1", []sdkhooks.SubmitHook{submitHook}); err != nil {
		t.Fatalf("Contribute SubmitHooks: %v", err)
	}
	// Non-candidate plane: tool reactors
	toolReactor := stubTool{id: "tool-1", ord: 2}
	if err := feature.Contribute(cs, feature.PlaneToolReactors, "feat-1", []sdkhooks.ToolReactor{toolReactor}); err != nil {
		t.Fatalf("Contribute ToolReactors: %v", err)
	}
	// Scalar plane
	if err := feature.Contribute(cs, feature.PlaneToolCallFinalizationMaxArgsBytes, "feat-1", 4096); err != nil {
		t.Fatalf("Contribute MaxArgsBytes: %v", err)
	}
	// Exclusive plane
	var termProv terminaldecision.Provider = terminalDecisionProvider{id: "term-provider-1"}
	if err := feature.Contribute(cs, feature.PlaneTerminalDecisionProvider, "term-provider-1", termProv); err != nil {
		t.Fatalf("Contribute TerminalDecisionProvider: %v", err)
	}
	// Explicit non-nil empty slice
	if err := feature.Contribute(cs, feature.PlaneSessionOpeners, "feat-1", []session.Opener{}); err != nil {
		t.Fatalf("Contribute SessionOpeners: %v", err)
	}

	frozen := cs.Freeze()

	// 2. Replay all planes into a clean destination
	dst := feature.NewContributionSet()
	if err := frozen.ReplayTo(dst, "replay-plugin"); err != nil {
		t.Fatalf("ReplayTo: %v", err)
	}

	// Verify all planes replayed
	dstFrozen := dst.Freeze()
	sh := feature.Get(dstFrozen, feature.PlaneSubmitHooks)
	if len(sh) != 1 || sh[0].ID() != "submit-1" {
		t.Fatalf("replayed SubmitHooks mismatch: %v", sh)
	}
	tr := feature.Get(dstFrozen, feature.PlaneToolReactors)
	if len(tr) != 1 || tr[0].ID() != "tool-1" {
		t.Fatalf("replayed ToolReactors mismatch: %v", tr)
	}
	capBytes := feature.Get(dstFrozen, feature.PlaneToolCallFinalizationMaxArgsBytes)
	if capBytes != 4096 {
		t.Fatalf("replayed MaxArgsBytes = %d, want 4096", capBytes)
	}
	tp := feature.Get(dstFrozen, feature.PlaneTerminalDecisionProvider)
	if tp == nil || tp.ID() != "term-provider-1" {
		t.Fatalf("replayed TerminalDecisionProvider mismatch: %v", tp)
	}
	id, ok := feature.FrozenIdentity(dstFrozen, feature.PlaneTerminalDecisionProvider)
	if !ok || id != "term-provider-1" {
		t.Fatalf("replayed FrozenIdentity = (%q, %v), want (term-provider-1, true)", id, ok)
	}
	so := feature.Get(dstFrozen, feature.PlaneSessionOpeners)
	if so == nil {
		t.Fatal("replayed SessionOpeners was nil, expected non-nil empty slice")
	}
	if len(so) != 0 {
		t.Fatalf("replayed SessionOpeners len = %d, want 0", len(so))
	}

	// 3. Atomic failure: destination already has a conflicting exclusive provider
	conflictDst := feature.NewContributionSet()
	var existingProv terminaldecision.Provider = terminalDecisionProvider{id: "existing-provider"}
	if err := feature.Contribute(conflictDst, feature.PlaneTerminalDecisionProvider, "existing-provider", existingProv); err != nil {
		t.Fatalf("Contribute existing provider: %v", err)
	}
	// Also add a submit hook to conflictDst
	if err := feature.Contribute(conflictDst, feature.PlaneSubmitHooks, "init", []sdkhooks.SubmitHook{stubSubmit{id: "init-hook", ord: 0}}); err != nil {
		t.Fatalf("Contribute init hook: %v", err)
	}

	err := frozen.ReplayTo(conflictDst, "replay-plugin")
	if err == nil {
		t.Fatal("expected exclusive conflict error on ReplayTo")
	}

	// Verify conflictDst was NOT modified (atomic failure)
	cDstFrozen := conflictDst.Freeze()
	cSH := feature.Get(cDstFrozen, feature.PlaneSubmitHooks)
	if len(cSH) != 1 || cSH[0].ID() != "init-hook" {
		t.Fatalf("conflictDst SubmitHooks mutated on failed replay: %v", cSH)
	}
	cTP := feature.Get(cDstFrozen, feature.PlaneTerminalDecisionProvider)
	if cTP == nil || cTP.ID() != "existing-provider" {
		t.Fatalf("conflictDst TerminalDecisionProvider mutated on failed replay: %v", cTP)
	}
	cTR := feature.Get(cDstFrozen, feature.PlaneToolReactors)
	if len(cTR) != 0 {
		t.Fatalf("conflictDst ToolReactors mutated on failed replay: %v", cTR)
	}
}

type callCountingAttemptTransform struct {
	id    string
	calls *int
}

func (c callCountingAttemptTransform) ID() string {
	if c.calls != nil {
		*c.calls++
	}
	return c.id
}
func (c callCountingAttemptTransform) Order() int                        { return 0 }
func (c callCountingAttemptTransform) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

func (c callCountingAttemptTransform) HandleAttempt(context.Context, *aipapi.Call, request.AttemptMeta, request.Services) (request.AttemptDecision, error) {
	return request.AttemptDecision{Kind: request.AttemptContinue}, nil
}

type callCountingStreamObserverFactory struct {
	id    string
	calls *int
}

func (c callCountingStreamObserverFactory) ID() string {
	if c.calls != nil {
		*c.calls++
	}
	return c.id
}

func (c callCountingStreamObserverFactory) Order() int { return 0 }

func (c callCountingStreamObserverFactory) FailureMode() sdkhooks.FailureMode {
	return sdkhooks.FailOpen
}

func (c callCountingStreamObserverFactory) Open(context.Context, response.StreamMeta, response.Services) (response.StreamObserver, error) {
	return stubStreamObserver{}, nil
}

type callCountingCompactionPreserver struct {
	stubPreserver
	calls *int
}

func (c callCountingCompactionPreserver) ID() string {
	if c.calls != nil {
		*c.calls++
	}
	return c.id
}

func TestPlaneSet_ValidateIdentity_ThreePlanes_AllTenCases(t *testing.T) {
	t.Parallel()

	longID := strings.Repeat("x", 300)
	whitespaceID := "   \t\n  "

	t.Run("PlaneAttemptTransforms", func(t *testing.T) {
		t.Parallel()

		// 1. valid contribution freezes ID
		csValid := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csValid, feature.PlaneAttemptTransforms, "p-valid", []request.AttemptTransform{stubAttemptTransform{id: "at-valid-1"}}))
		fValid := csValid.Freeze()
		idV, okV := feature.FrozenIdentity(fValid, feature.PlaneAttemptTransforms)
		assert.True(t, okV)
		assert.Equal(t, "at-valid-1", idV)

		// 2. FrozenIdentity returns exact ID without extra live ID call
		calls := 0
		csCount := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csCount, feature.PlaneAttemptTransforms, "p-count", []request.AttemptTransform{callCountingAttemptTransform{id: "at-count-1", calls: &calls}}))
		fCount := csCount.Freeze()
		callsAfterFreeze := calls
		idCount, okCount := feature.FrozenIdentity(fCount, feature.PlaneAttemptTransforms)
		assert.True(t, okCount)
		assert.Equal(t, "at-count-1", idCount)
		assert.Equal(t, callsAfterFreeze, calls, "FrozenIdentity must not call live ID")

		// 3. bundle Validate & frozen Validate call cached ValidateIdentity and no live ID
		bCount := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fCount}
		require.NoError(t, bCount.Validate())
		require.NoError(t, fCount.Validate())
		assert.Equal(t, callsAfterFreeze, calls, "Validate must not call live ID")

		// 4. >256 and whitespace IDs succeed
		csLong := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csLong, feature.PlaneAttemptTransforms, "p-long", []request.AttemptTransform{stubAttemptTransform{id: longID}}))
		fLong := csLong.Freeze()
		require.NoError(t, fLong.Validate())
		bLong := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fLong}
		require.NoError(t, bLong.Validate())
		idL, okL := feature.FrozenIdentity(fLong, feature.PlaneAttemptTransforms)
		assert.True(t, okL)
		assert.Equal(t, longID, idL)

		csWS := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csWS, feature.PlaneAttemptTransforms, "p-ws", []request.AttemptTransform{stubAttemptTransform{id: whitespaceID}}))
		fWS := csWS.Freeze()
		require.NoError(t, fWS.Validate())
		bWS := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fWS}
		require.NoError(t, bWS.Validate())
		idW, okW := feature.FrozenIdentity(fWS, feature.PlaneAttemptTransforms)
		assert.True(t, okW)
		assert.Equal(t, whitespaceID, idW)

		// 5. empty cached identity with nonempty value rejects
		fEmpty1 := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest([]request.AttemptTransform{stubAttemptTransform{id: "at-1"}}, "", false)
		bEmpty1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty1}
		errE1 := bEmpty1.Validate()
		require.Error(t, errE1)
		assert.Contains(t, errE1.Error(), "missing cached identity")
		assert.Contains(t, fEmpty1.Validate().Error(), "missing cached identity")

		fEmpty2 := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest([]request.AttemptTransform{stubAttemptTransform{id: "at-1"}}, "", true)
		bEmpty2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty2}
		errE2 := bEmpty2.Validate()
		require.Error(t, errE2)
		assert.Contains(t, errE2.Error(), "missing cached identity")
		assert.Contains(t, fEmpty2.Validate().Error(), "missing cached identity")

		// 6. invalid metadata ID nonempty/HasID false rejects
		fInvMeta := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest([]request.AttemptTransform{stubAttemptTransform{id: "at-1"}}, "at-1", false)
		bInvMeta := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fInvMeta}
		errInv := bInvMeta.Validate()
		require.Error(t, errInv)
		assert.Contains(t, errInv.Error(), "missing cached identity")
		assert.Contains(t, fInvMeta.Validate().Error(), "missing cached identity")

		// 7. metadata without value rejects
		fNoVal1 := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest(nil, "at-1", true)
		bNoVal1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal1}
		errNV1 := bNoVal1.Validate()
		require.Error(t, errNV1)
		assert.Contains(t, errNV1.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal1.Validate().Error(), "malformed metadata without value")

		fNoVal2 := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest(nil, "", true)
		bNoVal2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal2}
		errNV2 := bNoVal2.Validate()
		require.Error(t, errNV2)
		assert.Contains(t, errNV2.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal2.Validate().Error(), "malformed metadata without value")

		fNoVal3 := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest(nil, "at-1", false)
		bNoVal3 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal3}
		errNV3 := bNoVal3.Validate()
		require.Error(t, errNV3)
		assert.Contains(t, errNV3.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal3.Validate().Error(), "malformed metadata without value")

		// 8. cached ID mutation to another nonempty ID: validator accepts cached valid ID, no live ID call
		fMut := feature.NewMalformedGeneratedFrozenAttemptTransformsForTest([]request.AttemptTransform{stubAttemptTransform{id: "orig-id"}}, "mutated-id", true)
		bMut := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fMut}
		require.NoError(t, bMut.Validate())
		require.NoError(t, fMut.Validate())
		idMut, okMut := feature.FrozenIdentity(fMut, feature.PlaneAttemptTransforms)
		assert.True(t, okMut)
		assert.Equal(t, "mutated-id", idMut)

		// 9. clone/freeze/toContributions/request freeze retain exact ID
		fCloned := fValid.Clone()
		idC, okC := feature.FrozenIdentity(fCloned, feature.PlaneAttemptTransforms)
		assert.True(t, okC)
		assert.Equal(t, "at-valid-1", idC)

		fReq := feature.FreezeRequestPlanes(fValid)
		idR, okR := feature.FrozenIdentity(fReq, feature.PlaneAttemptTransforms)
		assert.True(t, okR)
		assert.Equal(t, "at-valid-1", idR)

		cset := fValid.ToContributions()
		fRecon := cset.Freeze()
		idRec, okRec := feature.FrozenIdentity(fRecon, feature.PlaneAttemptTransforms)
		assert.True(t, okRec)
		assert.Equal(t, "at-valid-1", idRec)

		// 10. replay into destination retains correct metadata and atomic failures
		dst := feature.NewContributionSet()
		require.NoError(t, fValid.ReplayTo(dst, "replay-p"))
		fDst := dst.Freeze()
		idDst, okDst := feature.FrozenIdentity(fDst, feature.PlaneAttemptTransforms)
		assert.True(t, okDst)
		assert.Equal(t, "at-valid-1", idDst)

		dstBefore := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dstBefore, feature.PlaneSubmitHooks, "init", []sdkhooks.SubmitHook{stubSubmit{id: "init-sub", ord: 0}}))
		snapBefore := dstBefore.Freeze()

		errRep := fNoVal1.ReplayTo(dstBefore, "bad-plugin")
		require.Error(t, errRep)
		snapAfter := dstBefore.Freeze()
		assert.Equal(t, feature.Get(snapBefore, feature.PlaneSubmitHooks), feature.Get(snapAfter, feature.PlaneSubmitHooks))
	})

	t.Run("PlaneStreamObserverFactories", func(t *testing.T) {
		t.Parallel()

		// 1. valid contribution freezes ID
		csValid := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csValid, feature.PlaneStreamObserverFactories, "p-valid", []response.StreamObserverFactory{stubStreamObserverFactory{id: "sof-valid-1"}}))
		fValid := csValid.Freeze()
		idV, okV := feature.FrozenIdentity(fValid, feature.PlaneStreamObserverFactories)
		assert.True(t, okV)
		assert.Equal(t, "sof-valid-1", idV)

		// 2. FrozenIdentity returns exact ID without extra live ID call
		calls := 0
		csCount := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csCount, feature.PlaneStreamObserverFactories, "p-count", []response.StreamObserverFactory{callCountingStreamObserverFactory{id: "sof-count-1", calls: &calls}}))
		fCount := csCount.Freeze()
		callsAfterFreeze := calls
		idCount, okCount := feature.FrozenIdentity(fCount, feature.PlaneStreamObserverFactories)
		assert.True(t, okCount)
		assert.Equal(t, "sof-count-1", idCount)
		assert.Equal(t, callsAfterFreeze, calls, "FrozenIdentity must not call live ID")

		// 3. bundle Validate & frozen Validate call cached ValidateIdentity and no live ID
		bCount := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fCount}
		require.NoError(t, bCount.Validate())
		require.NoError(t, fCount.Validate())
		assert.Equal(t, callsAfterFreeze, calls, "Validate must not call live ID")

		// 4. >256 and whitespace IDs succeed
		csLong := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csLong, feature.PlaneStreamObserverFactories, "p-long", []response.StreamObserverFactory{stubStreamObserverFactory{id: longID}}))
		fLong := csLong.Freeze()
		require.NoError(t, fLong.Validate())
		bLong := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fLong}
		require.NoError(t, bLong.Validate())
		idL, okL := feature.FrozenIdentity(fLong, feature.PlaneStreamObserverFactories)
		assert.True(t, okL)
		assert.Equal(t, longID, idL)

		csWS := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csWS, feature.PlaneStreamObserverFactories, "p-ws", []response.StreamObserverFactory{stubStreamObserverFactory{id: whitespaceID}}))
		fWS := csWS.Freeze()
		require.NoError(t, fWS.Validate())
		bWS := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fWS}
		require.NoError(t, bWS.Validate())
		idW, okW := feature.FrozenIdentity(fWS, feature.PlaneStreamObserverFactories)
		assert.True(t, okW)
		assert.Equal(t, whitespaceID, idW)

		// 5. empty cached identity with nonempty value rejects
		fEmpty1 := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest([]response.StreamObserverFactory{stubStreamObserverFactory{id: "sof-1"}}, "", false)
		bEmpty1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty1}
		errE1 := bEmpty1.Validate()
		require.Error(t, errE1)
		assert.Contains(t, errE1.Error(), "missing cached identity")
		assert.Contains(t, fEmpty1.Validate().Error(), "missing cached identity")

		fEmpty2 := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest([]response.StreamObserverFactory{stubStreamObserverFactory{id: "sof-1"}}, "", true)
		bEmpty2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty2}
		errE2 := bEmpty2.Validate()
		require.Error(t, errE2)
		assert.Contains(t, errE2.Error(), "missing cached identity")
		assert.Contains(t, fEmpty2.Validate().Error(), "missing cached identity")

		// 6. invalid metadata ID nonempty/HasID false rejects
		fInvMeta := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest([]response.StreamObserverFactory{stubStreamObserverFactory{id: "sof-1"}}, "sof-1", false)
		bInvMeta := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fInvMeta}
		errInv := bInvMeta.Validate()
		require.Error(t, errInv)
		assert.Contains(t, errInv.Error(), "missing cached identity")
		assert.Contains(t, fInvMeta.Validate().Error(), "missing cached identity")

		// 7. metadata without value rejects
		fNoVal1 := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(nil, "sof-1", true)
		bNoVal1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal1}
		errNV1 := bNoVal1.Validate()
		require.Error(t, errNV1)
		assert.Contains(t, errNV1.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal1.Validate().Error(), "malformed metadata without value")

		fNoVal2 := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(nil, "", true)
		bNoVal2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal2}
		errNV2 := bNoVal2.Validate()
		require.Error(t, errNV2)
		assert.Contains(t, errNV2.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal2.Validate().Error(), "malformed metadata without value")

		fNoVal3 := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest(nil, "sof-1", false)
		bNoVal3 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal3}
		errNV3 := bNoVal3.Validate()
		require.Error(t, errNV3)
		assert.Contains(t, errNV3.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal3.Validate().Error(), "malformed metadata without value")

		// 8. cached ID mutation to another nonempty ID: validator accepts cached valid ID, no live ID call
		fMut := feature.NewMalformedGeneratedFrozenStreamObserverFactoriesForTest([]response.StreamObserverFactory{stubStreamObserverFactory{id: "orig-sof"}}, "mutated-sof", true)
		bMut := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fMut}
		require.NoError(t, bMut.Validate())
		require.NoError(t, fMut.Validate())
		idMut, okMut := feature.FrozenIdentity(fMut, feature.PlaneStreamObserverFactories)
		assert.True(t, okMut)
		assert.Equal(t, "mutated-sof", idMut)

		// 9. clone/freeze/toContributions/request freeze retain exact ID
		fCloned := fValid.Clone()
		idC, okC := feature.FrozenIdentity(fCloned, feature.PlaneStreamObserverFactories)
		assert.True(t, okC)
		assert.Equal(t, "sof-valid-1", idC)

		fReq := feature.FreezeRequestPlanes(fValid)
		idR, okR := feature.FrozenIdentity(fReq, feature.PlaneStreamObserverFactories)
		assert.True(t, okR)
		assert.Equal(t, "sof-valid-1", idR)

		cset := fValid.ToContributions()
		fRecon := cset.Freeze()
		idRec, okRec := feature.FrozenIdentity(fRecon, feature.PlaneStreamObserverFactories)
		assert.True(t, okRec)
		assert.Equal(t, "sof-valid-1", idRec)

		// 10. replay into destination retains correct metadata and atomic failures
		dst := feature.NewContributionSet()
		require.NoError(t, fValid.ReplayTo(dst, "replay-sof"))
		fDst := dst.Freeze()
		idDst, okDst := feature.FrozenIdentity(fDst, feature.PlaneStreamObserverFactories)
		assert.True(t, okDst)
		assert.Equal(t, "sof-valid-1", idDst)

		dstBefore := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dstBefore, feature.PlaneSubmitHooks, "init", []sdkhooks.SubmitHook{stubSubmit{id: "init-sub", ord: 0}}))
		snapBefore := dstBefore.Freeze()

		errRep := fNoVal1.ReplayTo(dstBefore, "bad-plugin")
		require.Error(t, errRep)
		snapAfter := dstBefore.Freeze()
		assert.Equal(t, feature.Get(snapBefore, feature.PlaneSubmitHooks), feature.Get(snapAfter, feature.PlaneSubmitHooks))
	})

	t.Run("PlaneCompactionPreservers", func(t *testing.T) {
		t.Parallel()

		// 1. valid contribution freezes ID
		csValid := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csValid, feature.PlaneCompactionPreservers, "p-valid", []compaction.Preserver{stubPreserver{id: "cp-valid-1"}}))
		fValid := csValid.Freeze()
		idV, okV := feature.FrozenIdentity(fValid, feature.PlaneCompactionPreservers)
		assert.True(t, okV)
		assert.Equal(t, "cp-valid-1", idV)

		// 2. FrozenIdentity returns exact ID without extra live ID call
		calls := 0
		csCount := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csCount, feature.PlaneCompactionPreservers, "p-count", []compaction.Preserver{callCountingCompactionPreserver{stubPreserver: stubPreserver{id: "cp-count-1"}, calls: &calls}}))
		fCount := csCount.Freeze()
		callsAfterFreeze := calls
		idCount, okCount := feature.FrozenIdentity(fCount, feature.PlaneCompactionPreservers)
		assert.True(t, okCount)
		assert.Equal(t, "cp-count-1", idCount)
		assert.Equal(t, callsAfterFreeze, calls, "FrozenIdentity must not call live ID")

		// 3. bundle Validate & frozen Validate call cached ValidateIdentity and no live ID
		bCount := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fCount}
		require.NoError(t, bCount.Validate())
		require.NoError(t, fCount.Validate())
		assert.Equal(t, callsAfterFreeze, calls, "Validate must not call live ID")

		// 4. >256 and whitespace IDs succeed
		csLong := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csLong, feature.PlaneCompactionPreservers, "p-long", []compaction.Preserver{stubPreserver{id: longID}}))
		fLong := csLong.Freeze()
		require.NoError(t, fLong.Validate())
		bLong := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fLong}
		require.NoError(t, bLong.Validate())
		idL, okL := feature.FrozenIdentity(fLong, feature.PlaneCompactionPreservers)
		assert.True(t, okL)
		assert.Equal(t, longID, idL)

		csWS := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(csWS, feature.PlaneCompactionPreservers, "p-ws", []compaction.Preserver{stubPreserver{id: whitespaceID}}))
		fWS := csWS.Freeze()
		require.NoError(t, fWS.Validate())
		bWS := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fWS}
		require.NoError(t, bWS.Validate())
		idW, okW := feature.FrozenIdentity(fWS, feature.PlaneCompactionPreservers)
		assert.True(t, okW)
		assert.Equal(t, whitespaceID, idW)

		// 5. empty cached identity with nonempty value rejects
		fEmpty1 := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest([]compaction.Preserver{stubPreserver{id: "cp-1"}}, "", false)
		bEmpty1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty1}
		errE1 := bEmpty1.Validate()
		require.Error(t, errE1)
		assert.Contains(t, errE1.Error(), "missing cached identity")
		assert.Contains(t, fEmpty1.Validate().Error(), "missing cached identity")

		fEmpty2 := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest([]compaction.Preserver{stubPreserver{id: "cp-1"}}, "", true)
		bEmpty2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fEmpty2}
		errE2 := bEmpty2.Validate()
		require.Error(t, errE2)
		assert.Contains(t, errE2.Error(), "missing cached identity")
		assert.Contains(t, fEmpty2.Validate().Error(), "missing cached identity")

		// 6. invalid metadata ID nonempty/HasID false rejects
		fInvMeta := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest([]compaction.Preserver{stubPreserver{id: "cp-1"}}, "cp-1", false)
		bInvMeta := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fInvMeta}
		errInv := bInvMeta.Validate()
		require.Error(t, errInv)
		assert.Contains(t, errInv.Error(), "missing cached identity")
		assert.Contains(t, fInvMeta.Validate().Error(), "missing cached identity")

		// 7. metadata without value rejects
		fNoVal1 := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest(nil, "cp-1", true)
		bNoVal1 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal1}
		errNV1 := bNoVal1.Validate()
		require.Error(t, errNV1)
		assert.Contains(t, errNV1.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal1.Validate().Error(), "malformed metadata without value")

		fNoVal2 := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest(nil, "", true)
		bNoVal2 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal2}
		errNV2 := bNoVal2.Validate()
		require.Error(t, errNV2)
		assert.Contains(t, errNV2.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal2.Validate().Error(), "malformed metadata without value")

		fNoVal3 := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest(nil, "cp-1", false)
		bNoVal3 := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fNoVal3}
		errNV3 := bNoVal3.Validate()
		require.Error(t, errNV3)
		assert.Contains(t, errNV3.Error(), "malformed metadata without value")
		assert.Contains(t, fNoVal3.Validate().Error(), "malformed metadata without value")

		// 8. cached ID mutation to another nonempty ID: validator accepts cached valid ID, no live ID call
		fMut := feature.NewMalformedGeneratedFrozenCompactionPreserversForTest([]compaction.Preserver{stubPreserver{id: "orig-cp"}}, "mutated-cp", true)
		bMut := feature.FeatureBundle{SchemaVersion: feature.SchemaVersionV1, PlaneSet: fMut}
		require.NoError(t, bMut.Validate())
		require.NoError(t, fMut.Validate())
		idMut, okMut := feature.FrozenIdentity(fMut, feature.PlaneCompactionPreservers)
		assert.True(t, okMut)
		assert.Equal(t, "mutated-cp", idMut)

		// 9. clone/freeze/toContributions/request freeze retain exact ID
		fCloned := fValid.Clone()
		idC, okC := feature.FrozenIdentity(fCloned, feature.PlaneCompactionPreservers)
		assert.True(t, okC)
		assert.Equal(t, "cp-valid-1", idC)

		fReq := feature.FreezeRequestPlanes(fValid)
		idR, okR := feature.FrozenIdentity(fReq, feature.PlaneCompactionPreservers)
		assert.True(t, okR)
		assert.Equal(t, "cp-valid-1", idR)

		cset := fValid.ToContributions()
		fRecon := cset.Freeze()
		idRec, okRec := feature.FrozenIdentity(fRecon, feature.PlaneCompactionPreservers)
		assert.True(t, okRec)
		assert.Equal(t, "cp-valid-1", idRec)

		// 10. replay into destination retains correct metadata and atomic failures
		dst := feature.NewContributionSet()
		require.NoError(t, fValid.ReplayTo(dst, "replay-cp"))
		fDst := dst.Freeze()
		idDst, okDst := feature.FrozenIdentity(fDst, feature.PlaneCompactionPreservers)
		assert.True(t, okDst)
		assert.Equal(t, "cp-valid-1", idDst)

		dstBefore := feature.NewContributionSet()
		require.NoError(t, feature.Contribute(dstBefore, feature.PlaneSubmitHooks, "init", []sdkhooks.SubmitHook{stubSubmit{id: "init-sub", ord: 0}}))
		snapBefore := dstBefore.Freeze()

		errRep := fNoVal1.ReplayTo(dstBefore, "bad-plugin")
		require.Error(t, errRep)
		snapAfter := dstBefore.Freeze()
		assert.Equal(t, feature.Get(snapBefore, feature.PlaneSubmitHooks), feature.Get(snapAfter, feature.PlaneSubmitHooks))
	})

	t.Run("PlaneTerminalDecisionProvider keeps ValidateProviderID", func(t *testing.T) {
		t.Parallel()

		if err := feature.PlaneTerminalDecisionProvider.ValidateIdentity(""); err == nil {
			t.Fatal("expected empty provider ID to reject")
		}
		if err := feature.PlaneTerminalDecisionProvider.ValidateIdentity("   "); err == nil {
			t.Fatal("expected whitespace provider ID to reject for terminaldecision")
		}
		if err := feature.PlaneTerminalDecisionProvider.ValidateIdentity(strings.Repeat("x", 200)); err == nil {
			t.Fatal("expected >128 byte provider ID to reject for terminaldecision")
		}
		if err := feature.PlaneTerminalDecisionProvider.ValidateIdentity("term.valid.id"); err != nil {
			t.Fatalf("expected valid provider ID to succeed: %v", err)
		}
	})
}
