package proxymemory

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	configRoutesOnly = "extract_route: openai:gpt-5-mini\nconsolidate_route: openai:gpt-5\n"
	configRequired   = "principals: [alice, bob]\n" + configRoutesOnly
	// configModelsPolicy is the per-phase model policy: one default target, one
	// lock set and one allowed inventory per phase, with the allowed reasoning
	// efforts declared per target (design §3).
	configModelsPolicy = "models:\n" +
		"  extraction:\n" +
		"    defaults:\n" +
		"      backend: openai\n" +
		"      model: gpt-5-mini\n" +
		"      reasoning_effort: low\n" +
		"    locks:\n" +
		"      backend: true\n" +
		"    allowed:\n" +
		"      backends: [openai, anthropic]\n" +
		"      models: [gpt-5-mini, gpt-5]\n" +
		"      targets:\n" +
		"        - backend: openai\n" +
		"          model: gpt-5-mini\n" +
		"          reasoning_efforts: [low, medium]\n" +
		"        - backend: anthropic\n" +
		"          model: gpt-5\n" +
		"          reasoning_efforts: [high]\n" +
		"  consolidation:\n" +
		"    defaults:\n" +
		"      backend: openai\n" +
		"      model: gpt-5\n" +
		"      reasoning_effort: medium\n" +
		"    locks:\n" +
		"      model: true\n" +
		"    allowed:\n" +
		"      backends: [openai]\n" +
		"      models: [gpt-5]\n" +
		"      targets:\n" +
		"        - backend: openai\n" +
		"          model: gpt-5\n" +
		"          reasoning_efforts: [medium, high]\n"
	// configModelsLocks is the per-phase model policy with a lock set: the
	// extraction phase locks its model and its reasoning effort, so the owner
	// may choose neither, and the consolidation phase locks nothing
	// (design §3; Requirement 1.2).
	configModelsLocks = "models:\n" +
		"  extraction:\n" +
		"    defaults:\n" +
		"      backend: openai\n" +
		"      model: gpt-5-mini\n" +
		"      reasoning_effort: low\n" +
		"    locks:\n" +
		"      model: true\n" +
		"      reasoning_effort: true\n" +
		"    allowed:\n" +
		"      backends: [openai, anthropic]\n" +
		"      models: [gpt-5-mini, gpt-5]\n" +
		"      targets:\n" +
		"        - backend: openai\n" +
		"          model: gpt-5-mini\n" +
		"          reasoning_efforts: [low, medium]\n" +
		"        - backend: anthropic\n" +
		"          model: gpt-5\n" +
		"          reasoning_efforts: [high]\n" +
		"  consolidation:\n" +
		"    defaults:\n" +
		"      backend: openai\n" +
		"      model: gpt-5\n" +
		"      reasoning_effort: medium\n" +
		"    locks: {}\n" +
		"    allowed:\n" +
		"      backends: [openai]\n" +
		"      models: [gpt-5]\n" +
		"      targets:\n" +
		"        - backend: openai\n" +
		"          model: gpt-5\n" +
		"          reasoning_efforts: [medium, high]\n"
)

// mapToNode converts raw YAML text into the node shape a feature factory
// receives: a document node for text, or the zero node for an empty subtree.
func mapToNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	if raw == "" {
		return yaml.Node{}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("test YAML %q is invalid: %v", raw, err)
	}
	return doc
}

// requireConfigRejection decodes raw and requires a *ConfigError naming field.
func requireConfigRejection(t *testing.T, raw, field string) {
	t.Helper()
	cfg, err := DecodeConfig(mapToNode(t, raw))
	if err == nil {
		t.Fatalf("DecodeConfig() = %+v, want a %q rejection", cfg, field)
	}
	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("DecodeConfig() error %v is not *ConfigError", err)
	}
	if configErr.Field != field {
		t.Fatalf("DecodeConfig() rejection field = %q, want %q", configErr.Field, field)
	}
	if !strings.Contains(err.Error(), field) {
		t.Fatalf("DecodeConfig() error %q does not name %q", err, field)
	}
}

