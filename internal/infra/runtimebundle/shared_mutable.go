package runtimebundle

import (
	"context"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	affinitymem "github.com/aiproxer/aiproxer/internal/core/affinity/memorystore"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/policy"
	corestate "github.com/aiproxer/aiproxer/internal/core/state"
	"github.com/aiproxer/aiproxer/internal/infra/routinghealth"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

const healthNamespaceSep = "\x1e"

// sharedMutableRuntime holds process-owned overlap-sensitive mutable continuity.
type sharedMutableRuntime struct {
	ALegLifecycle    *leglifecycle.Coordinator
	ExtensionState   aipstate.Store
	affinity         *affinityRegistry
	healthState      *policy.CircuitBreakerState
	underlyingHealth policy.CandidateHealth
}

func buildSharedMutableRuntime(cfg *config.Config, nowFn func() time.Time) *sharedMutableRuntime {
	if nowFn == nil {
		nowFn = time.Now
	}
	healthState := policy.NewCircuitBreakerState(policy.CircuitBreakerStateOptions{})
	return &sharedMutableRuntime{
		ALegLifecycle:  leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: leglifecycle.DefaultCancelTimeout}),
		ExtensionState: corestate.NewMem(nowFn), affinity: newAffinityRegistry(), healthState: healthState,
		underlyingHealth: routinghealth.CandidateHealthPolicyFromState(cfg, healthState, nowFn),
	}
}

func (s *sharedMutableRuntime) candidateRoutingViews(active map[string]BackendStateIdentity, cfg *config.Config, nowFn func() time.Time) (affinity.Store, policy.CandidateHealth) {
	if s == nil {
		return nil, nil
	}
	if nowFn == nil {
		nowFn = time.Now
	}
	health := routinghealth.CandidateHealthPolicyFromState(cfg, s.healthState, nowFn)
	return newAffinityView(s.affinity, active), newHealthView(newHealthRegistry(health), active)
}

type affinityRegistry struct {
	store           *affinitymem.Store
	mu              sync.Mutex
	bindingIdentity map[affinity.Key]BackendStateIdentity
}

func newAffinityRegistry() *affinityRegistry {
	return &affinityRegistry{store: affinitymem.New(), bindingIdentity: make(map[affinity.Key]BackendStateIdentity)}
}

type affinityView struct {
	reg    *affinityRegistry
	active map[string]BackendStateIdentity
}

func newAffinityView(reg *affinityRegistry, active map[string]BackendStateIdentity) *affinityView {
	cp := make(map[string]BackendStateIdentity, len(active))
	maps.Copy(cp, active)
	return &affinityView{reg: reg, active: cp}
}

func (v *affinityView) Get(ctx context.Context, key affinity.Key) (affinity.Binding, bool, error) {
	if v == nil || v.reg == nil {
		return affinity.Binding{}, false, nil
	}
	b, ok, err := v.reg.store.Get(ctx, key)
	if err != nil || !ok {
		return b, ok, err
	}
	backend := strings.TrimSpace(b.BackendID)
	active, present := v.active[backend]
	if !present || backend == "" {
		return affinity.Binding{}, false, nil
	}
	v.reg.mu.Lock()
	stored, have := v.reg.bindingIdentity[key]
	v.reg.mu.Unlock()
	if !have || !stored.Compatible(active) {
		return affinity.Binding{}, false, nil
	}
	return b, true, nil
}

func (v *affinityView) Set(ctx context.Context, binding affinity.Binding) error {
	if v == nil || v.reg == nil {
		return nil
	}
	if err := v.reg.store.Set(ctx, binding); err != nil {
		return err
	}
	backend := strings.TrimSpace(binding.BackendID)
	if id, ok := v.active[backend]; ok && binding.Key.Valid() {
		v.reg.mu.Lock()
		v.reg.bindingIdentity[binding.Key] = id
		v.reg.mu.Unlock()
	}
	return nil
}

