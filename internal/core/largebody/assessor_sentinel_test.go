package largebody_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// =============================================================================
// Task 11.2: Side-Effect Sentinels and Duration Measurement Under Held Permit
//
// Spec Requirements:
// - Requirement 6: Two-Phase Assessment and One-Way Wire Commit.
//   6.1: Retain byte-weighted DecodeAdmission permit through protocol proof
//        and side-effect-free core assessment.
//   6.2: AssessLargeBody shall perform no BeginTurn, A-leg creation/fetch,
//        DB/store read or mutation, billing reservation, provider/network I/O,
//        client-body wait, spill I/O, or arbitrary unbounded plugin work.
//   6.3: If profile proof or core assessment declines, existing canonical
//        Spec.Decode executes under same permit; no release/reacquire cycle.
//   6.8: Additional permit hold introduced by assessment shall be measured
//        and bounded; assessment may iterate only generation-bounded data
//        structures and pure backend declarations.
// - Requirement 21: Evidence-Based Performance and Practical Eligibility.
//   21.4: Verify decode permits are never held while waiting for client upload;
//         measure the additional bounded hold across side-effect-free assessment.
// - Requirement 22: Conservative Rollout, Diagnostics, API Boundary.
//   22.4: Architecture tests prevent provider-name switches in core, fake Calls,
//         and protocol proof outside decode admission.
// =============================================================================

// SentinelSurface identifies which side-effect boundary was violated.
type SentinelSurface string

const (
	SurfaceBeginTurn          SentinelSurface = "BeginTurn"
	SurfaceALeg               SentinelSurface = "A-leg"
	SurfaceDBStore            SentinelSurface = "DB/store/route-override read"
	SurfaceBillingReservation SentinelSurface = "billing/accounting reservation"
	SurfaceProviderNetwork    SentinelSurface = "provider/network"
	SurfaceReplayBytes        SentinelSurface = "replay bytes"
	SurfaceClientWait         SentinelSurface = "client wait"
	SurfaceUnboundedCallback  SentinelSurface = "unbounded callback"
)

// SentinelViolationError records an attempted side effect during assessment.
type SentinelViolationError struct {
	Surface SentinelSurface
	Message string
}

func (e SentinelViolationError) Error() string {
	return fmt.Sprintf("largebody sentinel violation on %s: %s", e.Surface, e.Message)
}

// -----------------------------------------------------------------------------
// 1. BeginTurnSentinel
// -----------------------------------------------------------------------------

// BeginTurnSentinel is a fail-closed test double for secure-session BeginTurn.
// Assessment must never touch BeginTurn (Requirement 6.2).
type BeginTurnSentinel struct {
	touched atomic.Int64
}

func (s *BeginTurnSentinel) Touch(msg string) {
	s.touched.Add(1)
	panic(SentinelViolationError{Surface: SurfaceBeginTurn, Message: msg})
}

func (s *BeginTurnSentinel) BeginTurn(_ context.Context, _ any) (any, error) {
	s.Touch("assessment must not touch BeginTurn")
	return nil, errors.New("unreachable")
}

func (s *BeginTurnSentinel) Touched() bool { return s.touched.Load() > 0 }
func (s *BeginTurnSentinel) Count() int64  { return s.touched.Load() }
func (s *BeginTurnSentinel) Reset()        { s.touched.Store(0) }

// -----------------------------------------------------------------------------
// 2. ALegSentinel
// -----------------------------------------------------------------------------

// ALegSentinel is a fail-closed test double for A-leg creation and fetch.
// Assessment must never touch A-leg lineage (Requirement 6.2).
type ALegSentinel struct {
	touched atomic.Int64
}

func (s *ALegSentinel) Touch(msg string) {
	s.touched.Add(1)
	panic(SentinelViolationError{Surface: SurfaceALeg, Message: msg})
}

func (s *ALegSentinel) CreateALeg(_ context.Context, _ string) (any, error) {
	s.Touch("assessment must not touch A-leg creation")
	return nil, errors.New("unreachable")
}

func (s *ALegSentinel) FetchALeg(_ context.Context, _ string) (any, error) {
	s.Touch("assessment must not touch A-leg fetch")
	return nil, errors.New("unreachable")
}

func (s *ALegSentinel) Touched() bool { return s.touched.Load() > 0 }
func (s *ALegSentinel) Count() int64  { return s.touched.Load() }
func (s *ALegSentinel) Reset()        { s.touched.Store(0) }

// -----------------------------------------------------------------------------
// 3. DBStoreSentinel
// -----------------------------------------------------------------------------

// DBStoreSentinel is a fail-closed test double for DB/store and route-override reads.
// Assessment derives route envelopes without reading the live store (Requirements 6.2, 7.2).
type DBStoreSentinel struct {
	touched atomic.Int64
}

func (s *DBStoreSentinel) Touch(msg string) {
	s.touched.Add(1)
	panic(SentinelViolationError{Surface: SurfaceDBStore, Message: msg})
}

func (s *DBStoreSentinel) Snapshot(_ context.Context, _ string) (any, error) {
	s.Touch("assessment must not read route-override snapshot from live store")
	return nil, errors.New("unreachable")
}

