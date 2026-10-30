package secretguard

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

func TestHybridJSONRedactionAllocationTracksAdmittedBytes(t *testing.T) {
	raw, scalars := scalarDenseJSON(DefaultScanMaxBytes)
	g, services := scalarMappingRepairGuard(t)
	invoke := func() {
		call := betterLeaksJSONCall(raw)
		d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, services)
		if err != nil || d.Outcome != sdk.OutcomeRedacted || d.MutationCount != 1 || len(d.Findings) != 1 || d.Findings[0].OccurrenceCount != 1 {
			fatalDecisionSummary(t, d)
		}
		if err := d.Validate(); err != nil {
			t.Fatal("invalid hybrid redaction decision")
		}
		if bytes.Contains(call.Messages[0].Parts[0].Content, []byte(adapterGitHubToken)) {
			t.Fatal("hybrid JSON redaction left the credential")
		}
		assertBetterLeaksDecisionSafe(t, d)
	}
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
		t.Fatal("GC occurred during allocation measurement")
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	// The allowance covers full scanning, canonical decode/rewrite, cloning,
	// and private occurrence mapping. It rejects per-scalar retained mapping
	// trees and per-byte boundary arrays across repeated mapper traversals.
	limit := uint64(len(raw)) * 32
	t.Logf("admitted_bytes=%d scalars=%d full_hybrid_redaction_bytes=%d limit=%d", len(raw), scalars, allocated, limit)
	if allocated > limit {
		t.Fatalf("full hybrid JSON redaction allocation exceeded admitted-byte envelope: bytes=%d limit=%d", allocated, limit)
	}
}

