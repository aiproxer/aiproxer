// Package endpoint defines the immutable compatible-mode base URL contract.
//
// ParseBaseURL validates absolute http/https bases with no DNS or network I/O.
// Descriptor.Join applies one deterministic trailing-slash and separator policy
// for execution and inventory operations across the three generic modes.
package endpoint

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ErrNotImplemented is retained for Task 1.3 RED helpers; production paths no
// longer return it after Task 2.2.
var ErrNotImplemented = errors.New("endpoint: not implemented")

// Operation identifies a deterministic join target shared by execution and
// inventory for the three generic compatible modes.
type Operation string

const (
	OperationOpenAIChatCompletions Operation = "openai.chat_completions"
	OperationOpenAIResponses       Operation = "openai.responses"
	OperationOpenAIModels          Operation = "openai.models"
	OperationAnthropicMessages     Operation = "anthropic.messages"
	OperationAnthropicModels       Operation = "anthropic.models"
)

// Descriptor is an immutable validated base URL.
//
// Values are constructed only by ParseBaseURL. Zero value is invalid and must
// not be used for joining.
type Descriptor struct {
	scheme    string
	host      string
	port      string
	path      string
	baseURL   string
	validated bool
}

// Scheme returns the validated URL scheme (http or https).
func (d Descriptor) Scheme() string { return d.scheme }

// Host returns the validated host without userinfo.
func (d Descriptor) Host() string { return d.host }

// Port returns the explicit port when present; empty when the URL omitted it.
func (d Descriptor) Port() string { return d.port }

// Path returns the intentional path prefix preserved from the base URL.
func (d Descriptor) Path() string { return d.path }

// BaseURL returns the normalized absolute base URL without userinfo or fragment.
func (d Descriptor) BaseURL() string { return d.baseURL }

// Valid reports whether the descriptor was produced by a successful ParseBaseURL.
func (d Descriptor) Valid() bool { return d.validated }

// ParseBaseURL validates an absolute http/https base URL with a non-empty host.
//
// Contract:
//   - accept only http and https;
//   - reject userinfo, fragments, empty hosts, and malformed URLs;
//   - preserve explicit ports and intentional path prefixes;
//   - strip a single trailing slash for a deterministic join base;
//   - perform no DNS lookup or network I/O.
func ParseBaseURL(raw string) (Descriptor, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Descriptor{}, fmt.Errorf("endpoint: base_url is required")
	}
	if strings.Contains(trimmed, "#") {
		return Descriptor{}, fmt.Errorf("endpoint: fragment is not allowed in base_url")
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return Descriptor{}, fmt.Errorf("endpoint: invalid url: %w", err)
	}
	if u.Scheme == "" {
		return Descriptor{}, fmt.Errorf("endpoint: scheme is required; base_url must be absolute http or https")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return Descriptor{}, fmt.Errorf("endpoint: unsupported scheme %q (only http and https)", u.Scheme)
	}
	if !u.IsAbs() {
		return Descriptor{}, fmt.Errorf("endpoint: base_url must be absolute")
	}
	if u.User != nil {
		return Descriptor{}, fmt.Errorf("endpoint: userinfo is not allowed in base_url")
	}
	if u.Host == "" {
		return Descriptor{}, fmt.Errorf("endpoint: host is required")
	}
	host := u.Hostname()
	if host == "" {
		return Descriptor{}, fmt.Errorf("endpoint: host is required")
	}
	port := u.Port()
	path := cleanPathPrefix(u.EscapedPath())

	base := scheme + "://" + u.Host
	// u.Host already includes brackets for IPv6 and an explicit port when present.
	if path != "" {
		base += path
	}

	return Descriptor{
		scheme:    scheme,
		host:      host,
		port:      port,
		path:      path,
		baseURL:   base,
		validated: true,
	}, nil
}

// Join returns the absolute URL for an operation relative to the descriptor.
//
// One deterministic trailing-slash / separator policy covers execution and
// inventory joins: the validated base never ends with `/`, and each operation
// suffix begins with exactly one `/`.
//
// Anthropic policy (matches essential adapter + AnthropicModelsProvider):
// BaseURL is the API origin (and optional gateway prefix) without a `/v1`
// suffix. Join always appends `/v1/messages` or `/v1/models`. OpenAI-compatible
// bases typically already include `/v1`; Join appends `/chat/completions`,
// `/responses`, or `/models` only.
func (d Descriptor) Join(op Operation) (string, error) {
	if !d.validated || d.baseURL == "" {
		return "", fmt.Errorf("endpoint: descriptor is not validated")
	}
	suffix, err := operationSuffix(op)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(d.baseURL, "/")
	return base + suffix, nil
}

func operationSuffix(op Operation) (string, error) {
	switch op {
	case OperationOpenAIChatCompletions:
		return "/chat/completions", nil
	case OperationOpenAIResponses:
		return "/responses", nil
	case OperationOpenAIModels:
		return "/models", nil
	case OperationAnthropicMessages:
		return "/v1/messages", nil
	case OperationAnthropicModels:
		return "/v1/models", nil
	default:
		return "", fmt.Errorf("endpoint: unknown operation %q", op)
	}
}

// cleanPathPrefix preserves intentional path segments while dropping empty
// separators and a trailing slash (so Join never sees duplicated `/`).
func cleanPathPrefix(path string) string {
	if path == "" || path == "/" {
		return ""
	}
	parts := strings.Split(path, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return ""
	}
	return "/" + strings.Join(out, "/")
}
