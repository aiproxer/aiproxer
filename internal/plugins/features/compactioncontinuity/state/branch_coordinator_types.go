package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

const (
	stateNamespace = "aip.compaction-continuity/branch-coordinator/v1"
	stateKey       = "process"
	bindingDomain  = "aip.compaction-continuity/branch-binding/v1\x00"
	stateVersion   = 1

	DefaultMaxEntries        = 1024
	DefaultMaxPreviewIntents = 1024
	DefaultTTL               = 2 * time.Hour
	DefaultMaxCapsuleBytes   = 1 << 20
	DefaultMaxSourceBytes    = 4 << 20
)

var (
	ErrInvalidBranchKey         = errors.New("compactioncontinuity: invalid branch key")
	ErrBranchNotFound           = errors.New("compactioncontinuity: branch not found")
	ErrBranchMismatch           = errors.New("compactioncontinuity: branch binding mismatch")
	ErrRevisionConflict         = errors.New("compactioncontinuity: revision conflict")
	ErrPendingJobMismatch       = errors.New("compactioncontinuity: pending job mismatch")
	ErrPreviewIntentMismatch    = errors.New("compactioncontinuity: preview intent mismatch")
	ErrInvalidTransaction       = errors.New("compactioncontinuity: invalid transaction")
	ErrBranchLimit              = errors.New("compactioncontinuity: branch limit reached")
	ErrPreviewIntentLimit       = errors.New("compactioncontinuity: preview intent limit reached")
	ErrInjectionMismatch        = errors.New("compactioncontinuity: pending injection mismatch")
	ErrInjectionAlreadyReleased = errors.New("compactioncontinuity: injection already released")
	ErrInvalidState             = errors.New("compactioncontinuity: invalid persisted state")
	ErrStateTooLarge            = errors.New("compactioncontinuity: state blob exceeds bound")
)

// BranchKey identifies the authoritative parent primary branch. A private
// auxiliary child A-leg is never a valid replacement for ALegID.
type BranchKey struct {
	AuthoritativeSessionID string `json:"authoritative_session_id,omitempty"`
	ALegID                 string `json:"a_leg_id"`
	PrincipalPartition     string `json:"principal_partition,omitempty"`
}

// NewBranchKey validates and normalises an authoritative parent key. When no
// secure session exists, principalPartition must isolate the primary A-leg.
func NewBranchKey(sessionID, aLegID, principalPartition string) (BranchKey, error) {
	k := BranchKey{
		AuthoritativeSessionID: strings.TrimSpace(sessionID),
		ALegID:                 strings.TrimSpace(aLegID),
		PrincipalPartition:     strings.TrimSpace(principalPartition),
	}
	if err := k.Validate(); err != nil {
		return BranchKey{}, err
	}
	return k, nil
}

// CaptureParentBranchKey is an explicit name for the pre-child capture point.
func CaptureParentBranchKey(sessionID, aLegID, principalPartition string) (BranchKey, error) {
	return NewBranchKey(sessionID, aLegID, principalPartition)
}

// Validate checks that this key has an authoritative primary A-leg and either
// a secure session or a principal-isolated fallback partition.
func (k BranchKey) Validate() error {
	if strings.TrimSpace(k.AuthoritativeSessionID) == "" && strings.TrimSpace(k.PrincipalPartition) == "" {
		return fmt.Errorf("%w: secure session or principal partition is required", ErrInvalidBranchKey)
	}
	if strings.TrimSpace(k.ALegID) == "" {
		return fmt.Errorf("%w: primary A-leg is required", ErrInvalidBranchKey)
	}
	return nil
}

