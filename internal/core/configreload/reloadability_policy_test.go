package configreload_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
)

func TestReloadabilityInventory_EveryTopLevelHasExplicitDisposition(t *testing.T) {
	t.Parallel()
	inv := configreload.Inventory()
	if len(inv) == 0 {
		t.Fatal("inventory must not be empty")
	}
	seen := map[string]configreload.FieldClass{}
	for _, e := range inv {
		if e.Path == "" {
			t.Fatal("inventory entry missing path")
		}
		if !e.Disposition.Valid() {
			t.Fatalf("%s: invalid disposition %q (no implicit default)", e.Path, e.Disposition)
		}
		if _, dup := seen[e.Path]; dup {
			t.Fatalf("duplicate inventory path %q", e.Path)
		}
		seen[e.Path] = e
	}
	for _, path := range configreload.RequiredTopLevelPaths() {
		e, ok := seen[path]
		if !ok {
			t.Fatalf("missing top-level classification for %q", path)
		}
		if e.Disposition == "" {
			t.Fatalf("%s: empty disposition", path)
		}
	}
	for _, path := range configreload.RequiredStartupOverridePaths() {
		e, ok := seen[path]
		if !ok {
			t.Fatalf("missing startup override classification for %q", path)
		}
		if e.Disposition != configreload.DispositionStartupOnly {
			t.Fatalf("%s: startup overrides must be startup_only, got %q", path, e.Disposition)
		}
	}
}

func TestReloadabilityInventory_SecretBearingMarkedExplicitly(t *testing.T) {
	t.Parallel()
	wantSecret := map[string]bool{
		"auth":           true,
		"diagnostics":    true,
		"continuity":     true,
		"secure_session": true,
		"accounting":     true,
		"control_plane":  true,
		"metering":       true,
		"plugins":        true,
	}
	for _, e := range configreload.Inventory() {
		if want, ok := wantSecret[e.Path]; ok && e.SecretBearing != want {
			t.Fatalf("%s: SecretBearing=%v, want %v", e.Path, e.SecretBearing, want)
		}
	}
}

func TestReloadabilityClassify_StartupOnlyChange_RestartRequired(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.Access.Mode = "multi_user"

	changes, err := configreload.Classify(active, candidate)
	if err == nil {
		t.Fatalf("expected restart_required, got changes=%v", changes)
	}
	var rr *configreload.RestartRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("want RestartRequiredError, got %T %v", err, err)
	}
	assertSortedBoundedPaths(t, rr)
	if !containsPath(rr.RestartRequiredFields, "access") {
		t.Fatalf("want access in restart_required_fields, got %v", rr.RestartRequiredFields)
	}
	if rr.TotalBlocked < 1 {
		t.Fatalf("TotalBlocked=%d", rr.TotalBlocked)
	}
}

func TestReloadabilityClassify_ReloadableChange_OK(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.Routing.DefaultRoute = "stub:other"
	candidate.ModelAliases = []config.ModelAliasConfig{{Pattern: `^x$`, Replacement: "stub:x"}}

	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatalf("reloadable-only candidate must succeed: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("expected safe reloadable changes")
	}
	for _, c := range changes {
		if c.Disposition != configreload.ChangeReloadable {
			t.Fatalf("unexpected disposition %#v", c)
		}
		if c.Path == "" {
			t.Fatal("empty change path")
		}
	}
}

func TestReloadabilityClassify_HTTPHeaders_Reloadable(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.HTTPHeaders.Route = []string{"X-Custom-Route"}

	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatalf("header alias change must be reloadable: %v", err)
	}
	if !containsChange(changes, "http_headers.route", configreload.ChangeReloadable) {
		t.Fatalf("want http_headers.route reloadable, got %#v", changes)
	}
}

