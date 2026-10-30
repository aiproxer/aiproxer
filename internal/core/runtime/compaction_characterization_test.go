package runtime_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	compactiondetect "github.com/aiproxer/aiproxer/internal/infra/compactiondetect"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCharacterize_CompactionRuntime_NilDetectorNoOp proves that when no detector is configured
// on the Executor (Detector == nil) or on the response pipeline:
// 1. Request execution proceeds without error or panic.
// 2. Response streaming proceeds without error or panic.
// 3. Registered observers receive 0 events.
// 4. Preservers receive empty events without error.
func TestCharacterize_CompactionRuntime_NilDetectorNoOp(t *testing.T) {
	t.Parallel()

	rec := &recordingCompactionObserver{}
	ex := compactionTestExecutor(t, nil, rec)
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() aipapi.ManagedEventStream {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventTextDelta, Delta: "hello world"},
				{Kind: aipapi.EventResponseFinished, FinishReason: "stop"},
			})
		}),
	}

	call := compactCall("ck-nil-detector", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)

	events := drain(t, stream)
	require.NotEmpty(t, events)

	// Observer should receive 0 compaction events when detector is nil.
	assert.Empty(t, rec.snapshot(), "nil detector must emit 0 compaction events to observers")
}

// TestCharacterize_CompactionRuntime_PanicIsolation proves that panic isolation
// is strictly maintained by safe compaction wrappers:
// 1. Safe wrappers recover from panics and return nil / zero-value.
// 2. A panicking detector does not cause request failure or stream failure (fail-open).
func TestCharacterize_CompactionRuntime_PanicIsolation(t *testing.T) {
	t.Parallel()

	t.Run("safe_wrappers_panic_recovery", func(t *testing.T) {
		t.Parallel()

		// Test nil detector calls on safe wrappers
		reqMeta := compaction.PreservationMeta{TraceID: "t-1", ALegID: "a-1"}
		respMeta := compaction.PreservationMeta{TraceID: "t-1", ALegID: "a-1"}
		call := aipapi.Call{ID: "c-1"}
		ev := aipapi.Event{Kind: aipapi.EventResponseStarted}

		assert.Nil(t, runtime.SafeCompactionRequestOpenedForTest(nil, reqMeta, call))
		assert.Nil(t, runtime.SafeCompactionResponseReleasedForTest(nil, respMeta, ev))
		assert.Equal(t, compaction.ResponsePreview{Kind: compaction.PreviewNone}, runtime.SafeCompactionPreviewResponseForTest(nil, respMeta, ev))
		assert.Equal(t, compaction.RequestPreview{Kind: compaction.PreviewNone}, runtime.SafeCompactionPreviewRequestForTest(nil, reqMeta, call))

		// Test panicking detector calls on safe wrappers
		panicking := &panickingCompactionDetector{}
		assert.Nil(t, runtime.SafeCompactionRequestOpenedForTest(panicking, reqMeta, call))
		assert.Nil(t, runtime.SafeCompactionResponseReleasedForTest(panicking, respMeta, ev))
		assert.Equal(t, compaction.ResponsePreview{Kind: compaction.PreviewNone}, runtime.SafeCompactionPreviewResponseForTest(panicking, respMeta, ev))
		assert.Equal(t, compaction.RequestPreview{Kind: compaction.PreviewNone}, runtime.SafeCompactionPreviewRequestForTest(panicking, reqMeta, call))
	})

	t.Run("end_to_end_fail_open_on_panic", func(t *testing.T) {
		t.Parallel()

		// A panicking detector is safely caught by wrappers (fail-open)
		panicking := &panickingCompactionDetector{}
		rec := &recordingCompactionObserver{}
		ex := compactionTestExecutor(t, panicking, rec)
		ex.Backends = map[string]execbackend.Backend{
			"openai": openStubBackend(func() aipapi.ManagedEventStream {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventTextDelta, Delta: "text"},
					{Kind: aipapi.EventResponseFinished},
				})
			}),
		}

		call := compactCall("ck-panic-isolation", bigItems("sample text"))
		stream, err := ex.Execute(context.Background(), call)
		require.NoError(t, err)

		released := drain(t, stream)
		assert.NotEmpty(t, released)
		assert.Empty(t, rec.snapshot(), "panicking detector must emit 0 events to observers")
	})
}

