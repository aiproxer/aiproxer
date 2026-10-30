package runtime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	featuresecretguard "github.com/aiproxer/aiproxer/internal/plugins/features/secretguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	secretguardhost "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	observabilityExactCanary       = "sk-canary-observability-exact-001"          // #nosec G101 -- synthetic fixture only.
	observabilityBetterLeaksCanary = "ghp_Z9yX7wV5uT3sR1qP8nM6kJ4hG2fD0cB8aF6Q"   // #nosec G101 -- synthetic fixture only.
	observabilityOverlapCanary     = "ghp_Y8xW6vU4tS2rQ0pO7nL5jI3gF1eD9cA7bE5R"   // #nosec G101 -- synthetic fixture only.
	observabilityDecodedCanary     = "ghp_X7wV5uT3sR1qP8nM6kJ4hG2fD0cB8aF6Q9Y8Z0" // #nosec G101 -- synthetic fixture only.
	observabilityRedactionCanary   = "ghp_W6vU4tS2rQ0pO7nL5jI3gF1eD9cA7bE5R4Z9A1" // #nosec G101 -- synthetic fixture only.
	observabilityScannerCanary     = "scanner-canary-observability-005"
	observabilityFingerprintCanary = "private-fingerprint-observability-006"
	observabilityContextCanary     = "private-source-context-observability-007"
)

type observabilityCanaryEnv map[string]string

func (e observabilityCanaryEnv) Lookup(name string) (string, bool) {
	v, ok := e[name]
	return v, ok
}

func (e observabilityCanaryEnv) Snapshot() []string {
	out := make([]string, 0, len(e))
	for key, value := range e {
		out = append(out, key+"="+value)
	}
	return out
}

type canaryObservabilitySinks struct {
	logBuffer bytes.Buffer
	logger    *slog.Logger
	observer  sdk.Observer
	metrics   extensions.SecretGuardDecisionMetrics
	registry  *prometheus.Registry
	audits    []sdk.DecisionEvent
	evidence  []policydecision.Record
	emitter   *extensions.EvidenceEmitter
}

type capturingPolicyObserver struct {
	sinks *canaryObservabilitySinks
}

func (o *capturingPolicyObserver) OnPolicyDecision(_ context.Context, record policydecision.Record) error {
	o.sinks.evidence = append(o.sinks.evidence, record)
	return nil
}

type capturingGuard struct {
	delegate  sdk.Guard
	decision  sdk.Decision
	evaluated bool
	err       error
}

func (g *capturingGuard) ID() string                   { return g.delegate.ID() }
func (g *capturingGuard) Order() int                   { return g.delegate.Order() }
func (g *capturingGuard) FailureMode() sdk.FailureMode { return g.delegate.FailureMode() }
func (g *capturingGuard) Evaluate(ctx context.Context, call *aipapi.Call, meta sdk.Meta, services sdk.Services) (sdk.Decision, error) {
	g.decision, g.err = g.delegate.Evaluate(ctx, call, meta, services)
	g.evaluated = true
	return g.decision, g.err
}

