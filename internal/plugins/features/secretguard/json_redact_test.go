package secretguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type recordingJSONMatcher struct {
	scanTokens   []string
	redactTokens []string
	scanHits     map[string][]sdk.Finding
	redactHits   map[string][]sdk.Finding
	redactions   map[string]string
}

func newRecordingJSONMatcher() *recordingJSONMatcher {
	return &recordingJSONMatcher{
		scanHits:   make(map[string][]sdk.Finding),
		redactHits: make(map[string][]sdk.Finding),
		redactions: make(map[string]string),
	}
}

func (m *recordingJSONMatcher) ScanBytes(ctx context.Context, input []byte) ([]sdk.Finding, error) {
	return m.ScanString(ctx, string(input))
}

func (m *recordingJSONMatcher) ScanString(_ context.Context, input string) ([]sdk.Finding, error) {
	m.scanTokens = append(m.scanTokens, input)
	if findings, ok := m.scanHits[input]; ok {
		return append([]sdk.Finding(nil), findings...), nil
	}
	return nil, nil
}

func (m *recordingJSONMatcher) RedactBytes(ctx context.Context, input []byte) ([]byte, []sdk.Finding, error) {
	redacted, findings, err := m.RedactString(ctx, string(input))
	return []byte(redacted), findings, err
}

func (m *recordingJSONMatcher) RedactString(_ context.Context, input string) (string, []sdk.Finding, error) {
	m.redactTokens = append(m.redactTokens, input)
	if redacted, ok := m.redactions[input]; ok {
		if findings, ok := m.redactHits[input]; ok {
			return redacted, append([]sdk.Finding(nil), findings...), nil
		}
		if findings, ok := m.scanHits[input]; ok {
			return redacted, append([]sdk.Finding(nil), findings...), nil
		}
		return redacted, nil, nil
	}
	return input, nil, nil
}

func findingForRef(ref string) []sdk.Finding {
	return []sdk.Finding{{
		SecretRefName:   ref,
		SourceCategory:  sdk.SourceCategoryProxyEnv,
		OccurrenceCount: 1,
	}}
}

func TestScanJSONPayload_scansKeysAndScalarsDeterministically(t *testing.T) {
	t.Parallel()
	m := newRecordingJSONMatcher()
	raw := []byte(`{"b":true,"a":{"y":null,"x":7},"c":"text"}`)

	findings, err := scanJSONPayload(t.Context(), m, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings: %#v", findings)
	}
	want := []string{"a", "x", "7", "y", "null", "b", "true", "c", "text"}
	if !reflect.DeepEqual(m.scanTokens, want) {
		t.Fatalf("scan order:\n got %#v\nwant %#v", m.scanTokens, want)
	}
}

func TestGuard_JSONTokens_blockAndLog(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		action string
		raw    string
		token  string
		ref    string
		want   sdk.Outcome
	}{
		{name: "key_block", action: ActionBlock, raw: `{"blocked-key":"safe"}`, token: "blocked-key", ref: "KEY_SECRET", want: sdk.OutcomeBlock},
		{name: "key_log", action: ActionLog, raw: `{"blocked-key":"safe"}`, token: "blocked-key", ref: "KEY_SECRET", want: sdk.OutcomeLog},
		{name: "number_block", action: ActionBlock, raw: `{"n":7}`, token: "7", ref: "NUMBER_SECRET", want: sdk.OutcomeBlock},
		{name: "number_log", action: ActionLog, raw: `{"n":7}`, token: "7", ref: "NUMBER_SECRET", want: sdk.OutcomeLog},
		{name: "bool_block", action: ActionBlock, raw: `{"b":true}`, token: "true", ref: "BOOL_SECRET", want: sdk.OutcomeBlock},
		{name: "bool_log", action: ActionLog, raw: `{"b":true}`, token: "true", ref: "BOOL_SECRET", want: sdk.OutcomeLog},
		{name: "null_block", action: ActionBlock, raw: `{"n":null}`, token: "null", ref: "NULL_SECRET", want: sdk.OutcomeBlock},
		{name: "null_log", action: ActionLog, raw: `{"n":null}`, token: "null", ref: "NULL_SECRET", want: sdk.OutcomeLog},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newRecordingJSONMatcher()
			m.scanHits[tc.token] = findingForRef(tc.ref)
			g := NewGuard(mustCfg(t, tc.action))
			call := aipapi.Call{
				Messages: []aipapi.Message{{
					Role:  aipapi.RoleUser,
					Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage(tc.raw)}},
				}},
			}
			before := aipapi.CloneCall(call)
			d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
			if err != nil {
				t.Fatal(err)
			}
			if d.Outcome != tc.want {
				t.Fatalf("outcome: got %q want %q", d.Outcome, tc.want)
			}
			if len(d.Findings) != 1 {
				t.Fatalf("findings: %#v", d.Findings)
			}
			if d.MutationCount != 0 {
				t.Fatalf("mutation_count: %d", d.MutationCount)
			}
			if d.ScanLimitHit {
				t.Fatal("scan_limit_hit must be false")
			}
			if d.FailureKind != "" || d.FailureReason != "" {
				t.Fatalf("unexpected failure metadata: %#v", d)
			}
			if err := d.Validate(); err != nil {
				t.Fatalf("decision must validate: %v", err)
			}
			if !reflect.DeepEqual(call, before) {
				t.Fatal("JSON scan/log must not mutate the call")
			}
		})
	}
}

