package secretguard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

const scalarMappingCycle = "12345678901234567890123456789012345678901234567890,true,false,null,"

func scalarDenseJSON(size int) ([]byte, int) {
	const tail = `"` + adapterGitHubToken + `"]`
	cycles := (size - len(tail) - 1) / len(scalarMappingCycle)
	return []byte("[" + strings.Repeat(scalarMappingCycle, cycles) + tail), cycles * 4
}

func TestJSONScalarMappingRepair_SemanticsAndRawRanges(t *testing.T) {
	for _, raw := range []string{
		`[9007199254740993,-0,1.2300e+04,true,false,null,"\u0061ccepted-secret"] trailing ignored`,
		`{"z":false,"a":0,"a":1.00e-02,"nested":[null,true]}`,
		`1234567890123456789012345678901234567890 trailing`,
		`true trailing`, `false trailing`, `null trailing`,
		"[0,\ntrue,\tfalse,\rnull]",
	} {
		mappings, err := decodedJSONOccurrenceMappings([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		var canonical any
		if err := dec.Decode(&canonical); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, mapping := range mappings {
			got = append(got, string(mapping.decoded))
			start, end, ok := mapping.rawRange(0, len(mapping.decoded))
			if !ok || end > int(dec.InputOffset()) || start < 0 {
				t.Fatal("mapping escaped first-value bounds")
			}
			if string(mapping.decoded) == "accepted-secret" {
				if raw[start:end] != `\u0061ccepted-secret` {
					t.Fatal("escaped string mapping lost source range")
				}
			} else if raw[start:end] != string(mapping.decoded) {
				t.Fatal("scalar/key mapping changed original lexical spelling")
			}
		}
		var want []string
		canonicalScalarMappingTokens(canonical, &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("semantic traversal=%q; want %q", got, want)
		}
	}
}

func canonicalScalarMappingTokens(value any, out *[]string) {
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			*out = append(*out, key)
			canonicalScalarMappingTokens(value[key], out)
		}
	case []any:
		for _, child := range value {
			canonicalScalarMappingTokens(child, out)
		}
	case string:
		*out = append(*out, value)
	case json.Number:
		*out = append(*out, value.String())
	case bool:
		*out = append(*out, strconv.FormatBool(value))
	case nil:
		*out = append(*out, "null")
	}
}

func TestJSONScalarMappingRepair_OuterValidationRejectsMalformedFirstValue(t *testing.T) {
	for _, raw := range []string{`[01]`, `[1e]`, `[truefalse]`, `{"a":nul}`, `[-]`, `[1,]`, `{"a":false`, strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001)} {
		if _, err := decodedJSONOccurrenceMappings([]byte(raw)); err == nil {
			t.Fatal("outer validation accepted malformed or over-depth first value")
		}
	}
}

func TestJSONScalarMappingRepair_NearLimitBetterLeaksRedaction(t *testing.T) {
	raw, scalars := scalarDenseJSON(DefaultScanMaxBytes)
	if !json.Valid(raw) || len(raw) > DefaultScanMaxBytes || len(raw) < DefaultScanMaxBytes-128 || scalars < 100000 {
		t.Fatal("near-limit scalar-dense fixture invalid")
	}
	guard, services := scalarMappingRepairGuard(t)
	call := betterLeaksJSONCall(raw)
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
	decision, err := guard.Evaluate(t.Context(), &call, sdk.Meta{}, services)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != sdk.OutcomeRedacted || decision.MutationCount != 1 || len(decision.Findings) != 1 || decision.Findings[0].OccurrenceCount != 1 || decision.Findings[0].RuleID == "" {
		fatalDecisionSummary(t, decision)
	}
	if err := decision.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(call.Messages[0].Parts[0].Content) || bytes.Contains(call.Messages[0].Parts[0].Content, []byte(adapterGitHubToken)) {
		t.Fatal("near-limit rewrite invalid or credential leaked")
	}
	credentialStart := len(raw) - len(adapterGitHubToken) - len(`"]`)
	rewritten := call.Messages[0].Parts[0].Content
	if len(rewritten) != len(raw) || !bytes.Equal(rewritten[:credentialStart], raw[:credentialStart]) {
		t.Fatal("near-limit redaction changed scalar spelling or structure before the credential")
	}
	assertBetterLeaksDecisionSafe(t, decision)
}

func scalarMappingRepairGuard(tb testing.TB) (sdk.Guard, sdk.Services) {
	tb.Helper()
	generation, err := BuildGenerationServices(DetectorPolicy{BetterLeaks: BetterLeaksPolicy{
		Enabled: true, MinimumConfidence: DefaultBetterLeaksConfidence,
		MaxDecodeDepth: DefaultBetterLeaksDecodeDepth, Workers: 1, MaxFindings: DefaultBetterLeaksMaxFindings,
	}}, engine.NewDisabledSource())
	if err != nil {
		tb.Fatal(err)
	}
	// A real positional exact matcher exercises occurrence mapping and hybrid deduplication.
	catalog, err := engine.BuildCatalog([]engine.CatalogInput{{Name: "TAIL_CREDENTIAL", Value: adapterGitHubToken, SourceCategory: sdk.SourceCategoryRequestCred}}, 8)
	if err != nil {
		tb.Fatal(err)
	}
	return NewGuard(Config{Action: ActionRedact}), sdk.Services{Capability: generation, MatcherResolver: engine.NewStaticMatcherResolver(catalog, engine.MatcherOptions{})}
}

