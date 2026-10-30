package toolnamecorrection

import (
	"context"
	"path"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

// finalizerOrder is this feature's position in the tool-call finalizer chain: it
// sits after the shipped tool-call-repair order (40) and the path-virtualization
// expansion order (41), so a near-miss name those passes normalize is never
// treated as an unknown tool.
const finalizerOrder = 45

// Finalizer is the stateless completed-call decision of the feature: it steers a
// call whose name the model was never offered, and passes every other call
// through unchanged. It is immutable after construction and safe for concurrent
// use.
type Finalizer struct {
	turnBudget          uint8
	allowAfterReasoning bool
	passThroughNames    []string
}

var _ toolcall.Finalizer = (*Finalizer)(nil)

// newFinalizer re-validates cfg and returns the finalizer it describes. The
// bundle is reachable from Go callers that never ran DecodeConfig, and a budget
// outside the SDK bounds would make every steer request unusable; an unset (zero)
// budget therefore takes the shipped default, exactly as an absent YAML key does.
// The zero Config carries the documented defaults for the same reason: a false
// SkipAfterReasoning means correct_after_reasoning: true.
func newFinalizer(cfg Config) (*Finalizer, error) {
	correct := !cfg.SkipAfterReasoning
	raw := rawConfig{
		CorrectAfterReasoning: &correct,
		PassThroughNames:      cfg.PassThroughNames,
	}
	if cfg.MaxCorrectionsPerTurn != 0 {
		budget := int(cfg.MaxCorrectionsPerTurn)
		raw.MaxCorrectionsPerTurn = &budget
	}
	norm, err := normalizeConfig(raw)
	if err != nil {
		return nil, err
	}
	return &Finalizer{
		turnBudget:          norm.MaxCorrectionsPerTurn,
		allowAfterReasoning: !norm.SkipAfterReasoning,
		passThroughNames:    slices.Clone(norm.PassThroughNames),
	}, nil
}

// ID implements [toolcall.Finalizer].
func (f *Finalizer) ID() string { return ID }

// Order implements [toolcall.Finalizer].
func (f *Finalizer) Order() int { return finalizerOrder }

// Finalize implements [toolcall.Finalizer]. It passes the call through when the
// name is offered (including a name an earlier finalizer rewrote to an offered
// tool), when the request offered no tools at all, or when a pass-through
// pattern matches; every other completed call is steered with the hidden
// correction message.
func (f *Finalizer) Finalize(
	_ context.Context,
	call toolcall.CompletedCall,
	tool aipapi.ToolDef,
	catalog []aipapi.ToolDef,
	_ toolcall.Meta,
) (toolcall.Result, error) {
	if tool.Name != "" || len(catalog) == 0 || f.matchesPassThrough(call.ToolName) {
		return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
	}
	return toolcall.Result{
		Action:     toolcall.ActionSteer,
		ReasonCode: toolcall.ReasonUnknownToolName,
		Steer: &toolcall.SteerRequest{
			Instruction:         buildMessage(call.ToolName, catalog),
			TurnBudget:          f.turnBudget,
			AllowAfterReasoning: f.allowAfterReasoning,
		},
	}, nil
}

// matchesPassThrough reports whether name matches any configured pattern. A
// malformed pattern cannot match: DecodeConfig rejects those and newFinalizer
// re-validates every configuration the bundle can carry.
func (f *Finalizer) matchesPassThrough(name string) bool {
	for _, pattern := range f.passThroughNames {
		if ok, err := path.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return false
}
