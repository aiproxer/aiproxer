package codex

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	codexBetaHeader     = "responses=experimental"
	codexWSBetaHeader   = "responses-websocket-mode=v2"
	codexOriginator     = "codex_cli_rs"
	codexVersionHeader  = "0.0.0"
	codexTaskTypeHeader = "standard"
)

var codexUserAgentValue = fmt.Sprintf("%s/%s (%s; %s)", codexOriginator, codexVersionHeader, runtime.GOOS, runtime.GOARCH)

func normalizedResponsesBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasSuffix(base, "/responses") {
		base += "/responses"
	}
	return base
}

func responsesEndpoint(baseURL string) string {
	return normalizedResponsesBase(baseURL)
}

// responsesCompactionEndpoint is the upstream-only Codex/OpenResponses unary
// compaction route. It is intentionally not exposed as a client-facing route.
func responsesCompactionEndpoint(baseURL string) string {
	return strings.TrimRight(responsesEndpoint(baseURL), "/") + "/compact"
}

func applyCodexHeaders(req *http.Request, cfg Config, conversationID string) {
	mergeCodexHeaders(req.Header, cfg, conversationID)
}

// codexHeaders builds the Codex request headers shared by HTTPS and WebSocket
// transports. WebSocket dial uses this directly since it has no *http.Request.
func codexHeaders(cfg Config, conversationID string) http.Header {
	h := http.Header{}
	mergeCodexHeaders(h, cfg, conversationID)
	return h
}

func codexWSHeaders(cfg Config, conversationID string) http.Header {
	h := codexHeaders(cfg, conversationID)
	// The WebSocket handshake uses a different beta opt-in than HTTPS Responses.
	// WebSocket transport sends only responses-websocket-mode=v2, so this
	// intentionally replaces the HTTPS responses=experimental value.
	h.Set("OpenAI-Beta", codexWSBetaHeader)
	return h
}

func mergeCodexHeaders(h http.Header, cfg Config, conversationID string) {
	h.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.AccessToken))
	h.Set("OpenAI-Beta", codexBetaHeader)
	h.Set("Accept", "text/event-stream")
	h.Set("Content-Type", "application/json")
	h.Set("version", codexVersionHeader)
	h.Set("originator", codexOriginator)
	h.Set("User-Agent", codexUserAgent())
	h.Set("conversation_id", conversationID)
	h.Set("session_id", conversationID)
	h.Set("Codex-Task-Type", codexTaskTypeHeader)
	if id := strings.TrimSpace(cfg.AccountID); id != "" {
		h.Set("chatgpt-account-id", id)
	}
}

func codexUserAgent() string {
	return codexUserAgentValue
}

// primaryConversationID returns the proxy-owned conversation affinity when one is
// available. Client-only session hints remain suitable for transport stickiness,
// but are not used as native-context authority.
func primaryConversationID(call aipapi.Call) string {
	if id := strings.TrimSpace(call.Session.AuthoritativeSessionID); id != "" {
		return id
	}
	if id := strings.TrimSpace(call.ID); id != "" && !isGeneratedCallID(id) {
		return id
	}
	return ""
}

func conversationID(call aipapi.Call, model string) string {
	if id := primaryConversationID(call); id != "" {
		return id
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "codex"
	}
	return "aip-" + model
}

func conversationIDForPayload(call aipapi.Call, model string, payload Payload) string {
	return conversationIDForPayloadWithFingerprints(call, model, payload, nil)
}

func conversationIDForPayloadWithFingerprints(call aipapi.Call, model string, payload Payload, inputFingerprints []string) string {
	if id := primaryConversationID(call); id != "" {
		return id
	}
	if len(payload.Input) > 0 {
		fp := firstInputFingerprint(payload.Input, inputFingerprints)
		if len(fp) > 16 {
			fp = fp[:16]
		}
		return "aip-" + strings.TrimSpace(model) + "-" + fp
	}
	return conversationID(call, model)
}

func firstInputFingerprint(input []inputItem, inputFingerprints []string) string {
	if len(inputFingerprints) > 0 {
		return inputFingerprints[0]
	}
	if len(input) == 0 {
		return ""
	}
	return fingerprintJSON(input[0])
}

func isGeneratedCallID(id string) bool {
	// Heuristic only: generated canonical call IDs currently look like
	// call_<lower-hex>. A user-provided ID with the same shape is treated as
	// generated so it does not become Codex conversation affinity state; callers
	// that need stable affinity should set Session.ContinuityKey or the
	// authoritative/correlation session fields instead.
	if !strings.HasPrefix(id, "call_") || len(id) <= len("call_") {
		return false
	}
	for _, r := range id[len("call_"):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
