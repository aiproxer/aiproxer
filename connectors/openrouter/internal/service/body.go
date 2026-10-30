package service

import (
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func applyOpenRouterBody(body map[string]any, call aipapi.Call) error {
	passthrough := []struct{ jsonKey, extKey string }{
		{"provider", extProvider},
		{"models", extModels},
		{"route", extRoute},
		{"plugins", extPlugins},
		{"prediction", extPrediction},
		{"debug", extDebug},
		{"service_tier", extServiceTier},
		{"session_id", extSessionID},
		{"stop_server_tools_when", extStopServerTools},
		{"trace", extTrace},
		{"include", extInclude},
		{"user", extUser},
		{"response_format", extResponseFormat},
		{"reasoning", extReasoning},
	}
	for _, p := range passthrough {
		if raw := extRaw(call, p.extKey); len(raw) > 0 {
			var v any
			if err := json.Unmarshal(raw, &v); err == nil {
				body[p.jsonKey] = v
			}
		}
	}
	for key, raw := range call.Extensions {
		if !strings.HasPrefix(key, extraBodyPrefix) {
			continue
		}
		field := key[len(extraBodyPrefix):]
		if field == "" || len(raw) == 0 || len(raw) > 64<<10 {
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err == nil {
			body[field] = v
		}
	}
	return nil
}
