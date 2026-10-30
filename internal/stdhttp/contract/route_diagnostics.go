package contract

import (
	"sort"
	"strings"
)

// RouteDiagnostic is a sanitized route ownership snapshot for operator diagnostics.
type RouteDiagnostic struct {
	OwnerID   string `json:"owner_id"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Transport string `json:"transport,omitempty"`
}

// RouteDiagnosticsFromRegistry projects registered claims into sanitized diagnostics.
func RouteDiagnosticsFromRegistry(reg *RouteRegistry) []RouteDiagnostic {
	if reg == nil {
		return nil
	}
	claims := reg.Claims()
	out := make([]RouteDiagnostic, 0, len(claims))
	for _, c := range claims {
		out = append(out, RouteDiagnostic{
			OwnerID:   sanitizeOwnerID(c.OwnerID),
			Method:    c.Method,
			Path:      c.Path,
			Kind:      string(c.Kind),
			Transport: transportForKind(c.Kind),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

func sanitizeOwnerID(owner string) string {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return "unknown"
	}
	if strings.ContainsAny(owner, "\r\n\t") {
		return "invalid"
	}
	return owner
}

func transportForKind(kind RouteKind) string {
	if strings.HasSuffix(strings.ToLower(string(kind)), "websocket") {
		return "websocket"
	}
	if kind != "" {
		return "http"
	}
	return ""
}
