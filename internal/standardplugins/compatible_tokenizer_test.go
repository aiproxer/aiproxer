package standardplugins

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"gopkg.in/yaml.v3"
)

func TestCompatibleTokenizer_omissionDefaultValidOverrideUnknown(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(srv.Close)

	cases := []struct {
		name      string
		tokenizer string
		wantID    string
		wantErr   string
	}{
		{name: "omission", tokenizer: "", wantID: ""},
		{name: "cl100k", tokenizer: "cl100k_base", wantID: "cl100k_base"},
		{name: "o200k", tokenizer: "o200k_base", wantID: "o200k_base"},
		{name: "unknown", tokenizer: "p50k_base", wantErr: "unknown compatible tokenizer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := compatibleTokenizerYAML("tok-"+tc.name, srv.URL, tc.tokenizer)
			be, err := buildCompatibleExecBackend(t, reg, CustomOpenAILegacyCompatibleID, "tok-"+tc.name, raw, srv.Client())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("BuildBackend err=%v want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(be.TokenizerID); got != tc.wantID {
				t.Fatalf("TokenizerID=%q want %q", got, tc.wantID)
			}
			if tc.wantID == "" && be.LocalCounter != nil {
				t.Fatal("expected no LocalCounter for default omission")
			}
			if tc.wantID != "" && be.LocalCounter == nil {
				t.Fatal("expected LocalCounter for override")
			}
		})
	}
}

func TestCompatibleTokenizer_sameKindInstancesUseDifferentTokenizers(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(srv.Close)

	beA, err := buildCompatibleExecBackend(t, reg, CustomOpenAIResponsesCompatibleID, "resp-a", compatibleTokenizerYAML("resp-a", srv.URL, "cl100k_base"), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	beB, err := buildCompatibleExecBackend(t, reg, CustomOpenAIResponsesCompatibleID, "resp-b", compatibleTokenizerYAML("resp-b", srv.URL, "o200k_base"), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if beA.TokenizerID == beB.TokenizerID {
		t.Fatalf("tokenizer ids must differ: %q", beA.TokenizerID)
	}
	if beA.LocalCounter == beB.LocalCounter {
		t.Fatal("expected independent local counters")
	}
}

func compatibleTokenizerYAML(prefix, baseURL, tokenizer string) string {
	var tokLine string
	if strings.TrimSpace(tokenizer) != "" {
		tokLine = "tokenizer: " + tokenizer + "\n"
	}
	return `backend_prefix: ` + prefix + `
base_url: ` + baseURL + `/v1
` + tokLine
}

func buildCompatibleExecBackend(t *testing.T, reg *pluginreg.Registry, factory, instanceID, raw string, client *http.Client) (execbackend.Backend, error) {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		return execbackend.Backend{}, err
	}
	res, err := reg.BuildBackendWithLifecycle(factory, instanceID, node, client, pluginreg.BackendFactoryDeps{})
	if err != nil {
		return execbackend.Backend{}, err
	}
	return res.Backend, nil
}
