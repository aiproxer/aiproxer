package largebody_test

import (
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
)

func TestSpoolLedger_ConfigValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         largebody.SpoolBudgetConfig
		wantErr     bool
		errContains string
	}{
		{
			name: "valid configuration",
			cfg: largebody.SpoolBudgetConfig{
				MemorySpoolBytes:      64 * 1024,
				MaxInflightSpoolBytes: 256 * 1024 * 1024,
			},
			wantErr: false,
		},
		{
			name: "zero memory spool is valid (spill-only)",
			cfg: largebody.SpoolBudgetConfig{
				MemorySpoolBytes:      0,
				MaxInflightSpoolBytes: 10 * 1024 * 1024,
			},
			wantErr: false,
		},
		{
			name: "zero max inflight spool bytes is rejected",
			cfg: largebody.SpoolBudgetConfig{
				MemorySpoolBytes:      64 * 1024,
				MaxInflightSpoolBytes: 0,
			},
			wantErr:     true,
			errContains: "max_inflight_spool_bytes",
		},
		{
			name: "negative max inflight spool bytes is rejected",
			cfg: largebody.SpoolBudgetConfig{
				MemorySpoolBytes:      64 * 1024,
				MaxInflightSpoolBytes: -1,
			},
			wantErr:     true,
			errContains: "max_inflight_spool_bytes",
		},
		{
			name: "negative memory spool bytes is rejected",
			cfg: largebody.SpoolBudgetConfig{
				MemorySpoolBytes:      -1,
				MaxInflightSpoolBytes: 10 * 1024 * 1024,
			},
			wantErr:     true,
			errContains: "memory_spool_bytes",
		},
		{
			name: "memory spool bytes exceeding max inflight is rejected",
			cfg: largebody.SpoolBudgetConfig{
				MemorySpoolBytes:      20 * 1024 * 1024,
				MaxInflightSpoolBytes: 10 * 1024 * 1024,
			},
			wantErr:     true,
			errContains: "memory_spool_bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ledger, err := largebody.NewSpoolLedger(tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %s, got nil", tt.name)
				}
				if tt.errContains != "" && !errors.Is(err, largebody.ErrInvalidReservation) &&
					!containsSubstring(err.Error(), tt.errContains) {
					t.Fatalf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", tt.name, err)
			}
			if ledger == nil {
				t.Fatal("expected non-nil ledger")
			}
			if ledger.MaxInflightSpoolBytes() != tt.cfg.MaxInflightSpoolBytes {
				t.Errorf("got max inflight %d, want %d", ledger.MaxInflightSpoolBytes(), tt.cfg.MaxInflightSpoolBytes)
			}
			if ledger.MemorySpoolBytes() != tt.cfg.MemorySpoolBytes {
				t.Errorf("got memory spool %d, want %d", ledger.MemorySpoolBytes(), tt.cfg.MemorySpoolBytes)
			}
			if ledger.InflightBytes() != 0 {
				t.Errorf("initial inflight bytes must be 0, got %d", ledger.InflightBytes())
			}
			if ledger.AvailableBytes() != tt.cfg.MaxInflightSpoolBytes {
				t.Errorf("initial available bytes must equal max inflight %d, got %d", tt.cfg.MaxInflightSpoolBytes, ledger.AvailableBytes())
			}
			if ledger.ActiveReservations() != 0 {
				t.Errorf("initial active reservations must be 0, got %d", ledger.ActiveReservations())
			}
		})
	}
}

