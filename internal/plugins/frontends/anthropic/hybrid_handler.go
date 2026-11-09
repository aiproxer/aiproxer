package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type hybridHandler struct {
	next       http.Handler
	external   http.Handler
	models     map[string]hybridModel
	native     *http.Client
	maxBody    int64
	headers    aipsdk.HTTPHeaders
	generation context.Context
	log        *slog.Logger
}

func newHybridHandler(next http.Handler, cfg hybridConfig, opts aipsdk.FrontendMountOptions) *hybridHandler {
	h := &hybridHandler{next: next, models: make(map[string]hybridModel), native: newHybridNativeClient(), maxBody: opts.MaxRequestBodyBytes, headers: opts.HTTPHeaders.OrDefault(), generation: opts.GenerationContext, log: diag.LoggerOrDefault(opts.Log)}
	if h.maxBody <= 0 {
		h.maxBody = 8 << 20
	}
	h.external = next
	for _, m := range cfg.ExternalModels {
		h.models[m.Model] = m
	}
	return h
}

// Client recognition is NOT authentication. Only the loopback deployment gate
// makes these spoofable wire hints suitable for this opt-in local integration.
func isClaudeCodeCLI(h http.Header) bool {
	return len(h.Values("User-Agent")) == 1 && strings.HasPrefix(h.Get("User-Agent"), "claude-cli/") && len(h.Values("x-app")) == 1 && h.Get("x-app") == "cli"
}

func hybridError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": message}})
}

