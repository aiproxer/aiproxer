package reasoningpreservation_test

import (
	"crypto/sha256"
	"encoding/json"
	"maps"
	"math"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func surrogateParams(expected []int, sourceBytes int) reasoningpreservation.SurrogateDecodeParams {
	return reasoningpreservation.SurrogateDecodeParams{
		ExpectedIndexes:     expected,
		SourceBytes:         sourceBytes,
		MaxSurrogateBytes:   1024,
		MinSavedBytes:       1,
		MinSavingsRatio:     0.01,
		OriginalDigest:      sha256.Sum256([]byte("orig")),
		PolicyRevision:      "v1",
		Sanitization:        "none",
		SemanticDigest:      sha256.Sum256([]byte("semantic")),
		EgressPolicyHash:    sha256.Sum256([]byte("egress")),
		AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
	}
}

func marshalSurrogate(t *testing.T, schema int, segs []map[string]any, extraFields map[string]any) []byte {
	t.Helper()
	m := map[string]any{
		"schema_version": schema,
		"segments":       segs,
	}
	maps.Copy(m, extraFields)
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return b
}

func mustMarshalSurrogate(schema int, segs []map[string]any, extraFields map[string]any) []byte {
	m := map[string]any{
		"schema_version": schema,
		"segments":       segs,
	}
	maps.Copy(m, extraFields)
	b, _ := json.Marshal(m)
	return b
}

func TestSurrogateDecoder_HappyPath_Count2(t *testing.T) {
	t.Parallel()
	params := surrogateParams([]int{0, 2}, 100)
	raw := marshalSurrogate(t, 1, []map[string]any{
		{"index": 0, "text": "hello"},
		{"index": 2, "text": "world"},
	}, nil)
	sur, outcome, err := reasoningpreservation.DecodeSurrogate(raw, params)
	require.NoError(t, err)
	assert.Equal(t, reasoningpreservation.OutcomeSurrogateDecoded, outcome)
	require.Len(t, sur.Segments, 2)
	assert.Equal(t, 10, sur.Bytes) // 5+5
	assert.Equal(t, "v1", sur.PolicyRevision)
	assert.Equal(t, "none", sur.Sanitization)
	// One result covers expected local indexes exactly.
	idxs := map[int]bool{}
	for _, s := range sur.Segments {
		idxs[s.PlacementIndex] = true
	}
	assert.True(t, idxs[0])
	assert.True(t, idxs[2])
	// Segments sorted ascending.
	assert.Equal(t, 0, sur.Segments[0].PlacementIndex)
	assert.Equal(t, 2, sur.Segments[1].PlacementIndex)
}

func TestSurrogateDecoder_TableValidation(t *testing.T) {
	t.Parallel()
	baseParams := surrogateParams([]int{0, 2}, 100)
	cases := []struct {
		name        string
		mut         func([]byte) []byte
		params      reasoningpreservation.SurrogateDecodeParams
		wantOutcome reasoningpreservation.SurrogateDecodeOutcome
		wantErr     error
	}{
		{
			name: "unknown_field_rejected",
			mut: func(b []byte) []byte {
				// add unknown field
				var m map[string]any
				_ = json.Unmarshal(b, &m)
				m["unknown"] = "field"
				nb, _ := json.Marshal(m)
				return nb
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeDecodeInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateDecodeInvalid,
		},
		{
			name: "schema_version_wrong",
			mut: func(b []byte) []byte {
				var m map[string]any
				_ = json.Unmarshal(b, &m)
				m["schema_version"] = 2
				nb, _ := json.Marshal(m)
				return nb
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeSchemaInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateSchemaInvalid,
		},
		{
			name: "duplicate_index",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a"}, {"index": 0, "text": "b"}}, nil)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeSchemaInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateSchemaInvalid,
		},
		{
			name: "missing_index",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a"}}, nil)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeSchemaInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateSchemaInvalid,
		},
		{
			name: "unexpected_index",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a"}, {"index": 5, "text": "b"}}, nil)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeSchemaInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateSchemaInvalid,
		},
		{
			name: "empty_text",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": ""}, {"index": 2, "text": "b"}}, nil)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeControlInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateControlInvalid,
		},
		{
			name: "whitespace_text",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "   "}, {"index": 2, "text": "b"}}, nil)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeControlInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateControlInvalid,
		},
		{
			name: "control_char",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "hello\x00world"}, {"index": 2, "text": "b"}}, nil)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeControlInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateControlInvalid,
		},
		{
			name: "disallowed_control_0x1f",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a\x1f"}, {"index": 2, "text": "b"}}, nil)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeControlInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateControlInvalid,
		},
		{
			name: "allowed_newline_tab",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a\nb\tc"}, {"index": 2, "text": "d\re"}}, nil)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeSurrogateDecoded,
			wantErr:     nil,
		},
		{
			name: "invalid_utf8_via_raw",
			mut: func(b []byte) []byte {
				// raw bytes with invalid UTF8 not via json marshal
				return []byte("{\"schema_version\":1,\"segments\":[{\"index\":0,\"text\":\"\xff\"},{\"index\":2,\"text\":\"b\"}]}")
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeControlInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateControlInvalid,
		},
		{
			name: "malformed_json",
			mut: func(b []byte) []byte {
				return []byte("{not json")
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeDecodeInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateDecodeInvalid,
		},
		{
			name: "trailing_json",
			mut: func(b []byte) []byte {
				return append(b, []byte(" {\"schema_version\":1,\"segments\":[]}")...)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeDecodeInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateDecodeInvalid,
		},
		{
			name: "surrogate_oversize_per_segment",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": strings.Repeat("a", 20)}, {"index": 2, "text": "b"}}, nil)
			},
			params: func() reasoningpreservation.SurrogateDecodeParams {
				p := baseParams
				p.MaxSurrogateBytes = 10
				return p
			}(),
			wantOutcome: reasoningpreservation.OutcomeSurrogateOversize,
			wantErr:     reasoningpreservation.ErrSurrogateOversize,
		},
		{
			name: "surrogate_oversize_aggregate",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": strings.Repeat("a", 6)}, {"index": 2, "text": strings.Repeat("b", 6)}}, nil)
			},
			params: func() reasoningpreservation.SurrogateDecodeParams {
				p := baseParams
				p.MaxSurrogateBytes = 10
				return p
			}(),
			wantOutcome: reasoningpreservation.OutcomeSurrogateOversize,
			wantErr:     reasoningpreservation.ErrSurrogateOversize,
		},
		{
			name: "hard_ceiling_per_segment",
			mut: func(b []byte) []byte {
				big := strings.Repeat("a", reasoningpreservation.HardCompressionMaxSurrogateBytes+1)
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": big}, {"index": 2, "text": "b"}}, nil)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeSurrogateOversize,
			wantErr:     reasoningpreservation.ErrSurrogateOversize,
		},
		{
			name: "insufficient_savings_not_strictly_smaller",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": strings.Repeat("a", 50)}, {"index": 2, "text": strings.Repeat("b", 50)}}, nil)
			},
			params: func() reasoningpreservation.SurrogateDecodeParams {
				p := baseParams
				p.SourceBytes = 100 // decoded 100 == source not smaller
				return p
			}(),
			wantOutcome: reasoningpreservation.OutcomeInsufficientSavings,
			wantErr:     reasoningpreservation.ErrSurrogateInsufficientSavings,
		},
		{
			name: "insufficient_min_saved",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "hello"}, {"index": 2, "text": "world"}}, nil) // decoded 10, source 11 => saved 1
			},
			params: func() reasoningpreservation.SurrogateDecodeParams {
				p := baseParams
				p.SourceBytes = 11
				p.MinSavedBytes = 5
				return p
			}(),
			wantOutcome: reasoningpreservation.OutcomeInsufficientSavings,
			wantErr:     reasoningpreservation.ErrSurrogateInsufficientSavings,
		},
		{
			name: "insufficient_ratio",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": strings.Repeat("a", 40)}, {"index": 2, "text": strings.Repeat("b", 40)}}, nil) // decoded 80, source 100 => saved 20 ratio 0.2
			},
			params: func() reasoningpreservation.SurrogateDecodeParams {
				p := baseParams
				p.SourceBytes = 100
				p.MinSavedBytes = 1
				p.MinSavingsRatio = 0.5
				return p
			}(),
			wantOutcome: reasoningpreservation.OutcomeInsufficientSavings,
			wantErr:     reasoningpreservation.ErrSurrogateInsufficientSavings,
		},
		{
			name: "ratio_NaN_defensive",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a"}, {"index": 2, "text": "b"}}, nil)
			},
			params: func() reasoningpreservation.SurrogateDecodeParams {
				p := baseParams
				p.MinSavingsRatio = math.NaN()
				return p
			}(),
			wantOutcome: reasoningpreservation.OutcomeInsufficientSavings,
			wantErr:     reasoningpreservation.ErrSurrogateInsufficientSavings,
		},
		{
			name: "ratio_Inf_defensive",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a"}, {"index": 2, "text": "b"}}, nil)
			},
			params: func() reasoningpreservation.SurrogateDecodeParams {
				p := baseParams
				p.MinSavingsRatio = math.Inf(1)
				return p
			}(),
			wantOutcome: reasoningpreservation.OutcomeInsufficientSavings,
			wantErr:     reasoningpreservation.ErrSurrogateInsufficientSavings,
		},
		{
			name: "unknown_field_in_segment",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a", "extra": "field"}, {"index": 2, "text": "b"}}, nil)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeDecodeInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateDecodeInvalid,
		},
		{
			name: "empty_raw",
			mut: func(b []byte) []byte {
				return []byte("")
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeDecodeInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateDecodeInvalid,
		},
		{
			name: "count_mismatch_single",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a"}}, nil)
			},
			params:      baseParams,
			wantOutcome: reasoningpreservation.OutcomeSchemaInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateSchemaInvalid,
		},
		{
			name: "max_surrogate_zero_rejected",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a"}, {"index": 2, "text": "b"}}, nil)
			},
			params: func() reasoningpreservation.SurrogateDecodeParams {
				p := baseParams
				p.MaxSurrogateBytes = 0
				return p
			}(),
			wantOutcome: reasoningpreservation.OutcomeSurrogateOversize,
			wantErr:     reasoningpreservation.ErrSurrogateOversize,
		},
		{
			name: "max_surrogate_negative_rejected",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a"}, {"index": 2, "text": "b"}}, nil)
			},
			params: func() reasoningpreservation.SurrogateDecodeParams {
				p := baseParams
				p.MaxSurrogateBytes = -1
				return p
			}(),
			wantOutcome: reasoningpreservation.OutcomeSurrogateOversize,
			wantErr:     reasoningpreservation.ErrSurrogateOversize,
		},
		{
			name: "source_bytes_zero_rejected",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a"}, {"index": 2, "text": "b"}}, nil)
			},
			params: func() reasoningpreservation.SurrogateDecodeParams {
				p := baseParams
				p.SourceBytes = 0
				return p
			}(),
			wantOutcome: reasoningpreservation.OutcomeSchemaInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateSchemaInvalid,
		},
		{
			name: "source_bytes_negative_rejected",
			mut: func(b []byte) []byte {
				return mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "a"}, {"index": 2, "text": "b"}}, nil)
			},
			params: func() reasoningpreservation.SurrogateDecodeParams {
				p := baseParams
				p.SourceBytes = -5
				return p
			}(),
			wantOutcome: reasoningpreservation.OutcomeSchemaInvalid,
			wantErr:     reasoningpreservation.ErrSurrogateSchemaInvalid,
		},
	}
	// base raw for mut that expects base
	baseRaw := mustMarshalSurrogate(1, []map[string]any{{"index": 0, "text": "hello"}, {"index": 2, "text": "world"}}, nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := tc.mut(baseRaw)
			// Special case for allowed newline/tab we need to ensure outcome decoded handling with correct params
			if tc.name == "allowed_newline_tab" {
				// decoded has newline/tab, source must be larger
				_, outcome, err := reasoningpreservation.DecodeSurrogate(raw, tc.params)
				require.NoError(t, err)
				assert.Equal(t, tc.wantOutcome, outcome)
				return
			}
			_, outcome, err := reasoningpreservation.DecodeSurrogate(raw, tc.params)
			if tc.wantErr == nil {
				require.NoError(t, err)
				assert.Equal(t, tc.wantOutcome, outcome)
			} else {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Equal(t, tc.wantOutcome, outcome)
				// content-free: error must not contain surrogate text
				assert.NotContains(t, err.Error(), "hello")
				assert.NotContains(t, err.Error(), "world")
			}
		})
	}
}