func TestSpoolLedger_EarlyReservationKnownLength(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	// 1. Reserve known identity length of 256 KiB
	res, err := ledger.Reserve(256 * 1024)
	if err != nil {
		t.Fatalf("Reserve(256 KiB) failed: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil reservation")
	}
	if res.ReservedBytes() != 256*1024 {
		t.Errorf("got reserved bytes %d, want %d", res.ReservedBytes(), 256*1024)
	}
	if ledger.InflightBytes() != 256*1024 {
		t.Errorf("got ledger inflight bytes %d, want %d", ledger.InflightBytes(), 256*1024)
	}
	if ledger.AvailableBytes() != (1024-256)*1024 {
		t.Errorf("got ledger available bytes %d, want %d", ledger.AvailableBytes(), (1024-256)*1024)
	}
	if ledger.ActiveReservations() != 1 {
		t.Errorf("got active reservations %d, want 1", ledger.ActiveReservations())
	}

	// 2. Reserve another 512 KiB
	res2, err := ledger.Reserve(512 * 1024)
	if err != nil {
		t.Fatalf("Reserve(512 KiB) failed: %v", err)
	}
	if ledger.InflightBytes() != (256+512)*1024 {
		t.Errorf("got ledger inflight bytes %d, want %d", ledger.InflightBytes(), (256+512)*1024)
	}
	if ledger.ActiveReservations() != 2 {
		t.Errorf("got active reservations %d, want 2", ledger.ActiveReservations())
	}

	// 3. Release first reservation
	freed := res.Release()
	if freed != 256*1024 {
		t.Errorf("got freed bytes %d, want %d", freed, 256*1024)
	}
	if res.ReservedBytes() != 0 {
		t.Errorf("after release, reserved bytes must be 0, got %d", res.ReservedBytes())
	}
	if ledger.InflightBytes() != 512*1024 {
		t.Errorf("got ledger inflight bytes %d, want %d", ledger.InflightBytes(), 512*1024)
	}
	if ledger.ActiveReservations() != 1 {
		t.Errorf("got active reservations %d, want 1", ledger.ActiveReservations())
	}

	// 4. Release second reservation
	freed2 := res2.Release()
	if freed2 != 512*1024 {
		t.Errorf("got freed2 bytes %d, want %d", freed2, 512*1024)
	}
	if ledger.InflightBytes() != 0 {
		t.Errorf("after all releases, inflight bytes must be 0, got %d", ledger.InflightBytes())
	}
	if ledger.ActiveReservations() != 0 {
		t.Errorf("after all releases, active reservations must be 0, got %d", ledger.ActiveReservations())
	}
}

func TestSpoolLedger_EarlyReservationExhaustion(t *testing.T) {
	t.Parallel()

	maxBudget := int64(512 * 1024)
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: maxBudget,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	// 1. Single reservation exceeding total budget
	res, err := ledger.Reserve(maxBudget + 1)
	if err == nil {
		t.Fatal("expected error when reserving beyond max budget, got nil")
	}
	if res != nil {
		t.Fatal("expected nil reservation on exhaustion")
	}
	if !largebody.IsSpoolBudgetExhausted(err) {
		t.Fatalf("expected ErrSpoolBudgetExhausted, got %v", err)
	}
	if !errors.Is(err, largebody.ErrSpoolBudgetExhausted) {
		t.Fatalf("expected errors.Is(err, ErrSpoolBudgetExhausted), got %v", err)
	}
	if ledger.InflightBytes() != 0 {
		t.Errorf("inflight bytes must remain 0 after declined reservation, got %d", ledger.InflightBytes())
	}
	if ledger.ActiveReservations() != 0 {
		t.Errorf("active reservations must remain 0, got %d", ledger.ActiveReservations())
	}

	// 2. Reserve up to capacity, then next reservation is declined
	res1, err := ledger.Reserve(maxBudget)
	if err != nil {
		t.Fatalf("Reserve(maxBudget) failed: %v", err)
	}
	defer res1.Release()

	res2, err := ledger.Reserve(1)
	if err == nil {
		t.Fatal("expected error on saturated budget, got nil")
	}
	if res2 != nil {
		t.Fatal("expected nil reservation on exhaustion")
	}
	if !largebody.IsSpoolBudgetExhausted(err) {
		t.Fatalf("expected ErrSpoolBudgetExhausted, got %v", err)
	}
	if ledger.InflightBytes() != maxBudget {
		t.Errorf("inflight bytes must stay at %d, got %d", maxBudget, ledger.InflightBytes())
	}
	if ledger.ActiveReservations() != 1 {
		t.Errorf("active reservations must stay at 1, got %d", ledger.ActiveReservations())
	}
}

