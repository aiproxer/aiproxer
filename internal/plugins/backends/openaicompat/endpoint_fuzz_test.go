package openaicompat_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/infra/endpoint"
)

// FuzzCompatibleEndpoint is the backends-tree entrypoint required by Task 2.2
// validation. It fuzzes the shared infra/endpoint package only (no factories).
func FuzzCompatibleEndpoint(f *testing.F) {
	for _, seed := range []string{
		"https://api.example.com/v1",
		"https://api.example.com/v1/",
		"http://127.0.0.1:8080/v1",
		"https://gateway.example.com/anthropic",
		"https://user:pass@api.example.com/v1",
		"https://api.example.com/v1#x",
		"/v1",
		"https://[::1]:8443/v1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if !utf8.ValidString(raw) || len(raw) > 1024 {
			return
		}
		d, err := endpoint.ParseBaseURL(raw)
		if err != nil {
			return
		}
		joined, jerr := d.Join(endpoint.OperationOpenAIModels)
		if jerr != nil {
			t.Fatal(jerr)
		}
		if !strings.HasPrefix(joined, d.BaseURL()) {
			t.Fatalf("%q vs base %q", joined, d.BaseURL())
		}
		anth, aerr := d.Join(endpoint.OperationAnthropicModels)
		if aerr != nil {
			t.Fatal(aerr)
		}
		if !strings.HasSuffix(anth, "/v1/models") {
			t.Fatalf("anthropic models join=%q", anth)
		}
	})
}
