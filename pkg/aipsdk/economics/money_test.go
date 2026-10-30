package economics_test

import (
	"math"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

func TestMoney_PresentDistinguishesZeroFromAbsent(t *testing.T) {
	t.Parallel()
	absent := economics.Money{}
	if absent.Present {
		t.Fatal("zero value must be absent")
	}
	zero := economics.Money{NanoUnits: 0, Currency: "USD", Present: true}
	if !zero.Present || zero.NanoUnits != 0 {
		t.Fatal("authoritative zero must be present")
	}
}

func TestMoney_AddSubChecked(t *testing.T) {
	t.Parallel()
	a := economics.Money{NanoUnits: 10, Currency: "USD", Present: true}
	b := economics.Money{NanoUnits: 3, Currency: "USD", Present: true}
	sum, err := a.Add(b)
	if err != nil || sum.NanoUnits != 13 || !sum.Present {
		t.Fatalf("Add=%v,%v", sum, err)
	}
	diff, err := a.Sub(b)
	if err != nil || diff.NanoUnits != 7 {
		t.Fatalf("Sub=%v,%v", diff, err)
	}
	if _, err := b.Sub(a); err == nil {
		t.Fatal("underflow must error")
	}
	if _, err := a.Add(economics.Money{NanoUnits: 1, Currency: "EUR", Present: true}); err == nil {
		t.Fatal("currency mismatch must error")
	}
	if _, err := a.Add(economics.Money{NanoUnits: 1, Currency: "USD", Present: false}); err == nil {
		t.Fatal("absent operand must error")
	}
	maxMoney := economics.Money{NanoUnits: math.MaxInt64, Currency: "USD", Present: true}
	one := economics.Money{NanoUnits: 1, Currency: "USD", Present: true}
	if _, err := maxMoney.Add(one); err == nil {
		t.Fatal("overflow must error")
	}
}

func TestMoney_MulTokensByRatePer1M(t *testing.T) {
	t.Parallel()
	m, err := economics.MulTokensByRatePer1M(1_000_000, 100)
	if err != nil || m.NanoUnits != 100 || !m.Present {
		t.Fatalf("got %#v err=%v", m, err)
	}
	zero, err := economics.MulTokensByRatePer1M(0, 100)
	if err != nil || zero.NanoUnits != 0 || !zero.Present {
		t.Fatalf("zero tokens: %#v err=%v", zero, err)
	}
	if _, err := economics.MulTokensByRatePer1M(math.MaxInt64, math.MaxInt64); err == nil {
		t.Fatal("overflow must error")
	}
}
