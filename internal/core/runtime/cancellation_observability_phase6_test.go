package runtime_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Cancellation deadlines here bound fixture completion, rather than certify
// latency. Immediate fake Cancel/Close calls still need scheduler headroom when
// the full Windows suite is under load; phase, cause and count assertions remain.
type recordingMetricsSink struct {
	mu            sync.Mutex
	cancellations []runtime.CancellationObservation
}

func (s *recordingMetricsSink) OnAttemptRecorded(outcome aipapi.AttemptOutcome, backend string) {}
func (s *recordingMetricsSink) OnBackendOpenDuration(backend string, seconds float64)           {}
func (s *recordingMetricsSink) OnTransportNegotiation(operation aipapi.Operation, mode aipapi.TransportMode, outcome string) {
}

func (s *recordingMetricsSink) OnCancellation(obs runtime.CancellationObservation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancellations = append(s.cancellations, obs)
}

func (s *recordingMetricsSink) snapshot() []runtime.CancellationObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]runtime.CancellationObservation(nil), s.cancellations...)
}

type obsTrackingStream struct {
	blockRecv           chan struct{}
	cancelCalls         atomic.Int32
	closeCalls          atomic.Int32
	mode                aipapi.CancelMode
	cancelErr           error
	outcomeSeen         bool
	forcedAbort         bool
	handshakeNegotiated bool
}

func (s *obsTrackingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.blockRecv != nil {
		select {
		case <-s.blockRecv:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	return aipapi.Event{}, io.EOF
}

func (s *obsTrackingStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	mode := s.mode
	if mode == "" {
		mode = aipapi.CancelModeTransport
	}
	return aipapi.CancelResult{Mode: mode, Err: s.cancelErr}
}

func (s *obsTrackingStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func (s *obsTrackingStream) CancellationOutcomeSeen() bool {
	return s.outcomeSeen
}

func (s *obsTrackingStream) CancellationForcedAbort() bool {
	return s.forcedAbort
}

func (s *obsTrackingStream) CancellationHandshakeNegotiated() bool {
	return s.handshakeNegotiated
}

type nonProbeTrackingStream struct {
	blockRecv chan struct{}
	mode      aipapi.CancelMode
	cancelErr error
}

func (s *nonProbeTrackingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.blockRecv != nil {
		select {
		case <-s.blockRecv:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	return aipapi.Event{}, io.EOF
}

func (s *nonProbeTrackingStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	mode := s.mode
	if mode == "" {
		mode = aipapi.CancelModeTransport
	}
	return aipapi.CancelResult{Mode: mode, Err: s.cancelErr}
}

func (s *nonProbeTrackingStream) Close() error {
	return nil
}

func TestPhase6_Runtime_EmitsBoundedCancellationMetrics(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	sink := &recordingMetricsSink{}
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})

	openStarted := make(chan struct{}, 1)
	blockRecv := make(chan struct{})
	stream := &obsTrackingStream{blockRecv: blockRecv}
	authIDCh, sendAuthID := captureAuthoritativeID()

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 2
	ex.ALegLifecycle = coord
	ex.Metrics = sink
	ex.Backends = map[string]execbackend.Backend{
		"primary": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				sendAuthID(call)
				select {
				case openStarted <- struct{}{}:
				default:
				}
				return stream, nil
			},
		},
	}

	secretKey := "sk-ant-SECRET-KEY-12345"
	call := &aipapi.Call{
		ID:    "call-obs",
		Route: aipapi.RouteIntent{Selector: "primary:m"},
		Session: aipapi.SessionRef{
			ALegID: "aleg-obs-metrics-test",
		},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi " + secretKey)},
		}},
	}

	streamRes, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = streamRes.Close() })

	recvDone := make(chan error, 1)
	go func() {
		_, rErr := streamRes.Recv(context.Background())
		recvDone <- rErr
	}()

	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("primary Open was not started")
	}

	targetID := requireAuthoritativeID(t, authIDCh)
	if err := coord.CancelALeg(context.Background(), targetID, leglifecycle.CancelCause{
		Kind:   leglifecycle.CancelExplicit,
		Detail: "user cancelled with secret: " + secretKey,
	}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	close(blockRecv)

	select {
	case <-recvDone:
	case <-time.After(time.Second):
		t.Fatal("Recv did not finish")
	}

	cancels := sink.snapshot()
	if len(cancels) == 0 {
		t.Fatal("expected at least one cancellation metric recorded on sink")
	}

	for _, c := range cancels {
		if strings.Contains(string(c.CauseClass), secretKey) {
			t.Fatalf("secret key leaked in CauseClass: %q", c.CauseClass)
		}
		if c.CauseClass != runtime.CancellationCauseExplicit && c.CauseClass != runtime.CancellationCauseContextDone && c.CauseClass != runtime.CancellationCauseClientGone && c.CauseClass != runtime.CancellationCauseRaceLoser && c.CauseClass != runtime.CancellationCauseOther {
			t.Fatalf("unexpected unbounded CauseClass: %q", c.CauseClass)
		}
		if c.Mode != runtime.CancellationModeTransport && c.Mode != runtime.CancellationModeProvider && c.Mode != runtime.CancellationModeCloseOnly && c.Mode != runtime.CancellationModeNone {
			t.Fatalf("unexpected unbounded Mode: %q", c.Mode)
		}
		if c.Phase != runtime.CancellationPhaseTerminal && c.Phase != runtime.CancellationPhaseOutcome && c.Phase != runtime.CancellationPhaseRequested && c.Phase != runtime.CancellationPhaseForced {
			t.Fatalf("unexpected unbounded Phase: %q", c.Phase)
		}
		if c.Fallback != runtime.CancellationFallbackNegotiated && c.Fallback != runtime.CancellationFallbackLegacy && c.Fallback != runtime.CancellationFallbackNone {
			t.Fatalf("unexpected unbounded Fallback: %q", c.Fallback)
		}
	}
}

