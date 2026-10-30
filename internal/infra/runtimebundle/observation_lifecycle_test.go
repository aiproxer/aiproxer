package runtimebundle

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	dbinfra "github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// observationWriterConfig is the minimal standard-distribution configuration
// the writer-declaration and producer-fence proofs build against.
const observationWriterConfig = `server:
  address: "127.0.0.1:0"
access:
  mode: single_user
routing:
  max_attempts: 1
  default_route: "backend:test-model"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
  backends:
    - id: backend
      kind: openai-responses
      enabled: false
      config: {}
  features:
    - id: submit-noop
      enabled: true
      config: {}
`

// observationWriterHandlerComposer is the minimal handler composer the writer
// proofs build against. The proofs never serve HTTP; they only need a
// composable host.
func observationWriterHandlerComposer(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
	return http.NewServeMux(), nil
}

func writeObservationWriterConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "observation-writer.yaml")
	if err := os.WriteFile(path, []byte(observationWriterConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// blockingObservationSink is a borrowed atomic observation sink whose append
// blocks at a barrier. It records the admitted and completed appends so the
// fence proofs can observe the exact completion count.
//
// It declares the durable journal identity it belongs to, so a recorder built
// from the same journal is provably one pair with it.
type blockingObservationSink struct {
	journalID metering.ObservationJournalID
	entered   chan struct{}
	batch     chan struct{}
	release   chan struct{}
	once      sync.Once
	batchOnce sync.Once
	mu        sync.Mutex
	appended  int
	batches   int
	closed    int
}

func newBlockingObservationSink() *blockingObservationSink {
	return newBlockingObservationSinkFor("journal-borrowed")
}

func newBlockingObservationSinkFor(journalID metering.ObservationJournalID) *blockingObservationSink {
	return &blockingObservationSink{
		journalID: journalID,
		entered:   make(chan struct{}),
		batch:     make(chan struct{}),
		release:   make(chan struct{}),
	}
}

// ObservationJournalIdentity declares the durable journal this sink writes to.
func (s *blockingObservationSink) ObservationJournalIdentity() metering.ObservationJournalID {
	return s.journalID
}

func (s *blockingObservationSink) Append(ctx context.Context, _ metering.Observation) error {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.appended++
	s.mu.Unlock()
	return nil
}

func (s *blockingObservationSink) AppendObservations(ctx context.Context, observations []metering.Observation) error {
	s.batchOnce.Do(func() { close(s.batch) })
	s.mu.Lock()
	s.batches++
	s.mu.Unlock()
	for range observations {
		if err := s.Append(ctx, metering.Observation{}); err != nil {
			return err
		}
	}
	return nil
}

func (s *blockingObservationSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appended
}

// batchCount reports how many atomic batches the sink received.
func (s *blockingObservationSink) batchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.batches
}

// countingRecorder is a borrowed durable recorder. The runtime borrows it and
// never closes it, so its close counter witnesses that the runtime did not
// take ownership of the borrowed pair.
//
// It declares the durable journal identity it belongs to.
type countingRecorder struct {
	journalID metering.ObservationJournalID
	mu        sync.Mutex
	closed    int
}

func newCountingRecorderFor(journalID metering.ObservationJournalID) *countingRecorder {
	return &countingRecorder{journalID: journalID}
}

// ObservationJournalIdentity declares the durable journal this recorder
// writes to.
func (r *countingRecorder) ObservationJournalIdentity() metering.ObservationJournalID {
	return r.journalID
}

func (r *countingRecorder) Append(context.Context, metering.Fact) error { return nil }

func (r *countingRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed++
	return nil
}

func (r *countingRecorder) closes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// TestSelectObservationWriter locks the pure pre-acquisition selector: the
// complete external pair selects the external writer, an explicit legacy
// writer selects the legacy writer, no declaration selects none, and every
// incomplete or competing declaration is a typed conflict.
func TestSelectObservationWriter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      observationWriterInput
		want    coremetering.ObservationWriterSelection
		wantErr bool
	}{
		{name: "no declaration selects none", in: observationWriterInput{}, want: coremetering.ObservationWriterNone},
		{name: "complete external pair selects external", in: observationWriterInput{ExplicitRecorder: true, ExplicitSink: true}, want: coremetering.ObservationWriterExternal},
		{name: "legacy writer selects legacy", in: observationWriterInput{LegacyEconomicWriter: true}, want: coremetering.ObservationWriterLegacy},
		{name: "sink without recorder is a conflict", in: observationWriterInput{ExplicitSink: true}, wantErr: true},
		{name: "recorder without sink is a conflict", in: observationWriterInput{ExplicitRecorder: true}, wantErr: true},
		{name: "external pair with legacy writer is a conflict", in: observationWriterInput{ExplicitRecorder: true, ExplicitSink: true, LegacyEconomicWriter: true}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := selectObservationWriter(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("selectObservationWriter(%+v) = %v, want a conflict", tc.in, got)
				}
				if !errors.Is(err, coremetering.ErrObservationWriterConflict) {
					t.Fatalf("err = %v, want the typed observation-writer conflict", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectObservationWriter(%+v): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("selection = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestValidateObservationWriterDeclaration locks the carried-selection rules:
// the external writer needs a complete pair, the legacy writer refuses an
// external sink, and the neutral selections refuse an explicit pair.
func TestValidateObservationWriterDeclaration(t *testing.T) {
	t.Parallel()
	complete := observationWriterInput{ExplicitRecorder: true, ExplicitSink: true}
	tests := []struct {
		name      string
		selection coremetering.ObservationWriterSelection
		in        observationWriterInput
		wantErr   bool
	}{
		{name: "external accepts a complete pair", selection: coremetering.ObservationWriterExternal, in: complete},
		{name: "external refuses an incomplete pair", selection: coremetering.ObservationWriterExternal, in: observationWriterInput{ExplicitRecorder: true}, wantErr: true},
		{name: "external refuses a legacy writer", selection: coremetering.ObservationWriterExternal, in: observationWriterInput{ExplicitRecorder: true, ExplicitSink: true, LegacyEconomicWriter: true}, wantErr: true},
		{name: "legacy refuses an external sink", selection: coremetering.ObservationWriterLegacy, in: observationWriterInput{ExplicitSink: true}, wantErr: true},
		{name: "none refuses an explicit pair", selection: coremetering.ObservationWriterNone, in: complete, wantErr: true},
		{name: "unspecified refuses an explicit pair", selection: coremetering.ObservationWriterUnspecified, in: complete, wantErr: true},
		{name: "unspecified accepts a legacy recorder", selection: coremetering.ObservationWriterUnspecified, in: observationWriterInput{ExplicitRecorder: true}},
		{name: "unspecified accepts no declaration", selection: coremetering.ObservationWriterUnspecified, in: observationWriterInput{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := coremetering.ValidateObservationWriterSelection(tc.selection, tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("selection %v with %+v must be refused", tc.selection, tc.in)
				}
				if !errors.Is(err, coremetering.ErrObservationWriterConflict) {
					t.Fatalf("err = %v, want the typed observation-writer conflict", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("selection %v with %+v: %v", tc.selection, tc.in, err)
			}
		})
	}
}

// TestBuildHostRejectsLegacySelectionWithExternalPairBeforeAnyAcquisition
// proves the carried legacy selection conflicts with a complete external
// recorder/sink pair before the process acquires anything: the conflict is
// reported even though the configuration path does not exist, so no opener, no
// recovery pass and no worker start can have run.
//
// This is the retained-surface replacement for the removed stock economic-work
// builder leg: the same pre-acquisition conflict class is exercised through the
// carried writer selection and the borrowed pair only.
func TestBuildHostRejectsLegacySelectionWithExternalPairBeforeAnyAcquisition(t *testing.T) {
	t.Parallel()
	recorder := newCountingRecorderFor("journal-borrowed")
	sink := newBlockingObservationSink()
	_, err := BuildHost(t.Context(), BuildHostInput{
		ConfigPath:      filepath.Join(t.TempDir(), "missing-config.yaml"),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: observationWriterHandlerComposer,
		Production: ProductionOptions{
			MeteringRecorder:                   recorder,
			MeteringObservationSink:            sink,
			MeteringObservationWriterSelection: coremetering.ObservationWriterLegacy,
		},
	})
	if err == nil {
		t.Fatal("the carried legacy selection with an external pair must be refused")
	}
	if !errors.Is(err, coremetering.ErrObservationWriterConflict) {
		t.Fatalf("err = %v, want the typed observation-writer conflict", err)
	}
	if recorder.closes() != 0 {
		t.Fatalf("borrowed recorder closed %d times before the preflight refusal", recorder.closes())
	}
	if sink.count() != 0 {
		t.Fatalf("borrowed sink received %d appends before the preflight refusal", sink.count())
	}
}

// TestObservationProducerFenceDrainsAcceptedAppendsAndRetries proves the
// accepted-producer fence: an accepted append completes only after the
// borrowed sink returns, a drain timeout leaves the admission closed and the
// same wait retryable, no append is admitted after the cutoff, and the drain
// completes exactly once.
func TestObservationProducerFenceDrainsAcceptedAppendsAndRetries(t *testing.T) {
	t.Parallel()
	sink := newBlockingObservationSink()
	fence := coremetering.NewObservationProducerFence(sink)
	if fence == nil {
		t.Fatal("the fence must wrap a usable borrowed sink")
	}
	appended := make(chan error, 1)
	go func() {
		appended <- fence.Append(t.Context(), metering.Observation{})
	}()
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the borrowed append never reached the barrier")
	}
	// The drain observes the accepted append as in flight and reports the
	// caller's deadline instead of closing anything.
	timeoutCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := fence.Drain(timeoutCtx); err == nil {
		t.Fatal("a drain with an accepted append must not report success before it completes")
	}
	// The admission stays closed after the timeout: a new append is refused.
	if err := fence.Append(t.Context(), metering.Observation{}); !errors.Is(err, coremetering.ErrObservationProducerDraining) {
		t.Fatalf("append after the drain cutoff = %v, want the closed-admission class", err)
	}
	// Releasing the barrier completes the accepted append; the same drain
	// latch then completes and stays completed.
	close(sink.release)
	select {
	case err := <-appended:
		if err != nil {
			t.Fatalf("accepted append: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the accepted append never completed")
	}
	if err := fence.Drain(t.Context()); err != nil {
		t.Fatalf("retry drain: %v", err)
	}
	if err := fence.Drain(t.Context()); err != nil {
		t.Fatalf("repeated drain: %v", err)
	}
	if sink.count() != 1 {
		t.Fatalf("borrowed sink completed %d appends, want exactly the accepted one", sink.count())
	}
}

// TestHostCloseDrainsObservationProducersBeforeProcessClosers proves the host
// close order: a drain that cannot complete keeps the process open and its
// dependencies unclosed, and a later close completes the same shutdown.
func TestHostCloseDrainsObservationProducersBeforeProcessClosers(t *testing.T) {
	t.Parallel()
	recorder := newCountingRecorderFor("journal-borrowed")
	sink := newBlockingObservationSink()
	host, err := BuildHost(t.Context(), BuildHostInput{
		ConfigPath:      writeObservationWriterConfig(t),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: observationWriterHandlerComposer,
		Production: ProductionOptions{
			MeteringRecorder:                   recorder,
			MeteringObservationSink:            sink,
			MeteringObservationWriterSelection: coremetering.ObservationWriterExternal,
		},
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	fence, ok := host.process.meteringRT.ObservationSink.(coremetering.ObservationProducerFence)
	if !ok {
		t.Fatal("the process must expose the fenced observation sink")
	}
	appended := make(chan error, 1)
	go func() {
		appended <- fence.Append(t.Context(), metering.Observation{})
	}()
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the borrowed append never reached the barrier")
	}
	timeoutCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := host.Close(timeoutCtx); err == nil {
		t.Fatal("a close that cannot drain the accepted append must fail")
	}
	if host.process.Closed() {
		t.Fatal("the process must stay open while an accepted append is in flight")
	}
	close(sink.release)
	select {
	case err := <-appended:
		if err != nil {
			t.Fatalf("accepted append: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the accepted append never completed")
	}
	if err := host.Close(t.Context()); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	if !host.process.Closed() {
		t.Fatal("the retry close must complete the process shutdown")
	}
	// The borrowed pair stays owned by its owner: the runtime never closes it.
	if recorder.closes() != 0 {
		t.Fatalf("the runtime closed the borrowed recorder %d times", recorder.closes())
	}
}

// TestProcessServicesCloseDrainsObservationProducers proves the direct
// process cleanup path cannot bypass the producer fence: a drain that cannot
// complete keeps every process closer untouched and the close retryable.
func TestProcessServicesCloseDrainsObservationProducers(t *testing.T) {
	t.Parallel()
	sink := newBlockingObservationSink()
	ps := &ProcessServices{observationFence: coremetering.NewObservationProducerFence(sink)}
	appended := make(chan error, 1)
	go func() {
		appended <- ps.observationFence.Append(t.Context(), metering.Observation{})
	}()
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the borrowed append never reached the barrier")
	}
	closed := make(chan error, 1)
	go func() { closed <- ps.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("close returned %v while an accepted append was in flight", err)
	case <-time.After(200 * time.Millisecond):
	}
	if ps.Closed() {
		t.Fatal("the process must stay open while an accepted append is in flight")
	}
	close(sink.release)
	select {
	case err := <-appended:
		if err != nil {
			t.Fatalf("accepted append: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the accepted append never completed")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close after the drain completed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close never completed after the drain finished")
	}
	if !ps.Closed() {
		t.Fatal("the process must be closed after the drain completed")
	}
}

// TestBuildHostRejectsMismatchedObservationWriterPairBeforeAnyAcquisition
// proves the pair is one journal, not two individually valid ports: a recorder
// and a sink that belong to different durable journals are refused before the
// process acquires anything, so no opener, no recovery pass and no worker
// start can have run.
func TestBuildHostRejectsMismatchedObservationWriterPairBeforeAnyAcquisition(t *testing.T) {
	t.Parallel()
	recorder := newCountingRecorderFor("journal-recorder")
	sink := newBlockingObservationSinkFor("journal-sink")
	_, err := BuildHost(t.Context(), BuildHostInput{
		ConfigPath:      filepath.Join(t.TempDir(), "missing-config.yaml"),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: observationWriterHandlerComposer,
		Production: ProductionOptions{
			MeteringRecorder:                   recorder,
			MeteringObservationSink:            sink,
			MeteringObservationWriterSelection: coremetering.ObservationWriterExternal,
		},
	})
	if err == nil {
		t.Fatal("a recorder and a sink over two different journals must be refused")
	}
	if !errors.Is(err, coremetering.ErrObservationWriterConflict) {
		t.Fatalf("err = %v, want the typed observation-writer conflict", err)
	}
	if recorder.closes() != 0 {
		t.Fatalf("borrowed recorder closed %d times before the preflight refusal", recorder.closes())
	}
	if sink.count() != 0 {
		t.Fatalf("borrowed sink received %d appends before the preflight refusal", sink.count())
	}
}

// TestBuildHostAcceptsOneJournalBehindBothWriterPorts proves the pair proof is
// about the journal, not about the object identity: a recorder and a sink
// wrapped over the same durable journal are accepted.
func TestBuildHostAcceptsOneJournalBehindBothWriterPorts(t *testing.T) {
	t.Parallel()
	recorder := newCountingRecorderFor("journal-one")
	sink := newBlockingObservationSinkFor("journal-one")
	host, err := BuildHost(t.Context(), BuildHostInput{
		ConfigPath:      writeObservationWriterConfig(t),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: observationWriterHandlerComposer,
		Production: ProductionOptions{
			MeteringRecorder:                   recorder,
			MeteringObservationSink:            sink,
			MeteringObservationWriterSelection: coremetering.ObservationWriterExternal,
		},
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	if err := host.Close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestHostCloseDrainsAcceptedObservationBatchBeforeProcessClosers proves the
// accepted-producer fence covers the atomic batch, not only the single
// append: a batch held at the barrier keeps the process and its dependencies
// open, the caller's deadline is reported, and the released batch lets the
// retry close join and complete exactly once.
func TestHostCloseDrainsAcceptedObservationBatchBeforeProcessClosers(t *testing.T) {
	t.Parallel()
	recorder := newCountingRecorderFor("journal-borrowed")
	sink := newBlockingObservationSink()
	host, err := BuildHost(t.Context(), BuildHostInput{
		ConfigPath:      writeObservationWriterConfig(t),
		Mandatory:       aipsdk.StandardDistributionRequirements(),
		LogWriter:       io.Discard,
		HandlerComposer: observationWriterHandlerComposer,
		Production: ProductionOptions{
			MeteringRecorder:                   recorder,
			MeteringObservationSink:            sink,
			MeteringObservationWriterSelection: coremetering.ObservationWriterExternal,
		},
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	fence, ok := host.process.meteringRT.ObservationSink.(coremetering.ObservationProducerFence)
	if !ok {
		t.Fatal("the process must expose the fenced observation sink")
	}
	batched := make(chan error, 1)
	go func() {
		batched <- fence.AppendObservations(t.Context(), []metering.Observation{{}, {}})
	}()
	select {
	case <-sink.batch:
	case <-time.After(5 * time.Second):
		t.Fatal("the borrowed batch never reached the barrier")
	}
	timeoutCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := host.Close(timeoutCtx); err == nil {
		t.Fatal("a close that cannot drain the accepted batch must fail")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's context error", err)
	}
	if host.process.Closed() {
		t.Fatal("the process must stay open while an accepted batch is in flight")
	}
	if recorder.closes() != 0 {
		t.Fatalf("the borrowed recorder was closed %d times before the drain completed", recorder.closes())
	}
	close(sink.release)
	select {
	case err := <-batched:
		if err != nil {
			t.Fatalf("accepted batch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the accepted batch never completed")
	}
	if err := host.Close(t.Context()); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	if !host.process.Closed() {
		t.Fatal("the retry close must complete the process shutdown")
	}
	if sink.batchCount() != 1 {
		t.Fatalf("the borrowed sink received %d batches, want exactly the accepted one", sink.batchCount())
	}
	// The admission is closed after the shutdown: a late producer is refused
	// instead of reaching a store the process already released.
	if err := fence.Append(t.Context(), metering.Observation{}); !errors.Is(err, coremetering.ErrObservationProducerDraining) {
		t.Fatalf("append after the close = %v, want the closed-admission class", err)
	}
	if recorder.closes() != 0 {
		t.Fatalf("the runtime closed the borrowed recorder %d times", recorder.closes())
	}
}

// TestObservationProducerFenceConcurrentDrainCompletesOnce proves concurrent
// and repeated drains observe the same completion: every waiter completes, the
// accepted append completes exactly once, and the drain never reopens the
// admission.
func TestObservationProducerFenceConcurrentDrainCompletesOnce(t *testing.T) {
	t.Parallel()
	sink := newBlockingObservationSink()
	fence := coremetering.NewObservationProducerFence(sink)
	appended := make(chan error, 1)
	go func() {
		appended <- fence.Append(t.Context(), metering.Observation{})
	}()
	select {
	case <-sink.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the borrowed append never reached the barrier")
	}
	const waiters = 4
	drained := make(chan error, waiters)
	for range waiters {
		go func() { drained <- fence.Drain(t.Context()) }()
	}
	timeoutCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := fence.Drain(timeoutCtx); err == nil {
		t.Fatal("a drain with an accepted append must not report success before it completes")
	}
	close(sink.release)
	select {
	case err := <-appended:
		if err != nil {
			t.Fatalf("accepted append: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the accepted append never completed")
	}
	for range waiters {
		select {
		case err := <-drained:
			if err != nil {
				t.Fatalf("concurrent drain: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a concurrent drain never completed")
		}
	}
	if err := fence.Drain(t.Context()); err != nil {
		t.Fatalf("repeated drain: %v", err)
	}
	if sink.count() != 1 {
		t.Fatalf("borrowed sink completed %d appends, want exactly the accepted one", sink.count())
	}
	if err := fence.Append(t.Context(), metering.Observation{}); !errors.Is(err, coremetering.ErrObservationProducerDraining) {
		t.Fatalf("append after the drain cutoff = %v, want the closed-admission class", err)
	}
}

// TestBorrowedObservationBatchRollsBackWithoutAPrefix proves the atomic batch
// contract on the real durable journal: a batch whose second observation is
// invalid persists no prefix and no marker, the exact replay of the accepted
// batch is an idempotent no-op that invents no revision, and the journal keeps
// the original revision.
func TestBorrowedObservationBatchRollsBackWithoutAPrefix(t *testing.T) {
	t.Parallel()
	journal := newBridgeMeteringStore(t, "bridge-store")
	sink := journalstore.NewObservationSinkWithOutbox(journal)
	atomicSink, ok := sink.(metering.AtomicObservationSink)
	if !ok {
		t.Fatal("the durable journal must expose the atomic observation sink")
	}
	valid := bridgeRuntimeObservation("batch", 1)
	invalid := bridgeRuntimeObservation("batch", 2)
	invalid.Subject.BLegID = ""
	if err := atomicSink.AppendObservations(t.Context(), []metering.Observation{valid, invalid}); err == nil {
		t.Fatal("a batch with an invalid observation must be refused")
	}
	pending, err := journal.ListPendingObservationOutbox(t.Context(), 64)
	if err != nil {
		t.Fatalf("read the observation outbox: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("a refused batch persisted %d outbox items, want none", len(pending))
	}
	if err := atomicSink.AppendObservations(t.Context(), []metering.Observation{valid}); err != nil {
		t.Fatalf("accepted batch: %v", err)
	}
	pending, err = journal.ListPendingObservationOutbox(t.Context(), 64)
	if err != nil {
		t.Fatalf("read the observation outbox: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("the accepted batch persisted %d outbox items, want exactly one", len(pending))
	}
	if pending[0].Observation.Revision != valid.Revision {
		t.Fatalf("persisted revision = %d, want the original %d", pending[0].Observation.Revision, valid.Revision)
	}
	// The exact replay is idempotent and invents no new revision.
	if err := atomicSink.AppendObservations(t.Context(), []metering.Observation{valid}); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	pending, err = journal.ListPendingObservationOutbox(t.Context(), 64)
	if err != nil {
		t.Fatalf("read the observation outbox: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("the exact replay persisted %d outbox items, want exactly one", len(pending))
	}
	if pending[0].Observation.Revision != valid.Revision {
		t.Fatalf("replayed revision = %d, want the original %d", pending[0].Observation.Revision, valid.Revision)
	}
}

// newBridgeMeteringStore opens a private durable metering journal for the
// neutral atomic-batch proof.
func newBridgeMeteringStore(t *testing.T, storeID string) *journalstore.DurableStore {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "metering.sqlite")) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{StoreID: storeID, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// bridgeRuntimeObservation builds one valid provider observation for the
// atomic-batch proof.
func bridgeRuntimeObservation(id string, revision uint64) metering.Observation {
	now := time.Unix(1_700_001_000+int64(revision), 0).UTC()
	key := metering.ComponentKey{Direction: metering.DirectionOutput, Component: "vendor:tokens", Unit: metering.UnitToken, SchemaID: "bridge:v1"}
	value := metering.Decimal{Coefficient: "2", Scale: 0}
	amount := metering.Decimal{Coefficient: "3", Scale: 0}
	return metering.Observation{
		Version: 2, ID: "bridge-" + id, SourceEventKey: "bridge-source-" + id, Revision: revision,
		StreamID: "bridge-stream", Sequence: revision, Origin: metering.OriginProvider,
		Acquisition: metering.AcquisitionProviderResponse, Authority: metering.AuthorityObservedClaim,
		Perspective: metering.PerspectiveOperator, Boundary: metering.BoundaryBackendIngress, Lifecycle: metering.LifecycleBackendAttempt,
		Subject:     metering.SubjectRef{Kind: metering.SubjectBLeg, StoreID: "bridge-store", TenantID: "tenant", AccountID: "account", ALegID: "a", BillingCallID: "call", BLegID: "b", AttemptID: "attempt", ProviderAccountKey: "provider"},
		Correlation: metering.CorrelationV2{StoreID: "bridge-store", TenantID: "tenant", CallID: "call", BillingCallID: "call", ALegID: "a", BLegID: "b", AttemptID: "attempt", ProviderAccountKey: "provider"},
		Semantics:   metering.SemanticsDelta, ObservedAt: now, ReceivedAt: now, MappingRef: "bridge:v1",
		Measures: []metering.Measure{{Key: key, Value: &value, Quality: metering.QualityObserved}},
		Charges:  []metering.ReportedCharge{{ChargeItemID: "charge-" + id, Component: &key, Amount: &amount, Currency: "USD", Kind: metering.ChargeKindComponent}},
	}
}