func TestSpoolLedger_IncrementalReservationUnknownLength(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	// Begin unmetered / unknown reservation (0 bytes initially)
	res, err := ledger.BeginReservation()
	if err != nil {
		t.Fatalf("BeginReservation failed: %v", err)
	}
	defer res.Release()

	if res.ReservedBytes() != 0 {
		t.Errorf("initial reserved bytes must be 0, got %d", res.ReservedBytes())
	}
	if ledger.ActiveReservations() != 1 {
		t.Errorf("active reservations must be 1, got %d", ledger.ActiveReservations())
	}
	if ledger.InflightBytes() != 0 {
		t.Errorf("initial inflight bytes must be 0, got %d", ledger.InflightBytes())
	}

	// Incrementally grow in chunks
	chunks := []int64{32 * 1024, 32 * 1024, 64 * 1024, 128 * 1024}
	expectedTotal := int64(0)
	for i, chunk := range chunks {
		if err := res.ReserveMore(chunk); err != nil {
			t.Fatalf("chunk %d: ReserveMore(%d) failed: %v", i, chunk, err)
		}
		expectedTotal += chunk
		if res.ReservedBytes() != expectedTotal {
			t.Errorf("chunk %d: got reserved bytes %d, want %d", i, res.ReservedBytes(), expectedTotal)
		}
		if ledger.InflightBytes() != expectedTotal {
			t.Errorf("chunk %d: got ledger inflight bytes %d, want %d", i, ledger.InflightBytes(), expectedTotal)
		}
	}

	// Zero-byte incremental reservation is a no-op
	if err := res.ReserveMore(0); err != nil {
		t.Fatalf("ReserveMore(0) failed: %v", err)
	}
	if res.ReservedBytes() != expectedTotal {
		t.Errorf("after 0-byte grow, reserved bytes must remain %d, got %d", expectedTotal, res.ReservedBytes())
	}
}

func TestSpoolLedger_IncrementalReservationExhaustionRetainsPrefix(t *testing.T) {
	t.Parallel()

	maxBudget := int64(100 * 1024)
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      32 * 1024,
		MaxInflightSpoolBytes: maxBudget,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	res, err := ledger.BeginReservation()
	if err != nil {
		t.Fatalf("BeginReservation failed: %v", err)
	}

	// Successfully reserve 80 KiB
	if err := res.ReserveMore(80 * 1024); err != nil {
		t.Fatalf("ReserveMore(80 KiB) failed: %v", err)
	}

	// Attempt to reserve another 30 KiB (total 110 KiB > 100 KiB budget)
	err = res.ReserveMore(30 * 1024)
	if err == nil {
		t.Fatal("expected ErrSpoolBudgetExhausted when exceeding budget incrementally, got nil")
	}
	if !largebody.IsSpoolBudgetExhausted(err) {
		t.Fatalf("expected ErrSpoolBudgetExhausted, got %v", err)
	}

	// Critical invariant (Req 20.7):
	// The previously reserved 80 KiB must remain reserved while canonical fallback
	// continues to read the retained prefix!
	if res.ReservedBytes() != 80*1024 {
		t.Errorf("failed ReserveMore must not corrupt held reservation: got %d, want %d", res.ReservedBytes(), 80*1024)
	}
	if ledger.InflightBytes() != 80*1024 {
		t.Errorf("ledger inflight bytes must remain %d, got %d", 80*1024, ledger.InflightBytes())
	}

	// When fallback finishes or request cleans up, Release frees held prefix exactly once
	freed := res.Release()
	if freed != 80*1024 {
		t.Errorf("Release() got %d freed, want %d", freed, 80*1024)
	}
	if ledger.InflightBytes() != 0 {
		t.Errorf("ledger inflight bytes must be 0 after Release, got %d", ledger.InflightBytes())
	}
	if ledger.ActiveReservations() != 0 {
		t.Errorf("active reservations must be 0 after Release, got %d", ledger.ActiveReservations())
	}
}

func TestSpoolLedger_ReleaseExactlyOnce(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	res, err := ledger.Reserve(128 * 1024)
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}

	// First release
	freed1 := res.Release()
	if freed1 != 128*1024 {
		t.Errorf("first release got %d, want %d", freed1, 128*1024)
	}
	if ledger.InflightBytes() != 0 {
		t.Errorf("inflight bytes must be 0, got %d", ledger.InflightBytes())
	}
	if ledger.ActiveReservations() != 0 {
		t.Errorf("active reservations must be 0, got %d", ledger.ActiveReservations())
	}

	// Second release (idempotent)
	freed2 := res.Release()
	if freed2 != 0 {
		t.Errorf("second release must return 0, got %d", freed2)
	}
	if ledger.InflightBytes() != 0 {
		t.Errorf("inflight bytes must remain 0, got %d", ledger.InflightBytes())
	}
	if ledger.ActiveReservations() != 0 {
		t.Errorf("active reservations must remain 0, got %d", ledger.ActiveReservations())
	}

	// ReserveMore after release is rejected
	err = res.ReserveMore(1024)
	if err == nil {
		t.Fatal("expected error calling ReserveMore on released reservation, got nil")
	}
	if !errors.Is(err, largebody.ErrReservationClosed) {
		t.Fatalf("expected ErrReservationClosed, got %v", err)
	}
}