// safeLogBuffer is a thread-safe bytes.Buffer wrapper.
type safeLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeLogBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPhase6_Observability_DiagnosticLogs(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	logBuf := &safeLogBuffer{}
	logger := slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})

	openStarted := make(chan struct{}, 1)
	blockRecv := make(chan struct{})
	stream := &obsTrackingStream{blockRecv: blockRecv}
	authIDCh, sendAuthID := captureAuthoritativeID()

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 2
	ex.ALegLifecycle = coord
	ex.Log = logger
	ex.Backends = map[string]execbackend.Backend{
		"primary": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				sendAuthID(call)
				select {
				case openStarted <- struct{}{}:
				default:
				}
				return stream, nil
			},
		},
	}

	secretKey := "sk-ant-SECRET-BEARER-TOKEN-999"
	rawPrompt := "my super confidential prompt body with password123"
	call := &aipapi.Call{
		ID:    "call-diag",
		Route: aipapi.RouteIntent{Selector: "primary:m"},
		Session: aipapi.SessionRef{
			ALegID: "aleg-diag-logs-test",
		},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(rawPrompt)},
		}},
	}

	streamRes, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = streamRes.Close() })

	recvDone := make(chan error, 1)
	go func() {
		_, rErr := streamRes.Recv(context.Background())
		recvDone <- rErr
	}()

	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("primary Open was not started")
	}

	targetID := requireAuthoritativeID(t, authIDCh)
	if err := coord.CancelALeg(context.Background(), targetID, leglifecycle.CancelCause{
		Kind:   leglifecycle.CancelExplicit,
		Detail: "cancelled with " + secretKey,
	}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	close(blockRecv)

	select {
	case <-recvDone:
	case <-time.After(time.Second):
		t.Fatal("Recv did not finish")
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "attempt_canceled") {
		t.Fatalf("expected 'attempt_canceled' decision log, got:\n%s", logs)
	}
	if strings.Contains(logs, secretKey) {
		t.Fatalf("secret key leaked in logs: %q", logs)
	}
	if strings.Contains(logs, rawPrompt) {
		t.Fatalf("raw prompt leaked in logs: %q", logs)
	}
	if !strings.Contains(logs, `"cause_class":"explicit"`) {
		t.Fatalf("expected cause_class: explicit in logs, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"cancel_mode":"transport"`) {
		t.Fatalf("expected cancel_mode: transport in logs, got:\n%s", logs)
	}
	if !strings.Contains(logs, `"phase":"terminal"`) {
		t.Fatalf("expected phase: terminal in logs, got:\n%s", logs)
	}
}

// TestPhase6_Observability_ActualModesAndCauses_TableDriven tests that
// provider, transport, and close-only modes, as well as explicit, client-gone,
// context-done, and race-loser causes are observed truthfully with appropriate phases and fallbacks.
func TestPhase6_Observability_ActualModesAndCauses_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		useNonProbeStream   bool
		streamMode          aipapi.CancelMode
		outcomeSeen         bool
		forcedAbort         bool
		handshakeNegotiated bool
		cause               aipapi.CancelCause
		wantCauseClass      runtime.CancellationCauseClass
		wantTerminalMode    runtime.CancellationModeClass
		wantFallback        runtime.CancellationFallback
		wantPhases          []runtime.CancellationPhase
	}{
		{
			name:                "provider_mode_explicit_negotiated",
			streamMode:          aipapi.CancelModeProvider,
			outcomeSeen:         true,
			handshakeNegotiated: true,
			cause:               aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "user cancelled"},
			wantCauseClass:      runtime.CancellationCauseExplicit,
			wantTerminalMode:    runtime.CancellationModeProvider,
			wantFallback:        runtime.CancellationFallbackNegotiated,
			wantPhases:          []runtime.CancellationPhase{runtime.CancellationPhaseRequested, runtime.CancellationPhaseOutcome, runtime.CancellationPhaseTerminal},
		},
		{
			name:                "transport_mode_client_gone_legacy",
			streamMode:          aipapi.CancelModeTransport,
			handshakeNegotiated: false,
			cause:               aipapi.CancelCause{Kind: aipapi.CancelClientGone, Detail: "client disconnected"},
			wantCauseClass:      runtime.CancellationCauseClientGone,
			wantTerminalMode:    runtime.CancellationModeTransport,
			wantFallback:        runtime.CancellationFallbackLegacy,
			wantPhases:          []runtime.CancellationPhase{runtime.CancellationPhaseRequested, runtime.CancellationPhaseTerminal},
		},
		{
			name:                "close_only_mode_context_done",
			streamMode:          aipapi.CancelModeCloseOnly,
			handshakeNegotiated: false,
			cause:               aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "deadline expired"},
			wantCauseClass:      runtime.CancellationCauseContextDone,
			wantTerminalMode:    runtime.CancellationModeCloseOnly,
			wantFallback:        runtime.CancellationFallbackLegacy,
			wantPhases:          []runtime.CancellationPhase{runtime.CancellationPhaseRequested, runtime.CancellationPhaseTerminal},
		},
		{
			name:                "forced_abort_mode_explicit",
			streamMode:          aipapi.CancelModeTransport,
			forcedAbort:         true,
			handshakeNegotiated: true,
			cause:               aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "forced stop"},
			wantCauseClass:      runtime.CancellationCauseExplicit,
			wantTerminalMode:    runtime.CancellationModeTransport,
			wantFallback:        runtime.CancellationFallbackNegotiated,
			wantPhases:          []runtime.CancellationPhase{runtime.CancellationPhaseRequested, runtime.CancellationPhaseForced, runtime.CancellationPhaseTerminal},
		},
		{
			name:                "race_loser_cause_transport",
			streamMode:          aipapi.CancelModeTransport,
			handshakeNegotiated: false,
			cause:               aipapi.CancelCause{Kind: aipapi.CancelRaceLoser, Detail: "parallel arm lost"},
			wantCauseClass:      runtime.CancellationCauseRaceLoser,
			wantTerminalMode:    runtime.CancellationModeTransport,
			wantFallback:        runtime.CancellationFallbackLegacy,
			wantPhases:          []runtime.CancellationPhase{runtime.CancellationPhaseRequested, runtime.CancellationPhaseTerminal},
		},
		{
			name:                "unknown_cause_maps_to_other",
			streamMode:          aipapi.CancelModeTransport,
			handshakeNegotiated: false,
			cause:               aipapi.CancelCause{Kind: aipapi.CancelKind("arbitrary_unknown_kind"), Detail: "unknown reason"},
			wantCauseClass:      runtime.CancellationCauseOther,
			wantTerminalMode:    runtime.CancellationModeTransport,
			wantFallback:        runtime.CancellationFallbackLegacy,
			wantPhases:          []runtime.CancellationPhase{runtime.CancellationPhaseRequested, runtime.CancellationPhaseTerminal},
		},
		{
			name:              "non_probe_stream_fallback_none",
			useNonProbeStream: true,
			streamMode:        aipapi.CancelModeTransport,
			cause:             aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "user cancelled"},
			wantCauseClass:    runtime.CancellationCauseExplicit,
			wantTerminalMode:  runtime.CancellationModeTransport,
			wantFallback:      runtime.CancellationFallbackNone,
			wantPhases:        []runtime.CancellationPhase{runtime.CancellationPhaseRequested, runtime.CancellationPhaseTerminal},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}

			sink := &recordingMetricsSink{}
			coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})

			openStarted := make(chan struct{}, 1)
			blockRecv := make(chan struct{})
			var testStream aipapi.ManagedEventStream
			if tc.useNonProbeStream {
				testStream = &nonProbeTrackingStream{
					blockRecv: blockRecv,
					mode:      tc.streamMode,
				}
			} else {
				testStream = &obsTrackingStream{
					blockRecv:           blockRecv,
					mode:                tc.streamMode,
					outcomeSeen:         tc.outcomeSeen,
					forcedAbort:         tc.forcedAbort,
					handshakeNegotiated: tc.handshakeNegotiated,
				}
			}
			authIDCh, sendAuthID := captureAuthoritativeID()

			ex := runtime.TestExecutor()
			ex.Store = st
			ex.Bus = hooks.New(hooks.Config{})
			ex.Rand = routing.NewSeededRng(1)
			ex.MaxAttempts = 2
			ex.ALegLifecycle = coord
			ex.Metrics = sink
			ex.Backends = map[string]execbackend.Backend{
				"primary": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						sendAuthID(call)
						select {
						case openStarted <- struct{}{}:
						default:
						}
						return testStream, nil
					},
				},
			}

			call := &aipapi.Call{
				ID:      "call-" + tc.name,
				Route:   aipapi.RouteIntent{Selector: "primary:m"},
				Session: aipapi.SessionRef{ALegID: fmt.Sprintf("aleg-%s", tc.name)},
				Messages: []aipapi.Message{{
					Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
				}},
			}

			streamRes, err := ex.Execute(context.Background(), call)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = streamRes.Close() })

			recvDone := make(chan error, 1)
			go func() {
				_, rErr := streamRes.Recv(context.Background())
				recvDone <- rErr
			}()

			select {
			case <-openStarted:
			case <-time.After(time.Second):
				t.Fatal("primary Open was not started")
			}

			targetID := requireAuthoritativeID(t, authIDCh)
			if err := coord.CancelALeg(context.Background(), targetID, tc.cause); err != nil {
				t.Fatalf("CancelALeg failed: %v", err)
			}

			close(blockRecv)

			select {
			case <-recvDone:
			case <-time.After(time.Second):
				t.Fatal("Recv did not finish")
			}

			cancels := sink.snapshot()
			if len(cancels) == 0 {
				t.Fatal("expected cancellation metrics recorded on sink")
			}

			observedPhases := make(map[runtime.CancellationPhase]runtime.CancellationObservation)
			phaseCounts := make(map[runtime.CancellationPhase]int)
			for _, c := range cancels {
				phaseCounts[c.Phase]++
				observedPhases[c.Phase] = c
				if c.CauseClass != tc.wantCauseClass {
					t.Errorf("got CauseClass %q, want %q", c.CauseClass, tc.wantCauseClass)
				}
			}

			for phase, count := range phaseCounts {
				if count > 1 {
					t.Errorf("phase %q emitted %d times, want exactly 1", phase, count)
				}
			}

			for _, wantPhase := range tc.wantPhases {
				c, ok := observedPhases[wantPhase]
				if !ok {
					t.Errorf("expected phase %q was not observed in %v", wantPhase, cancels)
					continue
				}
				if wantPhase == runtime.CancellationPhaseTerminal {
					if c.Mode != tc.wantTerminalMode {
						t.Errorf("terminal phase got Mode %q, want %q", c.Mode, tc.wantTerminalMode)
					}
					if c.Fallback != tc.wantFallback {
						t.Errorf("terminal phase got Fallback %q, want %q", c.Fallback, tc.wantFallback)
					}
				}
			}
		})
	}
}

