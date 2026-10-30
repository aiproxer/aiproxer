package hooks

import (
	"slices"

	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// Config wires hook implementations into a Bus. Any slice may be nil or empty.
// It aliases the SDK-owned [aipfeature.HookConfig] projected from declared feature planes.
type Config = aipfeature.HookConfig

// Bus runs hook chains in stable order (Order ascending, then ID ascending, then
// registration index ascending so equal Order+ID hooks remain deterministic).
//
// After [New], the bus does not replace its internal hook slices; concurrent [Bus] method calls
// are safe only if hook implementations are safe for concurrent use. Callers must not mutate
// those slices or the bus fields after construction (including when the same bus is embedded in
// [github.com/aiproxer/aiproxer/internal/core/extensions.RequestRuntimeSnapshot]).
type Bus struct {
	submit        []sdk.SubmitHook
	requestParts  []sdk.RequestPartHook
	responseParts []sdk.ResponsePartHook
	tools         []sdk.ToolReactor
	toolErrPol    sdk.ToolReactorErrorPolicy
}

// New constructs a Bus with sorted hook chains.
func New(cfg Config) *Bus {
	sorted := MaterializeSorted(cfg)
	return &Bus{
		submit:        sorted.SubmitHooks,
		requestParts:  sorted.RequestPartHooks,
		responseParts: sorted.ResponsePartHooks,
		tools:         sorted.ToolReactors,
		toolErrPol:    sorted.ToolReactorErrorPolicy,
	}
}

// MaterializeSorted returns a copy of cfg with each hook chain sorted per design §17
// (order, id, registration index). Diagnostics and tests use it without constructing a Bus.
func MaterializeSorted(cfg Config) Config {
	return Config{
		SubmitHooks:            sortChain(slices.Clone(cfg.SubmitHooks)),
		RequestPartHooks:       sortChain(slices.Clone(cfg.RequestPartHooks)),
		ResponsePartHooks:      sortChain(slices.Clone(cfg.ResponsePartHooks)),
		ToolReactors:           sortChain(slices.Clone(cfg.ToolReactors)),
		ToolReactorErrorPolicy: cfg.ToolReactorErrorPolicy,
	}
}

// HookChainLengths returns hook counts for diagnostics and tests.
func (b *Bus) HookChainLengths() (submit, requestParts, responseParts, tools int) {
	if b == nil {
		return 0, 0, 0, 0
	}
	return len(b.submit), len(b.requestParts), len(b.responseParts), len(b.tools)
}

type chainParticipant interface {
	ID() string
	Order() int
}

// sortChain orders hook chains by StableParticipantLess using a permutation slice
// idx initialized to [0..n); slices.SortFunc compares two elements of idx (each an
// original hook index), not two positions in idx.
func sortChain[T chainParticipant](h []T) []T {
	if len(h) == 0 {
		return nil
	}
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := h[hi], h[hj]
		return StableParticipantLess(a.Order(), b.Order(), a.ID(), b.ID(), hi, hj)
	})
	out := make([]T, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}