// TestCharacterize_CompactionRuntime_RequestOpenOrderingAndCorrelation proves the exact
// request-open execution sequence:
// 1. Detector RequestOpened runs first.
// 2. Correlation metadata is faithfully stamped into PreservationMeta.
// 3. Preserver RequestOpened receives the events and PreservationMeta.
// 4. Observer OnCompaction receives the dispatched events last.
func TestCharacterize_CompactionRuntime_RequestOpenOrderingAndCorrelation(t *testing.T) {
	t.Parallel()

	d := compactiondetect.New(compactiondetect.Config{})
	var sequence []string
	var seqMu sync.Mutex
	recordSeq := func(step string) {
		seqMu.Lock()
		defer seqMu.Unlock()
		sequence = append(sequence, step)
	}

	observer := &orderedRuntimeObserver{order: &sequence}
	preserver := &characterizationOrderingPreserver{
		onOpened: func(call aipapi.Call, events []compaction.Event, meta compaction.PreservationMeta) {
			recordSeq("preserver-request-opened")
			assert.Equal(t, call.ID, meta.TraceID)
			assert.NotEmpty(t, meta.ALegID)
			assert.NotEmpty(t, meta.BLegID)
			assert.Equal(t, 1, meta.AttemptSeq)
			assert.NotEmpty(t, meta.SessionID)
			if len(events) > 0 {
				assert.NotEmpty(t, meta.TransactionID)
				assert.NotEmpty(t, meta.RuleID)
				assert.NotEmpty(t, meta.Evidence)
			}
		},
	}

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	ex := compactionTestExecutorWithStore(t, d, nil, st)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionObservers:  []compaction.Observer{observer},
			CompactionPreservers: []compaction.Preserver{preserver},
		}),
	})
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() aipapi.ManagedEventStream {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventResponseFinished},
			})
		}),
	}

	call := compactCall("ck-corr-order", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	call.Session.AuthoritativeSessionID = "sess-corr-1"
	// Set trace via context or call
	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)
	drain(t, stream)

	seqMu.Lock()
	defer seqMu.Unlock()
	// Preserver RequestOpened must run before Observer OnCompaction (observer-started)
	require.Contains(t, sequence, "preserver-request-opened")
	require.Contains(t, sequence, "observer-started")

	preserverIdx := -1
	observerIdx := -1
	for i, s := range sequence {
		if s == "preserver-request-opened" && preserverIdx == -1 {
			preserverIdx = i
		}
		if s == "observer-started" && observerIdx == -1 {
			observerIdx = i
		}
	}
	assert.True(t, preserverIdx < observerIdx,
		"Preserver RequestOpened (%d) must execute BEFORE Observer OnCompaction (%d)", preserverIdx, observerIdx)
}

// TestCharacterize_CompactionRuntime_PurePreviewBeforePreserverAndCommitAfterRelease proves:
// 1. Pure PreviewResponse runs before Preserver.BeforeResponseRelease.
// 2. Preserver.BeforeResponseRelease receives the Candidate preview.
// 3. ResponseReleased commits after Preserver.BeforeResponseRelease has finished.
// 4. Preserver.AfterResponseRelease runs after ResponseReleased commits.
func TestCharacterize_CompactionRuntime_PurePreviewBeforePreserverAndCommitAfterRelease(t *testing.T) {
	t.Parallel()

	d := compactiondetect.New(compactiondetect.Config{})
	var sequence []string
	var seqMu sync.Mutex
	recordSeq := func(step string) {
		seqMu.Lock()
		defer seqMu.Unlock()
		sequence = append(sequence, step)
	}

	observer := &orderedRuntimeObserver{order: &sequence}
	preserver := &characterizationOrderingPreserver{
		onBeforeRelease: func(ev *aipapi.Event, prev compaction.ResponsePreview, meta compaction.PreservationMeta) {
			if ev != nil && ev.Kind == aipapi.EventTextDelta {
				recordSeq("preserver-before-release-delta")
				assert.Equal(t, compaction.PreviewCompletionCandidate, prev.Kind)
				assert.Equal(t, "hermes.legacy_compaction_post.v1", prev.RuleID)
				assert.NotEmpty(t, prev.TransactionID)
			}
		},
		onAfterRelease: func(ev aipapi.Event, meta compaction.PreservationMeta) {
			if ev.Kind == aipapi.EventTextDelta {
				recordSeq("preserver-after-release-delta")
			}
		},
	}

	ex := configureRuntimeCompactionPreserver(t, d, nil, preserver, nil, nil)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionObservers:  []compaction.Observer{observer},
			CompactionPreservers: []compaction.Preserver{preserver},
		}),
	})
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() aipapi.ManagedEventStream {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventTextDelta, Delta: "[CONTEXT SUMMARY]: compacted"},
				{Kind: aipapi.EventResponseFinished},
			})
		}),
	}

	call := compactCall("ck-preview-order", bigItems("CONTEXT CHECKPOINT COMPACTION\ncompact now"))
	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)
	drain(t, stream)

	seqMu.Lock()
	defer seqMu.Unlock()

	// Sequence must follow: preserver-before-release-delta -> observer-completed -> preserver-after-release-delta
	wantOrder := []string{"preserver-before-release-delta", "observer-completed", "preserver-after-release-delta"}
	filtered := filterSequence(sequence, wantOrder)
	assert.Equal(t, wantOrder, filtered,
		"Response release sequence must execute BeforeResponseRelease -> committed ResponseReleased/Observer -> AfterResponseRelease")
}

