package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func writeResponse(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, stream aipapi.EventStream) error {
	result, usage, err := collectDecision(ctx, call, stream)
	if err != nil {
		return (WireErrors{}).WriteExecuteError(w, classifyExecute(err))
	}
	answers := make([]orderedMember, len(result.Answers))
	for i, a := range result.Answers {
		question := call.Decision.Questions[i]
		value := map[string]any{"type": a.Kind}
		probabilities := make([]orderedMember, len(a.Probabilities))
		for j, probability := range a.Probabilities {
			key := strconv.Itoa(j)
			if a.Kind == aipapi.DecisionKindChoice {
				key = question.Options[j].Name
			}
			raw, err := json.Marshal(probability)
			if err != nil {
				return err
			}
			probabilities[j] = orderedMember{name: key, raw: raw}
		}
		switch a.Kind {
		case aipapi.DecisionKindNoul:
			value["noul"] = a.PTrue
		case aipapi.DecisionKindChoice:
			value["choice"] = a.Selected
			value["probabilities"] = encodeOrderedObject(probabilities)
		case aipapi.DecisionKindScore:
			value["score"] = a.Expected
			value["probabilities"] = encodeOrderedObject(probabilities)
			legend := make([]orderedMember, len(question.Levels))
			for j, level := range question.Levels {
				legend[j] = orderedMember{name: strconv.Itoa(j), raw: level}
			}
			value["legend"] = encodeOrderedObject(legend)
		}
		if a.Kind != aipapi.DecisionKindNoul && a.Confidence != nil {
			value["confidence"] = *a.Confidence
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		answers[i] = orderedMember{name: a.QuestionID, raw: raw}
	}
	return writeJSON(w, http.StatusOK, struct {
		Model   string          `json:"model"`
		Answers json.RawMessage `json:"answers"`
		Usage   map[string]int  `json:"usage"`
	}{Model: result.Model, Answers: encodeOrderedObject(answers), Usage: map[string]int{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens}})
}

func encodeOrderedObject(members []orderedMember) json.RawMessage {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, member := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(member.name)
		out.Write(key)
		out.WriteByte(':')
		out.Write(member.raw)
	}
	out.WriteByte('}')
	return out.Bytes()
}

func collectDecision(ctx context.Context, call *aipapi.Call, stream aipapi.EventStream) (result *aipapi.DecisionResult, usage aipapi.Collected, err error) {
	if stream == nil {
		return nil, usage, aipapi.ErrNilEventStream
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	if ctx == nil {
		return nil, usage, aipapi.ErrNilContext
	}
	if call == nil || call.Decision == nil {
		return nil, usage, errors.New("decision request is missing")
	}
	started := false
	for {
		event, recvErr := stream.Recv(ctx)
		if recvErr != nil {
			return nil, usage, recvErr
		}
		if event.Kind != aipapi.EventResponseStarted && !started {
			return nil, usage, errors.New("decision event before response start")
		}
		switch event.Kind {
		case aipapi.EventResponseStarted:
			if started {
				return nil, usage, errors.New("duplicate response start")
			}
			started = true
		case aipapi.EventDecisionResult:
			if result != nil || event.Decision == nil {
				return nil, usage, errors.New("duplicate or missing decision result")
			}
			if err := event.Decision.ValidateFor(*call.Decision); err != nil {
				return nil, usage, err
			}
			result = event.Decision
		case aipapi.EventUsageDelta:
			usage.AccumulateUsage(event)
		case aipapi.EventWarning:
			// Optional diagnostics do not alter the public decision response.
		case aipapi.EventResponseFinished:
			if result == nil {
				return nil, usage, errors.New("missing decision result")
			}
			return result, usage, nil
		case aipapi.EventError:
			return nil, usage, aipapi.NewStreamError(event.ErrorCode, event.ErrorMessage)
		default:
			return nil, usage, errors.New("unexpected decision stream event")
		}
	}
}
