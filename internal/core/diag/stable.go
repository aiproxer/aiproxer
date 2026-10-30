package diag

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const stableTimestampBase = 1715620000

// StableCallToken returns a deterministic fingerprint for a canonical call.
// The token is stable across process runs for the same call content.
func StableCallToken(call *aipapi.Call) string {
	return StableCallTokenFromSum(StableCallSum(call))
}

// StableCallTokenFromSum returns a deterministic fingerprint for an already-computed
// canonical call sum.
func StableCallTokenFromSum(sum [32]byte) string {
	return hex.EncodeToString(sum[:8])
}

// StableCallID returns the caller-provided ID when present, otherwise a
// deterministic call-derived identifier suitable for trace and wire fallback use.
func StableCallID(call *aipapi.Call) string {
	if call != nil {
		if id := strings.TrimSpace(call.ID); id != "" {
			return id
		}
	}
	return StableCallIDFromSum("", StableCallSum(call))
}

// StableCallIDFromSum returns explicitID when present, otherwise a deterministic
// call-derived identifier derived from sum.
func StableCallIDFromSum(explicitID string, sum [32]byte) string {
	if id := strings.TrimSpace(explicitID); id != "" {
		return id
	}
	return "call_" + StableCallTokenFromSum(sum)
}

// StableUnix returns a deterministic Unix timestamp derived from the call.
// It is intentionally stable across runs so wire fallbacks stay reproducible.
func StableUnix(call *aipapi.Call) int64 {
	return StableUnixFromSum(StableCallSum(call))
}

// StableUnixFromSum returns a deterministic Unix timestamp derived from an
// already-computed canonical call sum.
// It is intentionally stable across runs so wire fallbacks stay reproducible.
func StableUnixFromSum(sum [32]byte) int64 {
	// Keep the value in a recent-looking range while staying deterministic.
	offset := int64(binary.BigEndian.Uint32(sum[:4]) % 86_400)
	return stableTimestampBase + offset
}

// StableTime returns a deterministic UTC timestamp derived from the call.
func StableTime(call *aipapi.Call) time.Time {
	return StableTimeFromSum(StableCallSum(call))
}

// StableTimeFromSum returns a deterministic UTC timestamp derived from an
// already-computed canonical call sum.
func StableTimeFromSum(sum [32]byte) time.Time {
	return time.Unix(StableUnixFromSum(sum), 0).UTC()
}

// StableCallSum returns the canonical SHA-256 identity digest for call.
func StableCallSum(call *aipapi.Call) [32]byte {
	var zero [32]byte
	if call == nil {
		return zero
	}
	cp := *call
	cp.ID = ""
	b, err := json.Marshal(cp)
	if err != nil {
		return zero
	}
	return sha256.Sum256(b)
}