// Binding derives a content-free stable branch identity. Raw session,
// principal, and A-leg identifiers need not cross the extractor boundary.
func (k BranchKey) Binding() string {
	if k.Validate() != nil {
		return ""
	}
	h := sha256.New()
	_, _ = h.Write([]byte(bindingDomain))
	writeBindingPart(h, strings.TrimSpace(k.AuthoritativeSessionID))
	writeBindingPart(h, strings.TrimSpace(k.ALegID))
	principal := strings.TrimSpace(k.PrincipalPartition)
	if strings.TrimSpace(k.AuthoritativeSessionID) != "" {
		principal = ""
	}
	writeBindingPart(h, principal)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// BranchBinding derives the stable content-free binding for a key.
func BranchBinding(k BranchKey) (string, error) {
	if err := k.Validate(); err != nil {
		return "", err
	}
	return k.Binding(), nil
}

func writeBindingPart(h interface{ Write([]byte) (int, error) }, value string) {
	// Length-delimited fields prevent concatenation ambiguities.
	f := fmt.Sprintf("%d:", len(value))
	_, _ = h.Write([]byte(f))
	_, _ = h.Write([]byte(value))
}

// PreviewIntent is a non-billable completion-only boundary. It becomes a
// committed transaction only after the corresponding primary Open succeeds.
type PreviewIntent struct {
	Key                  string `json:"key"`
	TargetSourceRevision uint64 `json:"target_source_revision"`
}

type InjectionTarget struct {
	BoundaryKey     string `json:"boundary_key"`
	CapsuleRevision uint64 `json:"capsule_revision"`
}

type InjectionWatermark struct {
	BranchBinding   string `json:"branch_binding"`
	BoundaryKey     string `json:"boundary_key"`
	CapsuleRevision uint64 `json:"capsule_revision"`
}

// BranchState is the bounded opaque coordinator state. Capsule/source bytes
// are intentionally not interpreted here.
type BranchState struct {
	Revision                  uint64              `json:"revision"`
	CapsuleJSON               json.RawMessage     `json:"capsule_json,omitempty"`
	CapsuleDigest             [32]byte            `json:"capsule_digest,omitempty"`
	SourceHighWatermark       string              `json:"source_high_watermark,omitempty"`
	SanitizedSourceJSON       json.RawMessage     `json:"sanitized_source_json,omitempty"`
	PendingPreviewIntent      *PreviewIntent      `json:"pending_preview_intent,omitempty"`
	PendingJobID              auxiliary.JobID     `json:"pending_job_id,omitempty"`
	PendingJobTargetRevision  uint64              `json:"pending_job_target_revision,omitempty"`
	PendingJobBranchBinding   string              `json:"pending_job_branch_binding,omitempty"`
	PendingInjection          *InjectionTarget    `json:"pending_injection,omitempty"`
	LastReleasedInjection     *InjectionWatermark `json:"last_released_injection,omitempty"`
	LastCompactionTransaction string              `json:"last_compaction_transaction,omitempty"`
	UpdatedAt                 time.Time           `json:"updated_at"`
}

// Config bounds process state and optionally supplies the process ExtensionState
// as opaque backing. Store is never used as semantic authority.
type Config struct {
	Store             aipstate.Store
	MaxEntries        int
	MaxPreviewIntents int
	TTL               time.Duration
	Now               func() time.Time
	MaxCapsuleBytes   int
	MaxSourceBytes    int
}

type BranchCoordinator struct {
	mu                sync.Mutex
	store             aipstate.Store
	now               func() time.Time
	maxEntries        int
	maxPreviewIntents int
	ttl               time.Duration
	maxCapsuleBytes   int
	maxSourceBytes    int
	entries           map[string]branchEntry
}

type branchEntry struct {
	Key       BranchKey   `json:"key"`
	State     BranchState `json:"state"`
	ExpiresAt time.Time   `json:"expires_at"`
}

type persistedState struct {
	Version uint8         `json:"version"`
	Entries []branchEntry `json:"entries"`
}

func normalizeBranchKey(k BranchKey) BranchKey {
	session := strings.TrimSpace(k.AuthoritativeSessionID)
	principal := strings.TrimSpace(k.PrincipalPartition)
	if session != "" {
		// PrincipalPartition is only a fallback when no authoritative secure
		// session exists; it must not fork one secure session into aliases.
		principal = ""
	}
	return BranchKey{
		AuthoritativeSessionID: session,
		ALegID:                 strings.TrimSpace(k.ALegID),
		PrincipalPartition:     principal,
	}
}

func cloneBytes(in []byte) []byte {
	if in == nil {
		return nil
	}
	return append([]byte(nil), in...)
}

func clonePreviewIntent(in *PreviewIntent) *PreviewIntent {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneInjectionTarget(in *InjectionTarget) *InjectionTarget {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneInjectionWatermark(in *InjectionWatermark) *InjectionWatermark {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneBranchState(in BranchState) BranchState {
	in.CapsuleJSON = cloneBytes(in.CapsuleJSON)
	in.SanitizedSourceJSON = cloneBytes(in.SanitizedSourceJSON)
	in.PendingPreviewIntent = clonePreviewIntent(in.PendingPreviewIntent)
	in.PendingInjection = cloneInjectionTarget(in.PendingInjection)
	in.LastReleasedInjection = cloneInjectionWatermark(in.LastReleasedInjection)
	return in
}

func cloneBranchEntry(in branchEntry) branchEntry {
	in.Key = normalizeBranchKey(in.Key)
	in.State = cloneBranchState(in.State)
	return in
}