func TestSurrogateDecoder_OverflowSafeMath(t *testing.T) {
	t.Parallel()
	// large source, small decoded, ensure no overflow with int64 path
	params := surrogateParams([]int{0}, 2000000000)
	params.MinSavedBytes = 100
	params.MinSavingsRatio = 0.01
	raw := marshalSurrogate(t, 1, []map[string]any{{"index": 0, "text": "x"}}, nil)
	sur, outcome, err := reasoningpreservation.DecodeSurrogate(raw, params)
	require.NoError(t, err)
	assert.Equal(t, reasoningpreservation.OutcomeSurrogateDecoded, outcome)
	assert.Equal(t, 1, sur.Bytes)
}

func TestSurrogateDecoder_TrailingWhitespaceAllowed(t *testing.T) {
	t.Parallel()
	params := surrogateParams([]int{0, 2}, 100)
	raw := marshalSurrogate(t, 1, []map[string]any{{"index": 0, "text": "a"}, {"index": 2, "text": "b"}}, nil)
	raw = append(raw, []byte("   \n\t  ")...)
	sur, outcome, err := reasoningpreservation.DecodeSurrogate(raw, params)
	require.NoError(t, err)
	assert.Equal(t, reasoningpreservation.OutcomeSurrogateDecoded, outcome)
	assert.Equal(t, 2, len(sur.Segments))
}

