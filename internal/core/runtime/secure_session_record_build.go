package runtime

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func buildClientTurnRecordInput(now time.Time, traceID string, br app.BeginResult, call *aipapi.Call) app.ClientTurnRecordInput {
	if call == nil {
		return app.ClientTurnRecordInput{
			Now:       now,
			TraceID:   strings.TrimSpace(traceID),
			SessionID: br.Record.SessionID,
			TurnID:    br.TurnID,
			Policy:    br.EffectivePolicy,
		}
	}
	items := aipapi.NormalizedItems(*call)
	lines := make([]app.ClientInputLine, 0, len(items))
	for ord, item := range items {
		lines = append(lines, clientInputLineFromItem(item, ord))
	}
	return app.ClientTurnRecordInput{
		Now:       now,
		TraceID:   strings.TrimSpace(traceID),
		SessionID: br.Record.SessionID,
		TurnID:    br.TurnID,
		Policy:    br.EffectivePolicy,
		Lines:     lines,
	}
}

func clientInputLineFromItem(item aipapi.Item, ordinal int) app.ClientInputLine {
	if item.Kind == aipapi.ItemKindMessage {
		kinds := make([]string, 0, len(item.Content))
		for _, cp := range item.Content {
			kinds = append(kinds, string(cp.Kind))
		}
		return app.ClientInputLine{
			Role:    string(item.Role),
			Ordinal: ordinal,
			Parts:   kinds,
		}
	}
	return app.ClientInputLine{
		Role:    string(item.Kind),
		Ordinal: ordinal,
		Parts:   []string{string(item.Kind)},
	}
}

// BuildClientTurnRecordInputFromShape builds a bounded ClientTurnRecordInput
// from a ClientTurnShape without prompt text materialization (Requirements 14.3, 14.5).
// It validates the shape under maxFactBytes and returns an error wrapping
// largebody.ErrSemanticFactBudgetExceeded if the semantic fact budget is exceeded
// (Requirement 14.4).
func BuildClientTurnRecordInputFromShape(
	now time.Time,
	traceID string,
	br app.BeginResult,
	shape largebody.ClientTurnShape,
	maxFactBytes int64,
) (app.ClientTurnRecordInput, error) {
	if maxFactBytes <= 0 {
		return app.ClientTurnRecordInput{}, fmt.Errorf("runtime: fact budget must be > 0, got %d", maxFactBytes)
	}
	if err := shape.Validate(maxFactBytes); err != nil {
		if errors.Is(err, largebody.ErrSemanticFactBudgetExceeded) {
			return app.ClientTurnRecordInput{}, err
		}
		return app.ClientTurnRecordInput{}, fmt.Errorf("%w: %v", largebody.ErrSemanticFactBudgetExceeded, err)
	}

	lines := make([]app.ClientInputLine, 0, len(shape.Items))
	for _, it := range shape.Items {
		lines = append(lines, clientInputLineFromShapeItem(it))
	}

	return app.ClientTurnRecordInput{
		Now:       now,
		TraceID:   strings.TrimSpace(traceID),
		SessionID: br.Record.SessionID,
		TurnID:    br.TurnID,
		Policy:    br.EffectivePolicy,
		Lines:     lines,
	}, nil
}

func clientInputLineFromShapeItem(item largebody.ClientTurnItemShape) app.ClientInputLine {
	if item.Kind == aipapi.ItemKindMessage {
		kinds := make([]string, 0, len(item.Parts))
		for _, p := range item.Parts {
			kinds = append(kinds, string(p.Kind))
		}
		return app.ClientInputLine{
			Role:    string(item.Role),
			Ordinal: int(item.Ordinal),
			Parts:   kinds,
		}
	}
	return app.ClientInputLine{
		Role:    string(item.Kind),
		Ordinal: int(item.Ordinal),
		Parts:   []string{string(item.Kind)},
	}
}
