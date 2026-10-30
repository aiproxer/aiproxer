package routing

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type nativeModelResolverKey struct{}

// ModelBindingKind distinguishes request-scoped native resolution outcomes.
// Exact-backend resolution never selects another backend's row (req 9.10).
type ModelBindingKind int

const (
	// ModelBindingUnspecified is the zero value; treat as unknown pass-through.
	ModelBindingUnspecified ModelBindingKind = iota
	// ModelBindingExactCanonical maps (backend, canonical) to that backend's NativeID.
	ModelBindingExactCanonical
	// ModelBindingKnownNative means model is already a NativeID published for that backend.
	ModelBindingKnownNative
	// ModelBindingUnknown means the raw/native input is unrecognized; preserve as pass-through.
	ModelBindingUnknown
	// ModelBindingWrongBackend means the canonical is registered only on another backend.
	ModelBindingWrongBackend
)

// ModelBinding is the multi-state resolution result for one backend+model leaf.
type ModelBinding struct {
	Kind   ModelBindingKind
	Native string // set for ExactCanonical and KnownNative
}

// ErrWrongBackendCanonical is returned when route planning binds a canonical
// that exists only on a different backend than the leaf's backend.
var ErrWrongBackendCanonical = errors.New("routing: canonical model not registered for leaf backend")

// WrongBackendCanonicalError names the rejected leaf for diagnostics.
type WrongBackendCanonicalError struct {
	Backend string
	Model   string
}

func (e *WrongBackendCanonicalError) Error() string {
	if e == nil {
		return ErrWrongBackendCanonical.Error()
	}
	return fmt.Sprintf("%v: backend %q model %q", ErrWrongBackendCanonical, e.Backend, e.Model)
}

func (e *WrongBackendCanonicalError) Unwrap() error { return ErrWrongBackendCanonical }

// NativeModelResolver maps a route leaf's backend+model using a request-bound
// registry view. Implementations must be fail-closed across backends.
type NativeModelResolver interface {
	ResolveModelBinding(backendID, model string) ModelBinding
}

// WithNativeModelResolver attaches resolver for request-scoped route planning.
func WithNativeModelResolver(ctx context.Context, resolver NativeModelResolver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if resolver == nil {
		return ctx
	}
	return context.WithValue(ctx, nativeModelResolverKey{}, resolver)
}

// NativeModelResolverFromContext returns the request-bound native resolver when present.
func NativeModelResolverFromContext(ctx context.Context) (NativeModelResolver, bool) {
	if ctx == nil {
		return nil, false
	}
	r, ok := ctx.Value(nativeModelResolverKey{}).(NativeModelResolver)
	if !ok || r == nil {
		return nil, false
	}
	return r, true
}

// BindNativeModelIDs sets NativeModel on every selector leaf from the request-bound
// resolver without overwriting Primary.Model (logical/canonical identity).
// Wrong-backend canonical leaves return a typed error (fail-closed).
// Unknown raw/native leaves keep NativeModel empty (compatibility pass-through).
// Params, weights, handicaps, TTFT, affinity, and selector identity are untouched.
func BindNativeModelIDs(sel *Selector, resolver NativeModelResolver) error {
	if sel == nil || resolver == nil {
		return nil
	}
	for i := range sel.Alternatives {
		if err := bindNativeToAlt(&sel.Alternatives[i], resolver); err != nil {
			return err
		}
	}
	return nil
}

// ApplyNativeModelIDs is retained as a deprecated alias that binds NativeModel
// without rewriting Model. Prefer BindNativeModelIDs; wrong-backend errors are dropped.
//
// Deprecated: use BindNativeModelIDs and handle ErrWrongBackendCanonical.
func ApplyNativeModelIDs(sel *Selector, resolver NativeModelResolver) {
	_ = BindNativeModelIDs(sel, resolver)
}

func bindNativeToAlt(a *FailoverAlt, resolver NativeModelResolver) error {
	if a == nil {
		return nil
	}
	switch {
	case a.Primary != nil:
		return bindNativeToPrimary(a.Primary, resolver)
	case a.Weighted != nil:
		for j := range a.Weighted.Branches {
			if err := bindNativeToWeightedBranch(&a.Weighted.Branches[j], resolver); err != nil {
				return err
			}
		}
	case a.Parallel != nil:
		for j := range a.Parallel.Branches {
			if err := bindNativeToPrimary(&a.Parallel.Branches[j].Target, resolver); err != nil {
				return err
			}
		}
	}
	return nil
}

func bindNativeToWeightedBranch(b *WeightedBranch, resolver NativeModelResolver) error {
	if b == nil {
		return nil
	}
	if b.Parallel != nil {
		for j := range b.Parallel.Branches {
			if err := bindNativeToPrimary(&b.Parallel.Branches[j].Target, resolver); err != nil {
				return err
			}
		}
		return nil
	}
	return bindNativeToPrimary(&b.Target, resolver)
}

func bindNativeToPrimary(p *Primary, resolver NativeModelResolver) error {
	if p == nil {
		return nil
	}
	backend := strings.TrimSpace(p.Backend)
	model := strings.TrimSpace(p.Model)
	if backend == "" || model == "" {
		return nil
	}
	b := resolver.ResolveModelBinding(backend, model)
	switch b.Kind {
	case ModelBindingExactCanonical, ModelBindingKnownNative:
		native := strings.TrimSpace(b.Native)
		if native == "" {
			return nil
		}
		p.NativeModel = native
		return nil
	case ModelBindingWrongBackend:
		return &WrongBackendCanonicalError{Backend: backend, Model: model}
	default:
		// Unknown / unspecified: leave NativeModel empty (pass-through at seams).
		return nil
	}
}

// WireModel returns the backend-facing model id: bound NativeModel when set, else Model.
func (p Primary) WireModel() string {
	if n := strings.TrimSpace(p.NativeModel); n != "" {
		return n
	}
	return p.Model
}

// BackendFacingCandidate returns a copy with Primary.Model projected to the bound
// NativeID for true backend-dependent seams (Open, ResolveCaps, transport caps).
// Logical Model, Key, affinity, and other identity fields are preserved on the
// original candidate; only the returned copy's Model is rewritten.
func BackendFacingCandidate(c AttemptCandidate) AttemptCandidate {
	out := c
	if n := strings.TrimSpace(c.Primary.NativeModel); n != "" {
		out.Primary.Model = n
	}
	return out
}