func (s *DBStoreSentinel) Replace(_ context.Context, _ string, _ any) (any, error) {
	s.Touch("assessment must not mutate route-override store")
	return nil, errors.New("unreachable")
}

func (s *DBStoreSentinel) Clear(_ context.Context, _ string) (any, error) {
	s.Touch("assessment must not clear route-override store")
	return nil, errors.New("unreachable")
}

func (s *DBStoreSentinel) Query(_ context.Context, _ any) (any, error) {
	s.Touch("assessment must not query continuity database")
	return nil, errors.New("unreachable")
}

func (s *DBStoreSentinel) Touched() bool { return s.touched.Load() > 0 }
func (s *DBStoreSentinel) Count() int64  { return s.touched.Load() }
func (s *DBStoreSentinel) Reset()        { s.touched.Store(0) }

// -----------------------------------------------------------------------------
// 4. BillingReservationSentinel
// -----------------------------------------------------------------------------

// BillingReservationSentinel is a fail-closed double for billing/accounting reservations.
// Assessment performs no billing quota reservation or exposure admission (Requirement 6.2).
type BillingReservationSentinel struct {
	touched atomic.Int64
}

func (s *BillingReservationSentinel) Touch(msg string) {
	s.touched.Add(1)
	panic(SentinelViolationError{Surface: SurfaceBillingReservation, Message: msg})
}

func (s *BillingReservationSentinel) Reserve(_ context.Context, _ any) (any, error) {
	s.Touch("assessment must not reserve billing quota")
	return nil, errors.New("unreachable")
}

func (s *BillingReservationSentinel) Admit(_ context.Context, _ any) (any, error) {
	s.Touch("assessment must not admit billing exposure")
	return nil, errors.New("unreachable")
}

func (s *BillingReservationSentinel) Hold(_ context.Context, _ any) (any, error) {
	s.Touch("assessment must not hold billing exposure")
	return nil, errors.New("unreachable")
}

func (s *BillingReservationSentinel) WriteJournal(_ context.Context, _ any) error {
	s.Touch("assessment must not write billing journal")
	return errors.New("unreachable")
}

func (s *BillingReservationSentinel) Touched() bool { return s.touched.Load() > 0 }
func (s *BillingReservationSentinel) Count() int64  { return s.touched.Load() }
func (s *BillingReservationSentinel) Reset()        { s.touched.Store(0) }

// -----------------------------------------------------------------------------
// 5. ProviderNetworkSentinel
// -----------------------------------------------------------------------------

// ProviderNetworkSentinel is a fail-closed double for provider/network connections.
// Assessment opens no provider network connections (Requirement 6.2).
type ProviderNetworkSentinel struct {
	touched atomic.Int64
}

func (s *ProviderNetworkSentinel) Touch(msg string) {
	s.touched.Add(1)
	panic(SentinelViolationError{Surface: SurfaceProviderNetwork, Message: msg})
}

func (s *ProviderNetworkSentinel) Open(_ context.Context, _ any) (any, error) {
	s.Touch("assessment must not open provider connection")
	return nil, errors.New("unreachable")
}

func (s *ProviderNetworkSentinel) Dial(_ context.Context, _, _ string) (any, error) {
	s.Touch("assessment must not dial network socket")
	return nil, errors.New("unreachable")
}

func (s *ProviderNetworkSentinel) RoundTrip(_ context.Context, _ any) (any, error) {
	s.Touch("assessment must not execute network round-trip")
	return nil, errors.New("unreachable")
}

func (s *ProviderNetworkSentinel) ExecuteAttempt(_ context.Context, _ any) (any, error) {
	s.Touch("assessment must not execute backend attempt")
	return nil, errors.New("unreachable")
}

func (s *ProviderNetworkSentinel) Touched() bool { return s.touched.Load() > 0 }
func (s *ProviderNetworkSentinel) Count() int64  { return s.touched.Load() }
func (s *ProviderNetworkSentinel) Reset()        { s.touched.Store(0) }

// -----------------------------------------------------------------------------
// 6. ReplayBytesSentinel
// -----------------------------------------------------------------------------

// ReplayBytesSentinel is a fail-closed double implementing largebody.Source and io.Reader.
// Assessment inspects bounded facts only, never reads replay/spill bytes (Requirement 6.2).
type ReplayBytesSentinel struct {
	touched atomic.Int64
	size    int64
}

func (s *ReplayBytesSentinel) Touch(msg string) {
	s.touched.Add(1)
	panic(SentinelViolationError{Surface: SurfaceReplayBytes, Message: msg})
}

// Size returns bounded metadata (safe).
func (s *ReplayBytesSentinel) Size() int64 { return s.size }

// Open fails closed: assessment must not open replay bytes.
func (s *ReplayBytesSentinel) Open() (io.ReadCloser, error) {
	s.Touch("assessment must not open replay bytes stream")
	return nil, errors.New("unreachable")
}

// Close is a no-op source cleanup.
func (s *ReplayBytesSentinel) Close() error { return nil }

// Read fails closed if treated as a raw stream.
func (s *ReplayBytesSentinel) Read(_ []byte) (int, error) {
	s.Touch("assessment must not read replay bytes")
	return 0, errors.New("unreachable")
}

