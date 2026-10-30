package injection

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/capsule"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	// ProjectionVersion is the model-facing continuity block format version.
	ProjectionVersion = 1
	// BlockStart and BlockEnd are deliberately explicit delimiters. They make
	// the proxy-owned block distinguishable from user text without exposing
	// storage integrity metadata.
	BlockStart         = "<AIP-CONTINUITY version=\"1\" begin>\n"
	BlockEnd           = "\n<AIP-CONTINUITY version=\"1\" end>"
	projectionNotice   = "This block carries prior continuation state from an earlier compaction. It is not a new user request.\n"
	projectionOverhead = len(BlockStart) + len(projectionNotice) + len(BlockEnd)
)

var (
	ErrInvalidInput      = errors.New("compactioncontinuity/injection: invalid input")
	ErrInvalidMarker     = errors.New("compactioncontinuity/injection: invalid call-local marker")
	ErrProjectionBudget  = errors.New("compactioncontinuity/injection: projection budget exceeded")
	ErrProjectionVerify  = errors.New("compactioncontinuity/injection: capsule verification failed")
	ErrProjectionInvalid = errors.New("compactioncontinuity/injection: injected call is invalid")
)

// ProjectionLimits bounds the complete serialized model-facing block. A zero
// bound means that dimension is not additionally constrained; negative bounds
// are invalid. TokenEquivalent uses the conservative one UTF-8 byte per unit
// convention shared by capsule bounds and extractor accounting.
type ProjectionLimits struct {
	MaxBytes           int
	MaxTokens          int
	MaxTokenEquivalent int
}

// Limits is the short name used by orchestration callers.
type Limits = ProjectionLimits

// Marker identifies one call-local application. It deliberately contains no
// durable state and is returned to the caller for retry/failover preparation.
type Marker struct {
	BranchBinding   string
	BoundaryKey     string
	CapsuleRevision uint64
}

// AppliedMarker is a descriptive alias for callers that want to distinguish
// the returned marker from durable branch watermarks.
type AppliedMarker = Marker

// Input is the pure injection request. Capsule must already be a locally
// available value; Inject rechecks its branch binding and content digest
// before creating any model-facing text.
type Input struct {
	Call                  aipapi.Call
	Capsule               capsule.Envelope
	ExpectedBranchBinding string
	BoundaryKey           string
	Limits                ProjectionLimits
	// Marker is the ephemeral marker from a prior attempt in this call-local
	// lifecycle. A matching marker makes injection an idempotent no-op.
	Marker Marker
}

// Outcome describes one pure application attempt. On every error, Call is the
// exact input value and Marker is zero. On a matching marker, Call and Marker
// are retained and Applied is false.
type Outcome struct {
	Call            aipapi.Call
	Marker          Marker
	Applied         bool
	Bytes           int
	TokenEquivalent int
	Block           []byte
}

// TokenEquivalent returns the deterministic conservative budget estimate used
// by this package. It is intentionally a byte-equivalent estimate, not
// provider tokenizer output.
func TokenEquivalent(b []byte) int { return len(b) }

// SerializeBlock verifies e against expectedBranch and emits the stable,
// delimited model-facing projection. BranchBinding and ContentDigest are
// integrity metadata and are intentionally absent from the projection.
func SerializeBlock(e capsule.Envelope, expectedBranch string, limits ProjectionLimits) ([]byte, error) {
	expectedBranch = strings.TrimSpace(expectedBranch)
	if expectedBranch == "" {
		return nil, fmt.Errorf("%w: expected branch binding is required", ErrInvalidInput)
	}
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	if err := e.VerifyBranch(expectedBranch); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProjectionVerify, err)
	}

	p := projectionEnvelope{
		Version:              ProjectionVersion,
		Revision:             e.Revision,
		Plan:                 clonePlan(e.Plan),
		Decisions:            cloneDecisions(e.Decisions),
		Constraints:          cloneFacts(e.Constraints),
		RejectedAlternatives: cloneFacts(e.RejectedAlternatives),
		OpenQuestions:        cloneFacts(e.OpenQuestions),
	}
	sortProjection(&p)
	payload, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("%w: serialize projection: %v", ErrInvalidInput, err)
	}
	capacity, err := projectionCapacity(len(payload))
	if err != nil {
		return nil, err
	}
	block := make([]byte, 0, capacity)
	block = append(block, BlockStart...)
	block = append(block, projectionNotice...)
	block = append(block, payload...)
	block = append(block, BlockEnd...)
	bytesN := len(block)
	tokensN := TokenEquivalent(block)
	if limits.MaxBytes > 0 && bytesN > limits.MaxBytes {
		return nil, fmt.Errorf("%w: bytes=%d max=%d", ErrProjectionBudget, bytesN, limits.MaxBytes)
	}
	maxTokens := limits.MaxTokens
	if limits.MaxTokenEquivalent > 0 && (maxTokens == 0 || limits.MaxTokenEquivalent < maxTokens) {
		maxTokens = limits.MaxTokenEquivalent
	}
	if maxTokens > 0 && tokensN > maxTokens {
		return nil, fmt.Errorf("%w: token_equivalent=%d max=%d", ErrProjectionBudget, tokensN, maxTokens)
	}
	return block, nil
}