func TestReloadabilityClassify_KeepaliveInterval_Reloadable(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.StreamRecovery.AutoResume.KeepaliveInterval = "5s"

	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatalf("keepalive interval change must be reloadable: %v", err)
	}
	if !containsChange(changes, "stream_recovery.auto_resume.keepalive_interval", configreload.ChangeReloadable) {
		t.Fatalf("want keepalive_interval reloadable, got %#v", changes)
	}
}

// TestReloadabilityClassify_EmptyVisibleTurnRetryGate_Reloadable proves the
// generation-owned corrective retry gate joins the classified reloadable
// sections: enabling it in a candidate is a safe generation reload, never a
// restart, and in-flight pinning stays the executor generation's own behavior.
func TestReloadabilityClassify_EmptyVisibleTurnRetryGate_Reloadable(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.EmptyVisibleTurnRetry.Enabled = true

	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatalf("empty-visible-turn retry gate change must be reloadable: %v", err)
	}
	if !containsChange(changes, "empty_visible_turn_retry.enabled", configreload.ChangeReloadable) {
		t.Fatalf("want empty_visible_turn_retry.enabled reloadable, got %#v", changes)
	}
}

func TestReloadabilityClassify_ShutdownTimeout_RestartRequired(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.Server.ShutdownTimeout = "30s"

	_, err := configreload.Classify(active, candidate)
	var rr *configreload.RestartRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("shutdown_timeout change must be restart_required, got %v", err)
	}
	if !containsPath(rr.RestartRequiredFields, "server.shutdown_timeout") {
		t.Fatalf("want server.shutdown_timeout, got %v", rr.RestartRequiredFields)
	}
}

func TestReloadabilityClassify_ConditionalTopologyChange_RestartRequired(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.ModelCatalog.CachePath = "/tmp/other-catalog-cache"

	_, err := configreload.Classify(active, candidate)
	var rr *configreload.RestartRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("topology change must be restart_required, got %v", err)
	}
	if !containsPath(rr.RestartRequiredFields, "model_catalog.cache_path") {
		t.Fatalf("want model_catalog.cache_path, got %v", rr.RestartRequiredFields)
	}
}

func TestReloadabilityClassify_ConditionalPolicyOnly_Reloadable(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.ModelCatalog.ModelOverrides = []config.ModelCatalogModelOverrideEntry{
		{Model: "m1"},
	}

	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatalf("policy-only model_catalog change must be reloadable: %v", err)
	}
	if !containsChange(changes, "model_catalog.model_overrides", configreload.ChangeReloadable) {
		t.Fatalf("want model_catalog.model_overrides reloadable, got %v", changes)
	}
}

func TestReloadabilityClassify_MixedCandidate_RejectsTransaction(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.Access.Mode = "multi_user"
	candidate.Routing.DefaultRoute = "stub:new"

	changes, err := configreload.Classify(active, candidate)
	if err == nil {
		t.Fatalf("mixed candidate must reject before construction, got changes=%v", changes)
	}
	var rr *configreload.RestartRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("want RestartRequiredError, got %T %v", err, err)
	}
	if containsPath(rr.RestartRequiredFields, "routing") || containsPath(rr.RestartRequiredFields, "routing.default_route") {
		t.Fatalf("restart_required_fields must not include reloadable-only paths; got %v", rr.RestartRequiredFields)
	}
	if !containsPath(rr.RestartRequiredFields, "access") {
		t.Fatalf("want access blocked, got %v", rr.RestartRequiredFields)
	}
	// Transactional: no partial apply — Classify returns only the error, never mixed success+error.
	if changes != nil {
		t.Fatalf("mixed reject must not return SafeChange slice, got %v", changes)
	}
}

