package secretguard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func manyExactFragments(n int) []exactPrivateFinding {
	out := make([]exactPrivateFinding, n)
	for i := range out {
		out[i] = exactPrivateFinding{
			finding:     sdk.Finding{Location: fmt.Sprintf("messages[%d].parts[0]", i), SecretRefName: "OPAQUE_KEY", SourceCategory: sdk.SourceCategoryProxyEnv},
			occurrences: []betterLeaksOccurrence{{value: []byte("opaque-secret-value"), fieldID: fmt.Sprint(i), span: betterLeaksSpan{1, 1, 1, 19}}},
		}
	}
	return out
}

func TestHybridMergeIndexedOverlapIdentity(t *testing.T) {
	exact := manyExactFragments(2)
	exact[1] = exact[0]
	exact[1].finding.SecretRefName = "OTHER_REFERENCE"
	exact[1].finding.Aliases = []string{"ALIAS"}
	exact[0].uncoveredCount = 2
	base := exact[0].occurrences[0]
	discovery := betterLeaksFinding{Location: exact[0].finding.Location, RuleID: "z-rule", Confidence: sdk.ConfidenceHigh, occurrences: []betterLeaksOccurrence{base}}
	winner := discovery
	winner.RuleID = "a-rule"
	got := mergePrivateHybridFindings(exact, []betterLeaksFinding{discovery, winner, discovery})
	if len(got) != 2 {
		t.Fatalf("overlap left %d groups", len(got))
	}
	for _, item := range got {
		if item.finding.RuleID != "a-rule" {
			t.Fatal("winning provenance was lost")
		}
		if item.finding.SecretRefName == "OPAQUE_KEY" && item.finding.OccurrenceCount != 3 {
			t.Fatal("fallback count lost")
		}
		if item.finding.SecretRefName == "OTHER_REFERENCE" && item.finding.OccurrenceCount != 1 {
			t.Fatal("duplicate overlap counted twice")
		}
	}
	for _, mismatch := range []string{"value", "field", "span", "location"} {
		t.Run(mismatch, func(t *testing.T) {
			input := discovery
			occurrence := base
			switch mismatch {
			case "value":
				occurrence.value = []byte("different-value")
			case "field":
				occurrence.fieldID = "different-field"
			case "span":
				occurrence.span.StartColumn++
			case "location":
				input.Location = "messages[1].parts[0]"
			}
			input.occurrences = []betterLeaksOccurrence{occurrence}
			if len(mergePrivateHybridFindings(exact[:1], []betterLeaksFinding{input})) != 2 {
				t.Fatal("different concrete identity overlapped")
			}
		})
	}
}

func TestHybridMergeAccumulationOwnsAndDeduplicatesOccurrences(t *testing.T) {
	exact := manyExactFragments(4096)
	for i := range exact {
		exact[i].finding.Location = "messages[0].parts[0]"
	}
	exact = append(exact, exact...)
	got := mergePrivateHybridFindings(exact, nil)
	if len(got) != 1 || got[0].finding.OccurrenceCount != 4096 {
		t.Fatal("accumulated duplicates changed cardinality")
	}
	before := bytes.Clone(got[0].occurrences[0].value)
	exact[0].occurrences[0].value[0] = 'X'
	if !bytes.Equal(got[0].occurrences[0].value, before) {
		t.Fatal("merged value aliases input bytes")
	}
}

func manyExactFragmentCall(tb testing.TB, n int) (aipapi.Call, sdk.Matcher, *GenerationServices) {
	tb.Helper()
	const secret = "opaque-secret-value"
	catalog, err := engine.BuildCatalog([]engine.CatalogInput{{Name: "OPAQUE_KEY", Value: secret, SourceCategory: sdk.SourceCategoryProxyEnv}}, 8)
	if err != nil {
		tb.Fatal(err)
	}
	generation, err := BuildGenerationServices(DetectorPolicy{BetterLeaks: benchmarkBetterLeaksPolicy()}, nil)
	if err != nil {
		tb.Fatal(err)
	}
	call := aipapi.Call{Messages: make([]aipapi.Message, n)}
	for i := range call.Messages {
		call.Messages[i] = aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: secret}}}
	}
	if err := call.Validate(); err != nil {
		tb.Fatal(err)
	}
	return call, engine.AsMatcher(engine.NewMatcher(catalog)), generation
}

