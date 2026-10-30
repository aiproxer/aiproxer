package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrInvalidRevisionIdentity identifies an incomplete or unsafe durable
// economic revision identity. Revision work is immutable, so accepting an
// identity that cannot be reconstructed would make restart/replay ambiguous.
var ErrInvalidRevisionIdentity = errors.New("metering/replay: invalid revision identity")

// RevisionIdentity is the replay key for pure economic work. The evidence
// revision and input-set hash are both part of the identity: a late correction
// therefore produces a new work/result identity rather than overwriting an
// earlier valuation.
type RevisionIdentity struct {
	Queue            string
	HeadKey          string
	EvidenceRevision uint64
	InputSetHash     string
}

// NewRevisionIdentity validates and constructs a deterministic revision key.
// Queue and head are opaque scoped references; callers own their namespace.
func NewRevisionIdentity(queue, headKey string, evidenceRevision uint64, inputSetHash string) (RevisionIdentity, error) {
	identity := RevisionIdentity{
		Queue: strings.TrimSpace(queue), HeadKey: strings.TrimSpace(headKey),
		EvidenceRevision: evidenceRevision, InputSetHash: strings.TrimSpace(inputSetHash),
	}
	if identity.Queue == "" || identity.Queue != queue {
		return RevisionIdentity{}, fmt.Errorf("%w: queue is required and must be trimmed", ErrInvalidRevisionIdentity)
	}
	if identity.HeadKey == "" || identity.HeadKey != headKey {
		return RevisionIdentity{}, fmt.Errorf("%w: head key is required and must be trimmed", ErrInvalidRevisionIdentity)
	}
	if identity.InputSetHash == "" || identity.InputSetHash != inputSetHash {
		return RevisionIdentity{}, fmt.Errorf("%w: input-set hash is required and must be trimmed", ErrInvalidRevisionIdentity)
	}
	if evidenceRevision == 0 {
		return RevisionIdentity{}, fmt.Errorf("%w: evidence revision is required", ErrInvalidRevisionIdentity)
	}
	if len(identity.InputSetHash) != sha256.Size*2 {
		return RevisionIdentity{}, fmt.Errorf("%w: input-set hash must be SHA-256 hex", ErrInvalidRevisionIdentity)
	}
	decoded, err := hex.DecodeString(identity.InputSetHash)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(identity.InputSetHash) != identity.InputSetHash {
		return RevisionIdentity{}, fmt.Errorf("%w: input-set hash must be lowercase SHA-256 hex", ErrInvalidRevisionIdentity)
	}
	return identity, nil
}

// Key returns an opaque, bounded identity suitable for durable work IDs.
// Hashing avoids placing tenant/provider identifiers in queue indexes while
// retaining every identity member in the canonical preimage.
func (i RevisionIdentity) Key() string {
	if _, err := NewRevisionIdentity(i.Queue, i.HeadKey, i.EvidenceRevision, i.InputSetHash); err != nil {
		return ""
	}
	preimage := i.Queue + "\x00" + i.HeadKey + "\x00" + strconv.FormatUint(i.EvidenceRevision, 10) + "\x00" + i.InputSetHash
	digest := sha256.Sum256([]byte(preimage))
	return "economic-revision:v1:" + hex.EncodeToString(digest[:])
}

// Less reports the deterministic ordering used to select a current head.
// Higher evidence revisions win; for two independent revisions with the same
// number, the canonical input hash and then key provide a stable tie-break.
func (i RevisionIdentity) Less(other RevisionIdentity) bool {
	if i.EvidenceRevision != other.EvidenceRevision {
		return i.EvidenceRevision < other.EvidenceRevision
	}
	if i.InputSetHash != other.InputSetHash {
		return i.InputSetHash < other.InputSetHash
	}
	return i.Key() < other.Key()
}