func TestJSONScalarMappingRepair_UnsupportedScalarsAndKeysBlock(t *testing.T) {
	for _, raw := range []string{`[true,"safe"]`, `{"12345678901234567890":"safe"}`, `[12345678901234567890,"safe"]`} {
		secret := "12345678901234567890"
		if strings.Contains(raw, "true") {
			secret = "true"
		}
		call := betterLeaksJSONCall([]byte(raw))
		before := aipapi.CloneCall(call)
		guard, services := newBetterLeaksActionGuard(t, ActionRedact, 0, 0)
		services.MatcherResolver = staticResolver{m: newExactStub(secret, "SCALAR_CANARY", sdk.SourceCategoryRequestCred)}
		decision, err := guard.Evaluate(t.Context(), &call, sdk.Meta{}, services)
		if err != nil {
			t.Fatal(err)
		}
		if decision.Outcome != sdk.OutcomeBlock || decision.FailureKind != FailureKindUnsupportedJSONToken || !reflect.DeepEqual(call, before) {
			fatalDecisionSummary(t, decision)
		}
	}
}

func BenchmarkJSONScalarMappingRepair(b *testing.B) {
	for _, size := range []int{16 << 10, 128 << 10, DefaultScanMaxBytes} {
		b.Run(fmt.Sprintf("mapping/%d", size), func(b *testing.B) {
			raw, scalars := scalarDenseJSON(size)
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				count := 0
				err := walkJSONOccurrenceTokens(raw, func(jsonStringMapping, bool, bool) bool { count++; return true })
				if err != nil || count != scalars+1 {
					b.Fatal("mapping failed")
				}
			}
		})
	}
	b.Run("actual_hybrid_redaction/2097152", func(b *testing.B) {
		raw, _ := scalarDenseJSON(DefaultScanMaxBytes)
		guard, services := scalarMappingRepairGuard(b)
		b.ReportAllocs()
		b.SetBytes(int64(len(raw)))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			call := betterLeaksJSONCall(raw)
			decision, err := guard.Evaluate(b.Context(), &call, sdk.Meta{}, services)
			if err != nil || decision.Outcome != sdk.OutcomeRedacted || len(decision.Findings) != 1 || decision.Findings[0].OccurrenceCount != 1 {
				b.Fatal("actual hybrid redaction failed")
			}
		}
	})
}

func FuzzJSONScalarMappingRepair_CanonicalFirstValue(f *testing.F) {
	f.Add([]byte(`[12345678901234567890,1.0e+02,true,false,null,"\u0061"] trailing`))
	f.Add([]byte(`{"a":1,"a":false,"b":null}`))
	f.Add([]byte(`{"\u0061":"discarded","a":["\uD800","café-\uD83D\uDE00",true]} trailing`))
	f.Add([]byte{'"', 0xff, '-', 's', 'e', 'c', 'r', 'e', 't', '"'})
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 16<<10 {
			return
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var canonical any
		canonicalErr := dec.Decode(&canonical)
		mappings, err := decodedJSONOccurrenceMappings(raw)
		if canonicalErr != nil {
			if err == nil {
				t.Fatal("mapping accepted invalid canonical first value")
			}
			return
		}
		if err != nil {
			t.Fatal("mapping rejected valid canonical first value")
		}
		var got, want []string
		for _, mapping := range mappings {
			got = append(got, string(mapping.decoded))
			oracle := mapping
			if mapping.encoded != nil {
				oracle, err = decodeJSONStringMapping(raw, mapping.rawStart, mapping.rawStart+len(mapping.encoded))
				if err != nil {
					t.Fatal("reference rejected valid string")
				}
			}
			if !mapping.materializeBoundaries() {
				t.Fatal("streaming candidate detail rejected valid token")
			}
			for boundary := 0; boundary <= len(mapping.decoded); boundary++ {
				if mapping.boundaryAt(boundary) != oracle.boundaryAt(boundary) {
					t.Fatal("streaming candidate detail differs from eager boundary oracle")
				}
			}
			if len(mapping.decoded) > 0 {
				start, end, ok := mapping.rawRange(0, len(mapping.decoded))
				if !ok || start < 0 || end > int(dec.InputOffset()) {
					t.Fatal("mapping escaped first-value bounds")
				}
			}
		}
		canonicalScalarMappingTokens(canonical, &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatal("mapping differs from canonical semantic tokens")
		}
	})
}
