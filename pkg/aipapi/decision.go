package aipapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// DecisionKind names the typed question shapes the canonical decision contract carries.
type DecisionKind string

const (
	// DecisionKindNoul is a yes/no question answered with a single PTrue probability.
	DecisionKindNoul DecisionKind = "noul"
	// DecisionKindChoice is a single-select question answered with a named option plus a probability per requested option.
	DecisionKindChoice DecisionKind = "choice"
	// DecisionKindScore is a graded question answered with a probability per level plus a zero-based expected score.
	DecisionKindScore DecisionKind = "score"
)

// Choice option and score level bounds of the decision contract.
const (
	// MinDecisionChoiceOptions is the smallest number of options a choice question may offer.
	MinDecisionChoiceOptions = 1
	// MaxDecisionChoiceOptions is the largest number of options a choice question may offer.
	MaxDecisionChoiceOptions = 255
	// MinDecisionScoreLevels is the smallest number of levels a score question may define.
	MinDecisionScoreLevels = 2
	// MaxDecisionScoreLevels is the largest number of levels a score question may define.
	MaxDecisionScoreLevels = 10
)

// decisionDistributionTolerance scales the accepted round-off slack when an answer's
// probability distribution must sum to 1: the tolerated deviation is
// decisionDistributionTolerance times the number of probabilities.
const decisionDistributionTolerance = 1e-6

// DecisionRequest is the canonical typed decision request: one shared evidence value
// and the ordered questions an upstream must judge. Evidence and question payloads are
// carried as the original JSON bytes so their type and content survive unchanged.
type DecisionRequest struct {
	Evidence  json.RawMessage    // original JSON value: string, object or array
	Questions []DecisionQuestion // client order
}

// DecisionQuestion is one named typed question. The fields a question carries must match
// its Kind: noul questions take TrueCriteria/FalseCriteria, choice questions take Options,
// and score questions take Levels.
type DecisionQuestion struct {
	ID           string          // required for System One; unique
	Kind         DecisionKind    // noul, choice or score
	Instructions json.RawMessage // string, object or array

	TrueCriteria, FalseCriteria json.RawMessage // noul only, optional

	Options []DecisionOption  // choice: 1..255, client order
	Levels  []json.RawMessage // score: 2..10 descriptions, low to high
}

// DecisionOption is one named choice of a choice question, in client order.
type DecisionOption struct {
	Name        string
	Description json.RawMessage // nil means JSON null
}

// DecisionResult is an upstream's typed judgment for a DecisionRequest: the resolved
// model plus one answer per question, aligned with request question order.
type DecisionResult struct {
	Model   string           // upstream-resolved model
	Answers []DecisionAnswer // aligned with request question order
}

// DecisionAnswer is the typed judgment for one question. Only the fields of Kind carry
// meaning; Confidence is nil when the upstream reported none and is never derived.
// No field exists for refusals, boolean choice values or vendor extensions: adding one
// is a deferred, versioned extension of this type.
type DecisionAnswer struct {
	QuestionID    string
	Kind          DecisionKind
	PTrue         float64   // noul
	Selected      string    // choice
	Probabilities []float64 // choice: per option; score: per level; request order
	Expected      float64   // score, zero-based
	Confidence    *float64  // nil when the upstream reported none
}

// DecisionRejectError is a client-safe decision rejection carrying only the offending
// field and a bounded message. Evidence, question criteria and answer content are never
// placed in it; they stay out of client error bodies and out of logs by construction.
type DecisionRejectError struct {
	Field   string
	Message string
}

