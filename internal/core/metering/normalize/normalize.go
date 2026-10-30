// Package normalize maps provider-family fields onto neutral V2 metering
// measures. It contains no provider-name branches and performs no pricing.
package normalize

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

var (
	ErrInvalidMapping = errors.New("metering/normalize: invalid provider mapping")
	ErrInvalidField   = errors.New("metering/normalize: invalid provider field")
)

const (
	maxMappingFields = metering.MaxObservationEvidence
	maxInputFields   = metering.MaxObservationEvidence
)

// InputMode declares whether a provider's input field is an inclusive total
// or an already-separated uncached component.
type InputMode string

const (
	InputInclusive InputMode = "inclusive_total"
	InputSeparate  InputMode = "separate_components"
)

// ReasoningMode declares whether reasoning is already contained by the output
// meter or is an explicitly disjoint output partition.
type ReasoningMode string

const (
	ReasoningIncluded ReasoningMode = "included_in_output"
	ReasoningDisjoint ReasoningMode = "disjoint_output_partition"
)

// Status reports whether the normalized partition is complete. Partial and
// conflict results are useful bounded evidence and are not silently converted
// to a zero or an invented residual.
type Status string

const (
	StatusComplete Status = "complete"
	StatusPartial  Status = "partial"
	StatusConflict Status = "conflict"
)

// FieldSpec binds one provider field to a neutral key. Name is the source
// field identity used for lookup; EvidencePath is the bounded safe location
// retained when the field maps to a V2 observation.
type FieldSpec struct {
	Name          string
	EvidencePath  string
	Key           metering.ComponentKey
	Required      bool
	NotApplicable bool
}

// Mapping is a versioned provider-family mapping. The mapping is data, not a
// provider switch in generic metering code.
type Mapping struct {
	ID            string
	Family        string
	Version       string
	InputMode     InputMode
	InputTotal    FieldSpec
	InputUncached FieldSpec
	CacheRead     []FieldSpec
	CacheWrite    []FieldSpec
	Output        FieldSpec
	Reasoning     FieldSpec
	ReasoningMode ReasoningMode
}

// Ref is the stable versioned mapping reference retained in result evidence.
func (m Mapping) Ref() string { return strings.TrimSpace(m.ID) + "@" + strings.TrimSpace(m.Version) }