func projectionCapacity(payloadBytes int) (int, error) {
	if payloadBytes < 0 || payloadBytes > math.MaxInt-projectionOverhead {
		return 0, fmt.Errorf("%w: serialized projection size overflow", ErrProjectionBudget)
	}
	return payloadBytes + projectionOverhead, nil
}

// Serialize is a concise alias for SerializeBlock.
func Serialize(e capsule.Envelope, expectedBranch string, limits ProjectionLimits) ([]byte, error) {
	return SerializeBlock(e, expectedBranch, limits)
}

// Inject applies one continuity block to the appropriate canonical authority.
// The operation is transactional: all validation occurs before the cloned
// call is returned, and failures return the original call and a zero marker.
func Inject(in Input) (Outcome, error) {
	out := Outcome{Call: in.Call}
	if err := in.Call.Validate(); err != nil {
		return out, fmt.Errorf("%w: input call: %v", ErrInvalidInput, err)
	}
	expected := strings.TrimSpace(in.ExpectedBranchBinding)
	boundary := strings.TrimSpace(in.BoundaryKey)
	if expected == "" || boundary == "" {
		return out, fmt.Errorf("%w: branch binding and boundary key are required", ErrInvalidInput)
	}
	block, err := SerializeBlock(in.Capsule, expected, in.Limits)
	if err != nil {
		return out, err
	}
	if in.Capsule.Revision == 0 {
		return out, fmt.Errorf("%w: capsule revision must be positive", ErrProjectionVerify)
	}
	identity := Marker{BranchBinding: expected, BoundaryKey: boundary, CapsuleRevision: in.Capsule.Revision}
	if in.Marker != (Marker{}) {
		marker := Marker{BranchBinding: strings.TrimSpace(in.Marker.BranchBinding), BoundaryKey: strings.TrimSpace(in.Marker.BoundaryKey), CapsuleRevision: in.Marker.CapsuleRevision}
		if marker.BranchBinding == "" || marker.BoundaryKey == "" || marker.CapsuleRevision == 0 {
			return out, fmt.Errorf("%w: marker fields are incomplete", ErrInvalidMarker)
		}
		if marker == identity {
			out.Marker = identity
			return out, nil
		}
	}

	mutated := cloneCallPreservingPresence(in.Call)
	if mutated.HasItemAuthority() {
		mutated.Items = append(mutated.Items, aipapi.Item{
			Kind:    aipapi.ItemKindMessage,
			Role:    aipapi.RoleDeveloper,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: string(block)}},
		})
	} else {
		mutated.Instructions = append(mutated.Instructions, aipapi.Message{
			Role:  aipapi.RoleDeveloper,
			Parts: []aipapi.Part{aipapi.TextPart(string(block))},
		})
	}
	if err := mutated.Validate(); err != nil {
		return out, fmt.Errorf("%w: %v", ErrProjectionInvalid, err)
	}
	out.Call = mutated
	out.Marker = identity
	out.Applied = true
	out.Bytes = len(block)
	out.TokenEquivalent = TokenEquivalent(block)
	out.Block = append([]byte(nil), block...)
	return out, nil
}

// Apply is an expressive alias for Inject.
func Apply(in Input) (Outcome, error) { return Inject(in) }

