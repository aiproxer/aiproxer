package runtimebundle_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	_ "modernc.org/sqlite"
)

func TestBuildHost_MeteringJournalInjectsAtomicObservationSinkBeforeTerminal(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "metering.sqlite")
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      writeRefinement41CompositionConfig(t, journalPath, true),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	executor := hostActiveExecutor(t, host)
	if executor.MeteringRecorder == nil {
		t.Fatal("metering-enabled production executor must expose its recorder")
	}
	sink := executor.MeteringObservationSink
	if sink == nil {
		t.Fatal("metering-enabled production executor must expose the V2 observation sink")
	}
	if _, ok := sink.(metering.AtomicObservationSink); !ok {
		t.Fatalf("production observation sink = %T, want metering.AtomicObservationSink", sink)
	}
	store, ok := runtimebundle.HostProcess(host).MeteringRecorder.(*journalstore.DurableStore)
	if !ok || store == nil {
		t.Fatalf("production metering recorder = %T, want *journalstore.DurableStore", runtimebundle.HostProcess(host).MeteringRecorder)
	}
	candidate := compileCandidateAfterHost(t, host)
	candidateExecutor := candidate.Executor()
	if candidateExecutor == nil {
		t.Fatal("compiled candidate must expose an executor")
	}
	if candidateExecutor.MeteringObservationSink != sink {
		t.Fatalf("reloaded candidate sink = %T, want the process-owned sink %T", candidateExecutor.MeteringObservationSink, sink)
	}
	if err := candidate.Close(); err != nil {
		t.Fatalf("close candidate generation: %v", err)
	}
	if err := store.CheckReadiness(t.Context()); err != nil {
		t.Fatalf("process-owned metering journal after candidate close: %v", err)
	}

	observation := refinement41ComposedObservation()
	backend := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &refinement41ComposedStream{
				ManagedEventStream: aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}}),
				evidence:           []execbackend.EconomicEvidence{{Observation: observation}},
			}, nil
		},
	}
	if executor.Backends == nil {
		executor.Backends = map[string]execbackend.Backend{}
	}
	executor.Backends["backend"] = backend
	capFn := func(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
		return execbackend.EffectiveCaps(ctx, backend, call, cand)
	}
	switch capMap := executor.CapsResolver.(type) {
	case capabilities.MapResolver:
		capMap["backend"] = capFn
	case nil:
		executor.CapsResolver = capabilities.MapResolver{"backend": capFn}
	default:
		t.Fatalf("CapsResolver type %T cannot accept test backend", executor.CapsResolver)
	}

	stream, err := executor.Execute(t.Context(), &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "backend:model"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	event, err := stream.Recv(t.Context())
	if err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	if event.Kind != aipapi.EventResponseStarted {
		t.Fatalf("first event kind = %q, want nonterminal response_started", event.Kind)
	}

	page, err := store.ListObservations(t.Context(), journalstore.ObservationQuery{
		StoreID:     "metering-sqlite",
		SubjectKind: metering.SubjectBLeg,
		SubjectID:   "b-composed-refinement41",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListObservations before terminal: %v", err)
	}
	if len(page.Observations) != 1 {
		t.Fatalf("pre-terminal durable observations = %d, want 1", len(page.Observations))
	}
	if got := page.Observations[0].Revision; got != 1 {
		t.Fatalf("pre-terminal observation revision = %d, want 1", got)
	}
}

func TestBuildHost_DisabledMeteringDoesNotAllocateSinkOrJournal(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "disabled-metering.sqlite")
	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:      writeRefinement41CompositionConfig(t, journalPath, false),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	hostServeCleanup(t, host)

	executor := hostActiveExecutor(t, host)
	if executor.MeteringRecorder != nil {
		t.Fatalf("disabled production metering recorder = %T, want nil", executor.MeteringRecorder)
	}
	if executor.MeteringObservationSink != nil {
		t.Fatalf("disabled production observation sink = %T, want nil", executor.MeteringObservationSink)
	}
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled metering journal stat error = %v, want os.ErrNotExist", err)
	}
}

type refinement41ComposedStream struct {
	aipapi.ManagedEventStream
	evidence []execbackend.EconomicEvidence
}

func (s *refinement41ComposedStream) DrainEconomicEvidenceRecords() []execbackend.EconomicEvidence {
	out := make([]execbackend.EconomicEvidence, len(s.evidence))
	copy(out, s.evidence)
	s.evidence = nil
	return out
}

func refinement41ComposedObservation() metering.Observation {
	now := time.Unix(1_700_000_001, 0).UTC()
	quantity := metering.Decimal{Coefficient: "7", Scale: 0}
	return metering.Observation{
		Version:        metering.ObservationVersionV2,
		ID:             "obs-composed-refinement41",
		SourceEventKey: "provider-usage",
		Revision:       1,
		StreamID:       "stream-composed-refinement41",
		Sequence:       1,
		Origin:         metering.OriginProvider,
		Acquisition:    metering.AcquisitionProviderResponse,
		Authority:      metering.AuthorityObservedClaim,
		Perspective:    metering.PerspectiveOperator,
		Boundary:       metering.BoundaryBackendIngress,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Subject: metering.SubjectRef{
			Kind:          metering.SubjectBLeg,
			StoreID:       "metering-sqlite",
			ALegID:        "a-composed-refinement41",
			BillingCallID: "call-composed-refinement41",
			BLegID:        "b-composed-refinement41",
		},
		Correlation: metering.CorrelationV2{
			StoreID:       "metering-sqlite",
			CallID:        "call-composed-refinement41",
			BillingCallID: "call-composed-refinement41",
			ALegID:        "a-composed-refinement41",
			BLegID:        "b-composed-refinement41",
		},
		Semantics:  metering.SemanticsCumulative,
		ObservedAt: now,
		ReceivedAt: now,
		MappingRef: "refinement4.1-composition",
		Measures: []metering.Measure{{
			Key:     metering.ComponentKey{Direction: metering.DirectionOutput, Component: metering.ComponentTextToken, Unit: metering.UnitToken, SchemaID: "refinement4.1"},
			Value:   &quantity,
			Quality: metering.QualityObserved,
		}},
	}
}

func writeRefinement41CompositionConfig(t *testing.T, journalPath string, meteringEnabled bool) string {
	t.Helper()
	configText := fmt.Sprintf(`server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 3
  default_route: "backend:test-model"
continuity:
  in_memory: true
  store: memory
metering:
  enabled: %t
  journal:
    store: sqlite
    sqlite_path: %q
logging:
  level: error
  format: text
diagnostics:
  enabled: false
hooks:
  tool_reactor_error_policy: fail_open
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: backend
      kind: openai-responses
      enabled: false
      config: {}
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
`, meteringEnabled, filepath.ToSlash(journalPath))
	path := filepath.Join(t.TempDir(), "refinement41-composition.yaml")
	if err := os.WriteFile(path, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var _ execbackend.EconomicEvidenceSource = (*refinement41ComposedStream)(nil)
