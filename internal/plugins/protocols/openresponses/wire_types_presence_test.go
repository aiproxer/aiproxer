package openresponses

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The usage wire types gained presence tracking so an explicitly surfaced zero
// counter is distinguishable from an omitted one. These tables pin the decoded
// values and presence flags together; presence alone would not catch a counter
// that decodes to the wrong number.

func TestWireUsage_UnmarshalPresenceDisambiguation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		want WireUsage
	}{
		{
			name: "explicit zero counters are present",
			json: `{"input_tokens":0,"output_tokens":0,"total_tokens":0}`,
			want: WireUsage{
				InputTokensPresent:  true,
				OutputTokensPresent: true,
				TotalTokensPresent:  true,
			},
		},
		{
			name: "omitted counters stay absent",
			json: `{"input_tokens":7}`,
			want: WireUsage{
				InputTokens:        7,
				InputTokensPresent: true,
			},
		},
		{
			name: "input detail zero counters tracked independently",
			json: `{"input_tokens_details":{"cached_tokens":0,"text_tokens":0,"audio_tokens":0,"images":0}}`,
			want: WireUsage{
				InputTokensDetails: WireUsageInputDetails{
					CachedTokensPresent: true,
					TextTokensPresent:   true,
					AudioTokensPresent:  true,
					ImagesPresent:       true,
				},
			},
		},
		{
			name: "output detail counters including images decode and track presence",
			json: `{"output_tokens_details":{"reasoning_tokens":0,"text_tokens":11,"audio_tokens":2,"images":3}}`,
			want: WireUsage{
				OutputTokensDetails: WireUsageOutputDetails{
					TextTokens:             11,
					AudioTokens:            2,
					Images:                 3,
					ReasoningTokensPresent: true,
					TextTokensPresent:      true,
					AudioTokensPresent:     true,
					ImagesPresent:          true,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got WireUsage
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatalf("Unmarshal(%s) returned unexpected error: %v", tc.json, err)
			}

			if string(got.RawJSON) != tc.json {
				t.Fatalf("RawJSON mismatch: got %q want %q", got.RawJSON, tc.json)
			}
			got.RawJSON = nil
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decoded WireUsage mismatch\n got: %+v\nwant: %+v", got, tc.want)
			}
		})
	}
}

func TestWireUsage_UnmarshalRejectsNonObject(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{`1`, `[]`, `"usage"`} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			var got WireUsage
			if err := json.Unmarshal([]byte(raw), &got); err == nil {
				t.Fatalf("expected error decoding non-object usage %s", raw)
			}
		})
	}
}

func TestWireUsageDetails_UnmarshalRejectsNonObject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		raw   string
		alloc func() any
	}{
		{
			name:  "input details",
			raw:   `1`,
			alloc: func() any { return new(WireUsageInputDetails) },
		},
		{
			name:  "output details",
			raw:   `"x"`,
			alloc: func() any { return new(WireUsageOutputDetails) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := json.Unmarshal([]byte(tc.raw), tc.alloc()); err == nil {
				t.Fatalf("expected error decoding non-object %s from %s", tc.name, tc.raw)
			}
		})
	}
}

