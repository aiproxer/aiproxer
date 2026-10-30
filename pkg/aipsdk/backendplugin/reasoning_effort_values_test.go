package backendplugin

import "testing"

func TestReasoningEffortValuesValidate(t *testing.T) {
	t.Parallel()
	valid := &ReasoningEffortValuesV1{ModelID: "gpt-6-astra", Supported: []string{"low", "high"}, Exhaustive: true}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid declaration rejected: %v", err)
	}
	cases := map[string]*ReasoningEffortValuesV1{
		"nil is no statement": nil,
		"empty model":         {ModelID: "", Supported: []string{"low"}},
		"blank model":         {ModelID: "   ", Supported: []string{"low"}},
		"blank value":         {ModelID: "m", Supported: []string{""}},
		"whitespace value":    {ModelID: "m", Supported: []string{"  "}},
		"control value":       {ModelID: "m", Supported: []string{"hi\x01"}},
		"control model":       {ModelID: "m\x7f", Supported: []string{"low"}},
		"duplicate":           {ModelID: "m", Supported: []string{"low", "low"}},
		"too many":            {ModelID: "m", Supported: make([]string, 33)},
	}
	for name, decl := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if decl == nil {
				if err := decl.Validate(); err != nil {
					t.Fatalf("nil must be valid (no statement): %v", err)
				}
				return
			}
			if name == "too many" {
				for i := range decl.Supported {
					decl.Supported[i] = "v"
				}
				// Make unique to isolate the count bound.
				for i := range decl.Supported {
					decl.Supported[i] = "v" + string(rune('a'+i%26)) + string(rune('0'+i/26))
				}
			}
			if err := decl.Validate(); err == nil {
				t.Fatalf("expected validation error for %q", name)
			}
		})
	}
	if err := (&ReasoningEffortValuesV1{ModelID: "m", Supported: []string{string([]byte{0xff, 0xfe})}}).Validate(); err == nil {
		t.Fatalf("expected invalid UTF-8 rejection")
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	if err := (&ReasoningEffortValuesV1{ModelID: "m", Supported: []string{string(long)}}).Validate(); err == nil {
		t.Fatalf("expected overlong value rejection")
	}
	longModel := make([]byte, 257)
	for i := range longModel {
		longModel[i] = 'a'
	}
	if err := (&ReasoningEffortValuesV1{ModelID: string(longModel)}).Validate(); err == nil {
		t.Fatalf("expected overlong model rejection")
	}
	// Empty exhaustive is a valid explicit "supports no graded values" claim.
	if err := (&ReasoningEffortValuesV1{ModelID: "m", Exhaustive: true}).Validate(); err != nil {
		t.Fatalf("empty exhaustive must be valid: %v", err)
	}
}

func TestReasoningEffortValuesClassify(t *testing.T) {
	t.Parallel()
	decl := &ReasoningEffortValuesV1{ModelID: "gpt-6-astra", Supported: []string{"low", "high"}, Exhaustive: true}
	if sup, decided := ClassifyEffortSupport(decl, "gpt-6-astra", "high"); !decided || !sup {
		t.Fatalf("expected supported decided, got %v %v", sup, decided)
	}
	if sup, decided := ClassifyEffortSupport(decl, "gpt-6-astra", "none"); !decided || sup {
		t.Fatalf("expected unsupported decided, got %v %v", sup, decided)
	}
	partial := &ReasoningEffortValuesV1{ModelID: "m", Supported: []string{"low"}}
	if _, decided := ClassifyEffortSupport(partial, "m", "high"); decided {
		t.Fatalf("partial omission must be undecided (unknown)")
	}
	if _, decided := ClassifyEffortSupport(nil, "m", "high"); decided {
		t.Fatalf("nil must be undecided")
	}
	if _, decided := ClassifyEffortSupport(decl, "other", "high"); decided {
		t.Fatalf("model mismatch must be undecided")
	}
	if sup, decided := ClassifyEffortSupport(decl, "gpt-6-astra", "HIGH"); !decided || sup {
		t.Fatalf("exact case-sensitive mismatch with exhaustive must be unsupported, got %v %v", sup, decided)
	}
	if _, decided := ClassifyEffortSupport(partial, "m", "LOW"); decided {
		t.Fatalf("case mismatch without exhaustive must be undecided")
	}
	if _, decided := ClassifyEffortSupport(decl, "gpt-6-astra", ""); decided {
		t.Fatalf("empty effort must be undecided")
	}
}

func TestReasoningEffortValuesNegotiated(t *testing.T) {
	t.Parallel()
	neg := Negotiation{Compatible: true, NegotiatedMinor: ProtocolMinorReasoningEffortValues, EnabledFeatures: []string{FeatureReasoningEffortValues}}
	if !ReasoningEffortValuesNegotiated(neg) {
		t.Fatalf("expected negotiated")
	}
	old := Negotiation{Compatible: true, NegotiatedMinor: ProtocolMinorReasoningEffortValues - 1, EnabledFeatures: []string{FeatureReasoningEffortValues}}
	if ReasoningEffortValuesNegotiated(old) {
		t.Fatalf("minor too low must not enable")
	}
	missing := Negotiation{Compatible: true, NegotiatedMinor: ProtocolMinorReasoningEffortValues, EnabledFeatures: nil}
	if ReasoningEffortValuesNegotiated(missing) {
		t.Fatalf("missing feature must not enable")
	}
}
