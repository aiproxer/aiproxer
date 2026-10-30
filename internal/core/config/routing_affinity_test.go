package config

import "testing"

func TestValidateRoutingAffinityDefaultsAndValues(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Routing.Affinity.Store != "memory" {
		t.Fatalf("store default got %q", cfg.Routing.Affinity.Store)
	}
	if cfg.Routing.Affinity.MissingIdentity != "fail_closed" {
		t.Fatalf("missing_identity default got %q", cfg.Routing.Affinity.MissingIdentity)
	}
}

func TestValidateRoutingAffinityNormalizesCase(t *testing.T) {
	t.Parallel()
	cfg := &Config{Routing: RoutingConfig{Affinity: RoutingAffinityConfig{Store: "MEMORY", MissingIdentity: "FAIL_CLOSED"}}}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Routing.Affinity.Store != "memory" || cfg.Routing.Affinity.MissingIdentity != "fail_closed" {
		t.Fatalf("normalized affinity config: %+v", cfg.Routing.Affinity)
	}
}

func TestValidateRoutingAffinityRejectsUnsupportedValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  Config
	}{
		{name: "store", cfg: Config{Routing: RoutingConfig{Affinity: RoutingAffinityConfig{Store: "sqlite"}}}},
		{name: "missing", cfg: Config{Routing: RoutingConfig{Affinity: RoutingAffinityConfig{MissingIdentity: "fallback"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := tc.cfg
			if err := Validate(&cfg); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