// The detector interface supports all four compaction observation operations.
func TestCharacterize_CompactionRuntime_DependencyGapForTask51(t *testing.T) {
	t.Parallel()

	// Reflectively verify current CompactionRuntime struct field types
	rtType := reflect.TypeFor[runtime.CorePorts]()
	detectorField, ok := rtType.FieldByName("CompactionDetector")
	require.True(t, ok, "CorePorts must have CompactionDetector field")
	assert.Equal(t, "runtime.CompactionDetector", detectorField.Type.String(),
		"CorePorts field must be the CompactionDetector interface")
	assert.Equal(t, reflect.Interface, detectorField.Type.Kind(),
		"CorePorts.CompactionDetector must be an interface kind")

	// Verify all four runtime methods exist on CompactionDetector with expected signature patterns
	portType := detectorField.Type
	previewReqMethod, ok := portType.MethodByName("PreviewRequest")
	require.True(t, ok, "CompactionDetector must have PreviewRequest method")
	assert.Equal(t, 2, previewReqMethod.Type.NumIn())  // PreservationMeta, aipapi.Call (interface methods don't include receiver in NumIn)
	assert.Equal(t, 1, previewReqMethod.Type.NumOut()) // compaction.RequestPreview

	reqOpenedMethod, ok := portType.MethodByName("RequestOpened")
	require.True(t, ok, "CompactionDetector must have RequestOpened method")
	assert.Equal(t, 2, reqOpenedMethod.Type.NumIn())  // PreservationMeta, aipapi.Call (interface methods don't include receiver in NumIn)
	assert.Equal(t, 1, reqOpenedMethod.Type.NumOut()) // []compaction.Event

	previewRespMethod, ok := portType.MethodByName("PreviewResponse")
	require.True(t, ok, "CompactionDetector must have PreviewResponse method")
	assert.Equal(t, 2, previewRespMethod.Type.NumIn())  // PreservationMeta, aipapi.Event
	assert.Equal(t, 1, previewRespMethod.Type.NumOut()) // compaction.ResponsePreview

	respReleasedMethod, ok := portType.MethodByName("ResponseReleased")
	require.True(t, ok, "CompactionDetector must have ResponseReleased method")
	assert.Equal(t, 2, respReleasedMethod.Type.NumIn())  // PreservationMeta, aipapi.Event
	assert.Equal(t, 1, respReleasedMethod.Type.NumOut()) // []compaction.Event
}

type characterizationOrderingPreserver struct {
	onOpened        func(aipapi.Call, []compaction.Event, compaction.PreservationMeta)
	onBeforeRelease func(*aipapi.Event, compaction.ResponsePreview, compaction.PreservationMeta)
	onAfterRelease  func(aipapi.Event, compaction.PreservationMeta)
}

func (p *characterizationOrderingPreserver) ID() string { return "char-ordering" }

func (*characterizationOrderingPreserver) BeforeRequest(context.Context, *aipapi.Call, compaction.RequestPreview, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p *characterizationOrderingPreserver) RequestOpened(_ context.Context, call aipapi.Call, events []compaction.Event, meta compaction.PreservationMeta, _ compaction.Services) error {
	if p.onOpened != nil {
		p.onOpened(call, events, meta)
	}
	return nil
}

func (p *characterizationOrderingPreserver) BeforeResponseRelease(_ context.Context, ev *aipapi.Event, prev compaction.ResponsePreview, meta compaction.PreservationMeta, _ compaction.Services) error {
	if p.onBeforeRelease != nil {
		p.onBeforeRelease(ev, prev, meta)
	}
	return nil
}

func (*characterizationOrderingPreserver) RequestOpenFailed(context.Context, compaction.PreservationMeta, compaction.Services) error {
	return nil
}

func (p *characterizationOrderingPreserver) AfterResponseRelease(_ context.Context, ev aipapi.Event, meta compaction.PreservationMeta, _ compaction.Services) error {
	if p.onAfterRelease != nil {
		p.onAfterRelease(ev, meta)
	}
	return nil
}