func TestRedactJSONPayload_unsupportedTokenIsAtomic(t *testing.T) {
	t.Parallel()
	secret := "redact-me"
	blockedKey := "blocked-key"
	m := newRecordingJSONMatcher()
	m.redactions[secret] = strings.Repeat("*", len(secret))
	m.redactHits[secret] = findingForRef("STRING_SECRET")
	m.scanHits[blockedKey] = findingForRef("KEY_SECRET")
	raw := []byte(`{"a":"` + secret + `","` + blockedKey + `":"safe"}`)
	before := append([]byte(nil), raw...)

	out, findings, err := redactJSONPayload(t.Context(), m, raw)
	if err == nil {
		t.Fatal("expected unsupported json token error")
	}
	if _, ok := errors.AsType[*unsupportedJSONTokenError](err); !ok {
		t.Fatalf("error type: %T", err)
	}
	if out != nil {
		t.Fatalf("output must be nil on unsupported token, got %q", string(out))
	}
	if !bytes.Equal(raw, before) {
		t.Fatal("input raw JSON was mutated")
	}
	if len(findings) != 2 {
		t.Fatalf("findings: %#v", findings)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), blockedKey) {
		t.Fatalf("error leaked token values: %q", err.Error())
	}
}

func TestGuard_redactUnsupportedJSONTokenBlocksAtomically(t *testing.T) {
	t.Parallel()
	secret := "redact-me"
	blockedKey := "blocked-key"
	m := newRecordingJSONMatcher()
	m.redactions[secret] = strings.Repeat("*", len(secret))
	m.redactHits[secret] = findingForRef("STRING_SECRET")
	m.scanHits[blockedKey] = findingForRef("KEY_SECRET")
	g := NewGuard(mustCfg(t, ActionRedact))
	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: json.RawMessage(`{"a":"` + secret + `","` + blockedKey + `":"safe"}`)}},
		}},
	}
	before := aipapi.CloneCall(call)

	d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != sdk.OutcomeBlock {
		t.Fatalf("outcome: got %q want block", d.Outcome)
	}
	if d.MutationCount != 0 {
		t.Fatalf("mutation_count: %d", d.MutationCount)
	}
	if d.ScanLimitHit {
		t.Fatal("scan_limit_hit must be false")
	}
	if d.FailureKind != FailureKindUnsupportedJSONToken {
		t.Fatalf("failure_kind: %q", d.FailureKind)
	}
	if d.FailureReason != "unsupported JSON token encountered" {
		t.Fatalf("failure_reason: %q", d.FailureReason)
	}
	if len(d.Findings) != 2 {
		t.Fatalf("findings: %#v", d.Findings)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("decision must validate: %v", err)
	}
	if !reflect.DeepEqual(call, before) {
		t.Fatal("unsupported JSON token must leave the call unchanged")
	}
	if strings.Contains(d.FailureReason, secret) || strings.Contains(d.FailureReason, blockedKey) {
		t.Fatalf("failure reason leaked token values: %q", d.FailureReason)
	}
}
