package processhost

import (
	"testing"
	"time"
)

// TestOutputBudget_BurstThenSustainedAllowance pins the diagnostic budget:
// the burst allowance is spendable at once, the sustained allowance refills
// it over time, and a connector that outruns both is reported.
func TestOutputBudget_BurstThenSustainedAllowance(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	budget := newOutputBudget(OutputBudget{BurstBytes: 100, BytesPerSecond: 50}, func() time.Time { return now })

	if budget.charge(60) {
		t.Fatal("the burst allowance must be spendable")
	}
	if !budget.charge(60) {
		t.Fatal("spending past the burst allowance must be reported")
	}
	// A second of sustained allowance refills half the burst.
	now = now.Add(time.Second)
	if budget.charge(30) {
		t.Fatal("the refilled allowance must be spendable")
	}
	if !budget.charge(30) {
		t.Fatal("spending past the refilled allowance must be reported")
	}
}

// TestOutputBudget_Defaults pins the package defaults: a zero budget selects
// the documented burst and sustained allowances instead of disabling them.
func TestOutputBudget_Defaults(t *testing.T) {
	t.Parallel()
	got := OutputBudget{}.normalized()
	if got.BurstBytes != defaultOutputBurstBytes || got.BytesPerSecond != defaultOutputBytesPerSecond {
		t.Fatalf("defaults: %+v", got)
	}
	now := time.Unix(0, 0)
	budget := newOutputBudget(OutputBudget{}, func() time.Time { return now })
	if budget.charge(defaultOutputBurstBytes) {
		t.Fatal("the default burst allowance must be spendable")
	}
	if !budget.charge(1) {
		t.Fatal("spending past the default burst allowance must be reported")
	}
}

// TestOutputBudget_LongIdleRefillStaysBounded proves the refill arithmetic
// cannot overflow on a long idle period: the bucket refills to its capacity
// and no further.
func TestOutputBudget_LongIdleRefillStaysBounded(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	budget := newOutputBudget(OutputBudget{BurstBytes: 100, BytesPerSecond: 50}, func() time.Time { return now })
	if budget.charge(100) {
		t.Fatal("the burst allowance must be spendable")
	}
	now = now.Add(72 * time.Hour)
	if budget.charge(100) {
		t.Fatal("a long idle period must refill the whole burst allowance")
	}
	if !budget.charge(1) {
		t.Fatal("spending past the refilled allowance must be reported")
	}
}