// Validate checks mapping identity and field/key consistency without mutating
// the mapping. Empty optional specs mean the provider declares that dimension
// not applicable; a configured but absent field remains unknown evidence.
func (m Mapping) Validate() error {
	for name, value := range map[string]string{"mapping id": m.ID, "mapping family": m.Family, "mapping version": m.Version} {
		if strings.TrimSpace(value) == "" || !utf8.ValidString(value) {
			return fmt.Errorf("%w: %s required and valid", ErrInvalidMapping, name)
		}
		if strings.TrimSpace(value) != value {
			return fmt.Errorf("%w: %s must not have surrounding whitespace", ErrInvalidMapping, name)
		}
	}
	if len(m.Ref()) > metering.MaxMappingRefBytes {
		return fmt.Errorf("%w: mapping reference exceeds %d bytes", ErrInvalidMapping, metering.MaxMappingRefBytes)
	}
	if len(m.CacheRead)+len(m.CacheWrite)+4 > maxMappingFields {
		return fmt.Errorf("%w: mapping field count exceeds %d", ErrInvalidMapping, maxMappingFields)
	}
	if m.InputMode != InputInclusive && m.InputMode != InputSeparate {
		return fmt.Errorf("%w: unknown input mode %q", ErrInvalidMapping, m.InputMode)
	}
	if m.ReasoningMode != "" && m.ReasoningMode != ReasoningIncluded && m.ReasoningMode != ReasoningDisjoint {
		return fmt.Errorf("%w: unknown reasoning mode %q", ErrInvalidMapping, m.ReasoningMode)
	}
	if m.InputMode == InputInclusive && (m.InputTotal.Name == "" || m.InputTotal.NotApplicable) {
		return fmt.Errorf("%w: inclusive input requires total field", ErrInvalidMapping)
	}
	if m.InputMode == InputSeparate && (m.InputUncached.Name == "" || m.InputUncached.NotApplicable) {
		return fmt.Errorf("%w: separate input requires uncached field", ErrInvalidMapping)
	}

	seenNames := make(map[string]struct{})
	seenEvidence := make(map[string]struct{})
	seenKeys := make(map[string]string)
	validate := func(role string, spec FieldSpec, requiredName bool) error {
		if requiredName && spec.Name == "" {
			return fmt.Errorf("%w: %s field name required", ErrInvalidMapping, role)
		}
		if spec.Name == "" {
			return nil
		}
		if err := validateSpec(role, spec); err != nil {
			return err
		}
		if !spec.NotApplicable {
			key := spec.Key
			if key.Component == "" {
				key = defaultKey(role)
			}
			canonical, err := key.Normalize()
			if err != nil {
				return fmt.Errorf("%w: %s key: %v", ErrInvalidMapping, role, err)
			}
			keyID := canonical.CanonicalKey()
			if prior, exists := seenKeys[keyID]; exists {
				return fmt.Errorf("%w: %s key overlaps %s", ErrInvalidMapping, role, prior)
			}
			seenKeys[keyID] = role
		}
		if _, exists := seenNames[spec.Name]; exists {
			return fmt.Errorf("%w: duplicate source field %q", ErrInvalidMapping, spec.Name)
		}
		seenNames[spec.Name] = struct{}{}
		if spec.EvidencePath != "" {
			if _, exists := seenEvidence[spec.EvidencePath]; exists {
				return fmt.Errorf("%w: duplicate evidence path %q", ErrInvalidMapping, spec.EvidencePath)
			}
			seenEvidence[spec.EvidencePath] = struct{}{}
		}
		return nil
	}
	for _, item := range m.specs() {
		if err := validate(item.role, item.spec, false); err != nil {
			return err
		}
	}
	for _, spec := range m.CacheRead {
		if err := validate("cache_read", spec, true); err != nil {
			return err
		}
	}
	for _, spec := range m.CacheWrite {
		if err := validate("cache_write", spec, true); err != nil {
			return err
		}
	}
	return nil
}

func (m Mapping) specs() []struct {
	role string
	spec FieldSpec
} {
	return []struct {
		role string
		spec FieldSpec
	}{
		{"input_total", m.InputTotal},
		{"input_uncached", m.InputUncached},
		{"output", m.Output},
		{"reasoning", m.Reasoning},
	}
}

func validateSpec(role string, spec FieldSpec) error {
	if spec.Name == "" {
		return nil
	}
	if !utf8.ValidString(spec.Name) || len(spec.Name) > metering.MaxSourceEventKeyBytes || strings.TrimSpace(spec.Name) != spec.Name {
		return fmt.Errorf("%w: %s field name is invalid", ErrInvalidMapping, role)
	}
	if spec.EvidencePath != "" && (!utf8.ValidString(spec.EvidencePath) || len(spec.EvidencePath) > metering.MaxSafeEvidenceFieldBytes) {
		return fmt.Errorf("%w: %s evidence path is invalid", ErrInvalidMapping, role)
	}
	if spec.EvidencePath != "" {
		if err := (metering.SafeEvidenceField{
			Path: spec.EvidencePath, Present: false, Null: true,
			Acquisition: metering.AcquisitionProviderResponse,
		}).Validate(); err != nil {
			return fmt.Errorf("%w: %s evidence path: %v", ErrInvalidMapping, role, err)
		}
	}
	key := spec.Key
	if key.Component == "" {
		key = defaultKey(role)
	}
	if err := key.Validate(); err != nil {
		return fmt.Errorf("%w: %s key: %v", ErrInvalidMapping, role, err)
	}
	return nil
}