func checkManyExactFragmentCall(tb testing.TB, call *aipapi.Call, matcher sdk.Matcher, generation *GenerationServices) {
	tb.Helper()
	out, err := scanCall(tb.Context(), call, matcher, modeScan, DefaultScanMaxBytes, generation)
	if err != nil || out.ScanLimitHit || len(out.discoveryFindings) != 0 || len(out.Findings) != len(call.Messages) {
		tb.Fatalf("invalid exact-only-hit hybrid corpus: err=%v, limit=%v, discovery=%d, findings=%d", err, out.ScanLimitHit, len(out.discoveryFindings), len(out.Findings))
	}
	for _, finding := range out.Findings {
		if finding.DetectorID != sdk.DetectorIDExact || finding.OccurrenceCount != 1 {
			tb.Fatal("wrong exact attribution")
		}
	}
}

func TestHybridMergeManyFragmentCallCorpus(t *testing.T) {
	call, matcher, generation := manyExactFragmentCall(t, 2048)
	checkManyExactFragmentCall(t, &call, matcher, generation)
}

func TestHybridMergeManyFragmentCallAllocationBound(t *testing.T) {
	const n = 2048
	call, matcher, generation := manyExactFragmentCall(t, n)
	result := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		for b.Loop() {
			checkManyExactFragmentCall(b, &call, matcher, generation)
		}
	})
	t.Logf("per call: %d bytes, %d allocations", result.AllocedBytesPerOp(), result.AllocsPerOp())
	// Bytes catch repeated rebuilding of the accumulated safe-finding index,
	// whose map buckets can grow quadratically without quadratic object counts.
	if got := result.AllocedBytesPerOp(); got > 16384*n {
		t.Fatalf("bytes per enabled hybrid call = %d, want <= %d", got, 16384*n)
	}
}

func TestScanOutcomeIndexedFindingsPreserveExactAttribution(t *testing.T) {
	first := sdk.Finding{Location: "initial", SecretRefName: "KEY", Aliases: []string{"FIRST"}, SourceCategory: sdk.SourceCategoryProxyEnv, OccurrenceCount: 2}
	for _, preloaded := range []bool{false, true} {
		out := scanOutcome{}
		var want []sdk.Finding
		if preloaded {
			out.Findings = []sdk.Finding{first}
			want = []sdk.Finding{first}
		}
		for i := range 4096 {
			finding := first
			finding.Aliases = []string{"LATER"}
			finding.SourceCategory = sdk.SourceCategoryRequestCred
			location := fmt.Sprintf("messages[%d]", i%17)
			if i%2 == 0 {
				location = "initial"
			}
			out.mergeFindingsAt([]sdk.Finding{finding}, location)
			finding.Location = location
			want = mergeFindings(want, []sdk.Finding{finding})
		}
		out.mergeFindingsAt(nil, "empty")
		if !reflect.DeepEqual(out.Findings, want) {
			t.Fatal("persistent index changed exact count/order/first attribution")
		}
	}
}

func TestScanCall_RejectsInconsistentJSONMirrorBeforeScanning(t *testing.T) {
	const secret = "opaque-secret-value"
	raw := `{"value":"` + secret + `"}`
	for _, tc := range []struct {
		name string
		mode scanMode
	}{
		{name: "scan", mode: modeScan},
		{name: "log", mode: modeLogScan},
		{name: "redact", mode: modeRedact},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := baseCall()
			call.Messages = nil
			call.Instructions = nil
			call.Items = []aipapi.Item{{
				Kind: aipapi.ItemKindMessage,
				Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{
					Kind:       aipapi.ContentPartJSON,
					Text:       raw,
					Annotation: &aipapi.AnnotationPart{Type: "json_content", Data: json.RawMessage(`{"value":"different"}`)},
				}},
			}}
			if err := call.Validate(); err != nil {
				t.Fatal(err)
			}
			before := aipapi.CloneCall(call)
			out, err := scanCall(t.Context(), &call, newExactStub(secret, "SECRET", sdk.SourceCategoryProxyEnv), tc.mode, DefaultScanMaxBytes)
			if err == nil {
				t.Fatalf("mode %v accepted an inconsistent JSON mirror", tc.mode)
			}
			if len(out.Findings) != 0 || out.MutationCount != 0 || out.BytesScanned != 0 || out.ScanLimitHit || len(out.exactPrivateFindings) != 0 || len(out.discoveryFindings) != 0 {
				t.Fatalf("mirror rejection left scan state: %+v", out)
			}
			if !reflect.DeepEqual(call, before) {
				t.Fatal("mirror rejection mutated the call")
			}
		})
	}
}

