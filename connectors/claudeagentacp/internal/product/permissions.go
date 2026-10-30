package product

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

// PermissionHandler auto-accepts offered approvals under bypass and selects
// offered declines under deny. Neither tool names nor inputs restrict bypass.
type PermissionHandler struct {
	bypass bool
}

func (h PermissionHandler) HandleServerRequest(_ context.Context, method string, _ json.RawMessage, params json.RawMessage) (any, error) {
	if method != "session/request_permission" {
		return nil, errors.New("claudeagentacp: method not found")
	}
	if h.bypass {
		return acp.AutoApprovePermissionRequest(params), nil
	}
	outcome := map[string]any{"outcome": "cancelled"}
	var request struct {
		Options []struct {
			ID   string `json:"optionId"`
			Kind string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return map[string]any{"outcome": outcome}, nil
	}
	// An ambiguous ID could select an allow option at the agent. Fail closed.
	seen := make(map[string]bool, len(request.Options))
	for _, option := range request.Options {
		if option.ID == "" || seen[option.ID] {
			return map[string]any{"outcome": outcome}, nil
		}
		seen[option.ID] = true
	}
	for _, kind := range []string{"reject_once", "reject_always"} {
		for _, option := range request.Options {
			if option.Kind == kind {
				return map[string]any{"outcome": map[string]any{
					"outcome": "selected", "optionId": option.ID,
				}}, nil
			}
		}
	}
	return map[string]any{"outcome": outcome}, nil
}

// TransportOptions applies the same approval posture between turns without
// running arbitrary callbacks on the transport reader.
func (a *AuthEnv) TransportOptions() acp.TransportOptions {
	if a.policy == "bypass" {
		return acp.TransportOptions{AutoApproveOutOfTurnPermissions: true}
	}
	return acp.TransportOptions{RefuseOutOfTurnRequests: true}
}

// ServerRequestHandler supplies the active-turn permission posture.
func (a *AuthEnv) ServerRequestHandler() acp.ServerRequestHandler {
	return PermissionHandler{bypass: a.policy == "bypass"}
}
