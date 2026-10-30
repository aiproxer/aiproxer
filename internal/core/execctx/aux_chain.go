package execctx

import (
	"context"
	"slices"
)

type auxDepthKey struct{}

type auxSuppressKey struct{}

// MaxAuxiliaryDepth limits nested auxiliary.Execute chains (one primary request has depth 0).
const MaxAuxiliaryDepth = 16

// WithAuxiliaryDepth stores the current auxiliary nesting level (0 = primary path).
func WithAuxiliaryDepth(ctx context.Context, depth int) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, auxDepthKey{}, depth)
}

// AuxiliaryDepth returns the auxiliary nesting level, or 0 if unset.
func AuxiliaryDepth(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	v, ok := ctx.Value(auxDepthKey{}).(int)
	if !ok {
		return 0
	}
	return v
}

// IncAuxiliaryDepth returns a child context with depth+1 or false if the limit is exceeded.
func IncAuxiliaryDepth(ctx context.Context) (context.Context, bool) {
	if ctx == nil {
		return nil, false
	}
	d := AuxiliaryDepth(ctx) + 1
	if d > MaxAuxiliaryDepth {
		return ctx, false
	}
	return WithAuxiliaryDepth(ctx, d), true
}

// WithSuppressedPluginIDs attaches handler/plugin ids that must be skipped on this execution
// path (auxiliary loop guards). Matching is exact against handler ID() strings.
func WithSuppressedPluginIDs(ctx context.Context, ids []string) context.Context {
	if ctx == nil || len(ids) == 0 {
		return ctx
	}
	uniq := slices.Clone(ids)
	slices.Sort(uniq)
	uniq = slices.Compact(uniq)
	return context.WithValue(ctx, auxSuppressKey{}, uniq)
}

// suppressedIDs returns the sorted unique id slice from context.
func suppressedIDs(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	raw, ok := ctx.Value(auxSuppressKey{}).([]string)
	if !ok || len(raw) == 0 {
		return nil
	}
	return raw
}

// IsSuppressedPluginID reports whether handlerID is listed for suppression on this ctx.
func IsSuppressedPluginID(ctx context.Context, handlerID string) bool {
	if handlerID == "" {
		return false
	}
	return slices.Contains(suppressedIDs(ctx), handlerID)
}
