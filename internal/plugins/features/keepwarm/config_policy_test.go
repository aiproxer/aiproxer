package keepwarm

import (
	"errors"
	"testing"
	"time"
)

func TestConfigDefaultsAndValidation(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.MaxRefreshesPerIdleEpoch != 6 || cfg.MaxIdleDuration != time.Hour || cfg.MaxActiveTargets != 1024 || cfg.MaxConcurrentRenewals != 4 || cfg.RenewTimeout != 15*time.Second {
		t.Fatalf("defaults=%+v", cfg)
	}
	bad := cfg
	bad.MaxRefreshesPerIdleEpoch = 0
	if !errors.Is(bad.Validate(), ErrInvalidConfig) {
		t.Fatal("zero refresh bound accepted")
	}
	bad = cfg
	bad.ContinueAfterColdRecreate = false
	bad.MaxColdRecreatesPerIdleEpoch = 1
	if !errors.Is(bad.Validate(), ErrInvalidConfig) {
		t.Fatal("contradictory cold policy accepted")
	}
	budget := int64(0)
	bad = cfg
	bad.MaxProviderTokensPerIdleEpoch = &budget
	if !errors.Is(bad.Validate(), ErrInvalidConfig) {
		t.Fatal("zero token budget accepted")
	}
}

func TestConfigFromYAMLParsesBoundedKeepwarmOnly(t *testing.T) {
	t.Parallel()
	cfg, err := ConfigFromYAML([]byte("prompt_cache:\n  keepwarm:\n    enabled: false\n    max_refreshes_per_idle_epoch: 2\n    max_idle_duration: 1h\n    renew_timeout: 2s\n    heuristic_overrides:\n      - backend_instance: local\n        canonical_model: model\n        interval: 30m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.MaxRefreshesPerIdleEpoch != 2 || cfg.MaxIdleDuration != time.Hour || len(cfg.HeuristicOverrides) != 1 {
		t.Fatalf("cfg=%+v", cfg)
	}
	if _, err := ConfigFromYAML([]byte("prompt_cache:\n  keepwarm:\n    renew_timeout: nope\n")); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err=%v", err)
	}
	if _, err := ConfigFromYAML([]byte("prompt_cache:\n  keepwarm:\n    heuristic_overrides:\n      - backend_instance: local\n        interval: 1h\n      - backend_instance: local\n        interval: 2h\n")); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("duplicate heuristic accepted: %v", err)
	}
}

func TestHeuristicOverrideExactMatchAndExpiryPrecedence(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.HeuristicOverrides = []HeuristicOverride{{BackendInstance: "backend", CanonicalModel: "model", Interval: time.Hour}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.heuristic("backend", "other"); ok {
		t.Fatal("model-specific heuristic widened")
	}
	if _, ok := cfg.heuristic("backend", "model"); !ok {
		t.Fatal("exact heuristic missing")
	}
}

type fakeInvalidator struct{ calls chan string }

func (f *fakeInvalidator) BeginForegroundTurn(id string) { f.calls <- id }

func TestEvidenceGateRequiresIndependentProviderProof(t *testing.T) {
	t.Parallel()
	gate := EvidenceGate{SafeControl: true, AffinityPreserved: true, CacheEffectProven: true, ForegroundIsolationProven: true}
	if !gate.ActiveRenewalSupported() {
		t.Fatal("complete evidence gate rejected")
	}
	for _, missing := range []func(*EvidenceGate){func(g *EvidenceGate) { g.SafeControl = false }, func(g *EvidenceGate) { g.AffinityPreserved = false }, func(g *EvidenceGate) { g.CacheEffectProven = false }, func(g *EvidenceGate) { g.ForegroundIsolationProven = false }} {
		copy := gate
		missing(&copy)
		if copy.ActiveRenewalSupported() {
			t.Fatal("incomplete evidence gate accepted")
		}
	}
}

// sessionDisablerStub records SetSessionDisabled calls for registry broadcast
// tests of the clear path.
type sessionDisablerStub struct {
	disabled bool
	last     string
}

func (s *sessionDisablerStub) BeginForegroundTurn(string) {}
func (s *sessionDisablerStub) SetSessionDisabled(aLegID string, disabled bool) {
	s.last = aLegID
	s.disabled = disabled
}

func TestPolicyStoreClearAndBroadcastRestoresLiveManagerDisabled(t *testing.T) {
	t.Parallel()
	store, err := NewPolicyStore(16)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewManagerRegistry()
	live := &sessionDisablerStub{}
	if _, err := reg.Register(live); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DisableAndBroadcast(reg, "a", time.Now()); err != nil {
		t.Fatal(err)
	}
	if !live.disabled || live.last != "a" {
		t.Fatalf("disable broadcast: last=%q disabled=%v", live.last, live.disabled)
	}
	if err := store.ClearAndBroadcast(reg, "a"); err != nil {
		t.Fatal(err)
	}
	if live.disabled || live.last != "a" {
		t.Fatalf("clear broadcast: last=%q disabled=%v", live.last, live.disabled)
	}
	if _, ok := store.Get("a"); ok {
		t.Fatal("store entry survived ClearAndBroadcast")
	}
}

func TestRegistryUnregistersAndBroadcastsWithoutRetainingGenerations(t *testing.T) {
	t.Parallel()
	r := NewManagerRegistry()
	f := &fakeInvalidator{calls: make(chan string, 1)}
	id, err := r.Register(f)
	if err != nil {
		t.Fatal(err)
	}
	if r.Len() != 1 {
		t.Fatal(r.Len())
	}
	r.Disable("a")
	if got := <-f.calls; got != "a" {
		t.Fatal(got)
	}
	if err := r.Unregister(id); err != nil {
		t.Fatal(err)
	}
	if r.Len() != 0 {
		t.Fatal("retired manager retained")
	}
	if err := r.Unregister(id); !errors.Is(err, ErrManagerNotRegistered) {
		t.Fatal(err)
	}
}
