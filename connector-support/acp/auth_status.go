package acp

import (
	"strings"
	"unicode"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// acpAuthStatusMethod is the adapter's fire-and-forget identity push. It
// arrives after initialize and whenever the login changes; clients that do
// not know it drop it silently.
const acpAuthStatusMethod = "_auth/status_update"

// maxAuthStatusFieldBytes bounds retained identity display strings.
const maxAuthStatusFieldBytes = 128

// mapAuthStatusToEvent maps one _auth/status_update notification to a
// redacted warning event. Only the identity kind and display label travel:
// account email, organization and vendor extras never leave the adapter.
// Unknown kinds and malformed payloads are dropped, never guessed.
func mapAuthStatusToEvent(params map[string]any) (aipapi.Event, bool) {
	if params == nil {
		return aipapi.Event{}, false
	}
	status, ok := params["authStatus"].(map[string]any)
	if !ok {
		return aipapi.Event{}, false
	}
	kind, ok := status["kind"].(string)
	if !ok {
		return aipapi.Event{}, false
	}
	switch kind {
	case "account", "api_key", "none", "external", "gateway":
	default:
		return aipapi.Event{}, false
	}
	summary := "auth identity kind=" + kind
	if label, ok := status["label"].(string); ok && label != "" {
		label, ok := cleanAuthStatusField(label)
		if !ok {
			return aipapi.Event{}, false
		}
		summary += " label=" + quoteJSONString(label)
	}
	// The account bag may carry email and organization: read nothing from it,
	// but require a sane shape so malformed vendor payloads cannot pass.
	if raw, present := status["account"]; present {
		bag, ok := raw.(map[string]any)
		if !ok {
			return aipapi.Event{}, false
		}
		if plan, present := bag["plan"]; present {
			if _, ok := plan.(string); !ok {
				return aipapi.Event{}, false
			}
		}
	}
	return aipapi.Event{
		Kind:           aipapi.EventWarning,
		WarningCode:    "auth_status",
		WarningMessage: summary,
	}, true
}

// cleanAuthStatusField keeps short printable display strings. Control
// characters and overlong values are rejected so display text cannot smuggle
// terminal sequences or bulk data into diagnostics.
func cleanAuthStatusField(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxAuthStatusFieldBytes {
		return "", false
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return "", false
	}
	return s, true
}
