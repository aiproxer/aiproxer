package systemonecompat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func classifyError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if failure, ok := errors.AsType[*wireHTTPError](err); ok {
		switch failure.status {
		case 400, 413, 422:
			// Upstream messages can echo evidence or criteria even when input/ctx
			// fields are dropped. Expose a fixed bounded rejection instead.
			return &aipapi.DecisionRejectError{Field: rejectionField(failure.body), Message: "upstream rejected the decision request"}
		default:
			return aipapi.RecoverablePreOutputError(errors.New(failure.Error()))
		}
	}
	if aipapi.IsRecoverablePreOutput(err) {
		return err
	}
	// Transport errors may contain URLs, credentials or response fragments. Keep
	// retry eligibility without propagating that untrusted text to diagnostics.
	return aipapi.RecoverablePreOutputError(errors.New("system one upstream transport failed"))
}

func rejectionField(body []byte) string {
	var envelope struct {
		Detail []struct {
			Loc []json.RawMessage `json:"loc"`
		} `json:"detail"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Detail) == 0 {
		return "body"
	}
	parts := []string{"body"}
	for _, raw := range envelope.Detail[0].Loc {
		if len(parts) >= 8 {
			break
		}
		var name string
		if json.Unmarshal(raw, &name) != nil {
			continue
		}
		// Only protocol field names can cross the diagnostic error boundary.
		// Arbitrary IDs or upstream-generated locations may echo content.
		switch name {
		case "model", "state", "questions", "instructions", "criteria", "type", "true", "false":
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, ".")
}
