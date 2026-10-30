package secretguard

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"gopkg.in/yaml.v3"
)

func FuzzJSONRedact_invariants(f *testing.F) {
	secret := testkit.SyntheticOpenAIAPIKey
	m := newExactStub(secret, "OPENAI_API_KEY", sdk.SourceCategoryProxyEnv)
	f.Add([]byte(`{"a":"x"}`))
	f.Add([]byte(`{"a":"` + secret + `","n":1}`))
	f.Add([]byte(`[` + jsonQuote(secret) + `,2,true,null]`))
	f.Add([]byte(`not-json-` + secret))
	f.Fuzz(func(t *testing.T, raw []byte) {
		before := append([]byte(nil), raw...)
		out, findings, err := redactJSONPayload(t.Context(), m, raw)
		if err != nil {
			assertNoSyntheticSecrets(t, err.Error())
			return
		}
		assertFindingsSafe(t, findings)
		if !bytes.Equal(raw, before) {
			t.Fatal("redactJSONPayload mutated its input")
		}
		if bytes.Contains(out, []byte(secret)) {
			t.Fatal("redacted JSON/opaque still contains secret")
		}
		if json.Valid(raw) && !json.Valid(out) {
			t.Fatal("valid JSON input produced invalid JSON output")
		}
	})
}

func FuzzGuard_scanNoPanic(f *testing.F) {
	secret := testkit.SyntheticUnicodeSecret
	m := newExactStub(secret, "UNICODE_SECRET", sdk.SourceCategoryOperatorEnv)
	g := NewGuard(decodeActionConfig(ActionBlock))
	f.Add("plain")
	f.Add(secret)
	f.Add("prefix-" + secret + "-suffix")
	f.Fuzz(func(t *testing.T, text string) {
		call := aipapi.Call{
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: text}},
			}},
		}
		d, err := g.Evaluate(t.Context(), &call, sdk.Meta{}, servicesWith(m))
		if err != nil {
			assertNoSyntheticSecrets(t, err.Error())
		}
		assertFindingsSafe(t, d.Findings)
		assertNoSyntheticSecrets(t, d.FailureReason)
		assertNoSyntheticSecrets(t, d.FailureKind)
	})
}

func decodeActionConfig(action string) Config {
	var n yaml.Node
	if err := yaml.Unmarshal([]byte("action: "+action), &n); err != nil {
		panic(err)
	}
	cfg, err := DecodeConfig(n)
	if err != nil {
		panic(err)
	}
	return cfg
}

func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func assertFindingsSafe(t *testing.T, findings []sdk.Finding) {
	t.Helper()
	for _, finding := range findings {
		assertNoSyntheticSecrets(t, finding.SecretRefName)
		assertNoSyntheticSecrets(t, finding.Location)
		for _, a := range finding.Aliases {
			assertNoSyntheticSecrets(t, a)
		}
	}
}
