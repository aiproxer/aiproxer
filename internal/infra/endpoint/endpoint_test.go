package endpoint_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/endpoint"
)

func TestParseBaseURL_acceptsAbsoluteHTTPAndHTTPS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "https_root", raw: "https://api.example.com", want: "https://api.example.com"},
		{name: "http_loopback", raw: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{name: "https_with_path", raw: "https://api.example.com/v1", want: "https://api.example.com/v1"},
		{name: "https_with_port_and_path", raw: "https://api.example.com:8443/v1", want: "https://api.example.com:8443/v1"},
		{name: "http_ipv6_with_port", raw: "http://[::1]:9000/v1", want: "http://[::1]:9000/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := endpoint.ParseBaseURL(tc.raw)
			failRED(t, err, "ParseBaseURL must validate absolute http/https URLs")
			if err != nil {
				t.Fatalf("ParseBaseURL(%q): %v", tc.raw, err)
			}
			if !d.Valid() {
				t.Fatal("expected validated descriptor")
			}
			if got := d.BaseURL(); got != tc.want {
				t.Fatalf("BaseURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseBaseURL_rejectsSchemesUserinfoHostFragmentsAndMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		wantSub string
	}{
		{name: "empty", raw: "", wantSub: "base_url"},
		{name: "whitespace", raw: "  ", wantSub: "base_url"},
		{name: "relative", raw: "/v1", wantSub: "absolute"},
		{name: "ftp", raw: "ftp://api.example.com/v1", wantSub: "scheme"},
		{name: "file", raw: "file:///tmp/models", wantSub: "scheme"},
		{name: "missing_scheme", raw: "api.example.com/v1", wantSub: "scheme"},
		{name: "userinfo", raw: "https://user:pass@api.example.com/v1", wantSub: "userinfo"},
		{name: "user_only", raw: "https://user@api.example.com/v1", wantSub: "userinfo"},
		{name: "fragment", raw: "https://api.example.com/v1#frag", wantSub: "fragment"},
		{name: "empty_host", raw: "https:///v1", wantSub: "host"},
		{name: "malformed", raw: "https://[::1", wantSub: "url"},
		{name: "ws", raw: "ws://api.example.com/v1", wantSub: "scheme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := endpoint.ParseBaseURL(tc.raw)
			failRED(t, err, "ParseBaseURL must reject unsafe/malformed base URLs")
			if err == nil {
				t.Fatalf("ParseBaseURL(%q): expected error", tc.raw)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantSub)) {
				t.Fatalf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestParseBaseURL_preservesPortsAndPathPrefixes(t *testing.T) {
	t.Parallel()
	d, err := endpoint.ParseBaseURL("https://api.example.com:8443/provider/v1")
	failRED(t, err, "ParseBaseURL must preserve ports and path prefixes")
	if err != nil {
		t.Fatal(err)
	}
	if d.Scheme() != "https" {
		t.Fatalf("Scheme = %q", d.Scheme())
	}
	if d.Host() != "api.example.com" {
		t.Fatalf("Host = %q", d.Host())
	}
	if d.Port() != "8443" {
		t.Fatalf("Port = %q", d.Port())
	}
	if d.Path() != "/provider/v1" {
		t.Fatalf("Path = %q", d.Path())
	}
}

func TestParseBaseURL_trailingSlashNormalizationIsDeterministic(t *testing.T) {
	t.Parallel()
	withSlash, err := endpoint.ParseBaseURL("https://api.example.com/v1/")
	failRED(t, err, "ParseBaseURL must normalize trailing slashes")
	if err != nil {
		t.Fatal(err)
	}
	withoutSlash, err := endpoint.ParseBaseURL("https://api.example.com/v1")
	failRED(t, err, "ParseBaseURL must normalize trailing slashes")
	if err != nil {
		t.Fatal(err)
	}
	if withSlash.BaseURL() != withoutSlash.BaseURL() {
		t.Fatalf("trailing-slash policy diverged: %q vs %q", withSlash.BaseURL(), withoutSlash.BaseURL())
	}
	if strings.HasSuffix(withSlash.BaseURL(), "/") {
		t.Fatalf("normalized base must not keep trailing slash: %q", withSlash.BaseURL())
	}
}

func TestParseBaseURL_collapsesEmptyPathSeparators(t *testing.T) {
	t.Parallel()
	d, err := endpoint.ParseBaseURL("https://api.example.com//provider///v1/")
	if err != nil {
		t.Fatal(err)
	}
	if d.Path() != "/provider/v1" {
		t.Fatalf("Path = %q, want /provider/v1", d.Path())
	}
	if d.BaseURL() != "https://api.example.com/provider/v1" {
		t.Fatalf("BaseURL = %q", d.BaseURL())
	}
	got, err := d.Join(endpoint.OperationOpenAIModels)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.example.com/provider/v1/models" {
		t.Fatalf("Join = %q", got)
	}
	if strings.Contains(strings.TrimPrefix(got, "https://"), "//") {
		t.Fatalf("duplicated separators remain: %q", got)
	}
}

func TestEndpointJoin_operationMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		base string
		op   endpoint.Operation
		want string
	}{
		{
			name: "openai_chat",
			base: "https://api.example.com/v1",
			op:   endpoint.OperationOpenAIChatCompletions,
			want: "https://api.example.com/v1/chat/completions",
		},
		{
			name: "openai_responses",
			base: "https://api.example.com/v1",
			op:   endpoint.OperationOpenAIResponses,
			want: "https://api.example.com/v1/responses",
		},
		{
			name: "openai_models_inventory",
			base: "https://api.example.com/v1",
			op:   endpoint.OperationOpenAIModels,
			want: "https://api.example.com/v1/models",
		},
		{
			name: "openai_path_prefix_preserved",
			base: "https://gateway.example.com/openai/v1",
			op:   endpoint.OperationOpenAIChatCompletions,
			want: "https://gateway.example.com/openai/v1/chat/completions",
		},
		{
			name: "openai_port_preserved",
			base: "http://127.0.0.1:8080/v1",
			op:   endpoint.OperationOpenAIModels,
			want: "http://127.0.0.1:8080/v1/models",
		},
		{
			name: "anthropic_messages",
			base: "https://api.example.com",
			op:   endpoint.OperationAnthropicMessages,
			want: "https://api.example.com/v1/messages",
		},
		{
			name: "anthropic_models_inventory",
			base: "https://api.example.com",
			op:   endpoint.OperationAnthropicModels,
			want: "https://api.example.com/v1/models",
		},
		{
			name: "anthropic_path_prefix_preserved",
			base: "https://gateway.example.com/anthropic",
			op:   endpoint.OperationAnthropicMessages,
			want: "https://gateway.example.com/anthropic/v1/messages",
		},
		{
			name: "no_duplicate_separators_from_trailing_slash_base",
			base: "https://api.example.com/v1/",
			op:   endpoint.OperationOpenAIModels,
			want: "https://api.example.com/v1/models",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := endpoint.ParseBaseURL(tc.base)
			failRED(t, err, "ParseBaseURL required before Join")
			if err != nil {
				t.Fatal(err)
			}
			got, err := d.Join(tc.op)
			failRED(t, err, "Descriptor.Join must implement operation joining")
			if err != nil {
				t.Fatalf("Join(%s): %v", tc.op, err)
			}
			if got != tc.want {
				t.Fatalf("Join(%s) = %q, want %q", tc.op, got, tc.want)
			}
			rest := got
			if _, after, ok := strings.Cut(got, "://"); ok {
				rest = after
			}
			if strings.Contains(rest, "//") {
				t.Fatalf("Join produced duplicated separators: %q", got)
			}
		})
	}
}

func TestEndpointJoin_rejectsZeroAndUnknownOperations(t *testing.T) {
	t.Parallel()
	var zero endpoint.Descriptor
	if _, err := zero.Join(endpoint.OperationOpenAIModels); err == nil {
		t.Fatal("zero descriptor Join must fail")
	} else {
		failRED(t, err, "zero descriptor Join must fail with a concrete validation error")
	}

	d, err := endpoint.ParseBaseURL("https://api.example.com/v1")
	failRED(t, err, "ParseBaseURL required before unknown-operation Join")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Join(endpoint.Operation("unknown.op")); err == nil {
		t.Fatal("unknown operation must fail")
	} else {
		failRED(t, err, "unknown operation Join must fail with a concrete validation error")
	}
}

// TestEndpointJoin_anthropicV1PolicyDocumentsAdapterInventoryContract locks the
// Anthropic base-URL policy used by the essential adapter and
// modeldiscover.AnthropicModelsProvider: BaseURL is an origin (optional gateway
// prefix allowed) without a `/v1` suffix; Join always appends `/v1/messages` or
// `/v1/models`. This differs from OpenAI-compatible bases that already include `/v1`.
func TestEndpointJoin_anthropicV1PolicyDocumentsAdapterInventoryContract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		base string
		op   endpoint.Operation
		want string
	}{
		{
			name: "origin_messages",
			base: "https://api.anthropic.com",
			op:   endpoint.OperationAnthropicMessages,
			want: "https://api.anthropic.com/v1/messages",
		},
		{
			name: "origin_models",
			base: "https://api.anthropic.com",
			op:   endpoint.OperationAnthropicModels,
			want: "https://api.anthropic.com/v1/models",
		},
		{
			name: "gateway_prefix_messages",
			base: "https://gateway.example.com/anthropic",
			op:   endpoint.OperationAnthropicMessages,
			want: "https://gateway.example.com/anthropic/v1/messages",
		},
		{
			name: "gateway_prefix_models",
			base: "https://gateway.example.com/anthropic",
			op:   endpoint.OperationAnthropicModels,
			want: "https://gateway.example.com/anthropic/v1/models",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := endpoint.ParseBaseURL(tc.base)
			if err != nil {
				t.Fatal(err)
			}
			got, err := d.Join(tc.op)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Join(%s) = %q, want %q", tc.op, got, tc.want)
			}
			if strings.Contains(d.Path(), "/v1") {
				t.Fatalf("Anthropic fixture base must not embed /v1; path=%q", d.Path())
			}
		})
	}
}

func failRED(t *testing.T, err error, msg string) {
	t.Helper()
	if errors.Is(err, endpoint.ErrNotImplemented) {
		t.Fatalf("RED: %s", msg)
	}
}