func (e *DecisionRejectError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func (e *DecisionRejectError) Unwrap() error { return ErrInvalidCall }

// IsDecisionReject reports whether err is or wraps a DecisionRejectError.
func IsDecisionReject(err error) bool {
	var rej *DecisionRejectError
	return errors.As(err, &rej)
}

// Validate checks the decision request invariants: required valid-JSON evidence, at least
// one question with a unique non-empty ID and a known kind, only the fields that kind
// defines, option and level bounds, unique option names and per-field byte bounds.
// It reports the offending field and never inspects evidence content.
func (r DecisionRequest) Validate() error {
	if err := validateDecisionJSONValue("Evidence", r.Evidence, MaxDecisionEvidenceBytes, true); err != nil {
		return err
	}
	if len(r.Questions) == 0 {
		return &ValidationError{Field: "Questions", Message: "at least one question is required"}
	}
	seenIDs := make(map[string]struct{}, len(r.Questions))
	for i, q := range r.Questions {
		if err := q.validate(fmt.Sprintf("Questions[%d]", i), seenIDs); err != nil {
			return err
		}
	}
	return nil
}

func (q DecisionQuestion) validate(field string, seenIDs map[string]struct{}) error {
	idField := field + ".ID"
	if q.ID == "" {
		return &ValidationError{Field: idField, Message: "question ID is required"}
	}
	if err := validateStringField(idField, q.ID, MaxDecisionQuestionIDBytes); err != nil {
		return err
	}
	if _, exists := seenIDs[q.ID]; exists {
		return &ValidationError{Field: idField, Message: fmt.Sprintf("duplicate question ID %q", q.ID)}
	}
	seenIDs[q.ID] = struct{}{}

	optionsField := field + ".Options"
	levelsField := field + ".Levels"
	switch q.Kind {
	case DecisionKindNoul:
		if len(q.Options) > 0 {
			return &ValidationError{Field: optionsField, Message: "options are only allowed on choice questions"}
		}
		if len(q.Levels) > 0 {
			return &ValidationError{Field: levelsField, Message: "levels are only allowed on score questions"}
		}
	case DecisionKindChoice:
		if len(q.Levels) > 0 {
			return &ValidationError{Field: levelsField, Message: "levels are only allowed on score questions"}
		}
	case DecisionKindScore:
		if len(q.Options) > 0 {
			return &ValidationError{Field: optionsField, Message: "options are only allowed on choice questions"}
		}
	case "":
		return &ValidationError{Field: field + ".Kind", Message: "kind is required"}
	default:
		return &ValidationError{
			Field:   field + ".Kind",
			Message: fmt.Sprintf("must be one of %q, %q, or %q", DecisionKindNoul, DecisionKindChoice, DecisionKindScore),
		}
	}

	if err := validateDecisionJSONValue(field+".Instructions", q.Instructions, MaxDecisionInstructionsBytes, false); err != nil {
		return err
	}
	if q.Kind == DecisionKindNoul {
		if err := validateDecisionJSONValue(field+".TrueCriteria", q.TrueCriteria, MaxDecisionCriteriaBytes, false); err != nil {
			return err
		}
		if err := validateDecisionJSONValue(field+".FalseCriteria", q.FalseCriteria, MaxDecisionCriteriaBytes, false); err != nil {
			return err
		}
	} else {
		criteriaField := field + ".TrueCriteria"
		if len(q.TrueCriteria) > 0 {
			return &ValidationError{Field: criteriaField, Message: "true and false criteria are only allowed on noul questions"}
		}
		criteriaField = field + ".FalseCriteria"
		if len(q.FalseCriteria) > 0 {
			return &ValidationError{Field: criteriaField, Message: "true and false criteria are only allowed on noul questions"}
		}
	}
	if err := q.validateOptions(field); err != nil {
		return err
	}
	return q.validateLevels(field)
}

func (q DecisionQuestion) validateOptions(field string) error {
	if q.Kind != DecisionKindChoice {
		return nil
	}
	optionsField := field + ".Options"
	switch {
	case len(q.Options) < MinDecisionChoiceOptions:
		return &ValidationError{Field: optionsField, Message: fmt.Sprintf("at least %d option is required", MinDecisionChoiceOptions)}
	case len(q.Options) > MaxDecisionChoiceOptions:
		return &ValidationError{Field: optionsField, Message: fmt.Sprintf("at most %d options", MaxDecisionChoiceOptions)}
	}
	seenNames := make(map[string]struct{}, len(q.Options))
	for i, o := range q.Options {
		optionField := fieldIndex(optionsField, i)
		if o.Name == "" {
			return &ValidationError{Field: optionField + ".Name", Message: "option name is required"}
		}
		if err := validateStringField(optionField+".Name", o.Name, MaxDecisionOptionNameBytes); err != nil {
			return err
		}
		if _, exists := seenNames[o.Name]; exists {
			return &ValidationError{Field: optionField + ".Name", Message: fmt.Sprintf("duplicate option name %q", o.Name)}
		}
		seenNames[o.Name] = struct{}{}
		if err := validateDecisionJSONValue(optionField+".Description", o.Description, MaxDecisionOptionDescriptionBytes, false); err != nil {
			return err
		}
	}
	return nil
}

func (q DecisionQuestion) validateLevels(field string) error {
	if q.Kind != DecisionKindScore {
		return nil
	}
	levelsField := field + ".Levels"
	switch {
	case len(q.Levels) < MinDecisionScoreLevels:
		return &ValidationError{Field: levelsField, Message: fmt.Sprintf("at least %d levels are required", MinDecisionScoreLevels)}
	case len(q.Levels) > MaxDecisionScoreLevels:
		return &ValidationError{Field: levelsField, Message: fmt.Sprintf("at most %d levels", MaxDecisionScoreLevels)}
	}
	for i, level := range q.Levels {
		if err := validateDecisionJSONValue(fieldIndex(levelsField, i), level, MaxDecisionLevelBytes, true); err != nil {
			return err
		}
	}
	return nil
}

// ValidateFor checks that res answers exactly req's questions, once each and in request
// order, with finite in-range values: PTrue and every probability within [0,1], each
// distribution summing to 1 within round-off tolerance, Selected naming a requested
// maximal-probability option, Expected within [0, levels-1] and Confidence within [0,1]
// when the upstream reported one. It never repairs, derives or drops a value.
func (r DecisionResult) ValidateFor(req DecisionRequest) error {
	if r.Model == "" {
		return &ValidationError{Field: "Model", Message: "upstream-resolved model is required"}
	}
	if err := validateExactStringField("Model", r.Model, MaxRefStringBytes); err != nil {
		return err
	}
	if len(r.Answers) != len(req.Questions) {
		return &ValidationError{
			Field:   "Answers",
			Message: fmt.Sprintf("expected %d answers in request order, got %d", len(req.Questions), len(r.Answers)),
		}
	}
	for i, a := range r.Answers {
		if err := a.validateFor(req.Questions[i], fmt.Sprintf("Answers[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

func (a DecisionAnswer) validateFor(q DecisionQuestion, field string) error {
	if a.QuestionID != q.ID {
		return &ValidationError{Field: field + ".QuestionID", Message: fmt.Sprintf("expected exactly one answer for question %q in request order", q.ID)}
	}
	if a.Kind != q.Kind {
		return &ValidationError{Field: field + ".Kind", Message: fmt.Sprintf("must be %q for question %q", q.Kind, q.ID)}
	}

	probabilitiesField := field + ".Probabilities"
	switch q.Kind {
	case DecisionKindNoul:
		if err := validateDecisionProbability(field+".PTrue", a.PTrue); err != nil {
			return err
		}
	case DecisionKindChoice:
		if len(a.Probabilities) != len(q.Options) {
			return &ValidationError{
				Field:   probabilitiesField,
				Message: fmt.Sprintf("expected %d option probabilities, got %d", len(q.Options), len(a.Probabilities)),
			}
		}
		if err := validateDecisionDistribution(probabilitiesField, a.Probabilities); err != nil {
			return err
		}
		selected := -1
		for i, o := range q.Options {
			if o.Name == a.Selected {
				selected = i
				break
			}
		}
		if selected < 0 {
			return &ValidationError{Field: field + ".Selected", Message: "names an option the question did not request"}
		}
		if a.Probabilities[selected] != decisionMaxProbability(a.Probabilities) {
			return &ValidationError{Field: field + ".Selected", Message: "names an option that is not a maximal-probability option"}
		}
	case DecisionKindScore:
		if len(a.Probabilities) != len(q.Levels) {
			return &ValidationError{
				Field:   probabilitiesField,
				Message: fmt.Sprintf("expected %d level probabilities, got %d", len(q.Levels), len(a.Probabilities)),
			}
		}
		if err := validateDecisionDistribution(probabilitiesField, a.Probabilities); err != nil {
			return err
		}
		expectedField := field + ".Expected"
		if err := validateDecisionFinite(expectedField, a.Expected); err != nil {
			return err
		}
		top := float64(len(q.Levels) - 1)
		if a.Expected < 0 || a.Expected > top {
			return &ValidationError{Field: expectedField, Message: fmt.Sprintf("must be within [0, %g] for %d levels", top, len(q.Levels))}
		}
	}

	if a.Confidence != nil {
		if err := validateDecisionProbability(field+".Confidence", *a.Confidence); err != nil {
			return err
		}
	}
	return nil
}

func validateDecisionDistribution(field string, probabilities []float64) error {
	for i, p := range probabilities {
		if err := validateDecisionProbability(fieldIndex(field, i), p); err != nil {
			return err
		}
	}
	sum := 0.0
	for _, p := range probabilities {
		sum += p
	}
	tolerance := decisionDistributionTolerance * float64(len(probabilities))
	if math.Abs(sum-1) > tolerance {
		return &ValidationError{Field: field, Message: fmt.Sprintf("must sum to 1 within %g, got %g", tolerance, sum)}
	}
	return nil
}

// decisionMaxProbability returns the largest probability; an empty slice has no maximum
// and yields 0. It keeps the comparison for Selected independent of option order.
func decisionMaxProbability(probabilities []float64) float64 {
	if len(probabilities) == 0 {
		return 0
	}
	best := probabilities[0]
	for _, p := range probabilities[1:] {
		if p > best {
			best = p
		}
	}
	return best
}

func validateDecisionProbability(field string, value float64) error {
	if err := validateDecisionFinite(field, value); err != nil {
		return err
	}
	if value < 0 || value > 1 {
		return &ValidationError{Field: field, Message: "must be within [0, 1]"}
	}
	return nil
}

func validateDecisionFinite(field string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return &ValidationError{Field: field, Message: "must be a finite number"}
	}
	return nil
}

func validateDecisionJSONValue(field string, value json.RawMessage, maxBytes int, required bool) error {
	if len(value) == 0 {
		if required {
			return &ValidationError{Field: field, Message: "is required and must be a JSON value"}
		}
		return nil
	}
	if len(value) > maxBytes {
		return &ValidationError{Field: field, Message: fmt.Sprintf("exceeds %d bytes", maxBytes)}
	}
	if !json.Valid(value) {
		return &ValidationError{Field: field, Message: "must be valid JSON when set"}
	}
	return nil
}
