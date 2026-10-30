package endpoint_test

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/infra/endpoint"
)

// FuzzCompatibleEndpoint exercises ParseBaseURL and Join without DNS/network.
// Seed corpus covers normalization, encoded separators, unusual ports, IPv6,
// Anthropic origin policy, and malformed inputs.
func FuzzCompatibleEndpoint(f *testing.F) {
	seeds := []string{
		"https://api.example.com",
		"https://api.example.com/v1",
		"https://api.example.com/v1/",
		"http://127.0.0.1:8080/v1",
		"http://[::1]:9000/v1",
		"https://api.example.com:8443/provider/v1",
		"https://gateway.example.com/anthropic",
		"https://user:pass@api.example.com/v1",
		"https://api.example.com/v1#frag",
		"ftp://api.example.com/v1",
		"/v1",
		"",
		"https:///v1",
		"https://[::1",
		"https://api.example.com/a%2Fb",
		"http://api.example.com:65535/v1",
		"https://api.example.com/" + strings.Repeat("p", 64),
		"http://0//0",
		"https://api.example.com//v1/",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	ops := []endpoint.Operation{
		endpoint.OperationOpenAIChatCompletions,
		endpoint.OperationOpenAIResponses,
		endpoint.OperationOpenAIModels,
		endpoint.OperationAnthropicMessages,
		endpoint.OperationAnthropicModels,
	}

	f.Fuzz(func(t *testing.T, raw string) {
		if !utf8.ValidString(raw) || len(raw) > 2048 {
			return
		}
		d, err := endpoint.ParseBaseURL(raw)
		if err != nil {
			if d.Valid() {
				t.Fatalf("invalid parse returned Valid descriptor for %q", raw)
			}
			return
		}
		if !d.Valid() {
			t.Fatalf("successful parse must be Valid for %q", raw)
		}
		if d.Scheme() != "http" && d.Scheme() != "https" {
			t.Fatalf("scheme=%q", d.Scheme())
		}
		if d.Host() == "" {
			t.Fatal("host must be non-empty")
		}
		if strings.HasSuffix(d.BaseURL(), "/") {
			t.Fatalf("normalized base keeps trailing slash: %q", d.BaseURL())
		}
		if strings.Contains(d.BaseURL(), "#") {
			t.Fatalf("normalized base retained fragment: %q", d.BaseURL())
		}
		parsed, perr := url.Parse(d.BaseURL())
		if perr != nil || parsed.User != nil || parsed.Fragment != "" || !parsed.IsAbs() {
			t.Fatalf("normalized base is not a clean absolute URL: %q err=%v user=%v", d.BaseURL(), perr, parsed.User)
		}
		rest := d.BaseURL()
		if _, after, ok := strings.Cut(rest, "://"); ok {
			rest = after
		}
		if strings.Contains(rest, "//") {
			t.Fatalf("normalized base has duplicated separators: %q", d.BaseURL())
		}
		for _, op := range ops {
			joined, jerr := d.Join(op)
			if jerr != nil {
				t.Fatalf("Join(%s) on valid descriptor: %v", op, jerr)
			}
			if !strings.HasPrefix(joined, d.BaseURL()) {
				t.Fatalf("Join(%s)=%q does not preserve base %q", op, joined, d.BaseURL())
			}
			rest := joined
			if _, after, ok := strings.Cut(joined, "://"); ok {
				rest = after
			}
			if strings.Contains(rest, "//") {
				t.Fatalf("Join duplicated separators: %q", joined)
			}
		}
	})
}
