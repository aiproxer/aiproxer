package runtime

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"slices"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// systemPromptDigestDomain separates the instruction-identity frame from every
// other HMAC use of the process-stable key, so an instruction digest can never
// collide with a per-item seam digest of the same bytes.
var systemPromptDigestDomain = []byte("aiproxer/system-prompt-digest/v1\x00")

// systemPromptDigestRegion identifies where one instruction sits in the
// canonical trajectory. Placement is part of the identity: moving an unchanged
// system or developer instruction between two anchors changes the cached prefix
// even though its content is identical, and the same-turn continuation
// comparison already treats such a move as an instruction change.
const (
	systemPromptDigestRegionInstructions byte = 0
	systemPromptDigestRegionMessages     byte = 1
	systemPromptDigestRegionItems        byte = 2
)

// systemPromptDigest returns the content-free identity of the effective
// provider-bound system/developer instructions of the final canonical call,
// including their effective placement, or false when an instruction fails to
// marshal.
//
// The call must be the request the backend actually receives: candidate attempt
// transforms, request-part hooks, and the final conversation-view reassertion
// are already applied. A caller must not substitute the pre-candidate request
// baseline, because a candidate can add, remove, or rewrite instructions that
// change the provider cache prefix without changing the baseline.
//
// volatile holds the trajectory positions of emissions from active volatile
// steering overlays (roles other than system or developer, for example the
// user-role thinker memo). They are excluded from placement accounting exactly
// as the same-turn comparison excludes them, so an unrelated conversational
// insertion is not mistaken for an instruction move. Positions must describe
// this final call: an authority adaptation can change the trajectory shape, so
// the producer resolves them with [conversationprojection.AdaptedInjectedPositions]
// in the same pass as the adaptation verifier, never by reusing pre-adaptation
// indexes. Exclusion is positional, never by semantic identity, so a client
// message that happens to share an overlay's text keeps its place. Client-authored
// and system/developer content still occupies a position, so a genuine
// instruction relocation between anchors changes the identity, while appending
// ordinary conversation content does not.
//
// Instruction content is normalized exactly like [providerInstructions]:
// proxy-owned traceability (message metadata, item ID and status) is excluded.
//
// The identity is an HMAC-SHA256 under the process-stable secret, never the
// instruction text: the same instructions at the same effective positions under
// the same process yield the same digest, different processes yield different
// digests, and the digest is not offline dictionary-matchable without the key.
// Each entry contributes its region, effective position and content behind a
// big-endian length prefix, so different partitions of the same bytes never
// collide. An empty instruction list still yields a non-zero identity: "no
// instructions" is a state the diagnostic must be able to compare, not an
// absent observation.
//
// Cost: the scan stops after the last instruction and marshals only the
// instructions themselves, so a request with thousands of ordinary messages
// and no instructions pays one role-only pass and no per-entry allocation. The
// instruction count is bounded by the canonical request limits
// ([aipapi.MaxInstructionMessages] and [aipapi.MaxItems]).
func systemPromptDigest(call aipapi.Call, volatile map[int]struct{}, key SeamDigestKey) ([32]byte, bool) {
	mac := hmac.New(sha256.New, key[:])
	mac.Write(systemPromptDigestDomain)
	last := lastInstructionTrajectoryIndex(call)
	if last < 0 {
		// No instruction content: the empty identity is the domain frame alone.
		var digest [32]byte
		copy(digest[:], mac.Sum(nil))
		return digest, true
	}
	var frame [16]byte
	var effective uint64
	hashInstruction := func(region byte, message *aipapi.Message, item *aipapi.Item) bool {
		frame[0] = region
		binary.BigEndian.PutUint64(frame[1:9], effective)
		mac.Write(frame[:9])
		var raw []byte
		var err error
		if item != nil {
			normalized := providerInstructionItem(*item)
			raw, err = json.Marshal(normalized)
		} else {
			normalized := providerInstructionMessage(*message)
			raw, err = json.Marshal(normalized)
		}
		if err != nil {
			return false
		}
		binary.BigEndian.PutUint64(frame[:8], uint64(len(raw)))
		mac.Write(frame[:8])
		mac.Write(raw)
		effective++
		return true
	}
	if call.HasItemAuthority() {
		for i := 0; i <= last; i++ {
			item := &call.Items[i]
			if item.Kind == aipapi.ItemKindMessage && isProviderInstructionRole(item.Role) {
				if !hashInstruction(systemPromptDigestRegionItems, nil, item) {
					return [32]byte{}, false
				}
				continue
			}
			if _, excluded := volatile[i]; excluded {
				// Volatile steering content is not a placement anchor.
				continue
			}
			effective++
		}
	} else {
		for i := 0; i <= last; i++ {
			region := systemPromptDigestRegionInstructions
			var message *aipapi.Message
			if i < len(call.Instructions) {
				message = &call.Instructions[i]
			} else {
				region = systemPromptDigestRegionMessages
				message = &call.Messages[i-len(call.Instructions)]
			}
			if isProviderInstructionRole(message.Role) {
				if !hashInstruction(region, message, nil) {
					return [32]byte{}, false
				}
				continue
			}
			if _, excluded := volatile[i]; excluded {
				// Volatile steering content is not a placement anchor.
				continue
			}
			effective++
		}
	}
	var digest [32]byte
	copy(digest[:], mac.Sum(nil))
	return digest, true
}