func (s *ReplayBytesSentinel) Touched() bool { return s.touched.Load() > 0 }
func (s *ReplayBytesSentinel) Count() int64  { return s.touched.Load() }
func (s *ReplayBytesSentinel) Reset()        { s.touched.Store(0) }

var (
	_ largebody.Source = (*ReplayBytesSentinel)(nil)
	_ io.Reader        = (*ReplayBytesSentinel)(nil)
)

// -----------------------------------------------------------------------------
// 7. ClientWaitSentinel
// -----------------------------------------------------------------------------

// ClientWaitSentinel is a fail-closed double implementing io.Reader.
// Assessment must never wait on client body upload (Requirements 6.2, 21.4).
type ClientWaitSentinel struct {
	touched atomic.Int64
}

func (s *ClientWaitSentinel) Touch(msg string) {
	s.touched.Add(1)
	panic(SentinelViolationError{Surface: SurfaceClientWait, Message: msg})
}

func (s *ClientWaitSentinel) Read(_ []byte) (int, error) {
	s.Touch("assessment must not wait on client body read")
	return 0, errors.New("unreachable")
}

func (s *ClientWaitSentinel) Touched() bool { return s.touched.Load() > 0 }
func (s *ClientWaitSentinel) Count() int64  { return s.touched.Load() }
func (s *ClientWaitSentinel) Reset()        { s.touched.Store(0) }

var _ io.Reader = (*ClientWaitSentinel)(nil)

// -----------------------------------------------------------------------------
// 8. UnboundedCallbackSentinel
// -----------------------------------------------------------------------------

// UnboundedCallbackSentinel is a fail-closed double for arbitrary callbacks.
// Assessment must never execute unbounded plugin callbacks (Requirement 6.2).
type UnboundedCallbackSentinel struct {
	touched atomic.Int64
}

func (s *UnboundedCallbackSentinel) Touch(msg string) {
	s.touched.Add(1)
	panic(SentinelViolationError{Surface: SurfaceUnboundedCallback, Message: msg})
}

func (s *UnboundedCallbackSentinel) Invoke(_ ...any) (any, error) {
	s.Touch("assessment must not invoke unbounded callback")
	return nil, errors.New("unreachable")
}

func (s *UnboundedCallbackSentinel) Callback() func(...any) (any, error) {
	return s.Invoke
}

func (s *UnboundedCallbackSentinel) Touched() bool { return s.touched.Load() > 0 }
func (s *UnboundedCallbackSentinel) Count() int64  { return s.touched.Load() }
func (s *UnboundedCallbackSentinel) Reset()        { s.touched.Store(0) }

// -----------------------------------------------------------------------------
// SentinelDecodeAdmission: Decode Permit Tracking Double
// -----------------------------------------------------------------------------

// SentinelDecodeAdmission tracks decode permit acquisition, hold state, and release.
// It verifies that assessment executes under a held permit and that no second
// admission decision is made (Requirements 6.1, 6.3, 6.8, 21.4).
type SentinelDecodeAdmission struct {
	isHeld        atomic.Bool
	tryAdmitCount atomic.Int64
	releaseCount  atomic.Int64
	admittedBytes atomic.Int64
}

func (d *SentinelDecodeAdmission) TryAcquire(_ context.Context, bytes int64) (func(), bool, error) {
	// Requirement 6.3: at most one admission decision per considered request.
	if d.isHeld.Load() {
		return nil, false, errors.New("sentinel violation: second decode admission decision attempted while permit held")
	}
	d.tryAdmitCount.Add(1)
	d.admittedBytes.Store(bytes)
	d.isHeld.Store(true)

	releaseOnce := new(atomic.Bool)
	release := func() {
		if releaseOnce.CompareAndSwap(false, true) {
			d.isHeld.Store(false)
			d.releaseCount.Add(1)
		}
	}
	return release, true, nil
}

func (d *SentinelDecodeAdmission) TryAdmit(ctx context.Context, bytes int64) (func(), bool, error) {
	return d.TryAcquire(ctx, bytes)
}

func (d *SentinelDecodeAdmission) IsHeld() bool         { return d.isHeld.Load() }
func (d *SentinelDecodeAdmission) TryAdmitCount() int64 { return d.tryAdmitCount.Load() }
func (d *SentinelDecodeAdmission) ReleaseCount() int64  { return d.releaseCount.Load() }
func (d *SentinelDecodeAdmission) AdmittedBytes() int64 { return d.admittedBytes.Load() }
func (d *SentinelDecodeAdmission) ForceRelease() {
	d.isHeld.Store(false)
	d.releaseCount.Add(1)
}

var _ aipsdk.DecodeAdmission = (*SentinelDecodeAdmission)(nil)

// -----------------------------------------------------------------------------
// SentinelHarness
// -----------------------------------------------------------------------------

// AssessmentMeasurement captures performance and hold evidence for an assessment.
type AssessmentMeasurement struct {
	Duration      time.Duration
	PermitHeld    bool
	AdmittedBytes int64
	Bounded       bool
}