type configFieldWant struct {
	name string
	got  any
	want any
}

func checkConfigFields(t *testing.T, cfg Config, wants []configFieldWant) {
	t.Helper()
	for _, want := range wants {
		if want.got != want.want {
			t.Errorf("%s = %v, want %v", want.name, want.got, want.want)
		}
	}
}

// checkTargetFields asserts the three fields of one phase's default target.
func checkTargetFields(t *testing.T, name string, got, want PhaseTarget) {
	t.Helper()
	if got != want {
		t.Errorf("%s defaults = %+v, want %+v", name, got, want)
	}
}

func TestConfigDecodeAndValidate(t *testing.T) {
	t.Parallel()

	const fullOverride = "principals: [alice, bob]\n" +
		"allow_local_principal: true\n" +
		"extract_route: openai:gpt-5-mini\n" +
		"consolidate_route: openai:gpt-5\n" +
		"min_idle: 8h\n" +
		"max_session_age: 100h\n" +
		"max_unused_age: 900h\n" +
		"max_raw_memories: 128\n" +
		"poll_interval: 1m\n" +
		"phase1_batch: 2\n" +
		"phase2_batch: 3\n" +
		"phase1_input_tokens: 1000\n" +
		"phase2_input_tokens: 2000\n" +
		"phase2_note_bytes: 1024\n" +
		"summary_tokens: 4000\n" +
		"max_transcript_bytes: 1024\n" +
		"lease: 30m\n" +
		"retry_backoff: 10m\n" +
		"aux_timeout: 1m\n" +
		"max_output_bytes: 2048\n" +
		"max_note_bytes: 512\n" +
		"max_read_bytes: 1024\n" +
		"max_search_results: 5\n"

	cases := []struct {
		name         string
		raw          string
		wantErrField string
		check        func(t *testing.T, cfg Config)
	}{
		{
			name:         "an empty node misses principals and routes",
			raw:          "",
			wantErrField: "principals",
		},
		{
			name:         "a null node misses principals and routes",
			raw:          "null",
			wantErrField: "principals",
		},
		{
			name:         "an empty mapping misses principals and routes",
			raw:          "{}",
			wantErrField: "principals",
		},
		{
			name:         "principals without routes miss extract_route",
			raw:          "principals: [alice]\nconsolidate_route: openai:gpt-5\n",
			wantErrField: "extract_route",
		},
		{
			name:         "routes without principals miss principals",
			raw:          configRoutesOnly,
			wantErrField: "principals",
		},
		{
			name:         "missing consolidate route",
			raw:          "principals: [alice]\nextract_route: openai:gpt-5-mini\n",
			wantErrField: "consolidate_route",
		},
		{
			name: "only the required keys set applies every documented default",
			raw:  configRequired,
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if want := []string{"alice", "bob"}; !slices.Equal(cfg.Principals, want) {
					t.Errorf("principals = %v, want %v", cfg.Principals, want)
				}
				if cfg.AllowLocalPrincipal {
					t.Error("allow_local_principal = true, want default false")
				}
				if cfg.ExtractRoute != "openai:gpt-5-mini" {
					t.Errorf("extract_route = %q", cfg.ExtractRoute)
				}
				if cfg.ConsolidateRoute != "openai:gpt-5" {
					t.Errorf("consolidate_route = %q", cfg.ConsolidateRoute)
				}
				checkConfigFields(t, cfg, []configFieldWant{
					{"min_idle", cfg.MinIdle, 6 * time.Hour},
					{"max_session_age", cfg.MaxSessionAge, 240 * time.Hour},
					{"max_unused_age", cfg.MaxUnusedAge, 720 * time.Hour},
					{"max_raw_memories", cfg.MaxRawMemories, 256},
					{"poll_interval", cfg.PollInterval, 5 * time.Minute},
					{"phase1_batch", cfg.Phase1Batch, 8},
					{"phase2_batch", cfg.Phase2Batch, 4},
					{"phase1_input_tokens", cfg.Phase1InputTokens, 150000},
					{"phase2_input_tokens", cfg.Phase2InputTokens, 150000},
					{"phase2_note_bytes", cfg.Phase2NoteBytes, 65536},
					{"summary_tokens", cfg.SummaryTokens, 2500},
					{"max_transcript_bytes", cfg.MaxTranscriptBytes, 8388608},
					{"lease", cfg.Lease, time.Hour},
					{"retry_backoff", cfg.RetryBackoff, time.Hour},
					{"aux_timeout", cfg.AuxTimeout, 10 * time.Minute},
					{"max_output_bytes", cfg.MaxOutputBytes, 1048576},
					{"max_note_bytes", cfg.MaxNoteBytes, 8192},
					{"max_read_bytes", cfg.MaxReadBytes, 65536},
					{"max_search_results", cfg.MaxSearchResults, 50},
					{"extraction default effort", cfg.Models.Extraction.Defaults.ReasoningEffort, "low"},
					{"consolidation default effort", cfg.Models.Consolidation.Defaults.ReasoningEffort, "medium"},
				})
			},
		},
		{
			name: "explicit snake_case values override every default",
			raw:  fullOverride,
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if !cfg.AllowLocalPrincipal {
					t.Error("allow_local_principal = false, want true")
				}
				checkConfigFields(t, cfg, []configFieldWant{
					{"min_idle", cfg.MinIdle, 8 * time.Hour},
					{"max_session_age", cfg.MaxSessionAge, 100 * time.Hour},
					{"max_unused_age", cfg.MaxUnusedAge, 900 * time.Hour},
					{"max_raw_memories", cfg.MaxRawMemories, 128},
					{"poll_interval", cfg.PollInterval, time.Minute},
					{"phase1_batch", cfg.Phase1Batch, 2},
					{"phase2_batch", cfg.Phase2Batch, 3},
					{"phase1_input_tokens", cfg.Phase1InputTokens, 1000},
					{"phase2_input_tokens", cfg.Phase2InputTokens, 2000},
					{"phase2_note_bytes", cfg.Phase2NoteBytes, 1024},
					{"summary_tokens", cfg.SummaryTokens, 4000},
					{"max_transcript_bytes", cfg.MaxTranscriptBytes, 1024},
					{"lease", cfg.Lease, 30 * time.Minute},
					{"retry_backoff", cfg.RetryBackoff, 10 * time.Minute},
					{"aux_timeout", cfg.AuxTimeout, time.Minute},
					{"max_output_bytes", cfg.MaxOutputBytes, 2048},
					{"max_note_bytes", cfg.MaxNoteBytes, 512},
					{"max_read_bytes", cfg.MaxReadBytes, 1024},
					{"max_search_results", cfg.MaxSearchResults, 5},
				})
			},
		},
		{
			name: "the wildcard alone is a valid allowlist",
			raw:  "principals: [\"*\"]\n" + configRoutesOnly,
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if want := []string{"*"}; !slices.Equal(cfg.Principals, want) {
					t.Errorf("principals = %v, want %v", cfg.Principals, want)
				}
			},
		},
		{
			name:         "the wildcard with another entry is rejected",
			raw:          "principals: [\"*\", alice]\n" + configRoutesOnly,
			wantErrField: "principals",
		},
		{
			name:         "a blank allowlist entry is rejected",
			raw:          "principals: [alice, \"  \"]\n" + configRoutesOnly,
			wantErrField: "principals",
		},
		{
			name:         "an empty allowlist is rejected",
			raw:          "principals: []\n" + configRoutesOnly,
			wantErrField: "principals",
		},
		{
			name:         "a non-string allowlist entry is rejected",
			raw:          "principals: [1]\n" + configRoutesOnly,
			wantErrField: "principals",
		},
		{
			name:         "a duplicated key is rejected",
			raw:          configRequired + "principals: [alice]\n",
			wantErrField: "principals",
		},
		{
			name:         "a blank extract route is rejected",
			raw:          "principals: [alice]\nextract_route: \"  \"\nconsolidate_route: openai:gpt-5\n",
			wantErrField: "extract_route",
		},
		{
			name:         "a blank consolidate route is rejected",
			raw:          "principals: [alice]\nextract_route: openai:gpt-5-mini\nconsolidate_route: \"\"\n",
			wantErrField: "consolidate_route",
		},
		{
			name:         "min_idle equal to max_session_age is rejected",
			raw:          configRequired + "min_idle: 240h\n",
			wantErrField: "min_idle",
		},
		{
			name:         "min_idle over max_session_age is rejected",
			raw:          configRequired + "min_idle: 300h\n",
			wantErrField: "min_idle",
		},
		{
			name:         "aux_timeout equal to lease is rejected",
			raw:          configRequired + "lease: 1h\naux_timeout: 1h\n",
			wantErrField: "aux_timeout",
		},
		{
			name:         "aux_timeout over lease is rejected",
			raw:          configRequired + "lease: 1h\naux_timeout: 2h\n",
			wantErrField: "aux_timeout",
		},
		{
			name:         "summary_tokens over 4000 is rejected",
			raw:          configRequired + "summary_tokens: 4001\n",
			wantErrField: "summary_tokens",
		},
		{
			name:         "max_transcript_bytes over 64 MiB is rejected",
			raw:          configRequired + "max_transcript_bytes: 67108865\n",
			wantErrField: "max_transcript_bytes",
		},
		{
			name:         "an unknown key is rejected",
			raw:          configRequired + "bogus_key: 1\n",
			wantErrField: "config",
		},
		{
			name:         "a nested enabled key is rejected",
			raw:          configRequired + "enabled: true\n",
			wantErrField: "enabled",
		},
		{
			name:         "a duration given as an integer is rejected",
			raw:          configRequired + "min_idle: 6\n",
			wantErrField: "min_idle",
		},
		{
			name:         "a non-positive duration is rejected",
			raw:          configRequired + "lease: 0s\n",
			wantErrField: "lease",
		},
		{
			name:         "a non-positive integer is rejected",
			raw:          configRequired + "phase1_batch: 0\n",
			wantErrField: "phase1_batch",
		},
		{
			name:         "a quoted boolean is rejected",
			raw:          configRequired + "allow_local_principal: \"true\"\n",
			wantErrField: "allow_local_principal",
		},
		{
			name:         "an in-memory sqlite path is rejected",
			raw:          configRequired + "sqlite_path: \":memory:\"\n",
			wantErrField: "sqlite_path",
		},
		{
			name:         "the in-memory sqlite URI spelling is rejected",
			raw:          configRequired + "sqlite_path: \"file::memory:\"\n",
			wantErrField: "sqlite_path",
		},
		{
			name: "the per-phase model policy decodes",
			raw:  configRequired + configModelsPolicy,
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				if cfg.ExtractRoute != "openai:gpt-5-mini" {
					t.Errorf("extract_route = %q", cfg.ExtractRoute)
				}
				if cfg.ConsolidateRoute != "openai:gpt-5" {
					t.Errorf("consolidate_route = %q", cfg.ConsolidateRoute)
				}
				extraction, ok := cfg.Models.For(PhaseExtraction)
				if !ok {
					t.Fatal("the extraction policy is missing")
				}
				checkTargetFields(t, "extraction", extraction.Defaults, PhaseTarget{
					Backend:         "openai",
					Model:           "gpt-5-mini",
					ReasoningEffort: "low",
				})
				if want := []string{"openai", "anthropic"}; !slices.Equal(extraction.Inventory.Backends, want) {
					t.Errorf("extraction backends = %v, want %v", extraction.Inventory.Backends, want)
				}
				if want := []string{"gpt-5-mini", "gpt-5"}; !slices.Equal(extraction.Inventory.Models, want) {
					t.Errorf("extraction models = %v, want %v", extraction.Inventory.Models, want)
				}
				consolidation, ok := cfg.Models.For(PhaseConsolidation)
				if !ok {
					t.Fatal("the consolidation policy is missing")
				}
				checkTargetFields(t, "consolidation", consolidation.Defaults, PhaseTarget{
					Backend:         "openai",
					Model:           "gpt-5",
					ReasoningEffort: "medium",
				})
				if want := []string{"openai"}; !slices.Equal(consolidation.Inventory.Backends, want) {
					t.Errorf("consolidation backends = %v, want %v", consolidation.Inventory.Backends, want)
				}
			},
		},
		{
			name: "the per-target allowed efforts decode",
			raw:  configRequired + configModelsPolicy,
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				extraction, ok := cfg.Models.For(PhaseExtraction)
				if !ok {
					t.Fatal("the extraction policy is missing")
				}
				want := []TargetPolicy{
					{Backend: "openai", Model: "gpt-5-mini", ReasoningEfforts: []string{"low", "medium"}},
					{Backend: "anthropic", Model: "gpt-5", ReasoningEfforts: []string{"high"}},
				}
				if len(extraction.Inventory.Targets) != len(want) {
					t.Fatalf("extraction targets = %+v, want %+v", extraction.Inventory.Targets, want)
				}
				for i, target := range extraction.Inventory.Targets {
					if target.Backend != want[i].Backend || target.Model != want[i].Model {
						t.Errorf("extraction target %d = %+v, want %+v", i, target, want[i])
					}
					if !slices.Equal(target.ReasoningEfforts, want[i].ReasoningEfforts) {
						t.Errorf("extraction target %d efforts = %v, want %v", i, target.ReasoningEfforts, want[i].ReasoningEfforts)
					}
				}
			},
		},
		{
			name: "the per-phase lock set decodes",
			raw:  configRequired + configModelsPolicy,
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				extraction, ok := cfg.Models.For(PhaseExtraction)
				if !ok {
					t.Fatal("the extraction policy is missing")
				}
				if want := (TargetLocks{Backend: true}); extraction.Locks != want {
					t.Errorf("extraction locks = %+v, want %+v", extraction.Locks, want)
				}
				consolidation, ok := cfg.Models.For(PhaseConsolidation)
				if !ok {
					t.Fatal("the consolidation policy is missing")
				}
				if want := (TargetLocks{Model: true}); consolidation.Locks != want {
					t.Errorf("consolidation locks = %+v, want %+v", consolidation.Locks, want)
				}
			},
		},
		{
			name:         "an unknown key inside the lock set is rejected",
			raw:          configRequired + "models:\n  extraction:\n    defaults: {}\n    locks:\n      backends: true\n    allowed: {}\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.extraction.locks",
		},
		{
			name:         "a duplicated key inside the lock set is rejected",
			raw:          configRequired + "models:\n  extraction:\n    defaults: {}\n    locks:\n      backend: true\n      backend: false\n    allowed: {}\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.extraction.locks.backend",
		},
		{
			name:         "a non-boolean lock is rejected",
			raw:          configRequired + "models:\n  extraction:\n    defaults: {}\n    locks:\n      backend: \"true\"\n    allowed: {}\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.extraction.locks.backend",
		},
		{
			name:         "a non-mapping lock set is rejected",
			raw:          configRequired + "models:\n  extraction:\n    defaults: {}\n    locks: [backend]\n    allowed: {}\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.extraction.locks",
		},
		{
			name:         "an unknown reasoning effort token is rejected",
			raw:          configRequired + "models:\n  extraction:\n    defaults:\n      reasoning_effort: \"not a token\"\n    locks: {}\n    allowed: {}\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.extraction.defaults.reasoning_effort",
		},
		{
			name:         "an unknown key inside the phase defaults is rejected",
			raw:          configRequired + "models:\n  extraction:\n    defaults:\n      modelz: gpt-5\n",
			wantErrField: "models.extraction.defaults",
		},
		{
			name:         "a duplicated key inside the phase defaults is rejected",
			raw:          configRequired + "models:\n  extraction:\n    defaults:\n      backend: openai\n      backend: anthropic\n",
			wantErrField: "models.extraction.defaults.backend",
		},
		{
			name:         "an unknown key inside the allowed inventory is rejected",
			raw:          configRequired + "models:\n  extraction:\n    allowed:\n      backendsz: [openai]\n",
			wantErrField: "models.extraction.allowed",
		},
		{
			name:         "a non-mapping model policy is rejected",
			raw:          configRequired + "models: [openai]\n",
			wantErrField: "models",
		},
		{
			name:         "a blank inventory entry is rejected",
			raw:          configRequired + "models:\n  extraction:\n    defaults: {}\n    locks: {}\n    allowed:\n      models: [\"  \"]\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.extraction.allowed.models",
		},
		{
			name:         "a repeated inventory entry is rejected",
			raw:          configRequired + "models:\n  extraction:\n    defaults: {}\n    locks: {}\n    allowed:\n      backends: [openai, openai]\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.extraction.allowed.backends",
		},
		{
			name:         "a phase target with a multi-target default route is rejected",
			raw:          "principals: [alice]\nextract_route: \"openai:gpt-5-mini|anthropic:claude\"\nconsolidate_route: openai:gpt-5\n" + configModelsPolicy,
			wantErrField: "extract_route",
		},
		{
			name:         "a multi-target default route behind a leaf query is rejected",
			raw:          "principals: [alice]\nextract_route: \"openai:gpt-5-mini?temperature=0.1|anthropic:claude\"\nconsolidate_route: openai:gpt-5\n" + configModelsPolicy,
			wantErrField: "extract_route",
		},
		{
			name:         "an explicit policy must declare every phase",
			raw:          configRequired + "models:\n  extraction:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.consolidation",
		},
		{
			name:         "an explicit phase must declare its default target",
			raw:          configRequired + "models:\n  extraction:\n    allowed: {}\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.extraction.defaults",
		},
		{
			name:         "an explicit phase must declare its lock set",
			raw:          configRequired + "models:\n  extraction:\n    defaults: {}\n    allowed: {}\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.extraction.locks",
		},
		{
			name:         "an explicit phase must declare its allowed inventory",
			raw:          configRequired + "models:\n  extraction:\n    defaults: {}\n    locks: {}\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.extraction.allowed",
		},
		{
			name:         "a default model that needs an explicit backend is rejected",
			raw:          "principals: [alice]\nextract_route: gpt-5-mini\nconsolidate_route: openai:gpt-5\n" + "models:\n  extraction:\n    defaults:\n      model: qwen2.5-coder:7b\n    locks: {}\n    allowed: {}\n  consolidation:\n    defaults: {}\n    locks: {}\n    allowed: {}\n",
			wantErrField: "models.extraction.defaults.model",
		},
		{
			name: "the allowed efforts are declared per target",
			raw:  configRequired + configModelsPolicy,
			check: func(t *testing.T, cfg Config) {
				t.Helper()
				extraction, ok := cfg.Models.For(PhaseExtraction)
				if !ok {
					t.Fatal("the extraction policy is missing")
				}
				want := []TargetPolicy{
					{Backend: "openai", Model: "gpt-5-mini", ReasoningEfforts: []string{"low", "medium"}},
					{Backend: "anthropic", Model: "gpt-5", ReasoningEfforts: []string{"high"}},
				}
				if len(extraction.Inventory.Targets) != len(want) {
					t.Fatalf("extraction targets = %+v, want %+v", extraction.Inventory.Targets, want)
				}
				for i, target := range extraction.Inventory.Targets {
					if target.Backend != want[i].Backend || target.Model != want[i].Model {
						t.Errorf("extraction target %d = %+v, want %+v", i, target, want[i])
					}
					if !slices.Equal(target.ReasoningEfforts, want[i].ReasoningEfforts) {
						t.Errorf("extraction target %d efforts = %v, want %v", i, target.ReasoningEfforts, want[i].ReasoningEfforts)
					}
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.wantErrField != "" {
				requireConfigRejection(t, tc.raw, tc.wantErrField)
				return
			}
			cfg, err := DecodeConfig(mapToNode(t, tc.raw))
			if err != nil {
				t.Fatalf("DecodeConfig() error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, cfg)
			}
		})
	}
}