func TestSurrogateDecoder_CorrelatedFields(t *testing.T) {
	t.Parallel()
	orig := sha256.Sum256([]byte("orig2"))
	sem := sha256.Sum256([]byte("sem2"))
	eg := sha256.Sum256([]byte("eg2"))
	params := reasoningpreservation.SurrogateDecodeParams{
		ExpectedIndexes:     []int{1},
		SourceBytes:         50,
		MaxSurrogateBytes:   1024,
		MinSavedBytes:       1,
		MinSavingsRatio:     0.01,
		OriginalDigest:      orig,
		PolicyRevision:      "rev-5",
		Sanitization:        "redacted",
		SemanticDigest:      sem,
		EgressPolicyHash:    eg,
		AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
	}
	raw := marshalSurrogate(t, 1, []map[string]any{{"index": 1, "text": "compressed"}}, nil)
	sur, _, err := reasoningpreservation.DecodeSurrogate(raw, params)
	require.NoError(t, err)
	assert.Equal(t, orig, sur.OriginalDigest)
	assert.Equal(t, "rev-5", sur.PolicyRevision)
	assert.Equal(t, "redacted", sur.Sanitization)
	assert.Equal(t, sem, sur.SemanticDigest)
	assert.Equal(t, eg, sur.EgressPolicyHash)
	assert.Equal(t, 10, sur.Bytes)
	require.Len(t, sur.Segments, 1)
	assert.Equal(t, 1, sur.Segments[0].PlacementIndex)
	assert.Equal(t, "compressed", sur.Segments[0].Text)
	assert.Equal(t, 10, sur.Segments[0].Bytes)
}