// SentinelHarness aggregates all 8 side-effect sentinels and a decode-permit tracker.
type SentinelHarness struct {
	BeginTurn          *BeginTurnSentinel
	ALeg               *ALegSentinel
	DBStore            *DBStoreSentinel
	BillingReservation *BillingReservationSentinel
	ProviderNetwork    *ProviderNetworkSentinel
	ReplayBytes        *ReplayBytesSentinel
	ClientWait         *ClientWaitSentinel
	UnboundedCallback  *UnboundedCallbackSentinel
	DecodeAdmission    *SentinelDecodeAdmission
	MaxAllowedDuration time.Duration
}

// DefaultMaxAssessmentDuration is the maximum permitted assessment duration
// under held decode permit (250ms; bounded facts only, Requirement 6.8).
const DefaultMaxAssessmentDuration = 250 * time.Millisecond

// NewSentinelHarness creates a new SentinelHarness with all 8 sentinels armed.
func NewSentinelHarness() *SentinelHarness {
	return &SentinelHarness{
		BeginTurn:          &BeginTurnSentinel{},
		ALeg:               &ALegSentinel{},
		DBStore:            &DBStoreSentinel{},
		BillingReservation: &BillingReservationSentinel{},
		ProviderNetwork:    &ProviderNetworkSentinel{},
		ReplayBytes:        &ReplayBytesSentinel{size: 1 << 20},
		ClientWait:         &ClientWaitSentinel{},
		UnboundedCallback:  &UnboundedCallbackSentinel{},
		DecodeAdmission:    &SentinelDecodeAdmission{},
		MaxAllowedDuration: DefaultMaxAssessmentDuration,
	}
}

// AnyTouched reports whether any of the 8 sentinels has been touched.
func (h *SentinelHarness) AnyTouched() bool {
	return h.BeginTurn.Touched() ||
		h.ALeg.Touched() ||
		h.DBStore.Touched() ||
		h.BillingReservation.Touched() ||
		h.ProviderNetwork.Touched() ||
		h.ReplayBytes.Touched() ||
		h.ClientWait.Touched() ||
		h.UnboundedCallback.Touched()
}

// Violations returns all triggered sentinel violations.
func (h *SentinelHarness) Violations() []SentinelViolationError {
	var list []SentinelViolationError
	if h.BeginTurn.Touched() {
		list = append(list, SentinelViolationError{Surface: SurfaceBeginTurn, Message: "BeginTurn was touched"})
	}
	if h.ALeg.Touched() {
		list = append(list, SentinelViolationError{Surface: SurfaceALeg, Message: "A-leg was touched"})
	}
	if h.DBStore.Touched() {
		list = append(list, SentinelViolationError{Surface: SurfaceDBStore, Message: "DB/store was touched"})
	}
	if h.BillingReservation.Touched() {
		list = append(list, SentinelViolationError{Surface: SurfaceBillingReservation, Message: "Billing reservation was touched"})
	}
	if h.ProviderNetwork.Touched() {
		list = append(list, SentinelViolationError{Surface: SurfaceProviderNetwork, Message: "Provider/network was touched"})
	}
	if h.ReplayBytes.Touched() {
		list = append(list, SentinelViolationError{Surface: SurfaceReplayBytes, Message: "Replay bytes were touched"})
	}
	if h.ClientWait.Touched() {
		list = append(list, SentinelViolationError{Surface: SurfaceClientWait, Message: "Client wait was touched"})
	}
	if h.UnboundedCallback.Touched() {
		list = append(list, SentinelViolationError{Surface: SurfaceUnboundedCallback, Message: "Unbounded callback was touched"})
	}
	return list
}

// AssertUntouched verifies that no sentinel was triggered.
func (h *SentinelHarness) AssertUntouched(tb testing.TB) {
	tb.Helper()
	if h.AnyTouched() {
		violations := h.Violations()
		for _, v := range violations {
			tb.Errorf("sentinel violation: %s (%s)", v.Surface, v.Message)
		}
		tb.Fatalf("assessment touched %d side-effecting surfaces; must remain side-effect-free (Requirement 6.2)", len(violations))
	}
}

// Reset clears touch counters across all sentinels.
func (h *SentinelHarness) Reset() {
	h.BeginTurn.Reset()
	h.ALeg.Reset()
	h.DBStore.Reset()
	h.BillingReservation.Reset()
	h.ProviderNetwork.Reset()
	h.ReplayBytes.Reset()
	h.ClientWait.Reset()
	h.UnboundedCallback.Reset()
}

// RunAssessment runs AssessLargeBody safely, catching any SentinelViolationError panic.
func (h *SentinelHarness) RunAssessment(
	ctx context.Context,
	assessor largebody.LargeBodyAssessor,
	proof largebody.Proof,
) (result largebody.Assessment, violation *SentinelViolationError, err error) {
	defer func() {
		if r := recover(); r != nil {
			if v, ok := r.(SentinelViolationError); ok {
				violation = &v
			} else {
				panic(r)
			}
		}
	}()

	result, err = assessor.AssessLargeBody(ctx, proof)
	return result, violation, err
}

