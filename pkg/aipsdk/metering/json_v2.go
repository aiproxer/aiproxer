package metering

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// decodeV2JSON checks the transport bytes before encoding/json has an
// opportunity to replace malformed UTF-8 in string values with U+FFFD. Each
// public V2 value object supplies an alias target so decoding cannot recurse
// through its own UnmarshalJSON method.
func decodeV2JSON[T any](data []byte, name string, target *T) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("metering: %s must be valid UTF-8", name)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("metering: %s: %v", name, err)
	}
	return nil
}

func (d *Dimension) UnmarshalJSON(data []byte) error {
	if d == nil {
		return fmt.Errorf("metering: dimension: nil destination")
	}
	var wire dimensionV2Wire
	if err := decodeV2JSON(data, "dimension", &wire); err != nil {
		return err
	}
	*d = Dimension(wire)
	return nil
}

type dimensionV2Wire Dimension

func (k *ComponentKey) UnmarshalJSON(data []byte) error {
	if k == nil {
		return fmt.Errorf("metering: component key: nil destination")
	}
	var wire componentKeyV2Wire
	if err := decodeV2JSON(data, "component key", &wire); err != nil {
		return err
	}
	*k = ComponentKey(wire)
	return nil
}

type componentKeyV2Wire ComponentKey

func (r *ComponentRelationship) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("metering: component relationship: nil destination")
	}
	var wire componentRelationshipV2Wire
	if err := decodeV2JSON(data, "component relationship", &wire); err != nil {
		return err
	}
	*r = ComponentRelationship(wire)
	return nil
}

type componentRelationshipV2Wire ComponentRelationship

func (s *ComponentSchema) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("metering: component schema: nil destination")
	}
	var wire componentSchemaV2Wire
	if err := decodeV2JSON(data, "component schema", &wire); err != nil {
		return err
	}
	*s = ComponentSchema(wire)
	return nil
}

type componentSchemaV2Wire ComponentSchema

func (s *SubjectRef) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("metering: subject: nil destination")
	}
	var wire subjectRefV2Wire
	if err := decodeV2JSON(data, "subject", &wire); err != nil {
		return err
	}
	*s = SubjectRef(wire)
	return nil
}

type subjectRefV2Wire SubjectRef

func (c *CorrelationV2) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("metering: correlation: nil destination")
	}
	var wire correlationV2Wire
	if err := decodeV2JSON(data, "correlation", &wire); err != nil {
		return err
	}
	*c = CorrelationV2(wire)
	return nil
}

type correlationV2Wire CorrelationV2

func (r *ObservationRef) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("metering: observation ref: nil destination")
	}
	var wire observationRefV2Wire
	if err := decodeV2JSON(data, "observation ref", &wire); err != nil {
		return err
	}
	*r = ObservationRef(wire)
	return nil
}

type observationRefV2Wire ObservationRef

func (m *Measure) UnmarshalJSON(data []byte) error {
	if m == nil {
		return fmt.Errorf("metering: measure: nil destination")
	}
	var wire measureV2Wire
	if err := decodeV2JSON(data, "measure", &wire); err != nil {
		return err
	}
	*m = Measure(wire)
	return nil
}

type measureV2Wire Measure

func (r *ChargeRef) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("metering: charge ref: nil destination")
	}
	var wire chargeRefV2Wire
	if err := decodeV2JSON(data, "charge ref", &wire); err != nil {
		return err
	}
	*r = ChargeRef(wire)
	return nil
}

type chargeRefV2Wire ChargeRef

func (r *ChargeCoverageRef) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("metering: charge coverage: nil destination")
	}
	var wire chargeCoverageRefV2Wire
	if err := decodeV2JSON(data, "charge coverage", &wire); err != nil {
		return err
	}
	*r = ChargeCoverageRef(wire)
	return nil
}

type chargeCoverageRefV2Wire ChargeCoverageRef

func (c *ReportedCharge) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("metering: reported charge: nil destination")
	}
	var wire reportedChargeV2Wire
	if err := decodeV2JSON(data, "reported charge", &wire); err != nil {
		return err
	}
	*c = ReportedCharge(wire)
	return nil
}

type reportedChargeV2Wire ReportedCharge

func (p *PaymentParty) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("metering: payment party: nil destination")
	}
	var wire paymentPartyV2Wire
	if err := decodeV2JSON(data, "payment party", &wire); err != nil {
		return err
	}
	*p = PaymentParty(wire)
	return nil
}

type paymentPartyV2Wire PaymentParty