// TestSecretGuardObservabilityCanaries_ComposedPaths exercises the real feature
// guard and stage runner for exact, BetterLeaks, overlap, decoded, and
// redaction-failure paths. Every representation is checked through its actual
// sink, while assertion failures report only fixed indexes and counts.
func TestSecretGuardObservabilityCanaries_ComposedPaths(t *testing.T) {
	t.Parallel()

	sinks := newCanaryObservabilitySinks(t)
	cases := []struct {
		name             string
		secret           string
		action           string
		exact            bool
		better           bool
		call             aipapi.Call
		want             sdk.Outcome
		wantFail         string
		wantFindings     bool
		wantFindingCount int
	}{
		{
			name:         "exact",
			secret:       observabilityExactCanary,
			action:       featuresecretguard.ActionBlock,
			exact:        true,
			call:         canaryTextCall("context=" + observabilityContextCanary + " OPENAI_API_KEY=" + observabilityExactCanary),
			want:         sdk.OutcomeBlock,
			wantFindings: true,
		},
		{
			name:         "betterleaks",
			secret:       observabilityBetterLeaksCanary,
			action:       featuresecretguard.ActionLog,
			better:       true,
			call:         canaryTextCall("context=" + observabilityContextCanary + " GITHUB_TOKEN=" + observabilityBetterLeaksCanary),
			want:         sdk.OutcomeLog,
			wantFindings: true,
		},
		{
			name:             "overlap",
			secret:           observabilityOverlapCanary,
			action:           featuresecretguard.ActionBlock,
			exact:            true,
			better:           true,
			call:             canaryTextCall("context=" + observabilityContextCanary + " GITHUB_TOKEN=" + observabilityOverlapCanary),
			want:             sdk.OutcomeBlock,
			wantFindings:     true,
			wantFindingCount: 1,
		},
		{
			name:         "decoded",
			secret:       observabilityDecodedCanary,
			action:       featuresecretguard.ActionLog,
			better:       true,
			call:         canaryJSONCall([]byte(`{"context":"` + observabilityContextCanary + `","credential":"` + base64.StdEncoding.EncodeToString([]byte(observabilityDecodedCanary)) + `"}`)),
			want:         sdk.OutcomeLog,
			wantFindings: true,
		},
		{
			name:         "redaction_failure",
			secret:       observabilityRedactionCanary,
			action:       featuresecretguard.ActionRedact,
			better:       true,
			call:         canaryJSONCall([]byte(`{"context":"` + observabilityContextCanary + `","` + observabilityRedactionCanary + `":"safe"}`)),
			want:         sdk.OutcomeBlock,
			wantFail:     "unsupported_json_token",
			wantFindings: true,
		},
	}

	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard, services := newCanaryGuard(t, tc.action, tc.exact, tc.better, tc.secret)
			generation, ok := services.Capability.(*featuresecretguard.GenerationServices)
			if !ok || generation == nil {
				t.Fatalf("canary generation capability missing: case=%d", index)
			}
			diagnostic := canaryDiagnosticJSON(t, generation)
			if !bytes.Contains(diagnostic, []byte(`"secret_guard"`)) ||
				!bytes.Contains(diagnostic, fmt.Appendf(nil, `"betterleaks_enabled":%t`, tc.better)) {
				t.Fatalf("canary detector posture mismatch: case=%d", index)
			}
			sinks.add("diagnostics-"+tc.name, diagnostic)
			call := tc.call
			before := aipapi.CloneCall(call)
			meta := sdk.Meta{
				TraceID:     "trace-canary-" + tc.name,
				Fingerprint: observabilityFingerprintCanary,
			}
			audit := &extensions.SecretGuardAudit{
				Observer:      sinks.observer,
				AccessMode:    "single_user",
				ConfigVersion: "cfg-canary",
				TurnID:        "turn-canary-" + tc.name,
				Now:           func() time.Time { return time.Unix(2700, 0).UTC() },
			}

			stageCtx := extensions.WithDecisionEvidence(t.Context(), &extensions.DecisionEvidence{
				Emitter: sinks.emitter,
				Views: execctx.Views{Annotations: map[string]string{
					"source_category": "single_user",
				}},
			})
			block, stageErr := extensions.RunSecretGuardStage(
				stageCtx, sinks.logger, nil, []sdk.Guard{guard}, &call, meta, services, audit, sinks.metrics,
			)
			if stageErr != nil {
				t.Fatalf("canary stage failed: case=%d", index)
			}
			if !guard.evaluated {
				t.Fatalf("canary guard was not evaluated: case=%d", index)
			}
			decision := guard.decision
			if block != nil {
				if !reflect.DeepEqual(block.Decision, decision) {
					t.Fatalf("canary block decision capture mismatch: case=%d", index)
				}
				if err := extensions.EmitSecretGuardAudit(t.Context(), audit, meta, &call, block.GuardID, decision, sdk.QuarantineResultSkipped, false); err != nil {
					t.Fatalf("canary audit failed: case=%d", index)
				}
			} else {
				if len(sinks.audits) < index+1 {
					t.Fatalf("canary audit missing: case=%d", index)
				}
			}
			if decision.Outcome != tc.want {
				t.Fatalf("canary outcome mismatch: case=%d", index)
			}
			if tc.wantFindings && len(decision.Findings) == 0 {
				t.Fatalf("canary findings missing: case=%d outcome=%q failure=%q scan_limit=%t", index, decision.Outcome, decision.FailureKind, decision.ScanLimitHit)
			}
			if tc.wantFindingCount > 0 && len(decision.Findings) != tc.wantFindingCount {
				t.Fatalf("canary finding count mismatch: case=%d count=%d", index, len(decision.Findings))
			}
			if tc.name == "overlap" {
				finding := decision.Findings[0]
				if finding.DetectorID != sdk.DetectorIDExact || finding.RuleID == "" || finding.OccurrenceCount != 1 {
					t.Fatalf("canary overlap provenance mismatch: case=%d", index)
				}
			}
			if tc.wantFail != "" && decision.FailureKind != tc.wantFail {
				t.Fatalf("canary failure classification mismatch: case=%d", index)
			}
			if err := decision.Validate(); err != nil {
				t.Fatalf("canary decision invalid: case=%d", index)
			}
			if block != nil {
				denial := block.DenialError()
				if denial == nil {
					t.Fatalf("canary denial error missing: case=%d", index)
				}
				sinks.add("denial-error", fmt.Append(nil, denial))
				sinks.add("denial-error-json", mustJSON(t, denial))
			}
			if raw, err := json.Marshal(decision); err != nil {
				t.Fatalf("canary decision marshal failed: case=%d", index)
			} else {
				sinks.add("decision", raw)
			}
			if tc.action == featuresecretguard.ActionLog && !reflectCallEqual(call, before) {
				t.Fatalf("canary log path mutated call: case=%d", index)
			}
		})
	}

	if len(sinks.audits) != len(cases) {
		t.Fatalf("canary audit count mismatch: got=%d want=%d", len(sinks.audits), len(cases))
	}
	sinks.add("audit", mustJSON(t, sinks.audits))
	sinks.add("policy-evidence", mustJSON(t, sinks.evidence))
	if len(sinks.evidence) != len(cases) {
		t.Fatalf("canary policy evidence count mismatch: got=%d want=%d", len(sinks.evidence), len(cases))
	}
	metricsText := canaryMetricsText(t, sinks.registry)
	sinks.add("metrics", []byte(metricsText))
	assertCanaryMetricLabels(t, metricsText)
	sinks.assertNoForbidden(t, true)
	assertPositiveForbiddenControls(t)
	if !strings.Contains(sinks.logBuffer.String(), "aip.secret_guard.decision") {
		t.Fatal("canary ordinary log sink count=0")
	}
}

