package economics_test

import (
	"fmt"
	"math"
	"math/big"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// Phase 2.5 RED contracts: checked money, explicit rate presence, rounding
// (requirements 2.5–2.9, 4.3, 4.6–4.10; design D3, D5, D15).

func TestPhase25_NanoRate_AbsentVsAuthoritativeZero(t *testing.T) {
	t.Parallel()
	absent := economics.NanoRate{}
	zero := economics.NanoRate{NanoUnits: 0, Present: true}
	if absent.Present == zero.Present {
		t.Fatal("absent and authoritative zero rate must remain distinct (req 2.9, 4.8)")
	}
}

func TestPhase25_ParseRequiredNanoRate_RejectsAbsent(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "  ", "\t"} {
		if _, err := economics.ParseRequiredNanoRate(raw); err == nil {
			t.Fatalf("required rate %q must be rejected when absent (req 4.10)", raw)
		}
	}
}

func TestPhase25_ParseRequiredNanoRate_AcceptsAuthoritativeZero(t *testing.T) {
	t.Parallel()
	got, err := economics.ParseRequiredNanoRate("0")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Present || got.NanoUnits != 0 {
		t.Fatalf("explicit zero required rate must be present: %+v", got)
	}
}

func TestPhase25_ParseOptionalNanoRate_AbsentAllowed(t *testing.T) {
	t.Parallel()
	got, err := economics.ParseOptionalNanoRate("")
	if err != nil {
		t.Fatal(err)
	}
	if got.Present {
		t.Fatalf("optional empty must be absent: %+v", got)
	}
	zero, err := economics.ParseOptionalNanoRate("0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !zero.Present || zero.NanoUnits != 0 {
		t.Fatalf("optional explicit zero must be present: %+v", zero)
	}
}

func TestPhase25_ParseDecimalToNano_RejectsInvalid(t *testing.T) {
	t.Parallel()
	cases := []string{
		"",
		"not-a-number",
		"-1.00",
		"+1.25",
		"1e2",
		"1E-2",
		"1/2",
		"0x10",
		"1_000",
		" 1.25",
		"1.25 ",
		"1 25",
		"1.",
		".5",
		"1.1234567891",
		"1/0",
		"999999999999999999999999999999999",
		"9223372036.854775808",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := economics.ParseDecimalToNano(raw); err == nil {
				t.Fatalf("ParseDecimalToNano(%q) must fail", raw)
			}
		})
	}
}

func TestPhase25_ParseDecimalToNano_ExactNanos(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want int64
	}{
		{"0", 0},
		{"0.0", 0},
		{"1", 1_000_000_000},
		{"1.25", 1_250_000_000},
		{"01.5", 1_500_000_000},
		{"0.000000001", 1},
		{"0.5", 500_000_000},
		{"1.123456789", 1_123_456_789},
		{"9223372036.854775807", math.MaxInt64},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			t.Parallel()
			got, err := economics.ParseDecimalToNano(c.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got %d want %d", got, c.want)
			}
		})
	}
}

func TestPhase25_MoneyValidate_RejectsBadPresentMoney(t *testing.T) {
	t.Parallel()
	cases := []economics.Money{
		{NanoUnits: -1, Currency: "USD", Present: true},
		{NanoUnits: 1, Currency: "", Present: true},
		{NanoUnits: 1, Currency: "usd", Present: true},
		{NanoUnits: 1, Currency: "US D", Present: true},
	}
	for _, m := range cases {
		if err := m.Validate(); err == nil {
			t.Fatalf("Money.Validate must reject %+v (req 4.6)", m)
		}
	}
	absent := economics.Money{}
	if err := absent.Validate(); err != nil {
		t.Fatalf("absent money must Validate: %v", err)
	}
	zero := economics.Money{NanoUnits: 0, Currency: "USD", Present: true}
	if err := zero.Validate(); err != nil {
		t.Fatalf("authoritative zero must Validate: %v", err)
	}
}

func TestPhase25_Add_RejectsUnnormalizedCurrency(t *testing.T) {
	t.Parallel()
	a := economics.Money{NanoUnits: 1, Currency: "usd", Present: true}
	b := economics.Money{NanoUnits: 1, Currency: "usd", Present: true}
	if _, err := a.Add(b); err == nil {
		t.Fatal("unnormalized currency must fail Add (req 4.6)")
	}
}

