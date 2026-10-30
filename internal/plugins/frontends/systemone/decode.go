// Package systemone implements the typed System One HTTP frontend.
package systemone

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/jsonshape"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type orderedMember struct {
	name string
	raw  json.RawMessage
}

func reject(field, message string) error {
	return &aipapi.DecisionRejectError{Field: aipapi.NormalizeClientMessage(field), Message: message}
}

func decodeRequest(body []byte, maxQuestions int) (*aipapi.Call, error) {
	if maxQuestions <= 0 {
		maxQuestions = 256
	}
	if _, err := jsonshape.Preflight(body, jsonshape.Limits{MaxDepth: 64, RejectDuplicateNames: true}); err != nil {
		return nil, reject("body", "invalid, duplicate or oversized JSON structure")
	}
	root, err := strictObject(body, "body", "model", "state", "questions")
	if err != nil {
		return nil, err
	}
	var model string
	if json.Unmarshal(root["model"], &model) != nil || strings.TrimSpace(model) == "" {
		return nil, reject("body.model", "model is required and must be a non-empty string")
	}
	if !structuredValue(root["state"], false) {
		return nil, reject("body.state", "state must be a string, object or array")
	}
	questions, err := orderedObject(root["questions"], "body.questions", maxQuestions)
	if err != nil {
		return nil, err
	}
	decision := &aipapi.DecisionRequest{Evidence: root["state"]}
	for _, member := range questions {
		q, err := decodeQuestion(member.name, member.raw)
		if err != nil {
			return nil, err
		}
		decision.Questions = append(decision.Questions, q)
	}
	call := &aipapi.Call{
		Decision: decision, Route: aipapi.RouteIntent{Selector: model},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationDecisionEvaluate, DeliveryMode: aipapi.DeliveryModeNonStreaming},
	}
	if err := call.Validate(); err != nil {
		field := "body"
		if validation, ok := errors.AsType[*aipapi.ValidationError](err); ok {
			field += "." + strings.NewReplacer("Evidence", "state", "Questions", "questions", "Options", "criteria", "Levels", "criteria", "Instructions", "instructions", "Kind", "type").Replace(validation.Field)
		}
		return nil, reject(field, "invalid decision field")
	}
	return call, nil
}

func strictObject(raw []byte, field string, allowed ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, reject(field, "object is required")
	}
	for key := range object {
		found := false
		for _, name := range allowed {
			found = found || name == key
		}
		if !found {
			return nil, reject(field+"."+key, "unknown field")
		}
	}
	return object, nil
}

func orderedObject(raw []byte, field string, limit int) ([]orderedMember, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return nil, reject(field, "object is required")
	}
	var members []orderedMember
	for dec.More() {
		if len(members) >= limit {
			return nil, reject(field, "too many entries")
		}
		key, err := dec.Token()
		if err != nil {
			return nil, reject(field, "invalid object key")
		}
		name, ok := key.(string)
		if !ok {
			return nil, reject(field, "invalid object key")
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return nil, reject(field, "invalid object value")
		}
		members = append(members, orderedMember{name: name, raw: value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, reject(field, "invalid object")
	}
	return members, nil
}

func structuredValue(raw []byte, nullable bool) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return false
	}
	return raw[0] == '"' || raw[0] == '{' || raw[0] == '[' || (nullable && bytes.Equal(raw, []byte("null")))
}

func decodeQuestion(id string, raw []byte) (aipapi.DecisionQuestion, error) {
	field := "body.questions." + id
	fields, err := strictObject(raw, field, "type", "instructions", "criteria")
	if err != nil {
		return aipapi.DecisionQuestion{}, err
	}
	q := aipapi.DecisionQuestion{ID: id, Instructions: fields["instructions"]}
	if json.Unmarshal(fields["type"], &q.Kind) != nil {
		return q, reject(field+".type", "type is required")
	}
	if q.Instructions != nil && !structuredValue(q.Instructions, true) {
		return q, reject(field+".instructions", "instructions must be a string, object, array or null")
	}
	criteria := fields["criteria"]
	switch q.Kind {
	case aipapi.DecisionKindNoul:
		if len(criteria) != 0 && !bytes.Equal(bytes.TrimSpace(criteria), []byte("null")) {
			parts, err := strictObject(criteria, field+".criteria", "true", "false")
			if err != nil {
				return q, err
			}
			q.TrueCriteria, q.FalseCriteria = parts["true"], parts["false"]
			for name, value := range parts {
				if !structuredValue(value, true) {
					return q, reject(field+".criteria."+name, "criteria must be a string, object, array or null")
				}
			}
		}
	case aipapi.DecisionKindChoice:
		options, err := orderedObject(criteria, field+".criteria", 255)
		if err != nil {
			return q, err
		}
		for _, option := range options {
			if !structuredValue(option.raw, true) {
				return q, reject(field+".criteria."+option.name, "invalid option description")
			}
			q.Options = append(q.Options, aipapi.DecisionOption{Name: option.name, Description: option.raw})
		}
	case aipapi.DecisionKindScore:
		if json.Unmarshal(criteria, &q.Levels) != nil || len(q.Levels) < 2 || len(q.Levels) > 10 {
			return q, reject(field+".criteria", "score requires 2 to 10 levels")
		}
		for _, level := range q.Levels {
			if !structuredValue(level, false) {
				return q, reject(field+".criteria", "invalid score level")
			}
		}
	default:
		return q, reject(field+".type", "unknown question type")
	}
	return q, nil
}
