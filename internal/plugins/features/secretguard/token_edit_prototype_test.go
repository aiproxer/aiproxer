package secretguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// Research-only: replacements cover complete JSON string-value tokens. This
// intentionally does not implement detector attribution or SDK matcher policy.
type prototypeTokenEdit struct {
	start, end            int
	original, replacement []byte
}

// This compares validation/application mechanisms with precomputed proposals,
// not complete Guard.Evaluate or discovery/attribution policy.
func BenchmarkTokenEditPrototype(b *testing.B) {
	for _, size := range []int{1024, 2 << 20} {
		for _, hit := range []bool{false, true} {
			for _, implementation := range []string{"bridge", "edits"} {
				b.Run(fmt.Sprintf("bytes=%d/hit=%t/%s", size, hit, implementation), func(b *testing.B) {
					raw := append([]byte(`["`), bytes.Repeat([]byte{'x'}, size-len(`[""]`)-len("secret"))...)
					value := "public"
					if hit {
						value = "secret"
					}
					start := len(raw)
					raw = append(append(raw, value...), []byte(`"]`)...)
					var edits []prototypeTokenEdit
					var occurrences []betterLeaksOccurrence
					want := bytes.Clone(raw)
					if hit {
						copy(want[start:start+len(value)], "******")
						edits = []prototypeTokenEdit{{start: 1, end: len(raw) - 1, original: raw[1 : len(raw)-1], replacement: want[1 : len(want)-1]}}
						occurrences = []betterLeaksOccurrence{{value: []byte(value), start: start, end: start + len(value), offsetsValid: true, ruleID: "fixture"}}
					}
					fragment := LogicalFragment{Kind: FragmentJSON, Raw: raw}
					exact := newExactStub("absent", "EXACT", sdk.SourceCategoryProxyEnv)
					b.ReportAllocs()
					for b.Loop() {
						var out []byte
						var err error
						if implementation == "edits" {
							out, err = prototypeApplyTokenEdits(b.Context(), raw, edits)
						} else {
							matcher, buildErr := newBetterLeaksRewriteMatcher(exact, fragment, occurrences)
							if buildErr != nil {
								b.Fatal(buildErr)
							}
							out, _, err = redactJSONPayload(b.Context(), matcher, raw)
							if bridge, ok := matcher.(*betterLeaksRewriteMatcher); ok && bridge.validateCoverage() != nil {
								b.Fatal("bridge lost coverage")
							}
						}
						if err != nil || !bytes.Equal(out, want) {
							b.Fatal("mechanism benchmark changed expected output")
						}
					}
				})
			}
		}
	}
}

func prototypeApplyTokenEdits(ctx context.Context, raw []byte, edits []prototypeTokenEdit) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !json.Valid(raw) {
		return nil, errBetterLeaksUnrewritable
	}
	ordered := append([]prototypeTokenEdit(nil), edits...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].start < ordered[j].start })
	covered := make([]bool, len(ordered))
	previous := 0
	for _, edit := range ordered {
		if edit.start < previous || edit.end <= edit.start || edit.end > len(raw) || !bytes.Equal(raw[edit.start:edit.end], edit.original) {
			return nil, errBetterLeaksUnrewritable
		}
		var replacement string
		if json.Unmarshal(edit.replacement, &replacement) != nil || len(edit.replacement) == 0 || edit.replacement[0] != '"' {
			return nil, errBetterLeaksUnrewritable
		}
		previous = edit.end
	}
	// Prove membership in effective string values, not keys/scalars or discarded
	// duplicate-key shadows. Candidate-local indexes replace traversal counters,
	// but canonical membership and occurrence coverage cannot be removed.
	err := walkJSONOccurrenceTokens(raw, func(mapping jsonStringMapping, stringValue, key bool) bool {
		if !stringValue || key {
			return true
		}
		start, end, ok := mapping.rawRange(0, len(mapping.decoded))
		if ok {
			for i, edit := range ordered {
				if edit.start == start-1 && edit.end == end+1 {
					covered[i] = true
				}
			}
		}
		return ctx.Err() == nil
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(raw))
	previous = 0
	for i, edit := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !covered[i] {
			return nil, errBetterLeaksUnrewritable
		}
		out = append(out, raw[previous:edit.start]...)
		out = append(out, edit.replacement...)
		previous = edit.end
	}
	out = append(out, raw[previous:]...)
	if !json.Valid(out) {
		return nil, errBetterLeaksUnrewritable
	}
	return out, nil
}

func TestTokenEditPrototype_ValidatesMembershipAndPreservesUntouchedBytes(t *testing.T) {
	for _, raw := range []string{` {"b": 2, "a": "secret"} `, `{"a":"discarded","a":"secret"}`, `{"a":"\u0073ecret"}`} {
		start := bytes.LastIndexByte([]byte(raw[:len(raw)-2]), ':')
		for raw[start] != '"' {
			start++
		}
		end := start + 1 + bytes.IndexByte([]byte(raw[start+1:]), '"') + 1
		edit := prototypeTokenEdit{start: start, end: end, original: []byte(raw[start:end]), replacement: []byte(`"******"`)}
		out, err := prototypeApplyTokenEdits(t.Context(), []byte(raw), []prototypeTokenEdit{edit})
		want := raw[:start] + `"******"` + raw[end:]
		if err != nil || string(out) != want {
			t.Fatal("valid token edit changed unrelated bytes")
		}
		edit.original = []byte(`"wrong"`)
		if _, err := prototypeApplyTokenEdits(t.Context(), []byte(raw), []prototypeTokenEdit{edit}); !errors.Is(err, errBetterLeaksUnrewritable) {
			t.Fatal("unproven edit was accepted")
		}
	}
	for _, raw := range []string{`{"secret":1}`, `{"a":"secret","a":"safe"}`} {
		start := bytes.Index([]byte(raw), []byte(`"secret"`))
		edit := prototypeTokenEdit{start: start, end: start + len(`"secret"`), original: []byte(`"secret"`), replacement: []byte(`"******"`)}
		if _, err := prototypeApplyTokenEdits(t.Context(), []byte(raw), []prototypeTokenEdit{edit}); !errors.Is(err, errBetterLeaksUnrewritable) {
			t.Fatal("key or discarded shadow was accepted as an effective value")
		}
	}
}