func TestReloadabilityClassify_MixedSection_AuthHandlerVsKeys(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.Auth.Handler = "external"
	candidate.Auth.LocalAPIKeys = []config.AuthLocalAPIKeyRecord{{
		KeyID: "k1", PrincipalID: "p1", Key: "super-secret-value-xyz-16",
	}}

	_, err := configreload.Classify(active, candidate)
	var rr *configreload.RestartRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("auth.handler change must restart_required, got %v", err)
	}
	if !containsPath(rr.RestartRequiredFields, "auth.handler") {
		t.Fatalf("want auth.handler, got %v", rr.RestartRequiredFields)
	}
	msg := rr.Error()
	if strings.Contains(msg, "super-secret-value-xyz-16") {
		t.Fatalf("restart error leaked secret value: %q", msg)
	}
	for _, p := range rr.RestartRequiredFields {
		if strings.Contains(p, "super-secret") {
			t.Fatalf("path leaked secret: %q", p)
		}
	}
}

func TestReloadabilityClassify_SecretBearingKeysOnly_ReloadableNoValues(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	active.Auth.Handler = "local_api_key"
	candidate := baseConfig()
	candidate.Auth.Handler = "local_api_key"
	candidate.Auth.LocalAPIKeys = []config.AuthLocalAPIKeyRecord{{
		KeyID: "k2", PrincipalID: "p2", Key: "another-secret-value-abc",
	}}

	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatalf("local key rotation within fixed handler must be reloadable: %v", err)
	}
	if !containsChange(changes, "auth.local_api_keys", configreload.ChangeReloadable) {
		t.Fatalf("want auth.local_api_keys reloadable, got %v", changes)
	}
	for _, c := range changes {
		if strings.Contains(c.Path, "another-secret") {
			t.Fatalf("change path leaked secret: %#v", c)
		}
	}
}

func TestReloadabilityClassify_Noop(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if changes != nil {
		t.Fatalf("noop must return nil changes, got %v", changes)
	}
}

func TestRestartRequiredFields_DeterministicSortedBounded(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.Access.Mode = "multi_user"
	candidate.Logging.Format = "text"
	candidate.Observability.Metrics.Enabled = true
	candidate.Database.MaxOpenConns = 9

	_, err := configreload.Classify(active, candidate)
	var rr *configreload.RestartRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("want RestartRequiredError, got %v", err)
	}
	assertSortedBoundedPaths(t, rr)
	if rr.TotalBlocked < len(rr.RestartRequiredFields) {
		t.Fatalf("TotalBlocked=%d len(fields)=%d", rr.TotalBlocked, len(rr.RestartRequiredFields))
	}
	// Second call identical ordering.
	_, err2 := configreload.Classify(active, candidate)
	var rr2 *configreload.RestartRequiredError
	if !errors.As(err2, &rr2) {
		t.Fatal(err2)
	}
	if strings.Join(rr.RestartRequiredFields, ",") != strings.Join(rr2.RestartRequiredFields, ",") {
		t.Fatalf("non-deterministic fields:\n1: %v\n2: %v", rr.RestartRequiredFields, rr2.RestartRequiredFields)
	}
}

func TestReloadabilityClassify_NilConfigs(t *testing.T) {
	t.Parallel()
	if _, err := configreload.Classify(nil, baseConfig()); err == nil {
		t.Fatal("nil active must error")
	}
	if _, err := configreload.Classify(baseConfig(), nil); err == nil {
		t.Fatal("nil candidate must error")
	}
}

func TestFieldCoverage_EveryTopLevelHasTypedComparator(t *testing.T) {
	t.Parallel()
	covered := map[string]bool{}
	for _, path := range configreload.TypedComparatorSections() {
		covered[path] = true
	}
	var missing []string
	for _, path := range configreload.RequiredTopLevelPaths() {
		if !covered[path] {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("top-level sections without typed comparators: %v", missing)
	}
	for _, path := range configreload.RequiredStartupOverridePaths() {
		if !covered[path] {
			t.Fatalf("startup override without typed comparator coverage: %q", path)
		}
	}
}

func TestClassifyEffective_UsesConfigProjection(t *testing.T) {
	t.Parallel()
	active := &config.EffectiveConfig{Config: baseConfig()}
	candidate := &config.EffectiveConfig{Config: baseConfig()}
	candidate.Config.Routing.DefaultRoute = "stub:other"

	changes, err := configreload.ClassifyEffective(active, candidate)
	if err != nil {
		t.Fatalf("ClassifyEffective: %v", err)
	}
	if !containsChange(changes, "routing.default_route", configreload.ChangeReloadable) {
		t.Fatalf("want routing.default_route reloadable, got %v", changes)
	}

	candidate.Config.Access.Mode = "multi_user"
	_, err = configreload.ClassifyEffective(active, candidate)
	var rr *configreload.RestartRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("want RestartRequiredError, got %v", err)
	}
	if !containsPath(rr.RestartRequiredFields, "access") {
		t.Fatalf("want access blocked, got %v", rr.RestartRequiredFields)
	}
}

