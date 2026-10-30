package economics

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
		return fmt.Errorf("economics: %s must be valid UTF-8", name)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("economics: %s: %v", name, err)
	}
	return nil
}

func (in *RatingInput) UnmarshalJSON(data []byte) error {
	if in == nil {
		return fmt.Errorf("economics: rating input: nil destination")
	}
	var wire ratingInputV2Wire
	if err := decodeV2JSON(data, "rating input", &wire); err != nil {
		return err
	}
	*in = RatingInput(wire)
	return nil
}

type ratingInputV2Wire RatingInput

func (in *QuoteInput) UnmarshalJSON(data []byte) error {
	if in == nil {
		return fmt.Errorf("economics: quote input: nil destination")
	}
	var wire quoteInputV2Wire
	if err := decodeV2JSON(data, "quote input", &wire); err != nil {
		return err
	}
	*in = QuoteInput(wire)
	return nil
}

type quoteInputV2Wire QuoteInput

func (l *Limit) UnmarshalJSON(data []byte) error {
	if l == nil {
		return fmt.Errorf("economics: limit: nil destination")
	}
	var wire limitV2Wire
	if err := decodeV2JSON(data, "limit", &wire); err != nil {
		return err
	}
	*l = Limit(wire)
	return nil
}

type limitV2Wire Limit

func (a *QuoteAssumption) UnmarshalJSON(data []byte) error {
	if a == nil {
		return fmt.Errorf("economics: quote assumption: nil destination")
	}
	var wire quoteAssumptionV2Wire
	if err := decodeV2JSON(data, "quote assumption", &wire); err != nil {
		return err
	}
	*a = QuoteAssumption(wire)
	return nil
}

type quoteAssumptionV2Wire QuoteAssumption

func (c *EvidenceCapability) UnmarshalJSON(data []byte) error {
	if c == nil {
		return fmt.Errorf("economics: evidence capability: nil destination")
	}
	var wire evidenceCapabilityV2Wire
	if err := decodeV2JSON(data, "evidence capability", &wire); err != nil {
		return err
	}
	*c = EvidenceCapability(wire)
	return nil
}

type evidenceCapabilityV2Wire EvidenceCapability

func (b *UnitBound) UnmarshalJSON(data []byte) error {
	if b == nil {
		return fmt.Errorf("economics: unit bound: nil destination")
	}
	var wire unitBoundV2Wire
	if err := decodeV2JSON(data, "unit bound", &wire); err != nil {
		return err
	}
	*b = UnitBound(wire)
	return nil
}

type unitBoundV2Wire UnitBound

func (q *ExposureQuote) UnmarshalJSON(data []byte) error {
	if q == nil {
		return fmt.Errorf("economics: exposure quote: nil destination")
	}
	var wire exposureQuoteV2Wire
	if err := decodeV2JSON(data, "exposure quote", &wire); err != nil {
		return err
	}
	*q = ExposureQuote(wire)
	return nil
}

type exposureQuoteV2Wire ExposureQuote

func (l *StatementLine) UnmarshalJSON(data []byte) error {
	if l == nil {
		return fmt.Errorf("economics: statement line: nil destination")
	}
	var wire statementLineV2Wire
	if err := decodeV2JSON(data, "statement line", &wire); err != nil {
		return err
	}
	*l = StatementLine(wire)
	return nil
}

type statementLineV2Wire StatementLine

func (b *StatementBatch) UnmarshalJSON(data []byte) error {
	if b == nil {
		return fmt.Errorf("economics: statement batch: nil destination")
	}
	var wire statementBatchV2Wire
	if err := decodeV2JSON(data, "statement batch", &wire); err != nil {
		return err
	}
	*b = StatementBatch(wire)
	return nil
}

type statementBatchV2Wire StatementBatch

func (r *SnapshotContentRef) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("economics: snapshot content ref: nil destination")
	}
	var wire snapshotContentRefV2Wire
	if err := decodeV2JSON(data, "snapshot content ref", &wire); err != nil {
		return err
	}
	*r = SnapshotContentRef(wire)
	return nil
}

type snapshotContentRefV2Wire SnapshotContentRef

func (f *FixedFeeIdentity) UnmarshalJSON(data []byte) error {
	if f == nil {
		return fmt.Errorf("economics: fixed fee identity: nil destination")
	}
	var wire fixedFeeIdentityV2Wire
	if err := decodeV2JSON(data, "fixed fee identity", &wire); err != nil {
		return err
	}
	*f = FixedFeeIdentity(wire)
	return nil
}

type fixedFeeIdentityV2Wire FixedFeeIdentity

func (r *AdjustmentRef) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("economics: adjustment ref: nil destination")
	}
	var wire adjustmentRefV2Wire
	if err := decodeV2JSON(data, "adjustment ref", &wire); err != nil {
		return err
	}
	*r = AdjustmentRef(wire)
	return nil
}

type adjustmentRefV2Wire AdjustmentRef

func (r *CurrencyConversionRef) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("economics: currency conversion ref: nil destination")
	}
	var wire currencyConversionRefV2Wire
	if err := decodeV2JSON(data, "currency conversion ref", &wire); err != nil {
		return err
	}
	*r = CurrencyConversionRef(wire)
	return nil
}

type currencyConversionRefV2Wire CurrencyConversionRef

func (l *LineItem) UnmarshalJSON(data []byte) error {
	if l == nil {
		return fmt.Errorf("economics: line item: nil destination")
	}
	var wire lineItemV2Wire
	if err := decodeV2JSON(data, "line item", &wire); err != nil {
		return err
	}
	*l = LineItem(wire)
	return nil
}

type lineItemV2Wire LineItem

func (t *CurrencyTotal) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("economics: currency total: nil destination")
	}
	var wire currencyTotalV2Wire
	if err := decodeV2JSON(data, "currency total", &wire); err != nil {
		return err
	}
	*t = CurrencyTotal(wire)
	return nil
}

type currencyTotalV2Wire CurrencyTotal

func (v *Valuation) UnmarshalJSON(data []byte) error {
	if v == nil {
		return fmt.Errorf("economics: valuation: nil destination")
	}
	var wire valuationWire
	if err := decodeV2JSON(data, "valuation", &wire); err != nil {
		return err
	}
	*v = Valuation(wire)
	return nil
}
