package normalize_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/normalize"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestNormalize_InclusiveInputUsesExactDisjointPartition(t *testing.T) {
	t.Parallel()

	mapping := tokenMapping(normalize.InputInclusive)
	result, err := normalize.Normalize(normalize.Input{
		Mapping: mapping,
		Fields: []normalize.Field{
			field("total", "1000"),
			field("read", "600"),
			field("write", "100"),
			field("output", "200"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != normalize.StatusComplete {
		t.Fatalf("status=%q diagnostics=%v", result.Status, result.Diagnostics)
	}
	if got := measureValue(t, result.Measures, metering.ComponentInputToken); got != "300/0" {
		t.Fatalf("uncached input=%q want 300/0", got)
	}
	if got := measureValue(t, result.Measures, metering.ComponentCacheReadInputToken); got != "600/0" {
		t.Fatalf("cache read=%q want 600/0", got)
	}
	if got := measureValue(t, result.Measures, metering.ComponentCacheWriteInputToken); got != "100/0" {
		t.Fatalf("cache write=%q want 100/0", got)
	}
	if got := measureValue(t, result.Measures, metering.ComponentOutputToken); got != "200/0" {
		t.Fatalf("output=%q want 200/0", got)
	}
	if got := measureValue(t, result.Measures, metering.ComponentInputTokenTotal); got != "" {
		t.Fatalf("inclusive total must not be chargeable, got %q", got)
	}
	if got := measureValue(t, result.Informational, metering.ComponentInputTokenTotal); got != "1000/0" {
		t.Fatalf("informational total=%q want 1000/0", got)
	}
	if result.MappingRef != "family.tokens@v1" {
		t.Fatalf("mapping ref=%q", result.MappingRef)
	}
	if len(result.OriginalFields) != 4 || originalLexeme(result.OriginalFields, "total") != "1000" {
		t.Fatalf("original fields did not retain exact source evidence: %+v", result.OriginalFields)
	}
}

func TestNormalize_SeparateInputDoesNotSubtractCaches(t *testing.T) {
	t.Parallel()

	result, err := normalize.Normalize(normalize.Input{
		Mapping: tokenMapping(normalize.InputSeparate),
		Fields: []normalize.Field{
			field("input", "300"),
			field("read", "600"),
			field("write", "100"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != normalize.StatusComplete {
		t.Fatalf("status=%q diagnostics=%v", result.Status, result.Diagnostics)
	}
	if got := measureValue(t, result.Measures, metering.ComponentInputToken); got != "300/0" {
		t.Fatalf("separate input=%q want 300/0", got)
	}
}

func TestNormalize_UnknownOperandAndNegativeResidualNeverInventZero(t *testing.T) {
	t.Parallel()

	t.Run("missing operand is partial", func(t *testing.T) {
		t.Parallel()
		result, err := normalize.Normalize(normalize.Input{
			Mapping: tokenMapping(normalize.InputInclusive),
			Fields:  []normalize.Field{field("total", "1000"), field("read", "600")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != normalize.StatusPartial {
			t.Fatalf("status=%q diagnostics=%v", result.Status, result.Diagnostics)
		}
		if got := measureValue(t, result.Measures, metering.ComponentInputToken); got != "" {
			t.Fatalf("missing operand invented uncached=%q", got)
		}
	})

	t.Run("negative residual is conflict", func(t *testing.T) {
		t.Parallel()
		result, err := normalize.Normalize(normalize.Input{
			Mapping: tokenMapping(normalize.InputInclusive),
			Fields:  []normalize.Field{field("total", "100"), field("read", "60"), field("write", "50")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != normalize.StatusConflict {
			t.Fatalf("status=%q diagnostics=%v", result.Status, result.Diagnostics)
		}
		if got := measureValue(t, result.Measures, metering.ComponentInputToken); got != "" {
			t.Fatalf("negative residual invented uncached=%q", got)
		}
	})

	t.Run("negative cache operand is conflict and cannot derive residual", func(t *testing.T) {
		t.Parallel()
		result, err := normalize.Normalize(normalize.Input{
			Mapping: tokenMapping(normalize.InputInclusive),
			Fields:  []normalize.Field{field("total", "100"), field("read", "-10"), field("write", "5")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != normalize.StatusConflict {
			t.Fatalf("status=%q diagnostics=%v", result.Status, result.Diagnostics)
		}
		if got := measureValue(t, result.Measures, metering.ComponentInputToken); got != "" {
			t.Fatalf("negative cache operand invented uncached=%q", got)
		}
	})
}

func TestNormalize_CacheLifetimesAndReasoningStayDisjoint(t *testing.T) {
	t.Parallel()

	mapping := tokenMapping(normalize.InputSeparate)
	mapping.CacheWrite = []normalize.FieldSpec{
		{Name: "write-5m", EvidencePath: "$.cache.write_tokens", Key: cacheWriteKey("5m")},
		{Name: "write-1h", EvidencePath: "$.usage.cache_write_input_tokens", Key: cacheWriteKey("1h")},
	}
	result, err := normalize.Normalize(normalize.Input{
		Mapping: mapping,
		Fields: []normalize.Field{
			field("input", "300"), field("write-5m", "20"), field("write-1h", "30"),
			field("output", "200"), field("reasoning", "50"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := measureCount(result.Measures, metering.ComponentCacheWriteInputToken); got != 2 {
		t.Fatalf("cache lifetime measures=%d want 2", got)
	}
	if got := measureValue(t, result.Measures, metering.ComponentOutputToken); got != "200/0" {
		t.Fatalf("output=%q want 200/0", got)
	}
	if got := measureValue(t, result.Measures, metering.ComponentReasoningOutputToken); got != "" {
		t.Fatalf("included reasoning became chargeable=%q", got)
	}
	if got := measureValue(t, result.Informational, metering.ComponentReasoningOutputToken); got != "50/0" {
		t.Fatalf("informational reasoning=%q want 50/0", got)
	}
}

func TestNormalize_DisjointReasoningHonorsNotApplicablePartition(t *testing.T) {
	t.Parallel()
	mapping := tokenMapping(normalize.InputSeparate)
	mapping.ReasoningMode = normalize.ReasoningDisjoint
	mapping.Reasoning.NotApplicable = true
	result, err := normalize.Normalize(normalize.Input{
		Mapping: mapping,
		Fields:  []normalize.Field{field("input", "3"), field("output", "200")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != normalize.StatusComplete {
		t.Fatalf("not-applicable reasoning status=%q diagnostics=%v", result.Status, result.Diagnostics)
	}
	if got := measureValue(t, result.Measures, metering.ComponentOutputToken); got != "200/0" {
		t.Fatalf("not-applicable reasoning output=%q want 200/0", got)
	}
}

func TestNormalize_PreservesUnknownBoundedFieldWithoutCreatingMeasure(t *testing.T) {
	t.Parallel()

	result, err := normalize.Normalize(normalize.Input{
		Mapping: tokenMapping(normalize.InputSeparate),
		Fields: []normalize.Field{
			field("input", "3"),
			{Name: "provider.custom.credits", Lexeme: "0.125", Present: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.OriginalFields) != 2 || result.OriginalFields[1].Name != "provider.custom.credits" || result.OriginalFields[1].Lexeme != "0.125" {
		t.Fatalf("unknown exact evidence lost: %+v", result.OriginalFields)
	}
	if len(result.Measures) != 1 {
		t.Fatalf("unknown field created a generic measure: %+v", result.Measures)
	}
}

func TestNormalize_PreservesAbsentAndExplicitNullPresence(t *testing.T) {
	t.Parallel()
	result, err := normalize.Normalize(normalize.Input{
		Mapping: tokenMapping(normalize.InputSeparate),
		Fields: []normalize.Field{
			field("input", "3"),
			{Name: "read", Null: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if evidence, ok := evidenceAt(result.Evidence, "$.usage.cache_read_input_tokens"); !ok || evidence.Present || !evidence.Null {
		t.Fatalf("explicit null presence lost: %+v", result.Evidence)
	}
	if evidence, ok := evidenceAt(result.Evidence, "$.usage.cache_write_input_tokens"); !ok || evidence.Present || evidence.Null {
		t.Fatalf("absent presence lost: %+v", result.Evidence)
	}
}

func tokenMapping(mode normalize.InputMode) normalize.Mapping {
	return normalize.Mapping{
		ID:            "family.tokens",
		Family:        "family",
		Version:       "v1",
		InputMode:     mode,
		InputTotal:    spec("total", "$.usage.total_tokens", key(metering.DirectionInput, metering.ComponentInputTokenTotal, metering.UnitToken)),
		InputUncached: spec("input", "$.usage.input_tokens", key(metering.DirectionInput, metering.ComponentInputToken, metering.UnitToken)),
		CacheRead:     []normalize.FieldSpec{spec("read", "$.usage.cache_read_input_tokens", key(metering.DirectionInput, metering.ComponentCacheReadInputToken, metering.UnitToken))},
		CacheWrite:    []normalize.FieldSpec{spec("write", "$.usage.cache_write_input_tokens", key(metering.DirectionInput, metering.ComponentCacheWriteInputToken, metering.UnitToken))},
		Output:        spec("output", "$.usage.output_tokens", key(metering.DirectionOutput, metering.ComponentOutputToken, metering.UnitToken)),
		Reasoning:     spec("reasoning", "$.usage.reasoning_output_tokens", key(metering.DirectionOutput, metering.ComponentReasoningOutputToken, metering.UnitToken)),
		ReasoningMode: normalize.ReasoningIncluded,
	}
}

func spec(name, evidence string, component metering.ComponentKey) normalize.FieldSpec {
	return normalize.FieldSpec{Name: name, EvidencePath: evidence, Key: component}
}

func key(direction metering.FlowDirection, component, unit string) metering.ComponentKey {
	return metering.ComponentKey{Direction: direction, Component: component, Unit: unit, SchemaID: metering.DefaultInclusionSchemaID}
}

func cacheWriteKey(lifetime string) metering.ComponentKey {
	return metering.ComponentKey{
		Direction: metering.DirectionInput, Component: metering.ComponentCacheWriteInputToken,
		Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID,
		Dimensions: []metering.Dimension{{Name: "cache_lifetime", Value: lifetime}},
	}
}

func field(name, lexeme string) normalize.Field {
	return normalize.Field{Name: name, Lexeme: lexeme, Present: true}
}

func measureValue(t *testing.T, measures []metering.Measure, component string) string {
	t.Helper()
	for _, measure := range measures {
		if measure.Key.Component == component && measure.Value != nil {
			return measure.Value.CanonicalString()
		}
	}
	return ""
}

func measureCount(measures []metering.Measure, component string) int {
	count := 0
	for _, measure := range measures {
		if measure.Key.Component == component {
			count++
		}
	}
	return count
}

func originalLexeme(fields []normalize.OriginalField, name string) string {
	for _, field := range fields {
		if field.Name == name {
			return field.Lexeme
		}
	}
	return ""
}

func evidenceAt(fields []metering.SafeEvidenceField, path string) (metering.SafeEvidenceField, bool) {
	for _, field := range fields {
		if field.Path == path {
			return field, true
		}
	}
	return metering.SafeEvidenceField{}, false
}