// MeasureAssessment runs AssessLargeBody under the held decode permit and
// measures the assessment duration (Requirements 6.1, 6.8, 21.4).
func (h *SentinelHarness) MeasureAssessment(
	ctx context.Context,
	assessor largebody.LargeBodyAssessor,
	proof largebody.Proof,
	bodyBytes int64,
) (largebody.Assessment, AssessmentMeasurement, error) {
	release, admitted, err := h.DecodeAdmission.TryAdmit(ctx, bodyBytes)
	if err != nil {
		return largebody.Assessment{}, AssessmentMeasurement{}, fmt.Errorf("decode admission failed: %w", err)
	}
	if !admitted {
		return largebody.Assessment{}, AssessmentMeasurement{}, errors.New("decode admission denied")
	}
	defer release()

	if !h.DecodeAdmission.IsHeld() {
		return largebody.Assessment{}, AssessmentMeasurement{}, errors.New("decode permit not held before assessment")
	}

	start := time.Now()
	assessment, violation, assessErr := h.RunAssessment(ctx, assessor, proof)
	elapsed := time.Since(start)

	if violation != nil {
		return largebody.Assessment{}, AssessmentMeasurement{}, violation
	}

	permitHeldAtCompletion := h.DecodeAdmission.IsHeld()
	bounded := elapsed <= h.MaxAllowedDuration

	meas := AssessmentMeasurement{
		Duration:      elapsed,
		PermitHeld:    permitHeldAtCompletion,
		AdmittedBytes: bodyBytes,
		Bounded:       bounded,
	}

	if !permitHeldAtCompletion {
		return assessment, meas, errors.New("sentinel violation: decode permit was released prematurely during assessment (Requirement 6.1)")
	}

	return assessment, meas, assessErr
}

// =============================================================================
// Tests for Task 11.2
// =============================================================================

func TestTask11_2_SentinelHarness_AllSentinelsArmed(t *testing.T) {
	t.Parallel()
	harness := NewSentinelHarness()
	if harness.BeginTurn == nil {
		t.Fatal("BeginTurnSentinel must not be nil")
	}
	if harness.ALeg == nil {
		t.Fatal("ALegSentinel must not be nil")
	}
	if harness.DBStore == nil {
		t.Fatal("DBStoreSentinel must not be nil")
	}
	if harness.BillingReservation == nil {
		t.Fatal("BillingReservationSentinel must not be nil")
	}
	if harness.ProviderNetwork == nil {
		t.Fatal("ProviderNetworkSentinel must not be nil")
	}
	if harness.ReplayBytes == nil {
		t.Fatal("ReplayBytesSentinel must not be nil")
	}
	if harness.ClientWait == nil {
		t.Fatal("ClientWaitSentinel must not be nil")
	}
	if harness.UnboundedCallback == nil {
		t.Fatal("UnboundedCallbackSentinel must not be nil")
	}
	if harness.DecodeAdmission == nil {
		t.Fatal("SentinelDecodeAdmission must not be nil")
	}
	if harness.AnyTouched() {
		t.Fatal("sentinels must not be touched initially")
	}
	harness.AssertUntouched(t)
}

// -----------------------------------------------------------------------------
// Sentinel Panic Tests for All 8 Surfaces
// -----------------------------------------------------------------------------

type violatingAssessor struct {
	action func()
}

func (a *violatingAssessor) AssessLargeBody(_ context.Context, _ largebody.Proof) (largebody.Assessment, error) {
	if a.action != nil {
		a.action()
	}
	return largebody.Assessment{}, nil
}

func TestTask11_2_Sentinel_BeginTurn_Panics(t *testing.T) {
	t.Parallel()
	harness := NewSentinelHarness()
	assessor := &violatingAssessor{
		action: func() {
			_, _ = harness.BeginTurn.BeginTurn(context.Background(), nil)
		},
	}

	_, violation, _ := harness.RunAssessment(context.Background(), assessor, validProof())
	if violation == nil {
		t.Fatal("expected BeginTurn sentinel violation, got nil")
	}
	if violation.Surface != SurfaceBeginTurn {
		t.Fatalf("violation surface = %v, want %v", violation.Surface, SurfaceBeginTurn)
	}
	if !harness.BeginTurn.Touched() {
		t.Fatal("BeginTurn.Touched() must report true")
	}
	if len(harness.Violations()) != 1 {
		t.Fatalf("violations count = %d, want 1", len(harness.Violations()))
	}
}

func TestTask11_2_Sentinel_ALeg_Panics(t *testing.T) {
	t.Parallel()
	t.Run("CreateALeg", func(t *testing.T) {
		t.Parallel()
		harness := NewSentinelHarness()
		assessor := &violatingAssessor{
			action: func() {
				_, _ = harness.ALeg.CreateALeg(context.Background(), "idemp-key")
			},
		}

		_, violation, _ := harness.RunAssessment(context.Background(), assessor, validProof())
		if violation == nil {
			t.Fatal("expected ALeg sentinel violation on CreateALeg")
		}
		if violation.Surface != SurfaceALeg {
			t.Fatalf("violation surface = %v, want %v", violation.Surface, SurfaceALeg)
		}
		if !harness.ALeg.Touched() {
			t.Fatal("ALeg.Touched() must report true")
		}
	})

	t.Run("FetchALeg", func(t *testing.T) {
		t.Parallel()
		harness := NewSentinelHarness()
		assessor := &violatingAssessor{
			action: func() {
				_, _ = harness.ALeg.FetchALeg(context.Background(), "aleg-123")
			},
		}

		_, violation, _ := harness.RunAssessment(context.Background(), assessor, validProof())
		if violation == nil {
			t.Fatal("expected ALeg sentinel violation on FetchALeg")
		}
		if violation.Surface != SurfaceALeg {
			t.Fatalf("violation surface = %v, want %v", violation.Surface, SurfaceALeg)
		}
	})
}