func TestSpoolLedger_ConcurrentReleaseSafety(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 10 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	const goroutines = 50
	const reservedPerRes = int64(10 * 1024)

	res, err := ledger.Reserve(reservedPerRes)
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}

	var wg sync.WaitGroup
	var totalFreed int64
	var mu sync.Mutex

	for range goroutines {
		wg.Go(func() {
			freed := res.Release()
			if freed > 0 {
				mu.Lock()
				totalFreed += freed
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if totalFreed != reservedPerRes {
		t.Fatalf("concurrent releases must release exactly once: got total %d, want %d", totalFreed, reservedPerRes)
	}
	if ledger.InflightBytes() != 0 {
		t.Fatalf("ledger inflight bytes must be 0, got %d", ledger.InflightBytes())
	}
	if ledger.ActiveReservations() != 0 {
		t.Fatalf("active reservations must be 0, got %d", ledger.ActiveReservations())
	}
}

func TestSpoolLedger_CheckedInt64Math(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	// 1. Negative initial reservation rejected
	_, err = ledger.Reserve(-1)
	if err == nil {
		t.Fatal("expected error on negative reservation, got nil")
	}
	if !errors.Is(err, largebody.ErrInvalidReservation) {
		t.Fatalf("expected ErrInvalidReservation, got %v", err)
	}

	// 2. Negative ReserveMore rejected
	res, err := ledger.BeginReservation()
	if err != nil {
		t.Fatalf("BeginReservation failed: %v", err)
	}
	defer res.Release()

	err = res.ReserveMore(-1)
	if err == nil {
		t.Fatal("expected error on negative ReserveMore, got nil")
	}
	if !errors.Is(err, largebody.ErrInvalidReservation) {
		t.Fatalf("expected ErrInvalidReservation, got %v", err)
	}

	// 3. Overflow in ReserveMore
	if err := res.ReserveMore(100); err != nil {
		t.Fatalf("ReserveMore(100) failed: %v", err)
	}
	err = res.ReserveMore(math.MaxInt64)
	if err == nil {
		t.Fatal("expected overflow error on math.MaxInt64 ReserveMore, got nil")
	}
	if !errors.Is(err, largebody.ErrInvalidReservation) && !largebody.IsSpoolBudgetExhausted(err) {
		t.Fatalf("expected ErrInvalidReservation or ErrSpoolBudgetExhausted, got %v", err)
	}

	// Held reservation remains at 100
	if res.ReservedBytes() != 100 {
		t.Errorf("reservation after overflow attempt must stay at 100, got %d", res.ReservedBytes())
	}
}

func TestSpoolLedger_ShrinkTo(t *testing.T) {
	t.Parallel()

	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	res, err := ledger.Reserve(500 * 1024)
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}
	defer res.Release()

	// Shrink to 300 KiB
	if err := res.ShrinkTo(300 * 1024); err != nil {
		t.Fatalf("ShrinkTo(300 KiB) failed: %v", err)
	}
	if res.ReservedBytes() != 300*1024 {
		t.Errorf("got reserved bytes %d, want %d", res.ReservedBytes(), 300*1024)
	}
	if ledger.InflightBytes() != 300*1024 {
		t.Errorf("got ledger inflight bytes %d, want %d", ledger.InflightBytes(), 300*1024)
	}

	// Shrink to same size is a no-op
	if err := res.ShrinkTo(300 * 1024); err != nil {
		t.Fatalf("ShrinkTo(same) failed: %v", err)
	}

	// Shrink to negative is rejected
	if err := res.ShrinkTo(-1); err == nil {
		t.Fatal("expected error on ShrinkTo(-1), got nil")
	}

	// Shrink to larger size is rejected (must use ReserveMore)
	if err := res.ShrinkTo(400 * 1024); err == nil {
		t.Fatal("expected error on ShrinkTo with larger size, got nil")
	}

	// Shrink after release is rejected
	res.Release()
	if err := res.ShrinkTo(100); err == nil {
		t.Fatal("expected error on ShrinkTo after release, got nil")
	}
}

func TestSpoolLedger_MemoryAndSpillAccounting(t *testing.T) {
	t.Parallel()

	memBudget := int64(64 * 1024)
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      memBudget,
		MaxInflightSpoolBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	// 1. Within memory budget (32 KiB)
	res1, err := ledger.Reserve(32 * 1024)
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}
	defer res1.Release()

	if res1.MemoryBytes() != 32*1024 {
		t.Errorf("got MemoryBytes %d, want %d", res1.MemoryBytes(), 32*1024)
	}
	if res1.SpillBytes() != 0 {
		t.Errorf("got SpillBytes %d, want 0", res1.SpillBytes())
	}
	if res1.MemorySpoolBytes() != memBudget {
		t.Errorf("got MemorySpoolBytes %d, want %d", res1.MemorySpoolBytes(), memBudget)
	}

	// 2. Exceeding memory budget (100 KiB -> 64 KiB memory, 36 KiB spill)
	res2, err := ledger.Reserve(100 * 1024)
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}
	defer res2.Release()

	if res2.MemoryBytes() != memBudget {
		t.Errorf("got MemoryBytes %d, want %d", res2.MemoryBytes(), memBudget)
	}
	if res2.SpillBytes() != (100-64)*1024 {
		t.Errorf("got SpillBytes %d, want %d", res2.SpillBytes(), (100-64)*1024)
	}
}

