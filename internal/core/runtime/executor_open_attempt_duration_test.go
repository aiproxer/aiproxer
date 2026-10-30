package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// slowAttemptTraceStore delays only the secure-session attempt-trace write, so
// a test can observe whether provider open-latency sampling happens before it.
type slowAttemptTraceStore struct {
	app.Store
	delay  time.Duration
	writes atomic.Int32
}

func (s *slowAttemptTraceStore) AppendAttemptTrace(ctx context.Context, trace domain.AttemptTrace) error {
	s.writes.Add(1)
	time.Sleep(s.delay)
	return s.Store.AppendAttemptTrace(ctx, trace)
}

// openDurationRecordingSink captures the provider open-duration metric.
type openDurationRecordingSink struct {
	mu        sync.Mutex
	durations []float64
}

func (s *openDurationRecordingSink) OnAttemptRecorded(aipapi.AttemptOutcome, string) {}
func (s *openDurationRecordingSink) OnBackendOpenDuration(_ string, seconds float64) {
	s.mu.Lock()
	s.durations = append(s.durations, seconds)
	s.mu.Unlock()
}

func (s *openDurationRecordingSink) OnTransportNegotiation(aipapi.Operation, aipapi.TransportMode, string) {
}

func (s *openDurationRecordingSink) OnCancellation(CancellationObservation) {}

func (s *openDurationRecordingSink) first() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.durations) == 0 {
		return -1
	}
	return s.durations[0]
}

// TestOpenAttempt_BackendOpenDurationExcludesSecureSessionPersistence pins the
// ordering the pre-persistence snapshot exists for: the provider open-duration
// metric is sampled before the secure-session attempt-trace write, so slow
// audit storage can never inflate it. The store blocks for a fixed delay; the
// reported duration must stay far below it while the slow write still runs.
func TestOpenAttempt_BackendOpenDurationExcludesSecureSessionPersistence(t *testing.T) {
	t.Parallel()
	const storeDelay = 500 * time.Millisecond

	rig := newBindPanicRig(t)
	// The shared fixture pins a synthetic clock; open-duration sampling is a
	// wall-clock claim, so this test observes the real one.
	rig.ex.Now = time.Now
	slow := &slowAttemptTraceStore{Store: memory.New(memory.Options{SimulateDurable: true}), delay: storeDelay}
	rig.ex.SecureSession = testSecureManager(t, slow, rig.ex.Store)
	sink := &openDurationRecordingSink{}
	rig.ex.Metrics = sink
	rig.ex.Backends = map[string]execbackend.Backend{
		"good": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return makeStreamWithEvents(aipapi.Event{Kind: aipapi.EventResponseFinished}), nil
			},
		},
	}

	ctx := execview.WithPrincipal(context.Background(), execview.PrincipalView{ID: "usr-open-duration"})
	stream, err := rig.ex.Execute(ctx, canonicalPanicCall("call-open-duration", "good:m"))
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	require.Equal(t, int32(1), slow.writes.Load(), "the attempt trace write must run for the opened attempt")
	got := sink.first()
	require.Greater(t, got, 0.0, "provider open duration must be recorded")
	require.Less(t, got, storeDelay.Seconds()/2, "provider open duration must exclude secure-session persistence")
}
