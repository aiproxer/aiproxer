package modelregistry

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

type boundViewKey struct{}

// BoundView is an immutable, request-scoped publication of the model registry.
// It freezes registry lookups, models JSON, generation, and publication-tied
// discovery metadata so a later Runtime refresh cannot alter an in-flight request.
//
// BoundView never exposes the mutable Runtime, its mutexes, or atomics.
// Live operational last-refresh/cache failure categories are captured at bind
// time for diagnostics but are not part of the immutable publication object.
type BoundView struct {
	pub       *published
	lastFail  RefreshFailureCategory // live operational at bind; not publication data
	cacheFail RefreshFailureCategory // live operational at bind; not publication data
}

// EmptyBoundView returns a safe empty view (nil/unavailable runtime).
func EmptyBoundView() BoundView {
	return BoundView{}
}

// BoundView captures the current atomic publication (registry+discoveries) once.
// A nil receiver returns an empty view.
func (r *Runtime) BoundView() BoundView {
	if r == nil {
		return EmptyBoundView()
	}
	pub := r.published.Load()
	r.mu.Lock()
	defer r.mu.Unlock()
	return BoundView{
		pub:       pub,
		lastFail:  r.lastFail,
		cacheFail: r.cacheFail,
	}
}

// WithBoundView attaches v to ctx for request-scoped consumers.
func WithBoundView(ctx context.Context, v BoundView) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, boundViewKey{}, v)
}

// BoundViewFromContext returns the request-bound registry view when present.
func BoundViewFromContext(ctx context.Context) (BoundView, bool) {
	if ctx == nil {
		return BoundView{}, false
	}
	v, ok := ctx.Value(boundViewKey{}).(BoundView)
	return v, ok
}

// Active reports whether a publication was bound.
func (v BoundView) Active() bool {
	return v.pub != nil
}

// Generation returns the frozen model-registry generation identity.
func (v BoundView) Generation() string {
	if v.pub == nil {
		return ""
	}
	return v.pub.snap.Generation
}

// RefreshedAt returns the frozen publication refresh time.
func (v BoundView) RefreshedAt() time.Time {
	if v.pub == nil {
		return time.Time{}
	}
	return v.pub.snap.RefreshedAt
}

// Lookup returns defensive clones of models for canonicalID from the frozen registry.
func (v BoundView) Lookup(canonicalID string) ([]BackendModel, bool) {
	if v.pub == nil || v.pub.reg == nil {
		return nil, false
	}
	return v.pub.reg.Lookup(canonicalID)
}

// All returns a defensive clone of all frozen registry rows.
func (v BoundView) All() []BackendModel {
	if v.pub == nil || v.pub.reg == nil {
		return []BackendModel{}
	}
	return v.pub.reg.All()
}

// ModelsJSON returns a defensive copy of the frozen OpenAI /v1/models body.
func (v BoundView) ModelsJSON() (body []byte, generation string, ok bool) {
	if v.pub == nil {
		return nil, "", false
	}
	return slices.Clone(v.pub.modelsJSON), v.pub.snap.Generation, true
}

// Diagnostics returns coherent publication diagnostics frozen at bind time.
// LastRefreshErrorCategory / LastCacheErrorCategory reflect live operational
// state captured at bind and are not publication identity fields.
func (v BoundView) Diagnostics() Diagnostics {
	out := Diagnostics{
		BackendModelCounts:       map[string]int{},
		BackendDiscoveries:       []BackendDiscovery{},
		LastRefreshErrorCategory: v.lastFail,
		LastCacheErrorCategory:   v.cacheFail,
	}
	if v.pub == nil {
		return out
	}
	out.Active = true
	out.Generation = v.pub.snap.Generation
	out.RefreshedAt = v.pub.snap.RefreshedAt
	out.ModelCount = len(v.pub.snap.Models)
	out.BackendModelCounts = cloneIntMap(v.pub.backendModelCounts)
	if v.pub.discoveries != nil {
		out.BackendDiscoveries = slices.Clone(v.pub.discoveries)
	}
	if out.BackendDiscoveries == nil {
		out.BackendDiscoveries = []BackendDiscovery{}
	}
	return out
}

// PublicationGeneration returns the frozen publication generation (alias of Generation).
func (v BoundView) PublicationGeneration() string { return v.Generation() }

// ResolveModelBinding implements [routing.NativeModelResolver] with multi-state outcomes.
func (v BoundView) ResolveModelBinding(backendID, model string) routing.ModelBinding {
	backendID = strings.TrimSpace(backendID)
	model = strings.TrimSpace(model)
	if backendID == "" || model == "" || v.pub == nil || v.pub.reg == nil {
		return routing.ModelBinding{Kind: routing.ModelBindingUnknown}
	}
	rows, found := v.pub.reg.Lookup(model)
	if found {
		var exact *BackendModel
		for i := range rows {
			if strings.TrimSpace(rows[i].BackendID) == backendID {
				exact = &rows[i]
				break
			}
		}
		if exact == nil {
			return routing.ModelBinding{Kind: routing.ModelBindingWrongBackend}
		}
		native := strings.TrimSpace(exact.NativeID)
		if native == "" {
			return routing.ModelBinding{Kind: routing.ModelBindingUnknown}
		}
		return routing.ModelBinding{Kind: routing.ModelBindingExactCanonical, Native: native}
	}
	// Already-known native for this backend (not a canonical registry key).
	for _, row := range v.pub.reg.All() {
		if strings.TrimSpace(row.BackendID) == backendID && strings.TrimSpace(row.NativeID) == model {
			return routing.ModelBinding{Kind: routing.ModelBindingKnownNative, Native: model}
		}
	}
	return routing.ModelBinding{Kind: routing.ModelBindingUnknown}
}

// ResolveNative is a compatibility helper: ok is true only for exact-canonical
// or known-native bindings. Wrong-backend and unknown both return ok=false;
// callers that need fail-closed planning must use ResolveModelBinding.
func (v BoundView) ResolveNative(backendID, model string) (native string, ok bool) {
	b := v.ResolveModelBinding(backendID, model)
	switch b.Kind {
	case routing.ModelBindingExactCanonical, routing.ModelBindingKnownNative:
		return b.Native, true
	default:
		return "", false
	}
}

var _ routing.NativeModelResolver = BoundView{}
