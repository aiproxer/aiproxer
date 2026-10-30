package diag

import (
	"fmt"
	"sort"
	"strings"
)

// InstanceDiagnostic is the bounded common operator projection for an extension
// instance. Protocol-specific data belongs in Details, supplied by its owner.
type InstanceDiagnostic struct {
	ID                string      `json:"id"`
	InstanceID        string      `json:"instance_id,omitempty"`
	FactoryKind       string      `json:"factory_kind"`
	Origin            string      `json:"origin"`
	Enabled           bool        `json:"enabled"`
	Family            string      `json:"family,omitempty"`
	Profile           string      `json:"profile,omitempty"`
	Capabilities      []string    `json:"capabilities,omitempty"`
	RouteClaims       []string    `json:"route_claims,omitempty"`
	InventoryState    string      `json:"inventory_state,omitempty"`
	Conformance       string      `json:"conformance,omitempty"`
	ConfigError       string      `json:"config_error,omitempty"`
	Details           []SafeField `json:"details,omitempty"`
	BasePath          string      `json:"base_path,omitempty"`
	WebSocketEnabled  bool        `json:"websocket_enabled,omitempty"`
	ContinuationStore string      `json:"continuation_store,omitempty"`
	ContinuationTTL   string      `json:"continuation_ttl,omitempty"`
	AllowedOrigins    []string    `json:"allowed_origins,omitempty"`
}

type SafeField struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

const (
	maxDiagnosticDetails  = 16
	maxDiagnosticBytes    = 256
	maxDiagnosticSliceLen = 64
)

// SanitizeInstanceDiagnostic bounds and sanitizes the common operator projection
// before a contribution-owned row reaches HTTP JSON: scalar fields and slice
// items are truncated to the byte bound with control characters stripped, and
// cardinality limits are enforced as errors. Values are preserved (not dropped)
// so operators keep diagnostic detail; rejection is reserved for unbounded
// cardinality.
func SanitizeInstanceDiagnostic(in InstanceDiagnostic) (InstanceDiagnostic, error) {
	out := in
	out.ID = boundedString(in.ID)
	out.InstanceID = boundedString(in.InstanceID)
	out.FactoryKind = boundedString(in.FactoryKind)
	out.Origin = boundedString(in.Origin)
	out.Family = boundedString(in.Family)
	out.Profile = boundedString(in.Profile)
	out.InventoryState = boundedString(in.InventoryState)
	out.Conformance = boundedString(in.Conformance)
	out.ConfigError = boundedString(in.ConfigError)
	out.BasePath = boundedString(in.BasePath)
	out.ContinuationStore = boundedString(in.ContinuationStore)
	out.ContinuationTTL = boundedString(in.ContinuationTTL)
	if len(in.Details) > maxDiagnosticDetails {
		return InstanceDiagnostic{}, fmt.Errorf("diag: too many extension details")
	}
	out.Details = make([]SafeField, 0, len(in.Details))
	for _, field := range in.Details {
		key := boundedString(field.Key)
		if key == "" {
			return InstanceDiagnostic{}, fmt.Errorf("diag: invalid extension detail")
		}
		out.Details = append(out.Details, SafeField{Key: key, Value: boundedString(field.Value)})
	}
	sort.Slice(out.Details, func(i, j int) bool { return out.Details[i].Key < out.Details[j].Key })

	var err error
	if out.Capabilities, err = sanitizeSlice(in.Capabilities, "capabilities"); err != nil {
		return InstanceDiagnostic{}, err
	}
	if out.RouteClaims, err = sanitizeSlice(in.RouteClaims, "route_claims"); err != nil {
		return InstanceDiagnostic{}, err
	}
	if out.AllowedOrigins, err = sanitizeSlice(in.AllowedOrigins, "allowed_origins"); err != nil {
		return InstanceDiagnostic{}, err
	}
	return out, nil
}

func sanitizeSlice(in []string, name string) ([]string, error) {
	if len(in) > maxDiagnosticSliceLen {
		return nil, fmt.Errorf("diag: too many %s items", name)
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		out = append(out, boundedString(item))
	}
	sort.Strings(out)
	return out, nil
}

// FallbackRowForRejectedProjection converts a contribution row that failed
// SanitizeInstanceDiagnostic into a bounded error row. It re-applies the same
// byte and control-character bounds so a rejected input cannot re-enter the
// projection verbatim through the fallback path.
// SanitizedInstanceProjection is the canonical operator projection shared by
// inventory and route read models. OpenResponsesRows is a filtered view of the
// same sanitized instances, never a second sanitization pass.
type SanitizedInstanceProjection struct {
	Instances         []InstanceDiagnostic
	OpenResponsesRows []InstanceDiagnostic
}

func ProjectSanitizedInstanceDiagnostics(rows []InstanceDiagnostic) SanitizedInstanceProjection {
	out := SanitizedInstanceProjection{
		Instances:         make([]InstanceDiagnostic, 0, len(rows)),
		OpenResponsesRows: make([]InstanceDiagnostic, 0),
	}
	for _, row := range rows {
		sanitized, err := SanitizeInstanceDiagnostic(row)
		if err != nil {
			sanitized = FallbackRowForRejectedProjection(row, err)
		}
		out.Instances = append(out.Instances, sanitized)
		if sanitized.Origin == "client_facing" && strings.EqualFold(sanitized.FactoryKind, "openresponses") {
			out.OpenResponsesRows = append(out.OpenResponsesRows, sanitized)
		}
	}
	return out
}

func FallbackRowForRejectedProjection(row InstanceDiagnostic, sanitizeErr error) InstanceDiagnostic {
	id := row.ID
	if id == "" {
		id = row.InstanceID
	}
	return InstanceDiagnostic{
		ID:          boundedString(id),
		InstanceID:  boundedString(row.InstanceID),
		FactoryKind: boundedString(row.FactoryKind),
		Origin:      boundedString(row.Origin),
		Enabled:     false,
		ConfigError: boundedString(sanitizeErr.Error()),
	}
}

func boundedString(s string) string {
	if len(s) > maxDiagnosticBytes {
		s = s[:maxDiagnosticBytes]
	}
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', '\t':
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}