// TestConfigRejectsAuxTimeoutNotBelowLease pins the lease guard: a worker
// leases a claimed item for `lease` while one auxiliary job may run for
// `aux_timeout`, so an aux_timeout that is not strictly below the lease can
// outlive the lease and let a second instance duplicate the work.
func TestConfigRejectsAuxTimeoutNotBelowLease(t *testing.T) {
	t.Parallel()

	cfg, err := DecodeConfig(mapToNode(t, configRequired+"lease: 1h\naux_timeout: 2h\n"))
	if err == nil {
		t.Fatalf("DecodeConfig() = %+v, want a rejection", cfg)
	}
	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("DecodeConfig() error %v is not *ConfigError", err)
	}
	if configErr.Field != "aux_timeout" {
		t.Errorf("rejection field = %q, want %q", configErr.Field, "aux_timeout")
	}
	if want := "must be less than lease"; configErr.Reason != want {
		t.Errorf("rejection reason = %q, want %q", configErr.Reason, want)
	}
	if !strings.Contains(err.Error(), ID+": aux_timeout: must be less than lease") {
		t.Errorf("rejection message %q does not render the field and reason", err)
	}
}

// TestConfigRejectsPhase2NoteBytesBelowMaxNoteBytes pins the note-budget
// invariant: Requirement 5.9 defers notes that did not fit to a later
// consolidation, so the phase-2 note budget must admit at least one largest
// note; a budget below the note limit would strand every note in a
// notes-only loop (design §7.3).
func TestConfigRejectsPhase2NoteBytesBelowMaxNoteBytes(t *testing.T) {
	t.Parallel()

	cfg, err := DecodeConfig(mapToNode(t, configRequired+"max_note_bytes: 8192\nphase2_note_bytes: 4096\n"))
	if err == nil {
		t.Fatalf("DecodeConfig() = %+v, want a rejection", cfg)
	}
	var configErr *ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("DecodeConfig() error %v is not *ConfigError", err)
	}
	if configErr.Field != "phase2_note_bytes" {
		t.Errorf("rejection field = %q, want %q", configErr.Field, "phase2_note_bytes")
	}
	if want := "must not be less than max_note_bytes"; configErr.Reason != want {
		t.Errorf("rejection reason = %q, want %q", configErr.Reason, want)
	}
	if !strings.Contains(err.Error(), ID+": phase2_note_bytes: must not be less than max_note_bytes") {
		t.Errorf("rejection message %q does not render the field and reason", err)
	}
	if _, err := DecodeConfig(mapToNode(t, configRequired+"max_note_bytes: 65536\nphase2_note_bytes: 65536\n")); err != nil {
		t.Fatalf("DecodeConfig(equal budgets) error: %v", err)
	}
}

// TestConfigDecodeAcceptsBareMappingNode pins the factory-facing shape: the
// registry hands DecodeConfig the unwrapped config mapping, not a document.
func TestConfigDecodeAcceptsBareMappingNode(t *testing.T) {
	t.Parallel()

	doc := mapToNode(t, configRequired)
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		t.Fatalf("test setup: node kind = %v", doc.Kind)
	}
	cfg, err := DecodeConfig(*doc.Content[0])
	if err != nil {
		t.Fatalf("DecodeConfig(bare mapping) error: %v", err)
	}
	if !slices.Equal(cfg.Principals, []string{"alice", "bob"}) {
		t.Fatalf("principals = %v", cfg.Principals)
	}
}
