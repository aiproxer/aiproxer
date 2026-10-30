package codex

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestAccountWindowSnapshots_PreservePoolsResetAndExactGaugeValues(t *testing.T) {
	t.Parallel()
	headers := http.Header{
		"X-Codex-Primary-Used-Percent":      []string{"12.500"},
		"X-Codex-Primary-Remaining-Percent": []string{"87.5"},
		"X-Codex-Primary-Reset-At":          []string{"1700000000"},
		"X-Codex-Primary-Window-Id":         []string{"five-hour"},
		"X-Codex-Secondary-Used-Percent":    []string{"0"},
		"X-Codex-Secondary-Reset-At":        []string{"2024-01-01T00:00:00Z"},
	}
	observed := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	got := accountWindowSnapshots(headers, "acct-1", observed)
	if len(got) != 2 {
		t.Fatalf("snapshots=%d, want 2", len(got))
	}
	if got[0].PoolID != "primary" || got[1].PoolID != "secondary" {
		t.Fatalf("pool order/ids: %+v", got)
	}
	if got[0].ProviderAccountKey != "acct-1" || got[0].WindowID != "five-hour" || got[0].ResetAt.Unix() != 1700000000 {
		t.Fatalf("primary identity: %+v", got[0])
	}
	if got[0].UsedPercent == nil || got[0].UsedPercent.Coefficient != "125" || got[0].UsedPercent.Scale != 1 || got[0].RemainingPercent == nil || got[0].RemainingPercent.Coefficient != "875" || got[0].RemainingPercent.Scale != 1 {
		t.Fatalf("primary decimals: %+v", got[0])
	}
	if got[1].UsedPercent == nil || got[1].UsedPercent.Coefficient != "0" || got[1].UsedPercent.Scale != 0 {
		t.Fatalf("present zero gauge lost: %+v", got[1])
	}
	if got[0].Semantics != "gauge" || got[0].Origin != "provider" {
		t.Fatalf("provenance: %+v", got[0])
	}
}

func TestAccountWindowSnapshots_DropsMalformedValuesAndNeverCreatesDebit(t *testing.T) {
	t.Parallel()
	headers := http.Header{
		"X-Codex-Primary-Used-Percent":   []string{"not-a-number"},
		"X-Codex-Primary-Credits":        []string{"999999999999999999999999999999999999999999999"},
		"X-Codex-Secondary-Used-Percent": []string{"2"},
	}
	got := accountWindowSnapshots(headers, "acct-1", time.Unix(1700000000, 0))
	if len(got) != 1 {
		t.Fatalf("partial snapshots=%d, want 1", len(got))
	}
	if got[0].PoolID != "secondary" || got[0].UsedPercent == nil || got[0].UsedPercent.Coefficient != "2" || got[0].UsedPercent.Scale != 0 {
		t.Fatalf("valid secondary gauge missing: %+v", got[0])
	}
	// There is deliberately no request cost or debit field on this type. The
	// provider snapshot remains a gauge until an owner promotes it separately.
	if _, ok := any(got[0]).(interface{ RequestDebit() }); ok {
		t.Fatal("account gauge unexpectedly exposes a debit")
	}
}

func TestAccountWindowSnapshots_DropsNegativeGaugeValues(t *testing.T) {
	t.Parallel()
	headers := http.Header{
		"X-Codex-Primary-Used-Percent": []string{"-1"},
		"X-Codex-Primary-Limit":        []string{"100"},
	}
	got := accountWindowSnapshots(headers, "acct-1", time.Unix(1700000000, 0))
	if len(got) != 1 || got[0].UsedPercent != nil || got[0].Limit == nil {
		t.Fatalf("negative gauge should be omitted while valid fields remain: %+v", got)
	}
}

func TestCodexStream_PreservesConcurrentOutOfOrderWindowSnapshots(t *testing.T) {
	t.Parallel()
	stream := &codexStream{}
	first := http.Header{
		"X-Codex-Primary-Window-Id":    []string{"window-old"},
		"X-Codex-Primary-Used-Percent": []string{"10"},
	}
	second := http.Header{
		"X-Codex-Primary-Window-Id":    []string{"window-new"},
		"X-Codex-Primary-Used-Percent": []string{"20"},
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		stream.captureAccountWindowSnapshots(second, "acct-1", time.Unix(1700000002, 0))
	}()
	go func() {
		defer wg.Done()
		stream.captureAccountWindowSnapshots(first, "acct-1", time.Unix(1700000001, 0))
	}()
	wg.Wait()

	got := stream.DrainAccountWindowSnapshots()
	if len(got) != 2 {
		t.Fatalf("captured windows=%d, want 2", len(got))
	}
	seen := map[string]bool{}
	for _, snapshot := range got {
		seen[snapshot.WindowID] = true
	}
	if !seen["window-old"] || !seen["window-new"] {
		t.Fatalf("out-of-order windows were overwritten: %+v", got)
	}
	if replay := stream.DrainAccountWindowSnapshots(); len(replay) != 0 {
		t.Fatalf("drain replay=%d, want 0", len(replay))
	}
}