func (h *hybridHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Disabled mode never installs this wrapper. Unrecognized clients retain
	// the ordinary canonical frontend; neither native nor hybrid routing arms.
	if !isClaudeCodeCLI(r.Header) {
		h.next.ServeHTTP(w, r)
		return
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() {
		hybridError(w, 403, "Claude Code hybrid mode is loopback-only")
		return
	}
	if !hybridLoopbackHost(r.Host) {
		hybridError(w, 400, "hybrid requests require a loopback Host")
		return
	}
	if r.Method != "POST" || r.URL.Path != "/v1/messages" || r.URL.RawPath != "" || r.URL.IsAbs() || r.URL.Host != "" || r.URL.Fragment != "" || (r.URL.RawQuery != "" && r.URL.RawQuery != "beta=true") {
		hybridError(w, 400, "unsupported hybrid operation or query")
		return
	}
	for _, name := range append([]string{aipsdk.HeaderRoute}, h.headers.Route...) {
		if len(r.Header.Values(name)) != 0 {
			hybridError(w, 400, "client route overrides are forbidden in hybrid mode")
			return
		}
	}
	if !validHybridCredential(r.Header) {
		hybridError(w, 400, "exactly one first-party Bearer or x-api-key credential is required")
		return
	}
	if len(r.Header.Values("Connection")) > 0 {
		for _, v := range r.Header.Values("Connection") {
			for name := range strings.SplitSeq(v, ",") {
				name = strings.ToLower(strings.TrimSpace(name))
				if name != "close" && name != "keep-alive" {
					hybridError(w, 400, "connection header nominations are forbidden in hybrid mode")
					return
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), hybridExchangeTimeout)
	defer cancel()
	if h.generation != nil {
		stop := context.AfterFunc(h.generation, cancel)
		defer stop()
		if h.generation.Err() != nil {
			hybridError(w, 503, "hybrid generation retired")
			return
		}
	}
	r = r.WithContext(ctx)
	stopRead := context.AfterFunc(ctx, func() { _ = r.Body.Close() })
	defer stopRead()
	// The server read timeout bounds slow reads; direct mounts must supply
	// their own HTTP server timeouts, as for the ordinary frontend.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBody))
	if err != nil {
		hybridError(w, 413, "hybrid request body exceeds limit or cannot be read")
		return
	}
	if ctx.Err() != nil {
		hybridError(w, 503, "hybrid request cancelled or generation retired")
		return
	}
	model, ok := hybridModelFromJSON(body)
	if !ok {
		hybridError(w, 400, "hybrid request requires one exact model string")
		return
	}
	if m, exists := h.models[model]; exists {
		if m.RequireSubagent && !hybridSubagent(r.Header) {
			hybridError(w, 400, "external model requires non-main subagent evidence")
			return
		}
		// Build from scratch: no original auth, beta, cookie, proxy or control
		// headers survive into canonical decoding, traffic hooks or execution.
		fresh, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", bytes.NewReader(body))
		fresh.RemoteAddr = r.RemoteAddr
		fresh.Header.Set("Content-Type", "application/json")
		fresh.Header.Set("User-Agent", r.Header.Get("User-Agent"))
		fresh.Header.Set("x-app", "cli")
		fresh.Header.Set(aipsdk.HeaderRoute, m.Route)
		h.external.ServeHTTP(w, fresh)
		return
	}
	if !strings.HasPrefix(model, "claude-") || !validHybridIdentifier(model) {
		hybridError(w, 400, "model is not allowlisted for hybrid mode")
		return
	}
	h.serveNative(w, r, body)
}

func hybridLoopbackHost(host string) bool {
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// Anthropic places tool results in user content; canonical backends consume
// each as a tool-role message. Split at the existing block boundaries without
// reordering any content or inventing model-authored turns. Native bytes and
// the normal (non-hybrid) decoder are untouched.
func hybridToolResultMessages(messages []aipapi.Message) []aipapi.Message {
	var out []aipapi.Message
	for _, m := range messages {
		if m.Role != aipapi.RoleUser {
			out = append(out, m)
			continue
		}
		start := 0
		for i, p := range m.Parts {
			if p.Kind != aipapi.PartToolResult {
				continue
			}
			if start < i {
				out = append(out, aipapi.Message{Role: aipapi.RoleUser, Parts: m.Parts[start:i]})
			}
			// Legacy canonical consumers read the tool result's JSON Content;
			// Anthropic's decoder also retains its plain-text projection in Text.
			if len(p.Content) == 0 {
				p.Content, _ = json.Marshal(p.Text)
			}
			out = append(out, aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{p}})
			start = i + 1
		}
		if start < len(m.Parts) {
			out = append(out, aipapi.Message{Role: m.Role, Parts: m.Parts[start:]})
		}
	}
	return out
}

func validHybridCredential(h http.Header) bool {
	a, k := h.Values("Authorization"), h.Values("x-api-key")
	if len(a) == 1 && len(k) == 0 {
		parts := strings.Fields(a[0])
		return len(parts) == 2 && strings.EqualFold(parts[0], "Bearer")
	}
	return len(a) == 0 && len(k) == 1 && strings.TrimSpace(k[0]) != ""
}

func hybridSubagent(h http.Header) bool {
	if len(h.Values("x-claude-code-agent-id")) != 1 || strings.TrimSpace(h.Get("x-claude-code-agent-id")) == "" {
		return false
	}
	for _, name := range []string{"request-class", "x-claude-code-request-class"} {
		if len(h.Values(name)) > 1 || strings.EqualFold(strings.TrimSpace(h.Get(name)), "main") {
			return false
		}
	}
	return true
}

// Reject duplicate top-level keys (especially model) instead of allowing the
// dispatcher and upstream JSON parsers to disagree. Struct decoding also matches
// model case-insensitively, so only its exact lowercase spelling is admitted.
// Never interpret prompts.
func hybridModelFromJSON(body []byte) (string, bool) {
	d := json.NewDecoder(bytes.NewReader(body))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return "", false
	}
	seen := map[string]bool{}
	model := ""
	for d.More() {
		t, err = d.Token()
		if err != nil {
			return "", false
		}
		key, ok := t.(string)
		if !ok || seen[key] || (strings.EqualFold(key, "model") && key != "model") {
			return "", false
		}
		seen[key] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return "", false
		}
		if key == "model" {
			if json.Unmarshal(raw, &model) != nil {
				return "", false
			}
		}
	}
	if _, err = d.Token(); err != nil {
		return "", false
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return "", false
	}
	return model, model != ""
}