func TestJSONFirstValueValidationAllocationTracksSyntaxOnly(t *testing.T) {
	raw, _ := scalarDenseJSON(DefaultScanMaxBytes)
	// Include trailing JSON whitespace: the returned interval must still end
	// at the first value, exactly as the canonical decoder's InputOffset does.
	raw = append(raw, ' ', '\t', '\r', '\n')
	dec := json.NewDecoder(bytes.NewReader(raw))
	var canonical json.RawMessage
	if err := dec.Decode(&canonical); err != nil {
		t.Fatal(err)
	}
	validate := func() {
		first, err := firstJSONOccurrenceValue(raw)
		if err != nil || len(first) != int(dec.InputOffset()) || !bytes.Equal(first, raw[:dec.InputOffset()]) {
			t.Fatal("validation changed the canonical first-value interval")
		}
	}
	validate()
	runtime.GC()
	previousLimit := debug.SetMemoryLimit(math.MaxInt64)
	previousGC := debug.SetGCPercent(-1)
	defer debug.SetMemoryLimit(previousLimit)
	defer debug.SetGCPercent(previousGC)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	validate()
	runtime.ReadMemStats(&after)
	if before.NumGC != after.NumGC {
		t.Fatal("GC occurred during validation allocation measurement")
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	// Syntax validation borrows the admitted bytes. A decoder-sized copy is
	// unnecessary for a complete value and is multiplied by mapper traversals.
	t.Logf("admitted_bytes=%d validation_bytes=%d", len(raw), allocated)
	if allocated > uint64(len(raw)) {
		t.Fatalf("syntax-only validation allocated an admitted-sized copy: bytes=%d admitted=%d", allocated, len(raw))
	}
}

func TestJSONStreamingMappingMatchesCanonicalTokens(t *testing.T) {
	t.Parallel()
	fixtures := [][]byte{
		[]byte(`[9007199254740993,-0,1.2300e+04,true,false,null,"\u0061-secret"] trailing`),
		[]byte(`{"z":[false,null],"a":0,"\u0061":{"later":1.00e-02,"early":"value"}}`),
		[]byte(`{"a":"discarded","a":"effective","nested":{"dup":0,"dup":true}}`),
		[]byte(`12345678901234567890trueignored`),
		[]byte(`"\uD800-replacement" trailing`),
		{'"', 0xff, '-', 's', 'e', 'c', 'r', 'e', 't', '"'},
	}
	for _, raw := range fixtures {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var canonical any
		if err := dec.Decode(&canonical); err != nil {
			t.Fatal("decode semantic control")
		}
		var got, want []string
		if err := walkJSONOccurrenceTokens(raw, func(mapping jsonStringMapping, _, _ bool) bool {
			got = append(got, string(mapping.decoded))
			start, end, ok := mapping.rawRange(0, len(mapping.decoded))
			if len(mapping.decoded) > 0 && (!ok || start < 0 || end > int(dec.InputOffset())) {
				t.Error("streamed mapping escaped the first-value interval")
			}
			return true
		}); err != nil {
			t.Fatal("streamed mapping rejected canonical first value")
		}
		canonicalScalarMappingTokens(canonical, &want)
		if !reflect.DeepEqual(got, want) {
			t.Error("streamed semantic tokens differ from canonical UseNumber traversal")
		}
	}
}

func TestJSONRewriteBridgeRetainsOnlyCandidateTokens(t *testing.T) {
	t.Parallel()
	raw, scalars := scalarDenseJSON(128 << 10)
	start := bytes.Index(raw, []byte(adapterGitHubToken))
	fragment := LogicalFragment{Kind: FragmentJSON, Raw: raw}
	matcher, err := newBetterLeaksRewriteMatcher(nil, fragment, []betterLeaksOccurrence{{
		value: []byte(adapterGitHubToken), start: start, end: start + len(adapterGitHubToken),
		offsetsValid: true, representation: betterLeaksOccurrenceLiteral,
	}})
	if err != nil {
		t.Fatal("construct candidate-directed bridge")
	}
	bridge, ok := matcher.(*betterLeaksRewriteMatcher)
	if !ok || len(bridge.jsonTokens) != 1 || bridge.jsonTokens[0].index != scalars {
		t.Fatal("bridge retained irrelevant scalar mappings or lost candidate traversal index")
	}
}

func TestJSONStreamingEscapedMappingsDeferDetailedBoundaries(t *testing.T) {
	t.Parallel()
	for _, raw := range [][]byte{[]byte(`"\u0061-secret"`), []byte(`"\uD800-secret"`), {'"', 0xff, '-', 's', 'e', 'c', 'r', 'e', 't', '"'}} {
		if err := walkJSONOccurrenceTokens(raw, func(mapping jsonStringMapping, _, _ bool) bool {
			if mapping.boundaries != nil || mapping.encoded == nil {
				t.Error("irrelevant escaped string retained detailed boundaries")
			}
			start := bytes.Index(mapping.decoded, []byte("secret"))
			rawStart, rawEnd, ok := mapping.rawRange(start, start+len("secret"))
			if !ok || !bytes.Equal(raw[rawStart:rawEnd], []byte("secret")) {
				t.Error("lazy mapping lost candidate byte interval")
			}
			return true
		}); err != nil {
			t.Fatal("stream replacement mapping rejected a canonical token")
		}
	}
}

func TestJSONStreamingDeferredContainersReuseValidatedSpanEnds(t *testing.T) {
	t.Parallel()
	const depth = 1000
	raw := []byte(strings.Repeat(`{"a":`, depth) + `[1e999,true,false,null,"tail"]` + strings.Repeat(`}`, depth))
	first, err := firstJSONOccurrenceValue(raw)
	if err != nil {
		t.Fatal("validate deep object fixture")
	}
	p := jsonOccurrenceParser{raw: first}
	var tokens int
	if _, err := p.walkTokens(func(jsonStringMapping, bool, bool) bool { tokens++; return true }); err != nil {
		t.Fatal("streamed deep object fixture failed")
	}
	if tokens != depth+5 || len(p.valueEnds) != depth {
		t.Fatal("deep object lost canonical tokens or deferred span reuse")
	}
}

func TestJSONOccurrenceCleanupPreservesBorrowedAdmittedBytes(t *testing.T) {
	t.Parallel()
	raw := []byte(`["` + adapterGitHubToken + `"]`)
	before := bytes.Clone(raw)
	matcher := positionalCredentialMatcher{secret: []byte(adapterGitHubToken), finding: sdk.Finding{SecretRefName: "REQUEST_KEY"}}
	occurrences, mapped := collectExactJSONOccurrencesMapped(matcher, raw, "field")
	if !mapped || len(occurrences) != 1 {
		t.Fatal("private mapping fixture lost its occurrence")
	}
	releaseBetterLeaksOccurrenceBytes([]betterLeaksFinding{{occurrences: occurrences}})
	if !bytes.Equal(raw, before) {
		t.Error("private occurrence cleanup cleared borrowed admitted bytes")
	}
}