func BenchmarkHybridMergeManyFragmentCall(b *testing.B) {
	for _, n := range []int{256, 1024, 4096} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			call, matcher, generation := manyExactFragmentCall(b, n)
			checkManyExactFragmentCall(b, &call, matcher, generation)
			b.ReportAllocs()
			for b.Loop() {
				checkManyExactFragmentCall(b, &call, matcher, generation)
			}
		})
	}
}

func BenchmarkHybridMergeDuplicateOverlapReports(b *testing.B) {
	for _, n := range []int{256, 1024, 4096} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			exact := manyExactFragments(n)
			for i := range exact {
				exact[i].finding.Location = "messages[0]"
				exact[i].finding.SecretRefName = fmt.Sprint(i)
				exact[i].occurrences = exact[0].occurrences
			}
			discovery := make([]betterLeaksFinding, n)
			for i := range discovery {
				discovery[i] = betterLeaksFinding{Location: "messages[0]", RuleID: "same-rule", occurrences: exact[0].occurrences}
			}
			b.ReportAllocs()
			for b.Loop() {
				if len(mergePrivateHybridFindings(exact, discovery)) != n {
					b.Fatal("wrong overlap cardinality")
				}
			}
		})
	}
}

func TestHybridMergeManyExactFragmentsAllocationBound(t *testing.T) {
	const n = 2048
	exact := manyExactFragments(n)
	// Non-empty aliases make every group key carry alias bytes, so the old
	// repeated group-key construction is observable as allocations. With empty
	// aliases the same quadratic construction allocates nothing and the bound
	// cannot reject it.
	for i := range exact {
		exact[i].finding.Aliases = []string{fmt.Sprintf("alias-%d", i)}
	}
	allocs := testing.AllocsPerRun(1, func() {
		got := mergePrivateHybridFindings(exact, nil)
		if len(got) != n {
			t.Fatalf("got %d groups, want %d", len(got), n)
		}
		for _, item := range got {
			if item.finding.OccurrenceCount != 1 {
				t.Fatal("exact occurrence lost")
			}
		}
	})
	t.Logf("allocations per merge = %.0f, bound = %d", allocs, 20*n)
	// Linear allowance for owned occurrence bytes, indexing and sorted projection.
	// The old repeated group-key construction allocates over two million objects.
	if allocs > 20*n {
		t.Fatalf("allocations per merge = %.0f, want <= %d", allocs, 20*n)
	}
}

func TestHybridMergeGroupKeyBoundaries(t *testing.T) {
	for _, aliases := range [][][]string{{{"A\x00B"}, {"A", "B"}}, {{}, {""}}} {
		exact := manyExactFragments(2)
		exact[1].finding = exact[0].finding
		exact[0].finding.Aliases, exact[1].finding.Aliases = aliases[0], aliases[1]
		if got := mergePrivateHybridFindings(exact, nil); len(got) != 2 {
			t.Fatalf("distinct alias lists collapsed into %d groups", len(got))
		}
	}
}

func BenchmarkHybridMergeManyExactFragments(b *testing.B) {
	for _, n := range []int{256, 1024, 4096} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			exact := manyExactFragments(n)
			b.ReportAllocs()
			for b.Loop() {
				if len(mergePrivateHybridFindings(exact, nil)) != n {
					b.Fatal("lost groups")
				}
			}
		})
	}
}

func BenchmarkHybridMergeAccumulatedOccurrences(b *testing.B) {
	for _, n := range []int{256, 1024, 4096} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			exact := manyExactFragments(n)
			for i := range exact {
				exact[i].finding.Location = "messages[0].parts[0]"
			}
			b.ReportAllocs()
			for b.Loop() {
				got := mergePrivateHybridFindings(exact, nil)
				if len(got) != 1 || got[0].finding.OccurrenceCount != n {
					b.Fatal("lost occurrences")
				}
			}
		})
	}
}