func TestPhase25_AggregateMoney_MixedCurrencyAndOverflow(t *testing.T) {
	t.Parallel()
	usd := economics.Money{NanoUnits: 10, Currency: "USD", Present: true}
	eur := economics.Money{NanoUnits: 5, Currency: "EUR", Present: true}
	if _, err := economics.AggregateMoney(usd, eur); err == nil {
		t.Fatal("mixed-currency AggregateMoney must fail (req 4.9)")
	}
	maxMoney := economics.Money{NanoUnits: math.MaxInt64, Currency: "USD", Present: true}
	one := economics.Money{NanoUnits: 1, Currency: "USD", Present: true}
	if _, err := economics.AggregateMoney(maxMoney, one); err == nil {
		t.Fatal("overflow AggregateMoney must fail (req 4.6)")
	}
	sum, err := economics.AggregateMoney(usd, economics.Money{NanoUnits: 3, Currency: "USD", Present: true})
	if err != nil || sum.NanoUnits != 13 || !sum.Present {
		t.Fatalf("AggregateMoney sum=%+v err=%v", sum, err)
	}
}

func TestPhase25_RoundToInt64_AllDeclaredPolicies(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		numer  int64
		denom  int64
		policy economics.RoundingPolicy
		want   int64
	}
	cases := []tc{
		// 5/2 = 2.5 ties
		{"unspecified_pos_tie_toward_zero", 5, 2, economics.RoundingUnspecified, 2},
		{"toward_zero_pos_tie", 5, 2, economics.RoundingTowardZero, 2},
		{"toward_zero_neg_tie", -5, 2, economics.RoundingTowardZero, -2},
		{"floor_pos_tie", 5, 2, economics.RoundingFloor, 2},
		{"floor_neg_tie", -5, 2, economics.RoundingFloor, -3},
		{"half_away_pos_tie", 5, 2, economics.RoundingHalfAwayFromZero, 3},
		{"half_away_neg_tie", -5, 2, economics.RoundingHalfAwayFromZero, -3},
		{"half_even_2_5_to_2", 5, 2, economics.RoundingHalfEven, 2}, // 2 even
		{"half_even_3_5_to_4", 7, 2, economics.RoundingHalfEven, 4}, // 3.5 → 4 even
		{"half_even_neg_2_5_to_neg_2", -5, 2, economics.RoundingHalfEven, -2},
		{"exact_integer", 4, 2, economics.RoundingHalfAwayFromZero, 2},
		{"floor_pos_frac", 3, 2, economics.RoundingFloor, 1},
		{"toward_zero_pos_frac", 3, 2, economics.RoundingTowardZero, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := big.NewRat(c.numer, c.denom)
			got, err := economics.RoundToInt64(r, c.policy)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("RoundToInt64(%d/%d,%q)=%d want %d", c.numer, c.denom, c.policy, got, c.want)
			}
		})
	}
}

func TestPhase25_RoundToInt64_RejectsNilAndZeroDenomAndUnknown(t *testing.T) {
	t.Parallel()
	if _, err := economics.RoundToInt64(nil, economics.RoundingTowardZero); err == nil {
		t.Fatal("nil rat must fail")
	}
	if _, err := economics.RoundQuotient(1, 0, economics.RoundingTowardZero); err == nil {
		t.Fatal("zero denominator must fail")
	}
	if _, err := economics.RoundToInt64(big.NewRat(1, 2), economics.RoundingPolicy("ceil")); err == nil {
		t.Fatal("unknown/ceil policy must fail (do not invent ceil)")
	}
}

func TestPhase25_MulTokensByRatePer1M_UsesCheckedMath(t *testing.T) {
	t.Parallel()
	m, err := economics.MulTokensByRatePer1M(500_000, 3) // 1.5 nanos toward zero → 1
	if err != nil {
		t.Fatal(err)
	}
	if m.NanoUnits != 1 || !m.Present {
		t.Fatalf("toward-zero default mul got %+v", m)
	}
}

func TestPhase25_TokensFromMoneyPer1M_CheckedInversion(t *testing.T) {
	t.Parallel()
	got, err := economics.TokensFromMoneyPer1M(4_000_000_000, 4_000_000_000, economics.RoundingTowardZero)
	if err != nil || got != 1_000_000 {
		t.Fatalf("TokensFromMoneyPer1M=%d err=%v want 1000000", got, err)
	}
	if _, err := economics.TokensFromMoneyPer1M(1, 0, economics.RoundingTowardZero); err == nil {
		t.Fatal("zero rate must fail")
	}
}

func TestPhase25_TokensFromMoneyPer1M_RejectsOverflow(t *testing.T) {
	t.Parallel()
	policies := []economics.RoundingPolicy{
		economics.RoundingUnspecified,
		economics.RoundingTowardZero,
		economics.RoundingFloor,
		economics.RoundingHalfAwayFromZero,
		economics.RoundingHalfEven,
	}
	for _, policy := range policies {
		t.Run(string(policy)+"_or_unspecified", func(t *testing.T) {
			t.Parallel()
			got, err := economics.TokensFromMoneyPer1M(math.MaxInt64, 1, policy)
			if err == nil {
				t.Fatalf("must reject unrepresentable result, got %d", got)
			}
			if got != 0 {
				t.Fatalf("overflow must return 0 tokens, got %d", got)
			}
		})
	}
}

