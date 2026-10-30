package adapter

import (
	"math"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestMapOptions_HugeMaxOutputTokensSaturates(t *testing.T) {
	t.Parallel()
	huge := 1 << 40
	out := mapOptions(aipapi.GenerationOptions{MaxOutputTokens: &huge})
	if out.MaxOutputTokens == nil {
		t.Fatalf("huge max tokens dropped to nil, want saturation to MaxUint32")
	}
	if got := *out.MaxOutputTokens; got != uint32(math.MaxUint32) {
		t.Fatalf("huge max tokens=%d, want %d (saturation, not wrap to 0)", got, uint32(math.MaxUint32))
	}
}

func TestMapOptions_NonFiniteTemperatureExplicit(t *testing.T) {
	t.Parallel()
	inf := math.Inf(1)
	out := mapOptions(aipapi.GenerationOptions{Temperature: &inf})
	if out.TemperatureMillis == nil {
		t.Fatalf("+Inf temperature dropped to nil; want explicit MaxInt32 saturation so downstream validation fails closed")
	}
	if got := *out.TemperatureMillis; got != int32(math.MaxInt32) {
		t.Fatalf("+Inf millis=%d, want %d", got, int32(math.MaxInt32))
	}
	ninf := math.Inf(-1)
	out = mapOptions(aipapi.GenerationOptions{Temperature: &ninf})
	if out.TemperatureMillis == nil {
		t.Fatalf("-Inf temperature dropped to nil; want explicit MinInt32 saturation")
	}
	if got := *out.TemperatureMillis; got != int32(math.MinInt32) {
		t.Fatalf("-Inf millis=%d, want %d", got, int32(math.MinInt32))
	}
}

func TestMapOptions_HugeFiniteTemperatureSaturates(t *testing.T) {
	t.Parallel()
	huge := 1e12
	out := mapOptions(aipapi.GenerationOptions{Temperature: &huge})
	if out.TemperatureMillis == nil {
		t.Fatalf("huge temperature dropped to nil, want MaxInt32 saturation")
	}
	if got := *out.TemperatureMillis; got != int32(math.MaxInt32) {
		t.Fatalf("huge temp millis=%d, want %d", got, int32(math.MaxInt32))
	}
}

func TestMapOptions_ValidOptionsPreserved(t *testing.T) {
	t.Parallel()
	tokens := 4096
	temp := 1.5
	out := mapOptions(aipapi.GenerationOptions{MaxOutputTokens: &tokens, Temperature: &temp})
	if out.MaxOutputTokens == nil || *out.MaxOutputTokens != uint32(4096) {
		t.Fatalf("valid tokens not preserved: %+v", out.MaxOutputTokens)
	}
	if out.TemperatureMillis == nil || *out.TemperatureMillis != int32(1500) {
		t.Fatalf("valid temp not preserved: %+v", out.TemperatureMillis)
	}
}