func TestTask11_2_Sentinel_DBStore_Panics(t *testing.T) {
	t.Parallel()
	for name, fn := range map[string]func(*DBStoreSentinel){
		"Snapshot": func(s *DBStoreSentinel) { _, _ = s.Snapshot(context.Background(), "aleg-1") },
		"Replace":  func(s *DBStoreSentinel) { _, _ = s.Replace(context.Background(), "aleg-1", nil) },
		"Clear":    func(s *DBStoreSentinel) { _, _ = s.Clear(context.Background(), "aleg-1") },
		"Query":    func(s *DBStoreSentinel) { _, _ = s.Query(context.Background(), nil) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			harness := NewSentinelHarness()
			assessor := &violatingAssessor{action: func() { fn(harness.DBStore) }}
			_, violation, _ := harness.RunAssessment(context.Background(), assessor, validProof())
			if violation == nil {
				t.Fatalf("expected DBStore sentinel violation on %s", name)
			}
			if violation.Surface != SurfaceDBStore {
				t.Fatalf("violation surface = %v, want %v", violation.Surface, SurfaceDBStore)
			}
			if !harness.DBStore.Touched() {
				t.Fatal("DBStore.Touched() must report true")
			}
		})
	}
}

func TestTask11_2_Sentinel_BillingReservation_Panics(t *testing.T) {
	t.Parallel()
	for name, fn := range map[string]func(*BillingReservationSentinel){
		"Reserve":      func(s *BillingReservationSentinel) { _, _ = s.Reserve(context.Background(), nil) },
		"Admit":        func(s *BillingReservationSentinel) { _, _ = s.Admit(context.Background(), nil) },
		"Hold":         func(s *BillingReservationSentinel) { _, _ = s.Hold(context.Background(), nil) },
		"WriteJournal": func(s *BillingReservationSentinel) { _ = s.WriteJournal(context.Background(), nil) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			harness := NewSentinelHarness()
			assessor := &violatingAssessor{action: func() { fn(harness.BillingReservation) }}
			_, violation, _ := harness.RunAssessment(context.Background(), assessor, validProof())
			if violation == nil {
				t.Fatalf("expected BillingReservation sentinel violation on %s", name)
			}
			if violation.Surface != SurfaceBillingReservation {
				t.Fatalf("violation surface = %v, want %v", violation.Surface, SurfaceBillingReservation)
			}
			if !harness.BillingReservation.Touched() {
				t.Fatal("BillingReservation.Touched() must report true")
			}
		})
	}
}

func TestTask11_2_Sentinel_ProviderNetwork_Panics(t *testing.T) {
	t.Parallel()
	for name, fn := range map[string]func(*ProviderNetworkSentinel){
		"Open":           func(s *ProviderNetworkSentinel) { _, _ = s.Open(context.Background(), nil) },
		"Dial":           func(s *ProviderNetworkSentinel) { _, _ = s.Dial(context.Background(), "tcp", "127.0.0.1:80") },
		"RoundTrip":      func(s *ProviderNetworkSentinel) { _, _ = s.RoundTrip(context.Background(), nil) },
		"ExecuteAttempt": func(s *ProviderNetworkSentinel) { _, _ = s.ExecuteAttempt(context.Background(), nil) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			harness := NewSentinelHarness()
			assessor := &violatingAssessor{action: func() { fn(harness.ProviderNetwork) }}
			_, violation, _ := harness.RunAssessment(context.Background(), assessor, validProof())
			if violation == nil {
				t.Fatalf("expected ProviderNetwork sentinel violation on %s", name)
			}
			if violation.Surface != SurfaceProviderNetwork {
				t.Fatalf("violation surface = %v, want %v", violation.Surface, SurfaceProviderNetwork)
			}
			if !harness.ProviderNetwork.Touched() {
				t.Fatal("ProviderNetwork.Touched() must report true")
			}
		})
	}
}