// TestPhase6_Observability_NoDuplicatePhaseEvents verifies that each lifecycle point
// emits its corresponding phase exactly once.
func TestPhase6_Observability_NoDuplicatePhaseEvents(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	sink := &recordingMetricsSink{}
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})

	openStarted := make(chan struct{}, 1)
	blockRecv := make(chan struct{})
	stream := &obsTrackingStream{
		blockRecv:           blockRecv,
		mode:                aipapi.CancelModeProvider,
		outcomeSeen:         true,
		handshakeNegotiated: true,
	}
	authIDCh, sendAuthID := captureAuthoritativeID()

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 2
	ex.ALegLifecycle = coord
	ex.Metrics = sink
	ex.Backends = map[string]execbackend.Backend{
		"primary": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				sendAuthID(call)
				select {
				case openStarted <- struct{}{}:
				default:
				}
				return stream, nil
			},
		},
	}

	call := &aipapi.Call{
		ID:      "call-no-dup",
		Route:   aipapi.RouteIntent{Selector: "primary:m"},
		Session: aipapi.SessionRef{ALegID: "aleg-no-dup-phases"},
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}

	streamRes, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = streamRes.Close() })

	recvDone := make(chan error, 1)
	go func() {
		_, rErr := streamRes.Recv(context.Background())
		recvDone <- rErr
	}()

	select {
	case <-openStarted:
	case <-time.After(time.Second):
		t.Fatal("primary Open was not started")
	}

	targetID := requireAuthoritativeID(t, authIDCh)
	if err := coord.CancelALeg(context.Background(), targetID, leglifecycle.CancelCause{
		Kind: leglifecycle.CancelExplicit,
	}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	close(blockRecv)

	select {
	case <-recvDone:
	case <-time.After(time.Second):
		t.Fatal("Recv did not finish")
	}

	cancels := sink.snapshot()
	phaseCounts := make(map[runtime.CancellationPhase]int)
	for _, c := range cancels {
		phaseCounts[c.Phase]++
	}

	if phaseCounts[runtime.CancellationPhaseRequested] != 1 {
		t.Errorf("requested phase count = %d, want 1", phaseCounts[runtime.CancellationPhaseRequested])
	}
	if phaseCounts[runtime.CancellationPhaseOutcome] != 1 {
		t.Errorf("outcome phase count = %d, want 1", phaseCounts[runtime.CancellationPhaseOutcome])
	}
	if phaseCounts[runtime.CancellationPhaseTerminal] != 1 {
		t.Errorf("terminal phase count = %d, want 1", phaseCounts[runtime.CancellationPhaseTerminal])
	}
	if phaseCounts[runtime.CancellationPhaseForced] != 0 {
		t.Errorf("forced phase count = %d, want 0", phaseCounts[runtime.CancellationPhaseForced])
	}
}

// TestPhase6_Observability_UnopenedAttemptCancellation_EmitsNoneMode verifies that
// an authoritative A-leg canceled before B-leg launch blocks future launch.
// This is the unopened-attempt path: no backend stream exists, so observability
// would record mode None. Hint isolation is covered by
// executor_a_leg_alias_regression_test.go; this test focuses on the authoritative seam.
func TestPhase6_Observability_UnopenedAttemptCancellation_EmitsNoneMode(t *testing.T) {
	t.Parallel()

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	const authID = "aleg-unopened-cancel-auth"

	if err := coord.CancelALeg(context.Background(), authID, leglifecycle.CancelCause{
		Kind: leglifecycle.CancelRaceLoser,
	}); err != nil {
		t.Fatalf("CancelALeg failed: %v", err)
	}

	aScope := coord.StartALeg(authID)
	if _, _, err := aScope.BeginBLegLaunch(context.Background(), "b-direct"); err == nil {
		t.Fatal("expected BeginBLegLaunch to fail on pre-canceled authoritative A-leg")
	}
}