// TestSecretGuardObservabilityCanary_ScannerFailureAndOrdinaryError uses a
// test-only guard to inject an upstream-like scanner error. This is the one
// private failure injection permitted by the task; the stage must collapse it
// to the ordinary safe policy error before any sink observes it.
func TestSecretGuardObservabilityCanary_ScannerFailureAndOrdinaryError(t *testing.T) {
	t.Parallel()
	sinks := newCanaryObservabilitySinks(t)
	guard := &capturingGuard{delegate: canaryScannerFailureGuard{}}
	call := canaryTextCall("context=" + observabilityContextCanary + " payload=" + observabilityScannerCanary)
	stageCtx := extensions.WithDecisionEvidence(t.Context(), &extensions.DecisionEvidence{
		Emitter: sinks.emitter,
		Views: execctx.Views{Annotations: map[string]string{
			"source_category": "single_user",
		}},
	})
	_, err := extensions.RunSecretGuardStage(
		stageCtx, sinks.logger, nil, []sdk.Guard{guard}, &call,
		sdk.Meta{TraceID: "trace-scanner-error", Fingerprint: observabilityFingerprintCanary},
		sdk.Services{},
		&extensions.SecretGuardAudit{Observer: sinks.observer, AccessMode: "single_user", ConfigVersion: "cfg-canary", TurnID: "turn-scanner-error"},
		sinks.metrics,
	)
	if err == nil {
		t.Fatal("scanner failure unexpectedly succeeded")
	}
	formatted := fmt.Sprint(err)
	sinks.add("ordinary-error", []byte(formatted))
	sinks.add("ordinary-error-json", mustJSON(t, map[string]string{"error": formatted}))
	metricsText := canaryMetricsText(t, sinks.registry)
	sinks.add("metrics", []byte(metricsText))
	sinks.add("policy-evidence", mustJSON(t, sinks.evidence))
	assertCanaryMetricLabels(t, metricsText)
	sinks.assertNoForbidden(t, false)
	if len(sinks.audits) != 0 || strings.Contains(sinks.logBuffer.String(), "aip.secret_guard.decision") {
		// Scanner failure is fail-closed before a secret decision exists, so the
		// production audit and secret-decision log paths are naturally absent;
		// the actual policy failure evidence record is the observable failure sink.
		t.Fatal("scanner failure emitted a decision audit or secret-decision log")
	}
	if len(sinks.evidence) != 1 {
		t.Fatalf("scanner failure policy evidence count mismatch: got=%d", len(sinks.evidence))
	}
	if !guard.evaluated || guard.err == nil {
		t.Fatal("scanner failure guard capture missing")
	}
	assertPositiveForbiddenControls(t)
	if !aipapi.IsPolicyFailure(err) {
		t.Fatal("scanner failure did not become a policy failure")
	}
}