// HasProjection reports whether the current authority's final canonical
// carrier already contains this exact bounded projection. It is call-local
// deduplication only; durable branch release state remains coordinator-owned.
func HasProjection(call aipapi.Call, e capsule.Envelope, expectedBranch string, limits ProjectionLimits) bool {
	block, err := SerializeBlock(e, expectedBranch, limits)
	if err != nil {
		return false
	}
	if call.HasItemAuthority() {
		if len(call.Items) == 0 {
			return false
		}
		item := call.Items[len(call.Items)-1]
		if item.Kind != aipapi.ItemKindMessage || len(item.Content) != 1 || item.Content[0].Kind != aipapi.ContentPartText {
			return false
		}
		return item.Role == aipapi.RoleDeveloper && item.Content[0].Text == string(block) || item.Role == aipapi.RoleSystem && item.Content[0].Text == string(block)
	}
	if len(call.Instructions) == 0 {
		return false
	}
	message := call.Instructions[len(call.Instructions)-1]
	return (message.Role == aipapi.RoleDeveloper || message.Role == aipapi.RoleSystem) && len(message.Parts) == 1 && message.Parts[0].Kind == aipapi.PartText && message.Parts[0].Text == string(block)
}

func validateLimits(limits ProjectionLimits) error {
	if limits.MaxBytes < 0 || limits.MaxTokens < 0 || limits.MaxTokenEquivalent < 0 {
		return fmt.Errorf("%w: projection limits must not be negative", ErrInvalidInput)
	}
	return nil
}

type projectionEnvelope struct {
	Version              int                `json:"projection_version"`
	Revision             uint64             `json:"revision"`
	Plan                 capsule.Plan       `json:"plan"`
	Decisions            []capsule.Decision `json:"decisions"`
	Constraints          []capsule.Fact     `json:"constraints"`
	RejectedAlternatives []capsule.Fact     `json:"rejected_alternatives"`
	OpenQuestions        []capsule.Fact     `json:"open_questions"`
}

func clonePlan(in capsule.Plan) capsule.Plan {
	out := in
	out.Steps = append([]capsule.PlanStep(nil), in.Steps...)
	return out
}

func cloneDecisions(in []capsule.Decision) []capsule.Decision {
	out := append([]capsule.Decision(nil), in...)
	for i := range out {
		out[i].Supersedes = append([]string(nil), in[i].Supersedes...)
	}
	return out
}

func cloneFacts(in []capsule.Fact) []capsule.Fact { return append([]capsule.Fact(nil), in...) }

func sortProjection(p *projectionEnvelope) {
	sort.SliceStable(p.Plan.Steps, func(i, j int) bool { return p.Plan.Steps[i].ID < p.Plan.Steps[j].ID })
	sort.SliceStable(p.Decisions, func(i, j int) bool { return p.Decisions[i].ID < p.Decisions[j].ID })
	sort.SliceStable(p.Constraints, func(i, j int) bool { return p.Constraints[i].ID < p.Constraints[j].ID })
	sort.SliceStable(p.RejectedAlternatives, func(i, j int) bool { return p.RejectedAlternatives[i].ID < p.RejectedAlternatives[j].ID })
	sort.SliceStable(p.OpenQuestions, func(i, j int) bool { return p.OpenQuestions[i].ID < p.OpenQuestions[j].ID })
}

func cloneCallPreservingPresence(in aipapi.Call) aipapi.Call {
	out := aipapi.CloneCall(in)
	if in.Instructions != nil && out.Instructions == nil {
		out.Instructions = make([]aipapi.Message, 0)
	}
	if in.Messages != nil && out.Messages == nil {
		out.Messages = make([]aipapi.Message, 0)
	}
	if in.Items != nil && out.Items == nil {
		out.Items = make([]aipapi.Item, 0)
	}
	if in.Tools != nil && out.Tools == nil {
		out.Tools = make([]aipapi.ToolDef, 0)
	}
	if in.SemanticExtensions != nil && out.SemanticExtensions == nil {
		out.SemanticExtensions = make([]aipapi.SemanticExtension, 0)
	}
	if in.Extensions != nil && out.Extensions == nil {
		out.Extensions = make(map[string]json.RawMessage)
	}
	return out
}
