package pathvirtualization_test

// Spec: b-leg-path-virtualization, review finding P1 #3: the lexical reserved-alias
// recognizer copied the ENTIRE remaining payload into a string once per marker
// occurrence, so its cost grew quadratically in the occurrence count and reached
// hundreds of megabytes on a payload inside the 1 MiB argument bound the assembler
// enforces.
//
// This file is the standing guard against that cost coming back. It is expressed in
// ALLOCATED BYTES for the same reason the outbound rewrite guard
// (rewrite/occurrence_scaling_test.go) is expressed in allocations: an allocation
// total is a deterministic function of the code and its input, so it is identical on
// every host, under every timing condition, and inside whatever GC state the process
// happens to be in, while a wall-clock threshold is none of those things.
//
// TWO ASSERTIONS, deliberately separated in strength:
//
//   - TestScanReservedAliasAllocationsStayBoundedOverManyMarkers is an ABSOLUTE
//     bound on one realistic many-marker payload. It is the one that would fail on a
//     reintroduced quadratic copy, and its slack is orders of magnitude below the
//     measured cost so ordinary implementation churn cannot trip it.
//   - TestScanReservedAliasAllocationsScaleWithOccurrenceCount is the RELATIVE
//     analogue of the outbound occurrence guard: per-occurrence cost must not grow
//     with the occurrence count. It is loose by design, exactly like its sibling.
//
// Neither test calls t.Parallel. runtime.ReadMemStats pins a process-wide allocation
// total, so a concurrent sibling's allocations would be counted as this test's own.
// The testing package runs serial top-level tests one at a time, so a serial test is
// measured on a quiet process.
//
// NOTHING HERE PRINTS CONTENT. Fixture bytes are synthetic filler and the reserved
// marker, and no assertion message carries a path, an alias, a workspace tag, or a
// tool-call id.

