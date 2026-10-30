package secretguard

import (
	"bytes"
	"encoding/json"
	"math"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestJSONEscapedCandidateMappingAllocationIndependentOfOccurrenceCount(t *testing.T) {
	for _, path := range []string{"bridge", "exact_collection"} {
		t.Run(path, func(t *testing.T) {
			var allocated [2]uint64
			for index, count := range []int{1, 12} {
				raw := escapedCredentialJSON(128<<10, count)
				var decoded string
				if err := json.Unmarshal(raw, &decoded); err != nil {
					t.Fatal("decode escaped allocation fixture")
				}
				var occurrences []betterLeaksOccurrence
				for offset := 0; offset < len(raw); {
					position := bytes.Index(raw[offset:], []byte(adapterGitHubToken))
					if position < 0 {
						break
					}
					start := offset + position
					span, err := spanForByteRangeWithLocationIndex(raw, newBetterLeaksLocationIndex(raw), start, start+len(adapterGitHubToken))
					if err != nil {
						t.Fatal("locate escaped-string fixture occurrence")
					}
					occurrences = append(occurrences, betterLeaksOccurrence{value: []byte(adapterGitHubToken), span: span, start: start, end: start + len(adapterGitHubToken), offsetsValid: true, representation: betterLeaksOccurrenceLiteral, ruleID: "fixture"})
					offset = start + len(adapterGitHubToken)
				}
				invoke := func() {
					if path == "exact_collection" {
						matcher := positionalCredentialMatcher{secret: []byte(adapterGitHubToken), finding: sdk.Finding{SecretRefName: "REQUEST_KEY"}}
						got, mapped := collectExactJSONOccurrencesMapped(matcher, raw, "field")
						if !mapped || len(got) != count {
							t.Fatal("exact collection lost repeated escaped-string occurrences")
						}
						for i, occurrence := range got {
							if occurrence.start != occurrences[i].start || occurrence.end != occurrences[i].end || !bytes.Equal(occurrence.value, []byte(adapterGitHubToken)) {
								t.Fatal("exact collection changed occurrence source intervals")
							}
						}
						return
					}
					matcher, err := newBetterLeaksRewriteMatcher(nil, LogicalFragment{Kind: FragmentJSON, Raw: raw}, occurrences)
					if err != nil {
						t.Fatal("construct escaped-string bridge")
					}
					bridge, ok := matcher.(*betterLeaksRewriteMatcher)
					if !ok {
						t.Fatal("fixture matcher is not a BetterLeaks rewrite bridge")
					}
					redacted, findings, err := bridge.RedactString(t.Context(), decoded)
					if err != nil || len(findings) != 1 || findings[0].OccurrenceCount != count || strings.Contains(redacted, adapterGitHubToken) || bridge.validateCoverage() != nil {
						t.Fatalf("bridge lost complete repeated-occurrence redaction: findings=%d covered=%d expected=%d tokens=%d error=%t", len(findings), len(bridge.covered), count, len(bridge.jsonTokens), err != nil)
					}
				}
				allocated[index] = escapedMappingAllocatedBytes(t, invoke)
				if allocated[index] > uint64(len(raw))*32 {
					t.Errorf("matched escaped token retained excessive detail allocation: bytes=%d limit=%d", allocated[index], uint64(len(raw))*32)
				}
			}
			// Token size is fixed: twelve hits may add occurrence records and masks,
			// but must not reconstruct the token's entire boundary array twelve times.
			limit := allocated[0]*2 + 128<<10
			t.Logf("one_hit_bytes=%d twelve_hit_bytes=%d limit=%d", allocated[0], allocated[1], limit)
			if allocated[1] > limit {
				t.Fatal("escaped token mapping allocation scales with occurrence count")
			}
		})
	}
}

func TestHybridJSONEscapedStringRedactionAllocationTracksAdmittedBytes(t *testing.T) {
	raw := escapedCredentialJSON(DefaultScanMaxBytes, 12)
	g, services := scalarMappingRepairGuard(t)
	allocated := escapedMappingAllocatedBytes(t, func() {
		call := betterLeaksJSONCall(raw)
		decision, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, services)
		if err != nil || decision.Outcome != sdk.OutcomeRedacted || len(decision.Findings) != 1 || decision.Findings[0].OccurrenceCount != 12 || decision.Validate() != nil || bytes.Contains(call.Messages[0].Parts[0].Content, []byte(adapterGitHubToken)) {
			t.Fatal("full escaped-string hybrid redaction failed")
		}
	})
	// Full scanning, canonical rewriting and clone ownership remain included.
	// A giant mostly-ASCII escaped token must not retain per-byte int maps or
	// rebuild a second decoded token just to resolve twelve candidate spans.
	limit := uint64(len(raw)) * 40
	t.Logf("admitted_bytes=%d full_hybrid_redaction_bytes=%d limit=%d", len(raw), allocated, limit)
	if allocated > limit {
		t.Fatal("full escaped-string redaction retained excessive mapping allocation")
	}
}

func escapedCredentialJSON(size, count int) []byte {
	const prefix = `"\u0061 `
	tail := " " + strings.Repeat(adapterGitHubToken+" ", count) + `"`
	return []byte(prefix + strings.Repeat("x", size-len(prefix)-len(tail)) + tail)
}

