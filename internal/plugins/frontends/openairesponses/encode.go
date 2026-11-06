package openairesponses

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openaiwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// EncodeOptions controls wire identifiers for encoded Responses payloads.
type EncodeOptions struct {
	ResponseID               string
	MessageID                string
	CreatedAt                int64
	ExposeAipUsageExtensions bool
	// namespaceTools restores client namespace/bare-member identities for
	// model-emitted projected tool names. It is per-request state, never a
	// wire field.
	namespaceTools *namespaceToolMapping
}

func defaultEncodeOptions(call *aipapi.Call, opts EncodeOptions) EncodeOptions {
	if opts.ResponseID == "" {
		opts.ResponseID = "resp_" + diag.StableCallToken(call)
	}
	if opts.MessageID == "" {
		opts.MessageID = "msg_" + opts.ResponseID
	}
	if opts.CreatedAt == 0 {
		opts.CreatedAt = diag.StableUnix(call)
	}
	return opts
}

// WriteErrorJSON writes an OpenAI-shaped JSON error before any streamed bytes.
func WriteErrorJSON(w http.ResponseWriter, status int, message, errType, code string) error {
	return openaiwire.WriteErrorJSON(w, status, message, errType, code)
}