// lastInstructionTrajectoryIndex returns the trajectory position of the last
// system/developer instruction, or -1 when the call carries none. The
// trajectory order matches projection provenance: instructions then messages
// for legacy calls, canonical items for item-authoritative calls.
func lastInstructionTrajectoryIndex(call aipapi.Call) int {
	if call.HasItemAuthority() {
		for i, item := range slices.Backward(call.Items) {
			if item.Kind == aipapi.ItemKindMessage && isProviderInstructionRole(item.Role) {
				return i
			}
		}
		return -1
	}
	base := len(call.Instructions)
	for i, v := range slices.Backward(call.Messages) {
		if isProviderInstructionRole(v.Role) {
			return base + i
		}
	}
	for i, v := range slices.Backward(call.Instructions) {
		if isProviderInstructionRole(v.Role) {
			return i
		}
	}
	return -1
}

func isProviderInstructionRole(role aipapi.Role) bool {
	return role == aipapi.RoleSystem || role == aipapi.RoleDeveloper
}

// providerInstructionMessage returns one legacy instruction as the provider-bound
// comparison sees it: proxy-owned traceability (metadata) is not instruction
// content.
func providerInstructionMessage(message aipapi.Message) aipapi.Message {
	message.Metadata = nil
	return message
}

// providerInstructionItem returns one item-authority instruction as the
// provider-bound comparison sees it: item identity and lifecycle status
// describe the envelope, not the instruction content.
func providerInstructionItem(item aipapi.Item) aipapi.Item {
	item.ID = ""
	item.Status = ""
	return item
}

// volatileEmissionIndices selects the final-call trajectory positions of
// emissions from active volatile steering overlays: overlays whose role is
// neither system nor developer, for example the user-role thinker memo.
//
// positions must describe the final provider-bound call. An empty set means the
// request has no volatile emission, so positional accounting needs no exclusion.
// false means active volatile overlays exist but the adaptation evidence located
// none of them; publishing no identity is safer than hashing guessed positions.
func volatileEmissionIndices(positions []conversationprojection.InjectedPosition, snapshot conversationprojection.Snapshot) (map[int]struct{}, bool) {
	volatileOverlays := make(map[string]struct{})
	for _, overlay := range snapshot.Steering {
		if !overlay.Active {
			continue
		}
		role := overlay.Message.Role
		if role == "" {
			role = aipapi.RoleSystem
		}
		if role == aipapi.RoleSystem || role == aipapi.RoleDeveloper {
			continue
		}
		volatileOverlays[overlay.OverlayID] = struct{}{}
	}
	if len(volatileOverlays) == 0 {
		return nil, true
	}
	out := make(map[int]struct{}, len(positions))
	found := false
	for _, position := range positions {
		if _, volatile := volatileOverlays[position.OverlayID]; volatile {
			out[position.TrajectoryIndex] = struct{}{}
			found = true
		}
	}
	if !found {
		return nil, false
	}
	return out, true
}

// adaptedVolatileEmissionIndices resolves volatile emission positions from
// projection evidence. Production uses the positions returned by
// VerifyAdaptationPreservesProjectionWithPositions in the same pass; this
// wrapper exists for focused tests and callers without verification evidence.
func adaptedVolatileEmissionIndices(final, projected aipapi.Call, snapshot conversationprojection.Snapshot, provenance []conversationprojection.OverlayProvenance) (map[int]struct{}, bool) {
	positions, err := conversationprojection.AdaptedInjectedPositions(projected, final, provenance)
	if err != nil {
		return nil, false
	}
	return volatileEmissionIndices(positions, snapshot)
}

// systemPromptLaunchSeqKey stamps the process-owned request launch ordinal on
// the request context. It is unexported so no plugin or frontend can forge the
// chronology the collapse diagnostic relies on.
type systemPromptLaunchSeqKey struct{}

// withSystemPromptLaunchSeq attaches seq to ctx. A nil context stays nil so the
// executor's nil-context contract is unaffected.
func withSystemPromptLaunchSeq(ctx context.Context, seq uint64) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, systemPromptLaunchSeqKey{}, seq)
}

// systemPromptLaunchSeqFromContext returns the launch ordinal attached by the
// executor, or zero when the observation did not travel through an executor
// request (a non-executor producer, a test fixture, or a lost context value).
func systemPromptLaunchSeqFromContext(ctx context.Context) uint64 {
	if ctx == nil {
		return 0
	}
	seq, _ := ctx.Value(systemPromptLaunchSeqKey{}).(uint64)
	return seq
}

// RequestLaunchSequence is the process-owned, strictly increasing source of
// request launch ordinals. The process composition owns one instance and
// injects it into every generation executor, so a reload or an overlapping
// retired generation never restarts the chronology. It is safe for concurrent
// use and carries no request content.
type RequestLaunchSequence struct {
	seq atomic.Uint64
}

// NewRequestLaunchSequence returns an empty process-owned launch sequence.
func NewRequestLaunchSequence() *RequestLaunchSequence {
	return &RequestLaunchSequence{}
}

// Next returns the next launch ordinal. The first call returns 1; zero is
// reserved for "no chronology available".
func (s *RequestLaunchSequence) Next() uint64 {
	if s == nil {
		return 0
	}
	return s.seq.Add(1)
}