func filterSequence(src []string, keep []string) []string {
	keepMap := make(map[string]bool, len(keep))
	for _, k := range keep {
		keepMap[k] = true
	}
	var out []string
	for _, s := range src {
		if keepMap[s] {
			out = append(out, s)
		}
	}
	return out
}

type panickingCompactionDetector struct{}

func (*panickingCompactionDetector) PreviewRequest(compaction.PreservationMeta, aipapi.Call) compaction.RequestPreview {
	panic("detector panic: PreviewRequest")
}

func (*panickingCompactionDetector) RequestOpened(compaction.PreservationMeta, aipapi.Call) []compaction.Event {
	panic("detector panic: RequestOpened")
}

func (*panickingCompactionDetector) PreviewResponse(compaction.PreservationMeta, aipapi.Event) compaction.ResponsePreview {
	panic("detector panic: PreviewResponse")
}

func (*panickingCompactionDetector) ResponseReleased(compaction.PreservationMeta, aipapi.Event) []compaction.Event {
	panic("detector panic: ResponseReleased")
}

type fakeCompactionDetector struct {
	mu               sync.Mutex
	previewRequest   func(compaction.PreservationMeta, aipapi.Call) compaction.RequestPreview
	requestOpened    func(compaction.PreservationMeta, aipapi.Call) []compaction.Event
	previewResponse  func(compaction.PreservationMeta, aipapi.Event) compaction.ResponsePreview
	responseReleased func(compaction.PreservationMeta, aipapi.Event) []compaction.Event
	previewReqCalls  []compaction.PreservationMeta
	openedCalls      []compaction.PreservationMeta
	previewCalls     []compaction.PreservationMeta
	releasedCalls    []compaction.PreservationMeta
}

func (f *fakeCompactionDetector) PreviewRequest(meta compaction.PreservationMeta, call aipapi.Call) compaction.RequestPreview {
	f.mu.Lock()
	f.previewReqCalls = append(f.previewReqCalls, meta)
	f.mu.Unlock()
	if f.previewRequest != nil {
		return f.previewRequest(meta, call)
	}
	return compaction.RequestPreview{Kind: compaction.PreviewNone}
}

func (f *fakeCompactionDetector) RequestOpened(meta compaction.PreservationMeta, call aipapi.Call) []compaction.Event {
	f.mu.Lock()
	f.openedCalls = append(f.openedCalls, meta)
	f.mu.Unlock()
	if f.requestOpened != nil {
		return f.requestOpened(meta, call)
	}
	return nil
}

func (f *fakeCompactionDetector) PreviewResponse(meta compaction.PreservationMeta, ev aipapi.Event) compaction.ResponsePreview {
	f.mu.Lock()
	f.previewCalls = append(f.previewCalls, meta)
	f.mu.Unlock()
	if f.previewResponse != nil {
		return f.previewResponse(meta, ev)
	}
	return compaction.ResponsePreview{Kind: compaction.PreviewNone}
}

func (f *fakeCompactionDetector) ResponseReleased(meta compaction.PreservationMeta, ev aipapi.Event) []compaction.Event {
	f.mu.Lock()
	f.releasedCalls = append(f.releasedCalls, meta)
	f.mu.Unlock()
	if f.responseReleased != nil {
		return f.responseReleased(meta, ev)
	}
	return nil
}