func TestSpoolLedger_NilSafety(t *testing.T) {
	t.Parallel()

	var ledger *largebody.SpoolLedger
	res, err := ledger.Reserve(100)
	if err == nil || res != nil {
		t.Fatalf("expected error from nil ledger Reserve, got res=%v, err=%v", res, err)
	}
	if !largebody.IsSpoolBudgetExhausted(err) {
		t.Errorf("expected ErrSpoolBudgetExhausted on nil ledger Reserve, got %v", err)
	}

	res, err = ledger.BeginReservation()
	if err == nil || res != nil {
		t.Fatalf("expected error from nil ledger BeginReservation, got res=%v, err=%v", res, err)
	}

	if ledger.InflightBytes() != 0 {
		t.Errorf("nil ledger InflightBytes must be 0, got %d", ledger.InflightBytes())
	}
	if ledger.AvailableBytes() != 0 {
		t.Errorf("nil ledger AvailableBytes must be 0, got %d", ledger.AvailableBytes())
	}
	if ledger.ActiveReservations() != 0 {
		t.Errorf("nil ledger ActiveReservations must be 0, got %d", ledger.ActiveReservations())
	}

	var r *largebody.SpoolReservation
	if r.Release() != 0 {
		t.Errorf("nil reservation Release must return 0, got %d", r.Release())
	}
	if r.ReservedBytes() != 0 {
		t.Errorf("nil reservation ReservedBytes must be 0, got %d", r.ReservedBytes())
	}
	if r.MemoryBytes() != 0 {
		t.Errorf("nil reservation MemoryBytes must be 0, got %d", r.MemoryBytes())
	}
	if r.SpillBytes() != 0 {
		t.Errorf("nil reservation SpillBytes must be 0, got %d", r.SpillBytes())
	}
	if err := r.ReserveMore(100); err == nil {
		t.Error("expected error from nil reservation ReserveMore, got nil")
	}
	if err := r.ShrinkTo(50); err == nil {
		t.Error("expected error from nil reservation ShrinkTo, got nil")
	}
}

func TestSpoolLedger_ConcurrentOperations(t *testing.T) {
	t.Parallel()

	const maxBudget = int64(10 * 1024 * 1024)
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 * 1024,
		MaxInflightSpoolBytes: maxBudget,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger failed: %v", err)
	}

	const workers = 20
	const iterations = 50
	var wg sync.WaitGroup

	for i := range workers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := range iterations {
				// Mix early reservation and incremental reservation
				if (workerID+j)%2 == 0 {
					res, err := ledger.Reserve(10 * 1024)
					if err != nil {
						if !largebody.IsSpoolBudgetExhausted(err) {
							t.Errorf("unexpected error: %v", err)
						}
						continue
					}
					_ = res.ReserveMore(5 * 1024)
					_ = res.ShrinkTo(12 * 1024)
					res.Release()
				} else {
					res, err := ledger.BeginReservation()
					if err != nil {
						if !largebody.IsSpoolBudgetExhausted(err) {
							t.Errorf("unexpected error: %v", err)
						}
						continue
					}
					_ = res.ReserveMore(20 * 1024)
					res.Release()
				}
			}
		}(i)
	}
	wg.Wait()

	if ledger.InflightBytes() != 0 {
		t.Fatalf("after all concurrent workers completed, inflight bytes must be 0, got %d", ledger.InflightBytes())
	}
	if ledger.ActiveReservations() != 0 {
		t.Fatalf("after all concurrent workers completed, active reservations must be 0, got %d", ledger.ActiveReservations())
	}
}

func containsSubstring(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