func TestJSONEscapedMappingReusesBoundariesAndEarliestInverseIntervals(t *testing.T) {
	for _, raw := range [][]byte{[]byte(`"\u0061-café-\uD83D\uDE00-secret"`), []byte(`"\uD800-secret"`), {'"', 0xff, '-', 's', 'e', 'c', 'r', 'e', 't', '"'}} {
		p := jsonOccurrenceParser{raw: raw}
		mapping, err := p.streamingString()
		if err != nil {
			t.Fatal("parse inverse mapping control")
		}
		start := bytes.Index(mapping.decoded, []byte("secret"))
		if _, _, ok := mapping.rawRange(start, start+len("secret")); !ok {
			t.Fatal("materialize candidate mapping")
		}
		oracle, err := decodeJSONStringMapping(raw, 1, len(raw)-1)
		if err != nil {
			t.Fatal("decode detailed inverse oracle")
		}
		for index, want := range oracle.boundaries {
			if mapping.boundaryAt(index) != want {
				t.Fatal("compact mapping changed a decoded boundary")
			}
		}
		boundaries, spans := mapping.boundaries, mapping.spans
		for rawStart := 0; rawStart < len(raw)+1; rawStart++ {
			for rawEnd := 0; rawEnd < len(raw)+1; rawEnd++ {
				wantStart, wantEnd, wantOK := linearInverseMappingControl(oracle.boundaries, rawStart, rawEnd)
				gotStart, gotEnd, gotOK := mappingRawRangeToDecoded(&mapping, rawStart, rawEnd)
				if gotStart != wantStart || gotEnd != wantEnd || gotOK != wantOK {
					t.Fatal("indexed inverse lookup changed earliest boundary semantics")
				}
			}
		}
		allocs := testing.AllocsPerRun(1, func() {
			for range 256 {
				rawStart, rawEnd, ok := mapping.rawRange(start, start+len("secret"))
				decodedStart, decodedEnd, mapped := mappingRawRangeToDecoded(&mapping, rawStart, rawEnd)
				if !ok || !mapped || decodedStart != start || decodedEnd != start+len("secret") {
					t.Fatal("reused mapping lost a literal candidate")
				}
			}
		})
		if allocs != 0 || (len(boundaries) > 0 && &mapping.boundaries[0] != &boundaries[0]) || (len(spans) > 0 && &mapping.spans[0] != &spans[0]) || mapping.encoded != nil {
			t.Fatal("repeated mapping rebuilt token detail")
		}
	}
}

func linearInverseMappingControl(boundaries []int, rawStart, rawEnd int) (int, int, bool) {
	if rawEnd <= rawStart {
		return 0, 0, false
	}
	for start, value := range boundaries {
		if value != rawStart {
			continue
		}
		for end := start + 1; end < len(boundaries); end++ {
			if boundaries[end] == rawEnd {
				return start, end, true
			}
		}
	}
	return 0, 0, false
}

func TestJSONEscapedDenseExceptionMappingStaysBounded(t *testing.T) {
	for _, content := range []string{strings.Repeat(`\u0061`, 1024), strings.Repeat(`\uD800`, 1024), strings.Repeat("é", 1024) + `\n`, strings.Repeat(string([]byte{0xff}), 1024)} {
		raw := []byte(`"` + content + `"`)
		p := jsonOccurrenceParser{raw: raw}
		mapping, err := p.streamingString()
		if err != nil {
			t.Fatal("parse dense exception fixture")
		}
		oracle, err := decodeJSONStringMapping(raw, 1, len(raw)-1)
		if err != nil {
			t.Fatal("decode dense exception oracle")
		}
		allocated := escapedMappingAllocatedBytes(t, func() {
			candidate := mapping
			if !candidate.materializeBoundaries() || len(candidate.spans) != 0 || len(candidate.boundaries) != len(candidate.decoded)+1 {
				t.Fatal("dense exception mapping did not use bounded fallback")
			}
			for index, want := range oracle.boundaries {
				if candidate.boundaryAt(index) != want {
					t.Fatal("dense exception fallback changed a decoded boundary")
				}
			}
		})
		// Includes geometric sparse growth before switching, plus one final
		// dense map. It rejects keeping one four-int record per decoded rune.
		limit := uint64(len(mapping.decoded)+1) * 8 * 4
		t.Logf("decoded_bytes=%d detail_bytes=%d limit=%d", len(mapping.decoded), allocated, limit)
		if allocated > limit {
			t.Fatal("dense exceptions exceeded mapping allocation envelope")
		}
	}
}

func BenchmarkHybridJSONEscapedStringRedaction(b *testing.B) {
	for _, count := range []int{1, 12} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			raw := escapedCredentialJSON(DefaultScanMaxBytes, count)
			g, services := scalarMappingRepairGuard(b)
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for b.Loop() {
				call := betterLeaksJSONCall(raw)
				decision, err := g.Evaluate(b.Context(), &call, sdk.Meta{}, services)
				if err != nil || decision.Outcome != sdk.OutcomeRedacted || len(decision.Findings) != 1 || decision.Findings[0].OccurrenceCount != count || decision.Validate() != nil || bytes.Contains(call.Messages[0].Parts[0].Content, []byte(adapterGitHubToken)) {
					b.Fatal("full escaped-string hybrid redaction failed")
				}
			}
		})
	}
}

func escapedMappingAllocatedBytes(t *testing.T, invoke func()) uint64 {
	t.Helper()
	invoke()
	runtime.GC()
	previousLimit := debug.SetMemoryLimit(math.MaxInt64)
	previousGC := debug.SetGCPercent(-1)
	defer debug.SetMemoryLimit(previousLimit)
	defer debug.SetGCPercent(previousGC)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	invoke()
	runtime.ReadMemStats(&after)
	if before.NumGC != after.NumGC {
		t.Fatal("GC occurred during escaped mapping allocation measurement")
	}
	return after.TotalAlloc - before.TotalAlloc
}