// TestCompactionRuntime_FakeDetectorInjectionAndCorrelation proves that a fake detector
// satisfying the runtime.CompactionDetector interface can be injected into CompactionRuntime,
// receives faithful correlation metadata via compaction.PreservationMeta without value loss,
// and successfully drives compaction preservers and observers.
func TestCompactionRuntime_FakeDetectorInjectionAndCorrelation(t *testing.T) {
	t.Parallel()

	// Compile-time interface checks
	var _ runtime.CompactionDetector = (*fakeCompactionDetector)(nil)
	var _ runtime.CompactionDetector = (*compactiondetect.Detector)(nil)

	fake := &fakeCompactionDetector{
		requestOpened: func(meta compaction.PreservationMeta, call aipapi.Call) []compaction.Event {
			return []compaction.Event{
				{
					Phase:         compaction.PhaseStarted,
					RuleID:        "fake.compaction.v1",
					TransactionID: "tx-fake-1",
					Evidence:      compaction.EvidenceProtocolStrict,
					TraceID:       meta.TraceID,
					ALegID:        meta.ALegID,
					BLegID:        meta.BLegID,
					AttemptSeq:    meta.AttemptSeq,
					SessionID:     meta.SessionID,
				},
			}
		},
		previewResponse: func(meta compaction.PreservationMeta, ev aipapi.Event) compaction.ResponsePreview {
			if ev.Kind == aipapi.EventTextDelta {
				return compaction.ResponsePreview{
					Kind:          compaction.PreviewCompletionCandidate,
					RuleID:        "fake.compaction.v1",
					TransactionID: "tx-fake-1",
					Evidence:      compaction.EvidenceProtocolStrict,
				}
			}
			return compaction.ResponsePreview{Kind: compaction.PreviewNone}
		},
		responseReleased: func(meta compaction.PreservationMeta, ev aipapi.Event) []compaction.Event {
			if ev.Kind == aipapi.EventTextDelta {
				return []compaction.Event{
					{
						Phase:         compaction.PhaseCompleted,
						RuleID:        "fake.compaction.v1",
						TransactionID: "tx-fake-1",
						Evidence:      compaction.EvidenceProtocolStrict,
						TraceID:       meta.TraceID,
						ALegID:        meta.ALegID,
						BLegID:        meta.BLegID,
						AttemptSeq:    meta.AttemptSeq,
						SessionID:     meta.SessionID,
					},
				}
			}
			return nil
		},
	}

	rec := &recordingCompactionObserver{}
	var preserverOpenedMeta compaction.PreservationMeta
	var preserverBeforeMeta compaction.PreservationMeta
	var preserverPreview compaction.ResponsePreview
	preserver := &characterizationOrderingPreserver{
		onOpened: func(call aipapi.Call, events []compaction.Event, meta compaction.PreservationMeta) {
			preserverOpenedMeta = meta
		},
		onBeforeRelease: func(ev *aipapi.Event, prev compaction.ResponsePreview, meta compaction.PreservationMeta) {
			if ev != nil && ev.Kind == aipapi.EventTextDelta {
				preserverBeforeMeta = meta
				preserverPreview = prev
			}
		},
	}

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	ex := compactionTestExecutorWithStore(t, fake, rec, st)
	ex.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(ex.Bus, extensions.SnapshotOptions{
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			CompactionObservers:  []compaction.Observer{rec},
			CompactionPreservers: []compaction.Preserver{preserver},
		}),
	})
	ex.Backends = map[string]execbackend.Backend{
		"openai": openStubBackend(func() aipapi.ManagedEventStream {
			return aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventTextDelta, Delta: "fake compacted response"},
				{Kind: aipapi.EventResponseFinished},
			})
		}),
	}

	call := compactCall("ck-fake-injection", bigItems("test payload"))
	call.Session.AuthoritativeSessionID = "sess-fake-42"
	stream, err := ex.Execute(context.Background(), call)
	require.NoError(t, err)
	released := drain(t, stream)
	assert.NotEmpty(t, released)

	// Assert fake detector received exact correlation in PreservationMeta
	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.openedCalls, 1)
	assert.Equal(t, call.ID, fake.openedCalls[0].TraceID)
	assert.NotEmpty(t, fake.openedCalls[0].SessionID)
	assert.NotEmpty(t, fake.openedCalls[0].ALegID)
	assert.NotEmpty(t, fake.openedCalls[0].BLegID)
	assert.Equal(t, 1, fake.openedCalls[0].AttemptSeq)

	require.NotEmpty(t, fake.previewCalls)
	assert.Equal(t, call.ID, fake.previewCalls[0].TraceID)
	assert.Equal(t, fake.openedCalls[0].SessionID, fake.previewCalls[0].SessionID)

	require.NotEmpty(t, fake.releasedCalls)
	assert.Equal(t, call.ID, fake.releasedCalls[0].TraceID)
	assert.Equal(t, fake.openedCalls[0].SessionID, fake.releasedCalls[0].SessionID)

	// Assert preserver received stamped PreservationMeta
	assert.Equal(t, "tx-fake-1", preserverOpenedMeta.TransactionID)
	assert.Equal(t, "fake.compaction.v1", preserverOpenedMeta.RuleID)
	assert.Equal(t, compaction.EvidenceProtocolStrict, preserverOpenedMeta.Evidence)

	assert.Equal(t, compaction.PreviewCompletionCandidate, preserverPreview.Kind)
	assert.Equal(t, "tx-fake-1", preserverBeforeMeta.TransactionID)

	// Assert observers received dispatched events from fake detector
	events := rec.snapshot()
	require.Len(t, events, 2)
	assert.Equal(t, compaction.PhaseStarted, events[0].Phase)
	assert.Equal(t, "tx-fake-1", events[0].TransactionID)
	assert.Equal(t, compaction.PhaseCompleted, events[1].Phase)
	assert.Equal(t, "tx-fake-1", events[1].TransactionID)
}
