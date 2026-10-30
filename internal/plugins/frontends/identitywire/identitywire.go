// Package identitywire captures protocol-neutral client identity carriers into
// canonical invocation metadata. Security and emission policy stay in core;
// adapters only lift the HTTP User-Agent header.
package identitywire

import (
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/identity"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const headerUserAgent = "User-Agent"

// CaptureClientUserAgent copies a trimmed User-Agent into inv when present and
// acceptable. Missing, blank, over-long, and CR/LF/NUL/control values leave the
// field unchanged (invalid identity is dropped; decode continues). The header
// map is never mutated. Only User-Agent is read.
func CaptureClientUserAgent(inv *aipapi.Invocation, h http.Header) {
	if inv == nil || h == nil {
		return
	}
	v, ok := identity.AcceptClientUserAgent(h.Get(headerUserAgent))
	if !ok {
		return
	}
	inv.ClientUserAgent = v
}