func FuzzParseDecimalToNano(f *testing.F) {
	for _, s := range []string{
		"0", "1", "1.25", "0.000000001", "1.123456789", "0.5", "01.5",
		"", "-", "+", "+1.25", "1/0", "1/2", "1e2", "0x10", "1_000",
		"1.", ".5", " 1.25", "1.25 ", "1.1234567891", "abc", "-0.1",
		"999999999999999999999999999999999", "9223372036.854775807",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		n, err := economics.ParseDecimalToNano(raw)
		okGrammar := strictDecimalGrammar(raw)
		if !okGrammar {
			if err == nil {
				t.Fatalf("non-grammar %q must fail, got %d", raw, n)
			}
			return
		}
		if err != nil {
			// Grammar-ok inputs may still overflow int64 nanos.
			return
		}
		if n < 0 {
			t.Fatalf("successful parse must be non-negative: %d from %q", n, raw)
		}
		want, wantErr := expectedNanosFromStrictDecimal(raw)
		if wantErr != nil {
			t.Fatalf("oracle failed for grammar-ok %q: %v", raw, wantErr)
		}
		if n != want {
			t.Fatalf("ParseDecimalToNano(%q)=%d want %d", raw, n, want)
		}
	})
}

func strictDecimalGrammar(raw string) bool {
	if raw == "" {
		return false
	}
	dot := -1
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '.' {
			if dot >= 0 {
				return false
			}
			dot = i
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	if dot < 0 {
		return true
	}
	if dot == 0 || dot == len(raw)-1 {
		return false
	}
	return len(raw)-dot-1 <= 9
}

func expectedNanosFromStrictDecimal(raw string) (int64, error) {
	dot := -1
	for i := 0; i < len(raw); i++ {
		if raw[i] == '.' {
			dot = i
			break
		}
	}
	intPart := raw
	fracPart := ""
	if dot >= 0 {
		intPart = raw[:dot]
		fracPart = raw[dot+1:]
	}
	for len(fracPart) < 9 {
		fracPart += "0"
	}
	whole := new(big.Int)
	if _, ok := whole.SetString(intPart, 10); !ok {
		return 0, fmt.Errorf("int part")
	}
	frac := new(big.Int)
	if _, ok := frac.SetString(fracPart, 10); !ok {
		return 0, fmt.Errorf("frac part")
	}
	nanos := new(big.Int).Mul(whole, big.NewInt(1_000_000_000))
	nanos.Add(nanos, frac)
	if !nanos.IsInt64() {
		return 0, fmt.Errorf("overflow")
	}
	return nanos.Int64(), nil
}

func FuzzRoundToInt64(f *testing.F) {
	f.Add(int64(5), int64(2), string(economics.RoundingHalfEven))
	f.Add(int64(-5), int64(2), string(economics.RoundingFloor))
	f.Add(int64(1), int64(0), string(economics.RoundingTowardZero))
	f.Add(int64(math.MaxInt64), int64(1), string(economics.RoundingTowardZero))
	f.Add(int64(math.MinInt64), int64(-1), string(economics.RoundingTowardZero))
	f.Fuzz(func(t *testing.T, numer, denom int64, policy string) {
		p := economics.RoundingPolicy(policy)
		got, err := economics.RoundQuotient(numer, denom, p)
		if denom == 0 {
			if err == nil {
				t.Fatal("zero denominator must error")
			}
			return
		}
		again, againErr := economics.RoundToInt64(big.NewRat(numer, denom), p)
		if (err == nil) != (againErr == nil) {
			t.Fatalf("RoundQuotient/RoundToInt64 error mismatch: %v vs %v", err, againErr)
		}
		if err != nil {
			return
		}
		if got != again {
			t.Fatalf("RoundQuotient=%d RoundToInt64=%d for %d/%d policy=%q", got, again, numer, denom, policy)
		}
		if p == economics.RoundingTowardZero || p == economics.RoundingUnspecified {
			quo := new(big.Int)
			rem := new(big.Int)
			quo.QuoRem(big.NewInt(numer), big.NewInt(denom), rem)
			if !quo.IsInt64() {
				t.Fatal("toward-zero success must fit int64 quotient")
			}
			if got != quo.Int64() {
				t.Fatalf("toward-zero RoundQuotient=%d want QuoRem=%d", got, quo.Int64())
			}
		}
	})
}