type canaryScannerFailureGuard struct{}

func (canaryScannerFailureGuard) ID() string                   { return "scanner-canary" }
func (canaryScannerFailureGuard) Order() int                   { return 0 }
func (canaryScannerFailureGuard) FailureMode() sdk.FailureMode { return sdk.FailClosed }
func (canaryScannerFailureGuard) Evaluate(context.Context, *aipapi.Call, sdk.Meta, sdk.Services) (sdk.Decision, error) {
	return sdk.Decision{}, fmt.Errorf("scanner upstream failed: %s fingerprint=%s context=%s", observabilityScannerCanary, observabilityFingerprintCanary, observabilityContextCanary)
}

func newCanaryGuard(t *testing.T, action string, exact, better bool, secret string) (*capturingGuard, sdk.Services) {
	t.Helper()
	services, _ := newCanaryGeneration(t, exact, better, observabilityCanaryEnv{"OPENAI_API_KEY": secret})
	return &capturingGuard{delegate: featuresecretguard.NewGuard(featuresecretguard.Config{Action: action})}, sdk.Services{
		MatcherResolver: services.MatcherResolver(),
		Capability:      services,
	}
}

func newCanaryGeneration(t *testing.T, exact, better bool, env observabilityCanaryEnv) (*featuresecretguard.GenerationServices, engine.Source) {
	t.Helper()
	source := engine.NewDisabledSource()
	if exact {
		var err error
		source, err = engine.NewSingleUserSource(env, engine.SingleUserOptions{})
		if err != nil {
			t.Fatal("canary exact source failed")
		}
	}
	services, err := featuresecretguard.BuildGenerationServices(featuresecretguard.DetectorPolicy{
		BetterLeaks: featuresecretguard.BetterLeaksPolicy{
			Enabled:           better,
			MinimumConfidence: featuresecretguard.DefaultBetterLeaksConfidence,
			MaxDecodeDepth:    featuresecretguard.DefaultBetterLeaksDecodeDepth,
			Workers:           1,
			MaxFindings:       featuresecretguard.DefaultBetterLeaksMaxFindings,
		},
	}, source)
	if err != nil {
		t.Fatal("canary generation services failed")
	}
	return services, source
}

