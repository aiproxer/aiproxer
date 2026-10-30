package acp

import (
	"crypto/rand"
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// CallRouteModel extracts the effective model from a canonical call's route
// selector (format "backend:model", or a bare model), falling back to the
// per-vendor model extension key when the selector is absent. The selector
// takes precedence so route-resolved models override request extensions.
//
// modelExtKey is the vendor-specific extension key (e.g. "acp.model",
// "codex.model") consulted only when Route.Selector is empty/whitespace.
func CallRouteModel(call *aipapi.Call, modelExtKey string) string {
	if call == nil {
		return ""
	}
	selector := strings.TrimSpace(call.Route.Selector)
	if selector != "" {
		if _, after, ok := strings.Cut(selector, ":"); ok {
			return strings.TrimSpace(after)
		}
		return selector
	}
	if call.Extensions != nil {
		if raw, ok := call.Extensions[modelExtKey]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// CallClientSession extracts the client session ID from a canonical call,
// preferring Session.ClientSessionID, then the "session.id" extension key,
// then "default". This is the shared extraction used by both the ACP CLI
// subprocess connectors and the Codex app-server backend.
//
// "default" is a shared global value: every request that carries no session
// identity would run on the same subprocess. Connectors that need one
// subprocess per conversation resolve their own identity with
// [CallRuntimeIdentity] instead.
func CallClientSession(call *aipapi.Call) string {
	if call == nil {
		return "default"
	}
	if sid := strings.TrimSpace(call.Session.ClientSessionID); sid != "" {
		return sid
	}
	if call.Extensions != nil {
		if raw, ok := call.Extensions["session.id"]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				if sid := strings.TrimSpace(s); sid != "" {
					return sid
				}
			}
		}
	}
	return "default"
}

// CallRuntimeIdentity resolves the runtime identity that keys the runtime
// pool for one request. It is the conversation-scoped counterpart of
// [CallClientSession]: one live agent process serves one conversation, so two
// conversations never share one.
//
// The identity is the proxy-owned authoritative session id when the host
// provides one. That id is stable exactly when the client resumes a session:
// a resumed session keeps one agent process, and a stateless client — which
// the host gives a new session per request — is isolated per request and
// replays its transcript into a fresh process.
//
// Neither a client-carried identifier nor transcript content is used as an
// identity. Neither is proof of conversation identity: a client could aim a
// hint at another conversation, and two conversations that open with the same
// text would otherwise share one agent process and its tool state.
//
// Without a session authority the request gets its own runtime key, so it never
// shares a process with an unrelated request.
func CallRuntimeIdentity(call *aipapi.Call) string {
	if call != nil {
		if sid := strings.TrimSpace(call.Session.AuthoritativeSessionID); sid != "" {
			return "session:" + sid
		}
	}
	// Called once at Open's execution boundary. Tracing IDs and identical
	// content are not authority to reuse a stateful process, including retries.
	return "request:" + rand.Text()
}
