package jsonshape_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
)

func FuzzPreflight(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"x":1}`),
		[]byte(`{`),
		[]byte(`[[[[[[[[[[0]]]]]]]]]]`),
		[]byte(`[` + strings.Repeat(`1,`, 128) + `1]`),
		[]byte(`"` + strings.Repeat(`a`, 4096) + `"`),
		[]byte(`{"a":1,"a":2}`),
		[]byte(`123456789012345678901234567890`),
		[]byte("\"\xff\""),
		[]byte(`{}{}`),
		[]byte(`null`),
		[]byte(`{"outer":{"x":1,"x":2}}`),
		[]byte(`[1,2,3]`),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, limits := range []jsonshape.Limits{
			jsonshape.RequestEnvelopeLimits(),
			jsonshape.ToolArgumentsLimits(),
		} {
			result, err := jsonshape.Preflight(data, limits)
			if err != nil {
				msg := err.Error()
				// Errors must stay payload-free; dedicated canary tests cover leak regression.
				if strings.Contains(msg, "\xff") {
					t.Fatalf("error appears to leak invalid UTF-8 payload: %q", msg)
				}
				continue
			}
			if !json.Valid(data) {
				t.Fatalf("Preflight succeeded but encoding/json.Valid returned false")
			}
			if result.Bytes != len(data) || result.Tokens <= 0 || result.MaxDepth < 0 {
				t.Fatalf("unexpected result: %+v", result)
			}
		}
	})
}

// FuzzScannerDifferential tests incremental Scanner against PreflightWithContext oracle
// across whole and chunked feeds, comparing stable error Kind and aggregate counts.
func FuzzScannerDifferential(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"x":1}`),
		[]byte(`{`),
		[]byte(`}`),
		[]byte(`[`),
		[]byte(`]`),
		[]byte(`[[[[[[[[[[0]]]]]]]]]]`),
		[]byte(`[` + strings.Repeat(`1,`, 128) + `1]`),
		[]byte(`"` + strings.Repeat(`a`, 4096) + `"`),
		[]byte(`{"a":1,"a":2}`),
		[]byte(`123456789012345678901234567890`),
		[]byte("\"\xff\""),
		[]byte(`{}{}`),
		[]byte(`null`),
		[]byte(`true`),
		[]byte(`false`),
		[]byte(`0`),
		[]byte(`-1.5e+10`),
		[]byte(`{"outer":{"x":1,"x":2}}`),
		[]byte(`[1,2,3]`),
		[]byte(`{"msg":"Hello 世界! 🚀"}`),
		[]byte(`{"quote":"\"","esc":"\n\t\r\\/\b\f"}`),
		[]byte(`{"emoji":"\uD83D\uDE80"}`),
		[]byte(`{"empty":{}}`),
		[]byte(`{"a": 1} trailing`),
		[]byte(`{"incomplete": "value`),
		[]byte(`{"bad_num": 01}`),
		[]byte(``),
		[]byte(`   `),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		profiles := []jsonshape.Limits{
			jsonshape.RequestEnvelopeLimits(),
			jsonshape.ToolArgumentsLimits(),
			{
				MaxBytes:             512,
				MaxDepth:             8,
				MaxTokens:            64,
				MaxArrayElems:        16,
				MaxObjectKeys:        16,
				MaxStringBytes:       64,
				MaxKeyBytes:          16,
				MaxNumberBytes:       16,
				RejectDuplicateNames: true,
			},
		}

		isValidUTF8 := utf8.Valid(data)

		for _, limits := range profiles {
			// 1. Oracle: PreflightWithContext
			expRes, expErr := jsonshape.PreflightWithContext(context.Background(), data, limits)
			expKind := jsonshape.Classify(expErr)

			// Errors must stay payload-free
			if expErr != nil {
				msg := expErr.Error()
				if strings.Contains(msg, "\xff") {
					t.Fatalf("oracle error appears to leak payload: %q", msg)
				}
			}

			// 2. Scanner: whole feed
			scWhole := jsonshape.NewScanner(context.Background(), limits)
			wholeFeedErr := scWhole.Feed(data)
			var scWholeRes jsonshape.Result
			var scWholeErr error
			if wholeFeedErr != nil {
				scWholeErr = wholeFeedErr
			} else {
				scWholeRes, scWholeErr = scWhole.Finish()
			}
			scWholeKind := jsonshape.Classify(scWholeErr)

			if scWholeErr != nil {
				msg := scWholeErr.Error()
				if strings.Contains(msg, "\xff") {
					t.Fatalf("scanner error appears to leak payload: %q", msg)
				}
			}

			// 3. Scanner: chunked feed (1-byte chunks for short inputs, or partitioned chunks)
			scChunk := jsonshape.NewScanner(context.Background(), limits)
			var chunkErr error
			var chunkRes jsonshape.Result

			chunkSize := 1
			if len(data) > 32 {
				chunkSize = max(len(data)/4, 1)
			}

			for i := 0; i < len(data); i += chunkSize {
				end := min(i+chunkSize, len(data))
				if chunkErr = scChunk.Feed(data[i:end]); chunkErr != nil {
					break
				}
			}
			if chunkErr == nil {
				chunkRes, chunkErr = scChunk.Finish()
			}
			chunkKind := jsonshape.Classify(chunkErr)

			// Parity verification:
			// A. Success parity: both must succeed and produce identical aggregate facts
			if expErr == nil {
				if scWholeErr != nil {
					t.Fatalf("Oracle succeeded but scanner whole feed failed: %v for input %q", scWholeErr, data)
				}
				if chunkErr != nil {
					t.Fatalf("Oracle succeeded but scanner chunked feed failed: %v for input %q", chunkErr, data)
				}
				if scWholeRes.Bytes != expRes.Bytes || scWholeRes.Tokens != expRes.Tokens || scWholeRes.MaxDepth != expRes.MaxDepth {
					t.Fatalf("Result mismatch (whole feed) for input %q: got %+v, want %+v", data, scWholeRes, expRes)
				}
				if chunkRes.Bytes != expRes.Bytes || chunkRes.Tokens != expRes.Tokens || chunkRes.MaxDepth != expRes.MaxDepth {
					t.Fatalf("Result mismatch (chunked feed) for input %q: got %+v, want %+v", data, chunkRes, expRes)
				}
				continue
			}

			// B. Error parity: scanner must also fail
			if scWholeErr == nil {
				t.Fatalf("Oracle failed with %q, but scanner whole feed succeeded for input %q", expKind, data)
			}
			if chunkErr == nil {
				t.Fatalf("Oracle failed with %q, but scanner chunked feed succeeded for input %q", expKind, data)
			}

			// C. Classification parity:
			//
			// 1) Clean inputs & single error: oracle and scanner agree on exact Kind.
			//
			// 2) Multi-error ordering differences:
			// - If !isValidUTF8: Oracle scans the entire slice upfront and returns KindInvalidUTF8.
			//   Streaming Scanner evaluates left-to-right; if another violation (e.g. KindMalformed,
			//   KindDuplicateName, KindTooManyItems) occurs before the invalid UTF-8 byte, Scanner
			//   aborts at that earlier violation. Both guaranteed reject.
			// - If expKind == KindMalformed: when an invalid/incomplete token also violates a resource
			//   limit (e.g. "[0,0... ,A" exceeding MaxArrayElems, or "10000000000000000." exceeding MaxNumberBytes),
			//   batch encoding/json fails inside Token() with syntax error before preflight can count/inspect
			//   the scalar. Streaming Scanner enforces resource bounds eagerly as tokens begin.
			// - If expKind == KindTooLarge: batch oracle checks total slice len upfront. In chunked streaming,
			//   an early chunk may contain a fatal syntax error (KindMalformed) that halts scanning before
			//   subsequent chunks are received to exceed MaxBytes.
			// Both guaranteed reject.
			matchesKind := func(scK jsonshape.Kind) bool {
				if scK == expKind {
					return true
				}
				if !isValidUTF8 {
					return true
				}
				if expKind == jsonshape.KindMalformed {
					switch scK {
					case jsonshape.KindNumberTooLong,
						jsonshape.KindStringTooLong,
						jsonshape.KindKeyTooLong,
						jsonshape.KindTooManyItems,
						jsonshape.KindTooManyTokens,
						jsonshape.KindTooDeep:
						return true
					}
				}
				if expKind == jsonshape.KindTooLarge {
					return true
				}
				return false
			}

			if !matchesKind(scWholeKind) {
				t.Fatalf("Kind mismatch (whole feed) for input %q: got %q, want %q (scErr=%v, expErr=%v)",
					data, scWholeKind, expKind, scWholeErr, expErr)
			}
			if !matchesKind(chunkKind) {
				t.Fatalf("Kind mismatch (chunked feed) for input %q: got %q, want %q (chunkErr=%v, expErr=%v)",
					data, chunkKind, expKind, chunkErr, expErr)
			}
		}
	})
}