func TestTask11_2_Sentinel_ReplayBytes_Panics(t *testing.T) {
	t.Parallel()
	t.Run("OpenReplayBytes", func(t *testing.T) {
		t.Parallel()
		harness := NewSentinelHarness()
		assessor := &violatingAssessor{
			action: func() {
				_, _ = harness.ReplayBytes.Open()
			},
		}

		_, violation, _ := harness.RunAssessment(context.Background(), assessor, validProof())
		if violation == nil {
			t.Fatal("expected ReplayBytes sentinel violation on Open")
		}
		if violation.Surface != SurfaceReplayBytes {
			t.Fatalf("violation surface = %v, want %v", violation.Surface, SurfaceReplayBytes)
		}
		if !harness.ReplayBytes.Touched() {
			t.Fatal("ReplayBytes.Touched() must report true")
		}
	})

	t.Run("ReadReplayBytes", func(t *testing.T) {
		t.Parallel()
		harness := NewSentinelHarness()
		assessor := &violatingAssessor{
			action: func() {
				buf := make([]byte, 16)
				_, _ = harness.ReplayBytes.Read(buf)
			},
		}

		_, violation, _ := harness.RunAssessment(context.Background(), assessor, validProof())
		if violation == nil {
			t.Fatal("expected ReplayBytes sentinel violation on Read")
		}
		if violation.Surface != SurfaceReplayBytes {
			t.Fatalf("violation surface = %v, want %v", violation.Surface, SurfaceReplayBytes)
		}
	})

	t.Run("SizeIsSafeMetadata", func(t *testing.T) {
		t.Parallel()
		harness := NewSentinelHarness()
		if harness.ReplayBytes.Size() <= 0 {
			t.Fatalf("expected positive size, got %d", harness.ReplayBytes.Size())
		}
		// Size check must NOT trigger sentinel
		if harness.ReplayBytes.Touched() {
			t.Fatal("Size() metadata query must not trigger ReplayBytes sentinel")
		}
	})
}

func TestTask11_2_Sentinel_ClientWait_Panics(t *testing.T) {
	t.Parallel()
	harness := NewSentinelHarness()
	assessor := &violatingAssessor{
		action: func() {
			buf := make([]byte, 16)
			_, _ = harness.ClientWait.Read(buf)
		},
	}

	_, violation, _ := harness.RunAssessment(context.Background(), assessor, validProof())
	if violation == nil {
		t.Fatal("expected ClientWait sentinel violation on Read")
	}
	if violation.Surface != SurfaceClientWait {
		t.Fatalf("violation surface = %v, want %v", violation.Surface, SurfaceClientWait)
	}
	if !harness.ClientWait.Touched() {
		t.Fatal("ClientWait.Touched() must report true")
	}
}

func TestTask11_2_Sentinel_UnboundedCallback_Panics(t *testing.T) {
	t.Parallel()
	t.Run("Invoke", func(t *testing.T) {
		t.Parallel()
		harness := NewSentinelHarness()
		assessor := &violatingAssessor{
			action: func() {
				_, _ = harness.UnboundedCallback.Invoke("arg1", 42)
			},
		}

		_, violation, _ := harness.RunAssessment(context.Background(), assessor, validProof())
		if violation == nil {
			t.Fatal("expected UnboundedCallback sentinel violation on Invoke")
		}
		if violation.Surface != SurfaceUnboundedCallback {
			t.Fatalf("violation surface = %v, want %v", violation.Surface, SurfaceUnboundedCallback)
		}
		if !harness.UnboundedCallback.Touched() {
			t.Fatal("UnboundedCallback.Touched() must report true")
		}
	})

	t.Run("CallbackFunc", func(t *testing.T) {
		t.Parallel()
		harness := NewSentinelHarness()
		cb := harness.UnboundedCallback.Callback()
		assessor := &violatingAssessor{
			action: func() {
				_, _ = cb("hook-event")
			},
		}

		_, violation, _ := harness.RunAssessment(context.Background(), assessor, validProof())
		if violation == nil {
			t.Fatal("expected UnboundedCallback sentinel violation on Callback func")
		}
		if violation.Surface != SurfaceUnboundedCallback {
			t.Fatalf("violation surface = %v, want %v", violation.Surface, SurfaceUnboundedCallback)
		}
	})
}

// -----------------------------------------------------------------------------
// Pure Assessment: Touches Only Bounded Facts
// -----------------------------------------------------------------------------

// pureFactAssessor inspects only bounded facts in Proof and frozen catalog data.
// It touches NO sentinels.
type pureFactAssessor struct {
	declineReason largebody.DeclineReason
	acceptStamp   largebody.AssessmentStamp
	acceptWireReq largebody.WireRequestFacts
	acceptDomain  largebody.WireDomainFacts
}

func (a *pureFactAssessor) AssessLargeBody(_ context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	// Inspect bounded facts only (Requirements 6.2, 6.8):
	if proof.ProfileID == "" {
		return largebody.NewDeclinedAssessment(largebody.DeclineReasonProofUncertain)
	}
	if proof.Operation != aipapi.OperationOpenAIResponses && proof.Operation != aipapi.OperationOpenAIChatCompletions {
		return largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
	}
	if a.declineReason != largebody.DeclineReasonNone {
		return largebody.NewDeclinedAssessment(a.declineReason)
	}
	return largebody.NewAcceptedAssessment(a.acceptStamp, a.acceptWireReq, a.acceptDomain)
}

func TestTask11_2_PureAssessment_TouchesOnlyBoundedFacts(t *testing.T) {
	t.Parallel()
	harness := NewSentinelHarness()

	pure := &pureFactAssessor{
		declineReason: largebody.DeclineReasonAuthorityBlocker,
	}

	proof := validProof()
	assessment, violation, err := harness.RunAssessment(context.Background(), pure, proof)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if violation != nil {
		t.Fatalf("unexpected sentinel violation: %v", violation)
	}
	if !assessment.Declined() {
		t.Fatal("expected declined assessment")
	}
	if assessment.Reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("assessment.Reason = %v, want %v", assessment.Reason, largebody.DeclineReasonAuthorityBlocker)
	}

	// Invariant: ALL 8 sentinels must remain completely untouched (Requirement 6.2)
	harness.AssertUntouched(t)
}

