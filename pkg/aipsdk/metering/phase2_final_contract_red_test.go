package metering_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
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
		{name: "decimal", field: "coefficient", decode: func(wire []byte) error {
			var value metering.Decimal
			return json.Unmarshal(wire, &value)
		}},
		{name: "dimension", field: "name", decode: func(wire []byte) error {
			var value metering.Dimension
			return json.Unmarshal(wire, &value)
		}},
		{name: "component key", field: "component", decode: func(wire []byte) error {
			var value metering.ComponentKey
			return json.Unmarshal(wire, &value)
		}},
		{name: "component relationship", field: "kind", decode: func(wire []byte) error {
			var value metering.ComponentRelationship
			return json.Unmarshal(wire, &value)
		}},
		{name: "component schema", field: "id", decode: func(wire []byte) error {
			var value metering.ComponentSchema
			return json.Unmarshal(wire, &value)
		}},
		{name: "subject", field: "store_id", decode: func(wire []byte) error {
			var value metering.SubjectRef
			return json.Unmarshal(wire, &value)
		}},
		{name: "correlation", field: "store_id", decode: func(wire []byte) error {
			var value metering.CorrelationV2
			return json.Unmarshal(wire, &value)
		}},
		{name: "observation ref", field: "store_id", decode: func(wire []byte) error {
			var value metering.ObservationRef
			return json.Unmarshal(wire, &value)
		}},
		{name: "measure", field: "quality", decode: func(wire []byte) error {
			var value metering.Measure
			return json.Unmarshal(wire, &value)
		}},
		{name: "charge ref", field: "store_id", decode: func(wire []byte) error {
			var value metering.ChargeRef
			return json.Unmarshal(wire, &value)
		}},
		{name: "charge coverage", field: "relation", decode: func(wire []byte) error {
			var value metering.ChargeCoverageRef
			return json.Unmarshal(wire, &value)
		}},
		{name: "reported charge", field: "charge_item_id", decode: func(wire []byte) error {
			var value metering.ReportedCharge
			return json.Unmarshal(wire, &value)
		}},
		{name: "payment party", field: "id", decode: func(wire []byte) error {
			var value metering.PaymentParty
			return json.Unmarshal(wire, &value)
		}},
		{name: "observation", field: "id", decode: func(wire []byte) error {
			var value metering.Observation
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

	wire, err := json.Marshal(metering.ComponentKey{
		Direction: metering.DirectionInput,
		Component: "custom:�",
		Unit:      metering.UnitByte,
		SchemaID:  "phase2:v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded metering.ComponentKey
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("valid U+FFFD identity must remain decodable: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("valid U+FFFD identity must remain valid: %v", err)
	}
}