func defaultKey(role string) metering.ComponentKey {
	component, direction := "", metering.DirectionNone
	switch role {
	case "input_total":
		component, direction = metering.ComponentInputTokenTotal, metering.DirectionInput
	case "input_uncached":
		component, direction = metering.ComponentInputToken, metering.DirectionInput
	case "cache_read":
		component, direction = metering.ComponentCacheReadInputToken, metering.DirectionInput
	case "cache_write":
		component, direction = metering.ComponentCacheWriteInputToken, metering.DirectionInput
	case "output":
		component, direction = metering.ComponentOutputToken, metering.DirectionOutput
	case "reasoning":
		component, direction = metering.ComponentReasoningOutputToken, metering.DirectionOutput
	}
	return metering.ComponentKey{Direction: direction, Component: component, Unit: metering.UnitToken, SchemaID: metering.DefaultInclusionSchemaID}
}

// Field is one bounded provider source field. Lexeme is retained exactly and
// parsed without floating point; absent fields never become zero values.
type Field struct {
	Name    string
	Lexeme  string
	Present bool
	Null    bool
}

func (f Field) Validate() error {
	if f.Name == "" || !utf8.ValidString(f.Name) || len(f.Name) > metering.MaxSourceEventKeyBytes || strings.TrimSpace(f.Name) != f.Name {
		return fmt.Errorf("%w: field name invalid", ErrInvalidField)
	}
	if len(f.Lexeme) > metering.MaxSafeEvidenceFieldBytes || !utf8.ValidString(f.Lexeme) {
		return fmt.Errorf("%w: field %q lexeme exceeds bound or is invalid", ErrInvalidField, f.Name)
	}
	if !f.Present && f.Lexeme != "" {
		return fmt.Errorf("%w: absent field %q cannot carry a lexeme", ErrInvalidField, f.Name)
	}
	if f.Present && f.Null {
		return fmt.Errorf("%w: present field %q cannot be null", ErrInvalidField, f.Name)
	}
	if f.Present && f.Lexeme == "" {
		return fmt.Errorf("%w: present field %q requires a lexeme", ErrInvalidField, f.Name)
	}
	return nil
}

// OriginalField is source-qualified evidence retained independently of the
// normalized measures. It preserves the provider field name and exact lexeme.
type OriginalField struct {
	Name         string
	Lexeme       string
	Present      bool
	Null         bool
	EvidencePath string
}

// Input is the pure normalizer input. No request body or provider SDK value is
// retained; only bounded field identity and economic lexemes are accepted.
type Input struct {
	Mapping Mapping
	Fields  []Field
}

// Result contains disjoint chargeable measures, informational aggregates and
// source evidence. No monetary amount is calculated here.
type Result struct {
	MappingRef     string
	Status         Status
	Measures       []metering.Measure
	Informational  []metering.Measure
	OriginalFields []OriginalField
	Evidence       []metering.SafeEvidenceField
	Diagnostics    []string
}

// Attach adds normalized measures/evidence to a caller-supplied V2 envelope.
// Caller-owned identity, subject and source fields remain authoritative.
func (r Result) Attach(base metering.Observation) (metering.Observation, error) {
	out := base.Clone()
	out.MappingRef = r.MappingRef
	out.Measures = append(out.Measures, cloneMeasures(r.Measures)...)
	out.Measures = append(out.Measures, cloneMeasures(r.Informational)...)
	out.Evidence = append(out.Evidence, r.Evidence...)
	if err := out.Validate(); err != nil {
		return metering.Observation{}, err
	}
	return out, nil
}

