package reasoningpreservation

import (
	"context"
	"crypto/sha256"
	"encoding/binary"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// PostAppendCorrelation carries trusted post-append attribution for compression.
// It contains only envelope/context data—no model payload or content telemetry.
// EgressPolicyRefHash is a provisional hash of the configured egress_policy_ref;
// task 4.3 will derive the authoritative PendingCompression.EgressPolicyHash from the
// trusted egress decision PolicyVersion via UpdateReservationPolicyHash before Bind.
// Do NOT treat EgressPolicyRefHash as authoritative — it is a stable provisional
// placeholder derived from config, not a policy decision hash.
type PostAppendCorrelation struct {
	Partition           SessionPartition
	ArtifactID          string
	Anchor              [32]byte
	OriginalDigest      [32]byte
	SemanticDigest      [32]byte
	EgressPolicyRefHash [32]byte
	SourceBytes         int
	TraceID             string
	ALegID              string
	BLegID              string
	BranchBinding       string
	Scope               scope.PrincipalScopeView
	PolicyRevision      string
}

// PostAppendHook is a local post-commit optimization seam invoked unlocked
// after authoritative Append success. It may reserve optional state but must
// not synchronously await provider; task 4.4 will submit after reservation.
// Failure must not invalidate original.
type PostAppendHook func(context.Context, PostAppendCorrelation) error

func computeSemanticDigest(placements []PlacedReasoning) [32]byte {
	segs := ExtractSemanticSegments(placements)
	if len(segs) == 0 {
		return [32]byte{}
	}
	h := sha256.New()
	for _, s := range segs {
		var idx [8]byte
		binary.BigEndian.PutUint64(idx[:], uint64(s.Index))
		_, _ = h.Write(idx[:])
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(s.Text)))
		_, _ = h.Write(l[:])
		_, _ = h.Write([]byte(s.Text))
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func computeEgressPolicyRefHash(ref string) [32]byte {
	if ref == "" {
		return [32]byte{}
	}
	return sha256.Sum256([]byte(ref))
}