func (v *affinityView) Delete(ctx context.Context, key affinity.Key) error {
	if v == nil || v.reg == nil {
		return nil
	}
	if err := v.reg.store.Delete(ctx, key); err != nil {
		return err
	}
	v.reg.mu.Lock()
	delete(v.reg.bindingIdentity, key)
	v.reg.mu.Unlock()
	return nil
}

var _ affinity.Store = (*affinityView)(nil)

type healthRegistry struct{ inner policy.CandidateHealth }

func newHealthRegistry(inner policy.CandidateHealth) *healthRegistry {
	return &healthRegistry{inner: inner}
}

type healthView struct {
	reg    *healthRegistry
	active map[string]BackendStateIdentity
}

func newHealthView(reg *healthRegistry, active map[string]BackendStateIdentity) *healthView {
	cp := make(map[string]BackendStateIdentity, len(active))
	maps.Copy(cp, active)
	return &healthView{reg: reg, active: cp}
}

func (v *healthView) UnhealthyCandidateKeys() map[string]struct{} {
	if v == nil || v.reg == nil || v.reg.inner == nil {
		return nil
	}
	raw := v.reg.inner.UnhealthyCandidateKeys()
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]struct{})
	for namespaced := range raw {
		candKey, id, ok := splitHealthNamespace(namespaced)
		if !ok {
			continue
		}
		backend := backendIDFromCandidateKey(candKey)
		active, present := v.active[backend]
		if !present || !active.Compatible(id) {
			continue
		}
		out[candKey] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (v *healthView) OnRoutingAttemptOutcome(candidateKey string, outcome aipapi.AttemptOutcome) {
	sink, ok := v.reg.inner.(policy.RoutingAttemptOutcomeSink)
	if !ok || v == nil {
		return
	}
	backend := backendIDFromCandidateKey(candidateKey)
	id, present := v.active[backend]
	if !present || backend == "" {
		return
	}
	sink.OnRoutingAttemptOutcome(joinHealthNamespace(id, candidateKey), outcome)
}

var (
	_ policy.CandidateHealth           = (*healthView)(nil)
	_ policy.RoutingAttemptOutcomeSink = (*healthView)(nil)
)

func joinHealthNamespace(id BackendStateIdentity, candidateKey string) string {
	return id.Namespace() + healthNamespaceSep + candidateKey
}

func splitHealthNamespace(namespaced string) (candidateKey string, id BackendStateIdentity, ok bool) {
	i := strings.Index(namespaced, healthNamespaceSep)
	if i <= 0 {
		return "", BackendStateIdentity{}, false
	}
	ns := namespaced[:i]
	candidateKey = namespaced[i+len(healthNamespaceSep):]
	at := strings.LastIndexByte(ns, '@')
	if at <= 0 {
		return "", BackendStateIdentity{}, false
	}
	left, digest := ns[:at], ns[at+1:]
	before, after, ok := strings.Cut(left, "/")
	if !ok {
		return "", BackendStateIdentity{}, false
	}
	return candidateKey, BackendStateIdentity{FactoryKind: before, InstanceID: after, ConfigDigest: digest}, true
}

func backendIDFromCandidateKey(candidateKey string) string {
	candidateKey = strings.TrimSpace(candidateKey)
	if candidateKey == "" {
		return ""
	}
	i := strings.IndexByte(candidateKey, ':')
	if i <= 0 {
		return ""
	}
	return candidateKey[:i]
}

type processAffinityHandle struct{ reg *affinityRegistry }

func (h *processAffinityHandle) Get(ctx context.Context, key affinity.Key) (affinity.Binding, bool, error) {
	if h == nil || h.reg == nil {
		return affinity.Binding{}, false, nil
	}
	return h.reg.store.Get(ctx, key)
}

func (h *processAffinityHandle) Set(ctx context.Context, binding affinity.Binding) error {
	if h == nil || h.reg == nil {
		return nil
	}
	return h.reg.store.Set(ctx, binding)
}

func (h *processAffinityHandle) Delete(ctx context.Context, key affinity.Key) error {
	if h == nil || h.reg == nil {
		return nil
	}
	return h.reg.store.Delete(ctx, key)
}

var _ affinity.Store = (*processAffinityHandle)(nil)