// Normalize converts provider fields according to a versioned mapping. It
// returns partial/conflict status for unknown or inconsistent partitions while
// returning an error only for malformed bounded input or mapping contracts.
func Normalize(input Input) (Result, error) {
	if err := input.Mapping.Validate(); err != nil {
		return Result{}, err
	}
	if len(input.Fields) > maxInputFields {
		return Result{}, fmt.Errorf("%w: field count exceeds %d", ErrInvalidField, maxInputFields)
	}
	byName := make(map[string]Field, len(input.Fields))
	original := make([]OriginalField, 0, len(input.Fields))
	for i, field := range input.Fields {
		if err := field.Validate(); err != nil {
			return Result{}, fmt.Errorf("field[%d]: %w", i, err)
		}
		if _, exists := byName[field.Name]; exists {
			return Result{}, fmt.Errorf("%w: duplicate source field %q", ErrInvalidField, field.Name)
		}
		byName[field.Name] = field
		original = append(original, OriginalField{Name: field.Name, Lexeme: field.Lexeme, Present: field.Present, Null: field.Null})
	}
	sort.Slice(original, func(i, j int) bool { return original[i].Name < original[j].Name })

	result := Result{MappingRef: input.Mapping.Ref(), Status: StatusComplete, OriginalFields: original}
	setStatus := func(status Status, diagnostic string) {
		if status == StatusConflict || result.Status == StatusComplete {
			result.Status = status
		}
		if diagnostic != "" {
			result.Diagnostics = append(result.Diagnostics, diagnostic)
		}
	}

	for _, spec := range allSpecs(input.Mapping) {
		if spec.Name == "" || spec.EvidencePath == "" {
			continue
		}
		field, exists := byName[spec.Name]
		if !exists {
			result.Evidence = append(result.Evidence, metering.SafeEvidenceField{Path: spec.EvidencePath, Present: false, Acquisition: metering.AcquisitionProviderResponse})
			continue
		}
		result.Evidence = append(result.Evidence, metering.SafeEvidenceField{Path: spec.EvidencePath, Lexeme: field.Lexeme, Present: field.Present, Null: field.Null, Acquisition: metering.AcquisitionProviderResponse})
	}

	total, err := read(byName, input.Mapping.InputTotal)
	if err != nil {
		return Result{}, err
	}
	uncached, err := read(byName, input.Mapping.InputUncached)
	if err != nil {
		return Result{}, err
	}
	if input.Mapping.InputTotal.Name != "" && total.present && !negative(total.value) {
		appendMeasure(&result.Informational, input.Mapping.InputTotal, "input_total", total.value, result.MappingRef)
	} else if input.Mapping.InputTotal.Name != "" && total.present {
		setStatus(StatusConflict, "inclusive input total is negative")
	}

	cacheValues, missingRequired, err := readCaches(byName, input.Mapping, &result, result.MappingRef)
	if err != nil {
		return Result{}, err
	}
	switch input.Mapping.InputMode {
	case InputInclusive:
		if !total.present {
			setStatus(StatusPartial, "inclusive input total is absent")
		}
		if missingRequired {
			setStatus(StatusPartial, "inclusive input partition has an unknown cache operand")
		}
		if total.present && !missingRequired && result.Status != StatusConflict {
			residual := total.value
			for _, value := range cacheValues {
				residual, err = subtractDecimal(residual, value)
				if err != nil {
					return Result{}, err
				}
			}
			if strings.HasPrefix(residual.Coefficient, "-") {
				setStatus(StatusConflict, "inclusive input residual is negative")
			} else {
				appendMeasure(&result.Measures, input.Mapping.InputUncached, "input_uncached", residual, result.MappingRef)
			}
		}
	case InputSeparate:
		if uncached.present {
			if negative(uncached.value) {
				setStatus(StatusConflict, "separate uncached input is negative")
			} else {
				appendMeasure(&result.Measures, input.Mapping.InputUncached, "input_uncached", uncached.value, result.MappingRef)
			}
		} else {
			setStatus(StatusPartial, "separate uncached input is absent")
		}
	}

	output, err := read(byName, input.Mapping.Output)
	if err != nil {
		return Result{}, err
	}
	reasoning, err := read(byName, input.Mapping.Reasoning)
	if err != nil {
		return Result{}, err
	}
	reasoningMode := input.Mapping.ReasoningMode
	if reasoningMode == "" {
		reasoningMode = ReasoningIncluded
	}
	switch reasoningMode {
	case ReasoningIncluded:
		if output.present && !negative(output.value) {
			appendMeasure(&result.Measures, input.Mapping.Output, "output", output.value, result.MappingRef)
		} else if output.present {
			setStatus(StatusConflict, "output is negative")
		} else if input.Mapping.Output.Required && !input.Mapping.Output.NotApplicable {
			setStatus(StatusPartial, "output is absent")
		}
		if reasoning.present {
			if negative(reasoning.value) {
				setStatus(StatusConflict, "reasoning output is negative")
			} else {
				appendMeasure(&result.Informational, input.Mapping.Reasoning, "reasoning", reasoning.value, result.MappingRef)
			}
		} else if input.Mapping.Reasoning.Required && !input.Mapping.Reasoning.NotApplicable {
			setStatus(StatusPartial, "reasoning output is absent")
		}
	case ReasoningDisjoint:
		if (output.present && negative(output.value)) || (reasoning.present && negative(reasoning.value)) {
			setStatus(StatusConflict, "disjoint output partition contains a negative operand")
			break
		}
		outputApplicable := input.Mapping.Output.Name != "" && !input.Mapping.Output.NotApplicable
		reasoningApplicable := input.Mapping.Reasoning.Name != "" && !input.Mapping.Reasoning.NotApplicable
		switch {
		case outputApplicable && reasoningApplicable && output.present && reasoning.present:
			visible, subErr := subtractDecimal(output.value, reasoning.value)
			if subErr != nil {
				return Result{}, subErr
			}
			if negative(visible) {
				setStatus(StatusConflict, "disjoint visible output residual is negative")
				break
			}
			appendMeasure(&result.Measures, input.Mapping.Output, "output", visible, result.MappingRef)
			appendMeasure(&result.Measures, input.Mapping.Reasoning, "reasoning", reasoning.value, result.MappingRef)
		case outputApplicable && reasoningApplicable:
			if reasoning.present && !negative(reasoning.value) {
				appendMeasure(&result.Measures, input.Mapping.Reasoning, "reasoning", reasoning.value, result.MappingRef)
			}
			setStatus(StatusPartial, "disjoint output partition has an unknown operand")
		case outputApplicable:
			if output.present {
				appendMeasure(&result.Measures, input.Mapping.Output, "output", output.value, result.MappingRef)
			} else if input.Mapping.Output.Required {
				setStatus(StatusPartial, "disjoint output is absent")
			}
		case reasoningApplicable:
			if reasoning.present {
				appendMeasure(&result.Measures, input.Mapping.Reasoning, "reasoning", reasoning.value, result.MappingRef)
			} else if input.Mapping.Reasoning.Required {
				setStatus(StatusPartial, "disjoint reasoning output is absent")
			}
		}
	}

	for i := range result.OriginalFields {
		if spec, ok := specByName(input.Mapping, result.OriginalFields[i].Name); ok {
			result.OriginalFields[i].EvidencePath = spec.EvidencePath
		}
	}
	sort.Slice(result.Measures, func(i, j int) bool {
		return result.Measures[i].Key.CanonicalKey() < result.Measures[j].Key.CanonicalKey()
	})
	sort.Slice(result.Informational, func(i, j int) bool {
		return result.Informational[i].Key.CanonicalKey() < result.Informational[j].Key.CanonicalKey()
	})
	sort.Slice(result.Evidence, func(i, j int) bool { return result.Evidence[i].Path < result.Evidence[j].Path })
	return result, nil
}

