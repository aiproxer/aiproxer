package economics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"testing"
)

// FuzzSupportAdvisoryValuationJSON checks conservation of advisory identities,
// canonical replay and ownership after decoding bounded, untrusted JSON.
func FuzzSupportAdvisoryValuationJSON(f *testing.F) {
	legacy := preAdvisoryValuationFixture()
	enabled := supportAdvisoryFixture()
	clean := enabled.Clone()
	clean.SupportAdvisory = nil
	incomplete := enabled.Clone()
	incomplete.SupportAdvisory.IncompleteContexts = []SupportAdvisoryIncomplete{{
		ContextKey: incomplete.SupportAdvisoryContexts[0].Key(), Reason: SupportAdvisoryPairLimit,
	}}
	for _, seed := range []Valuation{legacy, enabled, clean, incomplete} {
		// Bypass the canonical serializer so the seed corpus also contains
		// unsorted dimensions and represents independent raw wire input.
		payload, err := json.Marshal(valuationWire(seed))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(payload)
	}
	f.Add([]byte(`{"support_advisory_contexts":[{"version":"future"}]}`))
	f.Add([]byte(`{"support_advisory":{"pairs":[{}]}}`))

	f.Fuzz(func(t *testing.T, payload []byte) {
		// Bound the fuzz harness's decode allocation independently of the
		// published DTO cardinality checks, which run before canonical copies.
		if len(payload) > 1<<20 {
			return
		}
		var input Valuation
		if err := json.Unmarshal(payload, &input); err != nil || input.Validate() != nil {
			return
		}
		before, err := json.Marshal(valuationWire(input))
		if err != nil {
			t.Fatal(err)
		}
		identities := advisoryFuzzIdentities(input)
		canonical, err := input.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var decoded Valuation
		if err := json.Unmarshal(canonical, &decoded); err != nil {
			t.Fatal(err)
		}
		if err := decoded.Validate(); err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(identities, advisoryFuzzIdentities(decoded)) {
			t.Fatal("canonical wire round-trip lost or invented advisory identities")
		}
		replayed, err := decoded.CanonicalJSON()
		if err != nil || !bytes.Equal(canonical, replayed) {
			t.Fatalf("canonical replay changed: %v", err)
		}
		after, err := json.Marshal(valuationWire(input))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("serialization mutated caller-owned input")
		}
		clone := decoded.Clone()
		if len(clone.SupportAdvisoryContexts) != 0 {
			clone.SupportAdvisoryContexts[0].Tariff.ID = "mutated-fuzz-clone"
		}
		if clone.SupportAdvisory != nil {
			if len(clone.SupportAdvisory.Pairs) != 0 {
				clone.SupportAdvisory.Pairs[0].Left.Component = "mutated-fuzz-clone"
				if len(clone.SupportAdvisory.Pairs[0].Left.Dimensions) != 0 {
					clone.SupportAdvisory.Pairs[0].Left.Dimensions[0].Value = "mutated-fuzz-clone"
				}
			}
			if len(clone.SupportAdvisory.IncompleteContexts) != 0 {
				clone.SupportAdvisory.IncompleteContexts[0].Reason = "mutated-fuzz-clone"
			}
		}
		if !maps.Equal(identities, advisoryFuzzIdentities(decoded)) {
			t.Fatal("clone aliases the decoded advisory")
		}
	})
}

func advisoryFuzzIdentities(v Valuation) map[string]bool {
	set := make(map[string]bool)
	for _, context := range v.SupportAdvisoryContexts {
		set[fmt.Sprintf("context:%q", context.Key())] = true
	}
	if v.SupportAdvisory == nil {
		return set
	}
	for _, pair := range v.SupportAdvisory.Pairs {
		left, right := string(pair.Left.CanonicalBytes()), string(pair.Right.CanonicalBytes())
		if left > right {
			left, right = right, left
		}
		set[fmt.Sprintf("pair:%q", []string{pair.ContextKey, pair.ScopeKey, left, right})] = true
	}
	for _, incomplete := range v.SupportAdvisory.IncompleteContexts {
		set[fmt.Sprintf("incomplete:%q", []string{incomplete.ContextKey, string(incomplete.Reason)})] = true
	}
	return set
}