import (
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// scanMarkerUnit is one synthetic marker occurrence: the reserved marker as a COMPLETE
// segment followed by a one-character tag segment, which is a recognized MALFORMED tag
// and therefore keeps the scan iterating instead of returning at the first occurrence.
//
// A well-formed tag would end the scan at the first occurrence and measure nothing, so
// the fixture must make the scan visit every occurrence.
const scanMarkerUnit = "/.__aip_v1__/z"

// scanFixtureBytes is the payload size the absolute bound is measured over: 3000 marker
// occurrences padded to 66 KiB, which is the shape that measured on the order of a
// hundred megabytes when the recognizer copied the remaining suffix per occurrence.
const scanFixtureBytes = 66 << 10

// scanManyMarkerOccurrences is how many marker occurrences scanFixtureBytes holds.
const scanManyMarkerOccurrences = 3000

// scanAllocationBoundBytes is the absolute ceiling one many-marker scan may allocate.
//
// The scan needs no allocation at all: it reads bytes in place and never converts a
// payload slice to a string. The bound exists to absorb a future bounded scratch buffer
// and measurement noise, not to describe current behavior - it sits three orders of
// magnitude below the quadratic cost it replaces, and far below one payload length, so
// "cost proportional to the payload" cannot pass it.
const scanAllocationBoundBytes = 64 << 10

// reservedScanOccurrenceTiers are the occurrence counts the relative guard compares.
// They span two decades, which is what makes a per-occurrence cost that stops being
// flat visible rather than hidden in a total.
var reservedScanOccurrenceTiers = []int{10, 100, 1000}

// reservedScanAllocationRuns is how many measured scans each allocation sample
// averages. TotalAlloc is cumulative, so repeated identical executions allocate
// identically and averaging only reduces unrelated runtime noise.
const reservedScanAllocationRuns = 4

// reservedScanScalingTolerance is how much the per-occurrence allocation cost may grow
// between the smallest and the largest tier. See the file comment; two is loose enough
// for implementation churn and far below the growth factor a quadratic copy shows.
const reservedScanScalingTolerance = 2.0

// reservedScanAllocationSlack absorbs a per-occurrence cost that is legitimately zero
// at one tier and tiny at another, so the relative guard keeps its meaning without
// dividing by zero.
const reservedScanAllocationSlack = 64

// scanMarkerPayload builds one payload holding exactly occurrences marker occurrences
// with a malformed tag, padded with opaque filler to totalBytes when that is larger
// than the occurrences need.
//
// The filler carries no marker, so the scan's occurrence count is exactly the one the
// fixture asked for.
func scanMarkerPayload(occurrences int, totalBytes int) []byte {
	var b strings.Builder
	b.Grow(occurrences*len(scanMarkerUnit) + totalBytes)
	for range occurrences {
		b.WriteString(scanMarkerUnit)
	}
	if pad := totalBytes - b.Len(); pad > 0 {
		b.WriteString(strings.Repeat("x", pad))
	}
	return []byte(b.String())
}

// measureReservedScanBytes reports the average number of bytes one call to
// [pathvirtualization.ScanReservedAlias] allocates over raw.
//
// The measurement is deliberately a warm-up plus a small fixed run count rather than
// testing.Benchmark: the quadratic behavior under guard allocates on the order of a
// hundred megabytes per call, so an adaptive benchmark would spend seconds and gigabytes
// proving the failure. TotalAlloc is a monotonic cumulative counter, so it is unaffected
// by when a collection happens.
func measureReservedScanBytes(t *testing.T, raw []byte) float64 {
	t.Helper()
	pathvirtualization.ScanReservedAlias(raw)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range reservedScanAllocationRuns {
		if got := pathvirtualization.ScanReservedAlias(raw); got == pathvirtualization.ReservedAliasAbsent {
			t.Fatalf("fixture must hold %d recognized marker occurrences, got %v",
				reservedScanAllocationRuns, got)
		}
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(reservedScanAllocationRuns)
}

// TestScanReservedAliasAllocationsStayBoundedOverManyMarkers is the absolute half of
// the guard: a payload inside the default 1 MiB argument bound, carrying thousands of
// recognized marker occurrences, must not cost memory proportional to the product of
// its length and its occurrence count.
//
// It also pins the ANSWER the bounded inspection must keep returning, so a future
// "optimization" cannot buy its bound by recognizing fewer occurrences.
func TestScanReservedAliasAllocationsStayBoundedOverManyMarkers(t *testing.T) {
	raw := scanMarkerPayload(scanManyMarkerOccurrences, scanFixtureBytes)

	// NON-VACUITY, part one: the fixture really holds the occurrences it claims, so a
	// scan that simply stopped early would be caught before it is measured.
	if got := strings.Count(string(raw), scanMarkerUnit); got != scanManyMarkerOccurrences {
		t.Fatalf("fixture occurrences=%d want %d", got, scanManyMarkerOccurrences)
	}

	got := pathvirtualization.ScanReservedAlias(raw)
	if got != pathvirtualization.ReservedAliasMalformedTag {
		t.Fatalf("the answer must be unchanged: got %v want %v",
			got, pathvirtualization.ReservedAliasMalformedTag)
	}

	perScan := measureReservedScanBytes(t, raw)
	t.Logf("payload_bytes=%d occurrences=%d bytes_per_scan=%.0f",
		len(raw), scanManyMarkerOccurrences, perScan)
	if perScan > scanAllocationBoundBytes {
		t.Fatalf("one scan of %d bytes holding %d marker occurrences allocated %.0f bytes; "+
			"the recognizer must inspect only the bounded tag segment, not the remaining payload "+
			"(bound %d)",
			len(raw), scanManyMarkerOccurrences, perScan, scanAllocationBoundBytes)
	}
}

// TestScanReservedAliasAllocationsScaleWithOccurrenceCount is the relative half of the
// guard, mirroring the outbound rewrite guard: the per-occurrence cost of one scan must
// stay proportional to the occurrence count instead of growing with it.
//
// It catches the same defect as the absolute bound from the other side, so a future
// change that keeps the many-marker case inside the bound while reintroducing growth per
// occurrence still fails here.
func TestScanReservedAliasAllocationsScaleWithOccurrenceCount(t *testing.T) {
	perOccurrence := make([]float64, 0, len(reservedScanOccurrenceTiers))
	for _, occurrences := range reservedScanOccurrenceTiers {
		raw := scanMarkerPayload(occurrences, 0)
		perScan := measureReservedScanBytes(t, raw)
		perOccurrenceCost := perScan / float64(occurrences)
		t.Logf("occurrences=%4d bytes_per_scan=%9.0f bytes_per_occurrence=%.2f",
			occurrences, perScan, perOccurrenceCost)
		perOccurrence = append(perOccurrence, perOccurrenceCost)
	}

	smallest := perOccurrence[0]
	largest := perOccurrence[len(perOccurrence)-1]
	if largest > reservedScanScalingTolerance*smallest+reservedScanAllocationSlack {
		t.Fatalf("the recognizer allocated %.2f bytes per marker occurrence at the largest tier and "+
			"%.2f at the smallest: the per-occurrence cost grew by a factor that is not proportional "+
			"to the occurrence count, a factor of %.1f", largest, smallest, largest/(smallest+reservedScanAllocationSlack))
	}
}

// BenchmarkScanReservedAliasManyMarkers measures the guarded case so a regression is
// visible as a timing trend as well as an allocation total. The occurrence count is
// reported as an extra metric because it is a deterministic count rather than a timing,
// which is what makes two rows comparable across hosts.
func BenchmarkScanReservedAliasManyMarkers(b *testing.B) {
	for _, occurrences := range reservedScanOccurrenceTiers {
		b.Run("occurrences_"+strconv.Itoa(occurrences), func(b *testing.B) {
			raw := scanMarkerPayload(occurrences, scanFixtureBytes)
			if got := pathvirtualization.ScanReservedAlias(raw); got != pathvirtualization.ReservedAliasMalformedTag {
				b.Fatalf("fixture answer=%v want %v", got, pathvirtualization.ReservedAliasMalformedTag)
			}
			b.ReportAllocs()
			var sink pathvirtualization.ReservedAliasPresence
			for b.Loop() {
				sink = pathvirtualization.ScanReservedAlias(raw)
			}
			if sink != pathvirtualization.ReservedAliasMalformedTag {
				b.Fatalf("measured scan answer=%v want %v", sink, pathvirtualization.ReservedAliasMalformedTag)
			}
			b.ReportMetric(float64(occurrences), "occurrences")
			b.ReportMetric(float64(len(raw)), "payload_bytes")
		})
	}
}
