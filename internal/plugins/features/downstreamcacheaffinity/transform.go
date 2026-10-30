// Package downstreamcacheaffinity fills an empty canonical prompt-cache key with a
// stable, opaque, backend-scoped locality hint derived from the admitted
// authoritative session.
//
// The hint is advisory routing metadata only. It is never session, continuation,
// or cache-residency authority, and never evidence of a cache hit. The package
// imports only the standard library, pkg/aipapi and pkg/aipsdk contracts; the
// wire carriage is owned by the serializers.
package downstreamcacheaffinity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

const (
	// ID is the stable feature identity used by the standard feature table.
	ID = "downstream-cache-affinity"

	// GeneratedPrefix marks a proxy-generated hint so an operator reading a
	// provider dashboard can tell it apart from a client-owned key.
	GeneratedPrefix = "aipca1_"

	// GeneratedLength is the exact length of every generated value:
	// 7 + base64.RawURLEncoding.EncodedLen(sha256.Size).
	GeneratedLength = 50

	// transformOrder runs this pass after every existing standard attempt
	// transform, all of which currently sit at order 0.
	transformOrder = 1000

	// domain separates this hash from any other hash of the same session ID.
	domain = "aiproxer/downstream-cache-affinity/v1\x00"
)

// cont is the only decision this pass can return: the hint never excludes a
// candidate.
var cont = request.AttemptDecision{Kind: request.AttemptContinue}

// Derive returns the hint for one backend and authoritative session.
//
// The value is "aipca1_" plus unpadded base64url of
// SHA-256(domain || BackendID || 0x00 || AuthoritativeSessionID): exactly 50
// ASCII characters, never truncated. Unkeyed SHA-256 is sufficient because
// AuthoritativeSessionID is 256 bits of crypto/rand, which cannot be guessed or
// enumerated, so an HMAC key would add no secrecy. Being unkeyed also makes the
// value identical across retries, process restarts and proxy nodes, which a
// process-local or root-derived key would not guarantee.
func Derive(backendID, sessionID string) string {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte(backendID))
	h.Write([]byte{0})
	h.Write([]byte(sessionID))
	return GeneratedPrefix + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// Transform fills an empty canonical prompt-cache key for one candidate attempt.
//
// It holds no state, so parallel and failover attempts are independent by
// construction: every attempt derives from its own AttemptMeta.BackendID and the
// executor already hands each candidate its own call copy.
type Transform struct{}

// ID implements request.AttemptTransform.
func (Transform) ID() string { return ID }

// Order implements request.AttemptTransform. See transformOrder.
func (Transform) Order() int { return transformOrder }

// FailureMode implements request.AttemptTransform.
//
// FailOpen is the only mode consistent with this feature being an optional
// locality optimization: an internal problem in it must leave the real path
// intact rather than fail a request that was already valid.
func (Transform) FailureMode() hooks.FailureMode { return hooks.FailOpen }

// HandleAttempt implements request.AttemptTransform.
//
// It is fill-only and performs no I/O, so every condition it can meet is either a
// write to the canonical prompt-cache key or a plain request.AttemptContinue:
//
//   - no canonical call: the stage never supplies one, and an absent one leaves
//     nothing to fill;
//   - an effective prompt-cache value already present, whether through the legacy
//     field or its semantic-extension carrier: explicit client intent wins;
//   - an alias conflict between the two: the call is left untouched and the
//     candidate continues, because call validation and the serializers remain the
//     authority for that error;
//   - no admitted authoritative session, or no backend namespace: nothing is
//     generated.
//
// The pass reads only AttemptMeta.Session.AuthoritativeSessionID and
// AttemptMeta.BackendID. It never reads the client session hint, the A-leg,
// principal scope, workspace, trace ID, model, headers or safe metadata.
func (Transform) HandleAttempt(
	_ context.Context,
	call *aipapi.Call,
	meta request.AttemptMeta,
	_ request.Services,
) (request.AttemptDecision, error) {
	if call == nil {
		return cont, nil
	}
	if pck, err := call.PromptCacheKeyValue(); err != nil || pck != "" {
		return cont, nil
	}
	sid := strings.TrimSpace(meta.Session.AuthoritativeSessionID)
	backend := strings.TrimSpace(meta.BackendID)
	if sid == "" || backend == "" {
		return cont, nil
	}
	call.PromptCacheKey = Derive(backend, sid)
	return cont, nil
}
