package runtime

import (
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/json"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// SeamDigestKey is the process-stable HMAC key for the seam-digest feed.
// It is generated once per process in NewProcessServices, borrowed (copied)
// by each generation executor, and never logged, persisted, or exposed.
// The zero value is a valid HMAC key for tests; production always carries a
// random key so digests are not dictionary-matchable offline.
type SeamDigestKey [32]byte

// NewSeamDigestKey generates one process-stable digest key from crypto/rand.
// A read failure fails process startup rather than degrading to a predictable
// key: silent fallback would reintroduce offline dictionary matching.
func NewSeamDigestKey() (SeamDigestKey, error) {
	var key SeamDigestKey
	if _, err := crand.Read(key[:]); err != nil {
		return SeamDigestKey{}, err
	}
	return key, nil
}

// IsZero reports whether the key is the zero value. Tests use fixed non-zero
// keys; production keys must never be zero.
func (k SeamDigestKey) IsZero() bool { return k == SeamDigestKey{} }

// seamDigestMaxItems caps the per-leg digest vector. It matches the
// diagnostic retention bound
// (internal/infra/metrics.collapseMaxItems): vectors are truncated to the
// first entries, so a longer trajectory never grows the retained state and a
// truncated prefix match never fabricates a seam at the truncation point.
const seamDigestMaxItems = 128

// seamItemDigests returns content-free per-item HMAC-SHA256 digests of the
// model-bound call, oldest trajectory entry first, truncated to
// seamDigestMaxItems. It hashes the normalized item trajectory
// (aipapi.NormalizedItemsCapped) with volatile identity (ID and Status)
// stripped, so the digests track prompt content rather than proxy
// bookkeeping. An empty trajectory yields no digests; a marshal failure
// yields no digests rather than a fabricated attribution.
//
// The key is the process-stable secret borrowed from the process service:
// the same prompt hashed under the same process yields the same digests
// (stable across generation rebuilds), while different processes yield
// different digests, so an offline dictionary without the key cannot match
// low-entropy prompts. The detector only compares opaque 32-byte values and
// never sees the key.
//
// Index contract: entries address the normalized item trajectory —
// instructions first, then messages for legacy calls; canonical items for
// item-authoritative calls. The diagnostic reports this index as
// `seam_item_index`. It is not a message ordinal: a change to a non-message
// item (for example a tool call) shifts the reported index exactly like a
// message change at the same trajectory position.
//
// Cost: one bounded hash pass over the model-bound request. Only the first
// seamDigestMaxItems entries are projected and marshaled, so a huge
// trajectory pays only for the retained prefix; each selected entry still
// costs O(entry bytes) to marshal and hash, with the marshaled JSON held
// transiently per entry and only 32 bytes retained per entry. Callers gate
// it behind the seam-digests enable switch (off by default), and only for
// the request the backend actually receives, per B-leg. The wire/large-body
// path carries no canonical call and feeds no digests. No per-request state
// is cached: every B-leg hashes its own provider-bound call.
//
// Privacy: the digests are HMAC-SHA256 under a process-stable secret that is
// never logged, persisted, or exposed, so they are not offline
// dictionary-matchable without the key. They travel only on the leg's own
// usage sample to the collapse diagnostic; see
// pkg/aipsdk/usage.Event.SeamDigests. Observers must still treat them as
// sensitive diagnostic evidence: do not log, persist, exfiltrate, retain, or
// mutate them. The collapse diagnostic copies what it retains.
func seamItemDigests(call aipapi.Call, key SeamDigestKey) [][32]byte {
	items := aipapi.NormalizedItemsCapped(call, seamDigestMaxItems)
	if len(items) == 0 {
		return nil
	}
	out := make([][32]byte, len(items))
	for i := range items {
		item := items[i]
		item.ID = ""
		item.Status = ""
		raw, err := json.Marshal(item)
		if err != nil {
			return nil
		}
		mac := hmac.New(sha256.New, key[:])
		mac.Write(raw)
		copy(out[i][:], mac.Sum(nil))
	}
	return out
}