type fieldValue struct {
	present bool
	value   metering.Decimal
}

func read(fields map[string]Field, spec FieldSpec) (fieldValue, error) {
	if spec.Name == "" || spec.NotApplicable {
		return fieldValue{}, nil
	}
	field, ok := fields[spec.Name]
	if !ok || !field.Present {
		return fieldValue{}, nil
	}
	value, err := metering.ParseDecimal(field.Lexeme)
	if err != nil {
		return fieldValue{}, fmt.Errorf("%w: source field %q: %v", ErrInvalidField, spec.Name, err)
	}
	return fieldValue{present: true, value: value}, nil
}

func readCaches(fields map[string]Field, mapping Mapping, result *Result, method string) (values []metering.Decimal, missingRequired bool, err error) {
	for _, spec := range mapping.CacheRead {
		value, readErr := read(fields, spec)
		if readErr != nil {
			return nil, false, readErr
		}
		if value.present {
			if negative(value.value) {
				result.Status = StatusConflict
				result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("cache read %q is negative", spec.Name))
			} else {
				appendMeasure(&result.Measures, spec, "cache_read", value.value, method)
				values = append(values, value.value)
			}
		} else if mapping.InputMode == InputInclusive && !spec.NotApplicable {
			missingRequired = true
		} else if spec.Required && !spec.NotApplicable {
			missingRequired = true
		}
	}
	for _, spec := range mapping.CacheWrite {
		value, readErr := read(fields, spec)
		if readErr != nil {
			return nil, false, readErr
		}
		if value.present {
			if negative(value.value) {
				result.Status = StatusConflict
				result.Diagnostics = append(result.Diagnostics, fmt.Sprintf("cache write %q is negative", spec.Name))
			} else {
				appendMeasure(&result.Measures, spec, "cache_write", value.value, method)
				values = append(values, value.value)
			}
		} else if mapping.InputMode == InputInclusive && !spec.NotApplicable {
			missingRequired = true
		} else if spec.Required && !spec.NotApplicable {
			missingRequired = true
		}
	}
	return values, missingRequired, nil
}