func TestSurrogateDecoder_RawBoundHardCeiling(t *testing.T) {
	t.Parallel()
	params := surrogateParams([]int{0}, 100)
	raw := make([]byte, reasoningpreservation.HardRawOutputCeiling+1)
	for i := range raw {
		raw[i] = 'a'
	}
	_, outcome, err := reasoningpreservation.DecodeSurrogate(raw, params)
	require.Error(t, err)
	assert.Equal(t, reasoningpreservation.OutcomeSurrogateOversize, outcome)
	assert.ErrorIs(t, err, reasoningpreservation.ErrSurrogateOversize)
}

func FuzzSurrogateDecoder(f *testing.F) {
	f.Add([]byte(`{"schema_version":1,"segments":[{"index":0,"text":"hello"}]}`), 100)
	f.Add([]byte(`{"schema_version":1,"segments":[{"index":0,"text":"a"},{"index":2,"text":"b"}]}`), 100)
	f.Add([]byte(`{"schema_version":2,"segments":[]}`), 10)
	f.Add([]byte(`not json`), 10)
	f.Add([]byte(`{"schema_version":1,"segments":[{"index":0,"text":"\u0000"}]}`), 10)
	f.Fuzz(func(t *testing.T, raw []byte, sourceBytes int) {
		if len(raw) > reasoningpreservation.HardRawOutputCeiling+100 {
			return
		}
		if sourceBytes < 0 {
			sourceBytes = -sourceBytes
		}
		if sourceBytes > 10*1024*1024 {
			sourceBytes = 10 * 1024 * 1024
		}
		params := reasoningpreservation.SurrogateDecodeParams{
			ExpectedIndexes:     []int{0},
			SourceBytes:         sourceBytes,
			MaxSurrogateBytes:   1024,
			MinSavedBytes:       1,
			MinSavingsRatio:     0.1,
			OriginalDigest:      sha256.Sum256([]byte("orig")),
			PolicyRevision:      "v1",
			Sanitization:        "none",
			SemanticDigest:      sha256.Sum256([]byte("sem")),
			EgressPolicyHash:    sha256.Sum256([]byte("eg")),
			AuthorizedRouteHash: sha256.Sum256([]byte("test-route")),
		}
		sur, outcome, err := reasoningpreservation.DecodeSurrogate(raw, params)
		if err != nil {
			// error must be one of typed taxonomy and outcome matches
			switch outcome {
			case reasoningpreservation.OutcomeDecodeInvalid, reasoningpreservation.OutcomeSchemaInvalid, reasoningpreservation.OutcomeControlInvalid, reasoningpreservation.OutcomeSurrogateOversize, reasoningpreservation.OutcomeInsufficientSavings:
				// ok
			default:
				t.Fatalf("unexpected outcome %q for err %v", outcome, err)
			}
			// error must not leak raw content beyond content-free bounds (can't assert exact but ensure no panic)
			if err.Error() == "" {
				t.Fatalf("empty error")
			}
			return
		}
		// success path
		if outcome != reasoningpreservation.OutcomeSurrogateDecoded {
			t.Fatalf("success must have decoded outcome, got %q", outcome)
		}
		if len(sur.Segments) == 0 {
			t.Fatalf("decoded surrogate must have segments")
		}
		if sur.Bytes <= 0 || sur.Bytes > params.MaxSurrogateBytes {
			t.Fatalf("bytes %d out of bounds max %d", sur.Bytes, params.MaxSurrogateBytes)
		}
	})
}
