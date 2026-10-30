package compactiondetect

import (
	"time"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Heuristic thresholds pinned by the RED heuristic tests (design: 8k and 25%).
// The heuristic is deliberately conservative: precision over recall, and
// ambiguous transitions emit nothing (requirement 5.3).
const (
	// heuristicPriorTokens is the minimum previous-request context size for a
	// local-compaction inference (requirement 5.2: substantial prior context).
	heuristicPriorTokens = 8000
	// heuristicAbsoluteReduction is the absolute token-reduction floor.
	heuristicAbsoluteReduction = 8000
	// heuristicRelativeReduction is the relative token-reduction floor
	// (fraction of the previous request's estimated tokens).
	heuristicRelativeReduction = 0.25
	// heuristicTailItems is the number of recent semantic tail fingerprints
	// that must survive in order (requirement 5.2).
	heuristicTailItems = 2
	// heuristicPrefixItems is the number of leading item hashes folded into
	// the prefix fingerprint used for older-history disappearance checks.
	heuristicPrefixItems = 8
)

// requestFingerprint is the bounded, content-free summary of one successfully
// opened request. Only counts, timestamps, and SHA-256 semantic hashes are
// retained; source text is discarded after hashing (requirement 7.3).
type requestFingerprint struct {
	TraceID         string
	EstimatedTokens int
	ItemCount       int
	TailHashes      [heuristicTailItems][32]byte
	TailLen         int
	PrefixHash      [32]byte
	PrefixItems     int
	SeenAt          time.Time
}

// fingerprint derives the deterministic local fingerprint for one opened
// request and returns it together with every item's semantic hash (the caller
// uses the full hash list for the in-order tail-preservation check; only the
// bounded tail/prefix hashes are retained on the stored fingerprint).
func fingerprint(call aipapi.Call, at time.Time) (requestFingerprint, [][32]byte) {
	facts := compactionfacts.ExtractFactsFromCall(call)
	fp := requestFingerprint{
		EstimatedTokens: facts.EstimatedTokens,
		ItemCount:       facts.ItemCount,
		TailHashes:      facts.TailHashes,
		TailLen:         facts.TailLen,
		PrefixHash:      facts.PrefixHash,
		PrefixItems:     facts.PrefixItems,
		SeenAt:          at,
	}
	return fp, facts.ItemHashes
}

// estimateTokens is a deterministic local size estimate (characters/4). It
// performs no provider or network call (requirement 5.7).
func estimateTokens(call aipapi.Call) int {
	return compactionfacts.EstimateTokens(call)
}

// heuristicMatch requires ALL of: same authoritative A-leg (by construction),
// substantial prior context, material absolute and relative token reduction,
// at least two recent semantic tail hashes surviving in order, and meaningful
// older-history disappearance/replacement (requirements 5.1-5.3). Ambiguous
// transitions return false.
func heuristicMatch(prev, cur requestFingerprint, curHashes [][32]byte) bool {
	if prev.ItemCount == 0 || cur.ItemCount == 0 {
		return false
	}
	if prev.EstimatedTokens < heuristicPriorTokens {
		return false
	}
	reduction := prev.EstimatedTokens - cur.EstimatedTokens
	if reduction < heuristicAbsoluteReduction {
		return false
	}
	if reduction < int(float64(prev.EstimatedTokens)*heuristicRelativeReduction) {
		return false
	}
	if !tailPreserved(prev, curHashes) {
		return false
	}
	// Meaningful older-history disappearance/replacement: either the request
	// shrank (older items gone) or the leading prefix content changed.
	if cur.ItemCount >= prev.ItemCount && cur.PrefixHash == prev.PrefixHash {
		return false
	}
	return true
}

// tailPreserved checks that the previous request's most recent semantic tail
// hashes appear in the current request's item hashes in the same relative
// order (requirement 5.2). A reset, fork, or unrelated fresh short request
// drops the tail and therefore never matches from token reduction alone.
func tailPreserved(prev requestFingerprint, curHashes [][32]byte) bool {
	if prev.TailLen < heuristicTailItems {
		return false
	}
	pos := -1
	for i := 0; i < prev.TailLen; i++ {
		found := -1
		for j := pos + 1; j < len(curHashes); j++ {
			if curHashes[j] == prev.TailHashes[i] {
				found = j
				break
			}
		}
		if found < 0 {
			return false
		}
		pos = found
	}
	return true
}