func appendMeasure(dst *[]metering.Measure, spec FieldSpec, role string, value metering.Decimal, method string) {
	key := spec.Key
	if key.Component == "" {
		key = defaultKey(role)
	}
	canonical, err := key.Normalize()
	if err != nil {
		return
	}
	canonicalValue, err := value.Normalize()
	if err != nil {
		return
	}
	*dst = append(*dst, metering.Measure{Key: canonical, Value: &canonicalValue, Quality: metering.QualityObserved, MethodRef: method})
}

func allSpecs(mapping Mapping) []FieldSpec {
	out := []FieldSpec{mapping.InputTotal, mapping.InputUncached, mapping.Output, mapping.Reasoning}
	out = append(out, mapping.CacheRead...)
	out = append(out, mapping.CacheWrite...)
	return out
}

func specByName(mapping Mapping, name string) (FieldSpec, bool) {
	for _, spec := range allSpecs(mapping) {
		if spec.Name == name {
			return spec, true
		}
	}
	return FieldSpec{}, false
}

func cloneMeasures(in []metering.Measure) []metering.Measure {
	out := make([]metering.Measure, len(in))
	for i, measure := range in {
		out[i] = measure.Clone()
	}
	return out
}

func negative(value metering.Decimal) bool { return strings.HasPrefix(value.Coefficient, "-") }

func subtractDecimal(a, b metering.Decimal) (metering.Decimal, error) {
	left, err := a.Normalize()
	if err != nil {
		return metering.Decimal{}, err
	}
	right, err := b.Normalize()
	if err != nil {
		return metering.Decimal{}, err
	}
	leftInt, ok := new(big.Int).SetString(left.Coefficient, 10)
	if !ok {
		return metering.Decimal{}, fmt.Errorf("%w: invalid left coefficient", ErrInvalidField)
	}
	rightInt, ok := new(big.Int).SetString(right.Coefficient, 10)
	if !ok {
		return metering.Decimal{}, fmt.Errorf("%w: invalid right coefficient", ErrInvalidField)
	}
	scale := max(right.Scale, left.Scale)
	if scale > left.Scale {
		leftInt.Mul(leftInt, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale-left.Scale)), nil))
	}
	if scale > right.Scale {
		rightInt.Mul(rightInt, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale-right.Scale)), nil))
	}
	leftInt.Sub(leftInt, rightInt)
	return (metering.Decimal{Coefficient: leftInt.String(), Scale: scale}).Normalize()
}
