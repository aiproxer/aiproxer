package metering_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestQuantity_Validate_RegisteredComponents(t *testing.T) {
	t.Parallel()
	ok := []metering.Quantity{
		{Component: metering.ComponentRequest, Unit: metering.UnitCount, Value: 1, Present: true},
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 0, Present: true},
		{Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 10, Present: true},
		{Component: metering.ComponentCacheReadInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
		{Component: metering.ComponentCacheWriteInputToken, Unit: metering.UnitToken, Value: 1, Present: true},
		{Component: metering.ComponentReasoningOutputToken, Unit: metering.UnitToken, Value: 1, Present: true},
		{Component: metering.ComponentTotalToken, Unit: metering.UnitToken, Value: 11, Present: true},
		{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Present: false},
	}
	for _, q := range ok {
		if err := q.Validate(); err != nil {
			t.Fatalf("Validate(%+v)=%v", q, err)
		}
	}
}

func TestQuantity_Validate_WrongUnitRejected(t *testing.T) {
	t.Parallel()
	q := metering.Quantity{Component: metering.ComponentInputToken, Unit: metering.UnitCount, Present: true}
	if err := q.Validate(); err == nil {
		t.Fatal("expected unit mismatch error")
	}
}

func TestQuantity_Validate_UnknownComponentRequiresSchema(t *testing.T) {
	t.Parallel()
	missing := metering.Quantity{Component: "image", Unit: "count", Value: 1, Present: true}
	if err := missing.Validate(); err == nil {
		t.Fatal("unknown component without schema must fail")
	}
	withSchema := metering.Quantity{
		Component: "image",
		Unit:      "count",
		Value:     1,
		Present:   true,
		Schema:    "vendor.v1",
	}
	if err := withSchema.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestQuantity_Validate_EmptyComponent(t *testing.T) {
	t.Parallel()
	if err := (metering.Quantity{Unit: metering.UnitToken, Present: true}).Validate(); err == nil {
		t.Fatal("empty component must fail")
	}
}

func TestDefaultInclusionSchemaConstants(t *testing.T) {
	t.Parallel()
	// Documentation/helpers only: cache ⊂ input, reasoning ⊂ output, total = input + output.
	if metering.DefaultInclusionSchemaID == "" {
		t.Fatal("default inclusion schema id required")
	}
	if !metering.IsInputSubcomponent(metering.ComponentCacheReadInputToken) {
		t.Fatal("cache_read is input subcomponent")
	}
	if !metering.IsInputSubcomponent(metering.ComponentCacheWriteInputToken) {
		t.Fatal("cache_write is input subcomponent")
	}
	if !metering.IsOutputSubcomponent(metering.ComponentReasoningOutputToken) {
		t.Fatal("reasoning is output subcomponent")
	}
	if metering.IsInputSubcomponent(metering.ComponentOutputToken) {
		t.Fatal("output_token is not an input subcomponent")
	}
}
