package secretguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type unsupportedJSONTokenError struct {
	findings []sdk.Finding
}

func (e *unsupportedJSONTokenError) Error() string {
	return ID + ": unsupported json token"
}

func newUnsupportedJSONTokenError(findings []sdk.Finding) error {
	return &unsupportedJSONTokenError{findings: append([]sdk.Finding(nil), findings...)}
}

// redactJSONPayload redacts JSON string values in place and reports unsupported
// key/scalar matches as a safe block signal. Invalid JSON is treated as opaque
// bytes via Matcher.RedactBytes. Remarshaled object/array output is JSON-valid
// when the input parsed successfully. Numbers use json.Number so integer
// precision is preserved across rewrite.
func redactJSONPayload(ctx context.Context, m sdk.Matcher, raw []byte) ([]byte, []sdk.Finding, error) {
	if len(raw) == 0 || m == nil {
		return append([]byte(nil), raw...), nil, nil
	}
	v, err := decodeJSONPreserveNumbers(raw)
	if err != nil {
		return m.RedactBytes(ctx, raw)
	}
	changed := false
	findings, err := walkRedactJSON(ctx, m, &v, &changed)
	if err != nil {
		return nil, findings, err
	}
	// Findings do not imply a replacement. Preserve the caller's representation
	// unless a string value actually changed, including duplicate keys/escapes.
	if !changed {
		return bytes.Clone(raw), findings, nil
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: remarshal json: %w", ID, err)
	}
	if !json.Valid(out) {
		return nil, nil, fmt.Errorf("%s: remarshaled json is invalid", ID)
	}
	return out, findings, nil
}

// scanJSONPayload scans JSON string values, keys, and scalar tokens. Invalid
// JSON uses ScanBytes.
func scanJSONPayload(ctx context.Context, m sdk.Matcher, raw []byte) ([]sdk.Finding, error) {
	if len(raw) == 0 || m == nil {
		return nil, nil
	}
	v, err := decodeJSONPreserveNumbers(raw)
	if err != nil {
		return m.ScanBytes(ctx, raw)
	}
	return walkScanJSON(ctx, m, v)
}

func decodeJSONPreserveNumbers(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func walkRedactJSON(ctx context.Context, m sdk.Matcher, v *any, changed *bool) ([]sdk.Finding, error) {
	if v == nil {
		return nil, nil
	}
	switch cur := (*v).(type) {
	case string:
		redacted, findings, err := m.RedactString(ctx, cur)
		if err != nil {
			return nil, err
		}
		if redacted != cur {
			*changed = true
			*v = redacted
		}
		return findings, nil
	case json.Number:
		findings, err := m.ScanString(ctx, cur.String())
		if err != nil {
			return nil, err
		}
		if len(findings) > 0 {
			return findings, newUnsupportedJSONTokenError(findings)
		}
		return nil, nil
	case bool:
		token := "false"
		if cur {
			token = "true"
		}
		findings, err := m.ScanString(ctx, token)
		if err != nil {
			return nil, err
		}
		if len(findings) > 0 {
			return findings, newUnsupportedJSONTokenError(findings)
		}
		return nil, nil
	case nil:
		findings, err := m.ScanString(ctx, "null")
		if err != nil {
			return nil, err
		}
		if len(findings) > 0 {
			return findings, newUnsupportedJSONTokenError(findings)
		}
		return nil, nil
	case map[string]any:
		var all []sdk.Finding
		for _, k := range sortedJSONKeys(cur) {
			keyFindings, err := m.ScanString(ctx, k)
			if err != nil {
				return nil, err
			}
			all = mergeFindings(all, keyFindings)
			if len(keyFindings) > 0 {
				return all, newUnsupportedJSONTokenError(all)
			}
			c := cur[k]
			f, err := walkRedactJSON(ctx, m, &c, changed)
			if err != nil {
				if unsupported, ok := errors.AsType[*unsupportedJSONTokenError](err); ok {
					combined := mergeFindings(all, unsupported.findings)
					return combined, newUnsupportedJSONTokenError(combined)
				}
				return all, err
			}
			cur[k] = c
			all = mergeFindings(all, f)
		}
		return all, nil
	case []any:
		var all []sdk.Finding
		for i := range cur {
			c := cur[i]
			f, err := walkRedactJSON(ctx, m, &c, changed)
			if err != nil {
				if unsupported, ok := errors.AsType[*unsupportedJSONTokenError](err); ok {
					combined := mergeFindings(all, unsupported.findings)
					return combined, newUnsupportedJSONTokenError(combined)
				}
				return all, err
			}
			cur[i] = c
			all = mergeFindings(all, f)
		}
		return all, nil
	default:
		return nil, nil
	}
}

func walkScanJSON(ctx context.Context, m sdk.Matcher, v any) ([]sdk.Finding, error) {
	switch cur := v.(type) {
	case string:
		return m.ScanString(ctx, cur)
	case json.Number:
		return m.ScanString(ctx, cur.String())
	case bool:
		if cur {
			return m.ScanString(ctx, "true")
		}
		return m.ScanString(ctx, "false")
	case nil:
		return m.ScanString(ctx, "null")
	case map[string]any:
		var all []sdk.Finding
		for _, k := range sortedJSONKeys(cur) {
			keyFindings, err := m.ScanString(ctx, k)
			if err != nil {
				return nil, err
			}
			all = mergeFindings(all, keyFindings)
			childFindings, err := walkScanJSON(ctx, m, cur[k])
			if err != nil {
				return nil, err
			}
			all = mergeFindings(all, childFindings)
		}
		return all, nil
	case []any:
		var all []sdk.Finding
		for _, child := range cur {
			f, err := walkScanJSON(ctx, m, child)
			if err != nil {
				return nil, err
			}
			all = mergeFindings(all, f)
		}
		return all, nil
	default:
		return nil, nil
	}
}

func sortedJSONKeys(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(m))
}
