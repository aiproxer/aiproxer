package economics_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

func TestPhase2FinalV2JSONDecodeRejectsMalformedUTF8BeforeReplacement(t *testing.T) {
	t.Parallel()

	malformed := func(field string, bad byte) []byte {
		wire := append([]byte(`{"`+field+`":"`), bad)
		return append(wire, []byte(`"}`)...)
	}

	tests := []struct {
		name   string
		field  string
		decode func([]byte) error
	}{
		{name: "rating input", field: "scope", decode: func(wire []byte) error {
			var value economics.RatingInput
			return json.Unmarshal(wire, &value)
		}},
		{name: "quote input", field: "scope", decode: func(wire []byte) error {
			var value economics.QuoteInput
			return json.Unmarshal(wire, &value)
		}},
		{name: "exposure quote", field: "id", decode: func(wire []byte) error {
			var value economics.ExposureQuote
			return json.Unmarshal(wire, &value)
		}},
		{name: "valuation", field: "id", decode: func(wire []byte) error {
			var value economics.Valuation
			return json.Unmarshal(wire, &value)
		}},
		{name: "statement batch", field: "statement_id", decode: func(wire []byte) error {
			var value economics.StatementBatch
			return json.Unmarshal(wire, &value)
		}},
		{name: "snapshot content ref", field: "content_ref", decode: func(wire []byte) error {
			var value economics.SnapshotContentRef
			return json.Unmarshal(wire, &value)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, bad := range []byte{0xff, 0xfe} {
				if err := test.decode(malformed(test.field, bad)); err == nil {
					t.Fatalf("malformed UTF-8 byte 0x%02x must be rejected before encoding/json replacement", bad)
				}
			}
		})
	}
}

func TestPhase2FinalV2JSONDecodeAllowsValidReplacementRune(t *testing.T) {
	t.Parallel()

	wire, err := json.Marshal(economics.SnapshotContentRef{
		ContentRef:  "store://phase2/�",
		ContentHash: "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded economics.SnapshotContentRef
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("valid U+FFFD reference must remain decodable: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("valid U+FFFD reference must remain valid: %v", err)
	}
}