func TestClassifyEffective_NilEffective(t *testing.T) {
	t.Parallel()
	if _, err := configreload.ClassifyEffective(nil, &config.EffectiveConfig{Config: baseConfig()}); err == nil {
		t.Fatal("nil active EffectiveConfig must error")
	}
	if _, err := configreload.ClassifyEffective(&config.EffectiveConfig{Config: baseConfig()}, nil); err == nil {
		t.Fatal("nil candidate EffectiveConfig must error")
	}
}

func TestReloadabilityClassify_PluginsRows_Reloadable(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.Plugins.Backends = []config.PluginConfig{{
		ID: "stub", Kind: "echo", Enabled: true,
	}}
	changes, err := configreload.Classify(active, candidate)
	if err != nil {
		t.Fatalf("plugin row add must be reloadable: %v", err)
	}
	if !containsChange(changes, "plugins.backends", configreload.ChangeReloadable) {
		t.Fatalf("want plugins.backends reloadable, got %v", changes)
	}
}

func TestReloadabilityClassify_LoggingNested_RestartRequired(t *testing.T) {
	t.Parallel()
	active := baseConfig()
	candidate := baseConfig()
	candidate.Logging.Level = "debug"
	_, err := configreload.Classify(active, candidate)
	var rr *configreload.RestartRequiredError
	if !errors.As(err, &rr) {
		t.Fatalf("logging change must restart_required, got %v", err)
	}
	if !containsPath(rr.RestartRequiredFields, "logging.level") && !containsPath(rr.RestartRequiredFields, "logging") {
		t.Fatalf("want logging path blocked, got %v", rr.RestartRequiredFields)
	}
}

func baseConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{Address: "127.0.0.1:8080"},
		Access: config.AccessConfig{Mode: "single_user"},
		Auth:   config.AuthConfig{Handler: "none"},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Routing: config.RoutingConfig{
			DefaultRoute: "stub:default",
			MaxAttempts:  1,
		},
		ModelCatalog: config.ModelCatalogConfig{
			CachePath: "/tmp/catalog-cache",
		},
	}
}

func assertSortedBoundedPaths(t *testing.T, rr *configreload.RestartRequiredError) {
	t.Helper()
	if rr == nil {
		t.Fatal("nil RestartRequiredError")
	}
	if len(rr.RestartRequiredFields) > configreload.MaxRestartRequiredFields {
		t.Fatalf("fields exceed bound %d: %d", configreload.MaxRestartRequiredFields, len(rr.RestartRequiredFields))
	}
	for i := 1; i < len(rr.RestartRequiredFields); i++ {
		if rr.RestartRequiredFields[i-1] >= rr.RestartRequiredFields[i] {
			t.Fatalf("fields not strictly sorted: %v", rr.RestartRequiredFields)
		}
	}
	for _, p := range rr.RestartRequiredFields {
		if p == "" || strings.Contains(p, "=") || strings.Contains(p, ": ") {
			t.Fatalf("field path looks value-bearing: %q", p)
		}
	}
}

func containsPath(paths []string, want string) bool {
	return slices.Contains(paths, want)
}

func containsChange(changes []configreload.SafeChange, path string, d configreload.ChangeDisposition) bool {
	for _, c := range changes {
		if c.Path == path && c.Disposition == d {
			return true
		}
	}
	return false
}