// -----------------------------------------------------------------------------
// Duration Measurement Under Held Decode Permit Tests
// -----------------------------------------------------------------------------

func TestTask11_2_MeasureAssessment_UnderHeldPermit(t *testing.T) {
	t.Parallel()
	harness := NewSentinelHarness()

	pure := &pureFactAssessor{
		declineReason: largebody.DeclineReasonAuthorityBlocker,
	}

	proof := validProof()
	const requestBytes int64 = 1024 * 1024 // 1 MiB

	assessment, meas, err := harness.MeasureAssessment(context.Background(), pure, proof, requestBytes)
	if err != nil {
		t.Fatalf("MeasureAssessment failed: %v", err)
	}

	// Requirement 6.1, 6.8: permit was held across assessment
	if !meas.PermitHeld {
		t.Fatal("meas.PermitHeld must report true; permit must be retained across assessment")
	}
	if meas.AdmittedBytes != requestBytes {
		t.Fatalf("meas.AdmittedBytes = %d, want %d", meas.AdmittedBytes, requestBytes)
	}
	// Duration must be positive (non-zero time elapsed) and bounded
	if meas.Duration < 0 {
		t.Fatalf("meas.Duration = %v; must be non-negative", meas.Duration)
	}
	if !meas.Bounded {
		t.Fatalf("meas.Bounded = false; assessment duration %v exceeded ceiling %v", meas.Duration, harness.MaxAllowedDuration)
	}
	if assessment.Reason != largebody.DeclineReasonAuthorityBlocker {
		t.Fatalf("assessment.Reason = %v, want %v", assessment.Reason, largebody.DeclineReasonAuthorityBlocker)
	}

	// Permit must be released after measurement completes
	if harness.DecodeAdmission.IsHeld() {
		t.Fatal("decode permit must be released after MeasureAssessment completes")
	}
	if harness.DecodeAdmission.ReleaseCount() != 1 {
		t.Fatalf("releaseCount = %d, want 1", harness.DecodeAdmission.ReleaseCount())
	}

	// All side-effect sentinels must remain untouched
	harness.AssertUntouched(t)
}

func TestTask11_2_MeasureAssessment_DetectsPrematurePermitRelease(t *testing.T) {
	t.Parallel()
	harness := NewSentinelHarness()

	// Assessor that improperly forces early permit release during assessment
	badAssessor := &violatingAssessor{
		action: func() {
			harness.DecodeAdmission.ForceRelease()
		},
	}

	proof := validProof()
	_, meas, err := harness.MeasureAssessment(context.Background(), badAssessor, proof, 512)
	if err == nil {
		t.Fatal("expected error when permit is prematurely released during assessment")
	}
	if meas.PermitHeld {
		t.Fatal("meas.PermitHeld must be false when permit was prematurely released")
	}
}

func TestTask11_2_DecodeAdmission_SingleDecisionEnforcement(t *testing.T) {
	t.Parallel()
	adm := &SentinelDecodeAdmission{}
	ctx := context.Background()

	release1, ok, err := adm.TryAdmit(ctx, 1024)
	if err != nil || !ok {
		t.Fatalf("first TryAdmit failed: ok=%v, err=%v", ok, err)
	}
	if !adm.IsHeld() {
		t.Fatal("permit must be held after first TryAdmit")
	}

	// Requirement 6.3: second admission decision while permit held is rejected
	_, ok2, err2 := adm.TryAdmit(ctx, 1024)
	if ok2 || err2 == nil {
		t.Fatal("second TryAdmit while permit held must fail (Requirement 6.3 single-admission invariant)")
	}

	// Release permit
	release1()
	if adm.IsHeld() {
		t.Fatal("permit must not be held after release")
	}
	if adm.ReleaseCount() != 1 {
		t.Fatalf("releaseCount = %d, want 1", adm.ReleaseCount())
	}

	// After release, a subsequent decision is permitted (e.g. for a subsequent request)
	release2, ok3, err3 := adm.TryAdmit(ctx, 2048)
	if err3 != nil || !ok3 {
		t.Fatalf("TryAdmit after release failed: ok=%v, err=%v", ok3, err3)
	}
	release2()
	if adm.ReleaseCount() != 2 {
		t.Fatalf("releaseCount = %d, want 2", adm.ReleaseCount())
	}
}

func TestTask11_2_DurationBound_EnforcesCeiling(t *testing.T) {
	t.Parallel()
	harness := NewSentinelHarness()
	// Set an artificially tight ceiling to verify bound enforcement
	harness.MaxAllowedDuration = 10 * time.Microsecond

	slowAssessor := &violatingAssessor{
		action: func() {
			time.Sleep(5 * time.Millisecond)
		},
	}

	proof := validProof()
	_, meas, err := harness.MeasureAssessment(context.Background(), slowAssessor, proof, 512)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meas.Bounded {
		t.Fatalf("meas.Bounded must be false when duration (%v) exceeds ceiling (%v)", meas.Duration, harness.MaxAllowedDuration)
	}
}