func newCanaryObservabilitySinks(t *testing.T) *canaryObservabilitySinks {
	t.Helper()
	s := &canaryObservabilitySinks{}
	s.logger = slog.New(slog.NewJSONHandler(&s.logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logObserver, err := secretguardhost.NewSlogObserver(s.logger)
	if err != nil {
		t.Fatal("canary log observer failed")
	}
	s.observer = sdk.ObserverFunc(func(_ context.Context, event sdk.DecisionEvent) error {
		s.audits = append(s.audits, event)
		raw, err := json.Marshal(event)
		if err != nil {
			return err
		}
		s.add("decision-event", raw)
		return logObserver.OnSecretDecision(t.Context(), event)
	})
	promRegistry := metrics.NewRegistry()
	prom := metrics.RegisterSecretGuardProm(promRegistry)
	s.metrics = metrics.NewSecretGuardDecisionSink(prom)
	s.registry = promRegistry
	s.emitter = extensions.NewEvidenceEmitter(&capturingPolicyObserver{sinks: s}, s.logger, false)
	return s
}

func (s *canaryObservabilitySinks) add(_ string, data []byte) {
	// Keep one canonical sink surface for checks without retaining payloads in
	// failure messages. The ordinary log buffer and audit slice remain separate
	// so the test also proves each sink was actually exercised.
	if len(data) == 0 {
		return
	}
	s.logBuffer.Write(data)
}

func (s *canaryObservabilitySinks) assertNoForbidden(t *testing.T, requireAudit bool) {
	t.Helper()
	combined := s.logBuffer.String()
	if index, ok := forbiddenObservabilityMaterial(combined); ok {
		t.Fatalf("anti-secret canary failure: forbidden=%d bytes=%d", index, len(combined))
	}
	if requireAudit && len(s.audits) == 0 {
		t.Fatal("anti-secret canary failure: audit sink count=0")
	}
}

func forbiddenObservabilityMaterial(text string) (int, bool) {
	for i, forbidden := range observabilityForbiddenValues() {
		if strings.Contains(text, forbidden) {
			return i, true
		}
	}
	return 0, false
}

func observabilityForbiddenValues() []string {
	return []string{
		observabilityExactCanary,
		observabilityBetterLeaksCanary,
		observabilityOverlapCanary,
		observabilityDecodedCanary,
		observabilityRedactionCanary,
		observabilityScannerCanary,
		observabilityFingerprintCanary,
		observabilityContextCanary,
		base64.StdEncoding.EncodeToString([]byte(observabilityDecodedCanary)),
		betterLeaksFingerprint(observabilityBetterLeaksCanary),
		betterLeaksFingerprint(observabilityOverlapCanary),
		betterLeaksFingerprint(observabilityDecodedCanary),
		betterLeaksFingerprint(observabilityRedactionCanary),
	}
}

// betterLeaksFingerprint follows the pinned BetterLeaks v2 fingerprint contract:
// an unkeyed SHA-256 of the exact Match.Value bytes, formatted as lowercase hex.
// This test-only calculation keeps the upstream fingerprint out of runtime
// contracts while ensuring observability rejects the actual derived identifier.
func betterLeaksFingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func assertPositiveForbiddenControls(t *testing.T) {
	t.Helper()
	for index, material := range observabilityForbiddenValues() {
		if _, ok := forbiddenObservabilityMaterial(material); !ok {
			t.Fatalf("anti-secret positive control failed: index=%d", index)
		}
	}
}

func canaryDiagnosticJSON(t *testing.T, services *featuresecretguard.GenerationServices) []byte {
	t.Helper()
	posture := services.Posture()
	facts := posture.BetterLeaksFacts
	cfg := &config.Config{Plugins: config.PluginsConfig{Features: []config.PluginConfig{{ID: "secrets-guard", Kind: "secrets-guard", Enabled: true}}}}
	// The compiled inventory read model (including the secret-guard instance
	// association) is computed by the featurehost authority in production; the
	// canary supplies the same finished row and checks the diagnostics JSON.
	row := &diag.InventorySecretGuard{
		InstanceID:             "secrets-guard",
		Action:                 featuresecretguard.ActionBlock,
		AccessMode:             "single_user",
		BetterLeaksEnabled:     posture.BetterLeaksEnabled,
		BetterLeaksVersion:     facts.Version,
		BetterLeaksConfigHash:  facts.ConfigHash,
		BetterLeaksConfidence:  facts.MinimumConfidence,
		BetterLeaksWorkers:     facts.Workers,
		DiscoveryDetectorCount: posture.DiscoveryDetectorCount,
	}
	if posture.BetterLeaksEnabled {
		ruleCount, decodeDepth := facts.ActiveRuleCount, facts.MaxDecodeDepth
		row.BetterLeaksRuleCount, row.BetterLeaksDecodeDepth = &ruleCount, &decodeDepth
	}
	snapshot, err := diag.InventorySnapshotForConfig(t.Context(), cfg, &diag.InventoryExtras{
		FeatureMetadata: []diag.InventoryFeatureMetadata{{
			InstanceID:     "secrets-guard",
			StageOccupancy: []diag.InventoryStageOccupancy{},
			SecretGuard:    row,
		}},
	})
	if err != nil {
		t.Fatal("canary diagnostic snapshot failed")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal("canary diagnostic marshal failed")
	}
	return raw
}

func canaryMetricsText(t *testing.T, registry *prometheus.Registry) string {
	t.Helper()
	response := httptest.NewRecorder()
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if response.Code != 200 {
		t.Fatal("canary metrics endpoint failed")
	}
	return response.Body.String()
}

func canaryTextCall(text string) aipapi.Call {
	return aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart(text)}}}}
}

func canaryJSONCall(raw []byte) aipapi.Call {
	return aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: append([]byte(nil), raw...)}}}}}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal("canary JSON helper failed")
	}
	return raw
}

func reflectCallEqual(a, b aipapi.Call) bool {
	return reflect.DeepEqual(a, b)
}

func assertCanaryMetricLabels(t *testing.T, text string) {
	t.Helper()
	allowed := map[string]bool{"action": true, "outcome": true, "source_category": true}
	found := false
	for line := range strings.SplitSeq(text, "\n") {
		if !strings.HasPrefix(line, "aip_secret_guard_") {
			continue
		}
		found = true
		open := strings.IndexByte(line, '{')
		close := strings.IndexByte(line, '}')
		if open < 0 || close < open {
			continue
		}
		for label := range strings.SplitSeq(line[open+1:close], ",") {
			name, _, ok := strings.Cut(label, "=")
			if !ok || !allowed[strings.TrimSpace(name)] {
				t.Fatalf("anti-secret metric label failure: line=%d", len(line))
			}
		}
	}
	if !found {
		t.Fatal("anti-secret metric sink count=0")
	}
}
