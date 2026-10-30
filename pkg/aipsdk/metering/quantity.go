package metering

import "fmt"

// Registered quantity component identifiers (requirement 3.9).
const (
	ComponentRequest              = "request"
	ComponentInputToken           = "input_token"
	ComponentOutputToken          = "output_token"
	ComponentCacheReadInputToken  = "cache_read_input_token"
	ComponentCacheWriteInputToken = "cache_write_input_token"
	ComponentReasoningOutputToken = "reasoning_output_token"
	ComponentTotalToken           = "total_token"
)

// Registered quantity units.
const (
	UnitCount = "count"
	UnitToken = "token"
)

// DefaultInclusionSchemaID names the default component inclusion rules aligned
// with Phase 2 core accounting. This package does not infer totals at runtime;
// callers and journal aggregators apply the schema explicitly.
//
// Default inclusion:
//
//	input_token includes cache_read_input_token and cache_write_input_token
//	output_token includes reasoning_output_token
//	total_token = input_token + output_token
//
// Subcomponents are separately priced but are not added again to total.
const DefaultInclusionSchemaID = "aip.default.token_inclusion.v1"

// Quantity is an extensible billable or countable component observation.
// Unknown components are allowed only when Schema is set (requirement 3.9).
type Quantity struct {
	Component string `json:"component"`
	Unit      string `json:"unit"`
	Value     int64  `json:"value"`
	Present   bool   `json:"present"`
	Schema    string `json:"schema,omitempty"`
}

// registeredUnits maps known components to their required unit.
var registeredUnits = map[string]string{
	ComponentRequest:              UnitCount,
	ComponentInputToken:           UnitToken,
	ComponentOutputToken:          UnitToken,
	ComponentCacheReadInputToken:  UnitToken,
	ComponentCacheWriteInputToken: UnitToken,
	ComponentReasoningOutputToken: UnitToken,
	ComponentTotalToken:           UnitToken,
}

// Validate checks component/unit rules. Registered components must use their
// canonical unit. Unknown components require a non-empty Schema.
func (q Quantity) Validate() error {
	if q.Component == "" {
		return fmt.Errorf("metering: quantity component required")
	}
	if q.Unit == "" {
		return fmt.Errorf("metering: quantity unit required")
	}
	if want, ok := registeredUnits[q.Component]; ok {
		if q.Unit != want {
			return fmt.Errorf("metering: component %q requires unit %q, got %q", q.Component, want, q.Unit)
		}
		return nil
	}
	if q.Schema == "" {
		return fmt.Errorf("metering: unknown component %q requires schema", q.Component)
	}
	return nil
}

// IsRegisteredComponent reports whether component is in the built-in registry.
func IsRegisteredComponent(component string) bool {
	_, ok := registeredUnits[component]
	return ok
}

// IsInputSubcomponent reports whether component is included in input_token under
// the default inclusion schema (cache ⊂ input). Pure documentation helper.
func IsInputSubcomponent(component string) bool {
	return component == ComponentCacheReadInputToken || component == ComponentCacheWriteInputToken
}

// IsOutputSubcomponent reports whether component is included in output_token under
// the default inclusion schema (reasoning ⊂ output). Pure documentation helper.
func IsOutputSubcomponent(component string) bool {
	return component == ComponentReasoningOutputToken
}