func TestWireResponseResource_UsagePresence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		json             string
		wantUsagePresent bool
		wantErr          bool
	}{
		{
			name:             "usage omitted marks absent",
			json:             `{"id":"resp_1","object":"response"}`,
			wantUsagePresent: false,
		},
		{
			name:             "usage null marks absent",
			json:             `{"id":"resp_1","usage":null}`,
			wantUsagePresent: false,
		},
		{
			name:             "usage empty object marks absent",
			json:             `{"id":"resp_1","usage":{}}`,
			wantUsagePresent: false,
		},
		{
			name:             "usage explicit zero counters count as evidence",
			json:             `{"id":"resp_1","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`,
			wantUsagePresent: true,
		},
		{
			name:             "usage presence-only zero detail counts as evidence",
			json:             `{"id":"resp_1","usage":{"input_tokens_details":{"cached_tokens":0}}}`,
			wantUsagePresent: true,
		},
		{
			name:             "usage nonzero value counts as evidence",
			json:             `{"id":"resp_1","usage":{"total_tokens":12}}`,
			wantUsagePresent: true,
		},
		{
			name:    "non-object usage is rejected",
			json:    `{"id":"resp_1","usage":"nope"}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got WireResponseResource
			err := json.Unmarshal([]byte(tc.json), &got)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected decode error for %s", tc.json)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal(%s) returned unexpected error: %v", tc.json, err)
			}
			if got.UsagePresent != tc.wantUsagePresent {
				t.Fatalf("UsagePresent = %v, want %v", got.UsagePresent, tc.wantUsagePresent)
			}
			if string(got.RawJSON) != tc.json {
				t.Fatalf("RawJSON mismatch: got %q want %q", got.RawJSON, tc.json)
			}
		})
	}
}

func TestWireCompactResource_UsagePresence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		json             string
		wantUsagePresent bool
		wantErr          bool
	}{
		{
			name:             "usage omitted marks absent",
			json:             `{"id":"comp_1","object":"response.compaction"}`,
			wantUsagePresent: false,
		},
		{
			name:             "usage null marks absent",
			json:             `{"id":"comp_1","usage":null}`,
			wantUsagePresent: false,
		},
		{
			name:             "usage empty object marks absent",
			json:             `{"id":"comp_1","usage":{}}`,
			wantUsagePresent: false,
		},
		{
			name:             "usage explicit zero counters count as evidence",
			json:             `{"id":"comp_1","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`,
			wantUsagePresent: true,
		},
		{
			name:             "usage explicit zero output detail counts as evidence",
			json:             `{"id":"comp_1","usage":{"output_tokens_details":{"reasoning_tokens":0}}}`,
			wantUsagePresent: true,
		},
		{
			name:    "non-object usage is rejected",
			json:    `{"id":"comp_1","usage":[1,2]}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var got WireCompactResource
			err := json.Unmarshal([]byte(tc.json), &got)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected decode error for %s", tc.json)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal(%s) returned unexpected error: %v", tc.json, err)
			}
			if got.UsagePresent != tc.wantUsagePresent {
				t.Fatalf("UsagePresent = %v, want %v", got.UsagePresent, tc.wantUsagePresent)
			}
			if string(got.RawJSON) != tc.json {
				t.Fatalf("RawJSON mismatch: got %q want %q", got.RawJSON, tc.json)
			}
		})
	}
}

// TestWireUsagePresence_ValueAndPresenceCoexist locks one realistic payload so
// the presence flags and the decoded counters are asserted together.
func TestWireUsagePresence_ValueAndPresenceCoexist(t *testing.T) {
	t.Parallel()

	raw := `{"id":"resp_1","usage":{"input_tokens":3,"input_tokens_details":{"cached_tokens":1,"images":4},"output_tokens":5,"total_tokens":8}}`

	var res WireResponseResource
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.UsagePresent {
		t.Fatal("expected UsagePresent for nonzero usage")
	}
	if res.Usage.InputTokens != 3 || res.Usage.InputTokensDetails.CachedTokens != 1 ||
		res.Usage.InputTokensDetails.Images != 4 || res.Usage.OutputTokens != 5 ||
		res.Usage.TotalTokens != 8 {
		t.Fatalf("decoded counters mismatch: %+v", res.Usage)
	}
	if !res.Usage.InputTokensPresent || !res.Usage.InputTokensDetails.CachedTokensPresent ||
		!res.Usage.InputTokensDetails.ImagesPresent || !res.Usage.OutputTokensPresent ||
		!res.Usage.TotalTokensPresent {
		t.Fatalf("expected all surfaced counters present: %+v", res.Usage)
	}
	if res.Usage.OutputTokensDetails.ReasoningTokensPresent {
		t.Fatalf("reasoning_tokens was omitted and must not be present: %+v", res.Usage.OutputTokensDetails)
	}
}
