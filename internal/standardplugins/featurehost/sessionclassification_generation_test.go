package featurehost_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	featureclassification "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkclassification "github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// fixedClassificationClassifier is an independent second effective classifier
// contributor used to prove that a duplicate classifier cannot be published.
type fixedClassificationClassifier struct{ id string }

func (c fixedClassificationClassifier) ID() string { return c.id }

func (fixedClassificationClassifier) Classify(context.Context, sdkclassification.Input) (session.Classification, error) {
	return session.Classification{}, nil
}

func newClassificationGenerationRuntime(t *testing.T) *featurehost.Runtime {
	t.Helper()
	ctx := context.Background()
	rt, err := featurehost.NewProcess(ctx, featurehost.ProcessInput{Logger: slog.Default()})
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	t.Cleanup(func() {
		if err := rt.Close(); err != nil {
			t.Errorf("Runtime.Close: %v", err)
		}
	})
	return rt
}

func publishedClassifier(t *testing.T, planes aipfeature.FrozenPlaneSet) sdkclassification.Classifier {
	t.Helper()
	return aipfeature.Get(planes, aipfeature.PlaneSessionClassifier)
}

func classificationPrefixList(count int) string {
	prefixes := make([]string, 0, count)
	for i := range count {
		prefixes = append(prefixes, `"agent-`+string(rune('a'+i%26))+`-"`)
	}
	return "heuristic:\n  ignored_user_agent_prefixes: [" + strings.Join(prefixes, ", ") + "]\n"
}

const classificationHybridRemoteYAML = "mode: hybrid\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n" +
	"  timeout: 750ms\n  max_attempts_per_session: 1\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 0.90\n"

// classificationJevRemoteYAML is the same validated posture under the mode that
// forbids local promotion entirely (requirement 6.3).
const classificationJevRemoteYAML = "mode: jev\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n" +
	"  timeout: 750ms\n  max_attempts_per_session: 1\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 0.90\n"

// TestCompileGeneration_RemoteCapableModesMakeNoClassifierRequest pins
// requirements 6.1/6.2/6.3/8.3 at the generation boundary: composing a
// remote-capable mode constructs its decider without contacting the service, and
// classifying reaches no external service. The default HTTP transport is observed
// as an auxiliary guard because it can catch accidental use of the shared
// client.
//
// The credential is supplied only for composition: requirement 6.10 will not
// publish a remote-capable generation otherwise. It is removed before every
// classification, so a remote-eligible turn exercises the documented
// post-publication refusal before HTTP rather than the network. That keeps the
// composed suite hermetic while still proving the request survives a refused
// remote attempt.
func TestCompileGeneration_RemoteCapableModesMakeNoClassifierRequest(t *testing.T) {
	// Serial: this case temporarily observes the default HTTP transport and the
	// referenced credential. The credential must resolve during composition,
	// because requirement 6.10 refuses to publish a remote-capable generation
	// without one. It is removed before classification, so publication is tested
	// with a credential and turns are tested without network access.
	t.Setenv("TYPESAFE_API_KEY", "compile-generation-publication-only-token")
	originalTransport := http.DefaultTransport
	transport := &countingClassificationRoundTripper{}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	cases := []struct {
		name        string
		configYAML  string
		userAgent   string
		wantPromote bool
		wantSource  session.ClassificationSource
	}{
		{
			name:        "heuristic mode promotes from local identity only",
			configYAML:  "",
			userAgent:   "codex_cli_rs/1.2.3",
			wantPromote: true,
			wantSource:  session.SourceLocalIdentity,
		},
		{
			name:        "hybrid mode promotes on decisive local evidence",
			configYAML:  classificationHybridRemoteYAML,
			userAgent:   "codex_cli_rs/1.2.3",
			wantPromote: true,
			wantSource:  session.SourceLocalIdentity,
		},
		{
			// Requirement 6.4: a still-unknown hybrid session is remote-eligible, so
			// the only path to a positive is a remote decision. A refused one
			// leaves it unknown rather than promoting locally.
			name:       "hybrid mode keeps local-only-unknown sessions unknown",
			configYAML: classificationHybridRemoteYAML,
			userAgent:  "openai-python/1.40.0",
		},
		{
			// Requirement 6.3: jev mode never promotes from local evidence alone, so
			// even a decisive coding-harness turn stays unknown without a remote
			// positive.
			name:       "jev mode never promotes from local evidence alone",
			configYAML: classificationJevRemoteYAML,
			userAgent:  "codex_cli_rs/1.2.3",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := transport.calls.Load()
			ctx := context.Background()
			rt := newClassificationGenerationRuntime(t)
			out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
				Registrations: []aipsdk.Registration{featureRegistration(t, featureclassification.ID, true, tc.configYAML)},
			})
			if err != nil {
				t.Fatalf("CompileGeneration: %v", err)
			}
			classifier := publishedClassifier(t, out.Planes)
			if classifier == nil {
				t.Fatal("enabled generation published no classifier")
			}
			for _, lifecycle := range out.Lifecycles {
				if err := lifecycle.Start(ctx); err != nil {
					t.Fatalf("generation lifecycle Start: %v", err)
				}
			}
			// Remove the composition-only credential now: a remote-eligible
			// classification must refuse before HTTP and never leave the test.
			t.Setenv("TYPESAFE_API_KEY", "")
			got, err := classifier.Classify(ctx, sdkclassification.Input{
				Session:  session.SessionView{AuthoritativeSessionID: "sess-no-network"},
				Evidence: sdkclassification.Evidence{ClientUserAgent: tc.userAgent},
			})
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got.IsCodingAgent() != tc.wantPromote {
				t.Fatalf("coding_agent = %t, want %t (%+v)", got.IsCodingAgent(), tc.wantPromote, got)
			}
			if tc.wantPromote && got.Source != tc.wantSource {
				t.Fatalf("promotion source = %q, want %q (%+v)", got.Source, tc.wantSource, got)
			}
			if after := transport.calls.Load(); after != before {
				t.Fatalf("classifier issued %d external requests, want none", after-before)
			}
		})
	}
}

type countingClassificationRoundTripper struct{ calls atomic.Int32 }

func (r *countingClassificationRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return nil, errors.New("session classification must not perform an external request")
}

// TestCompileGeneration_EnabledSessionClassificationPublishesExactlyOneClassifierPlane
// pins the enabled generation surface: one exclusive classifier plane carrying
// the standard feature identity, and no process state initialized during
// composition.
func TestCompileGeneration_EnabledSessionClassificationPublishesExactlyOneClassifierPlane(t *testing.T) {
	t.Parallel()

	rt := newClassificationGenerationRuntime(t)
	out, err := rt.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Registrations: []aipsdk.Registration{featureRegistration(t, featureclassification.ID, true, "")},
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	classifier := publishedClassifier(t, out.Planes)
	if classifier == nil {
		t.Fatalf("enabled generation published no %q plane", aipfeature.PlaneSessionClassifier.ID)
	}
	if classifier.ID() != featureclassification.ID {
		t.Fatalf("published classifier identity = %q, want %q", classifier.ID(), featureclassification.ID)
	}
	if got := aipfeature.Get(out.Planes, aipfeature.PlaneSessionClassifier); got == nil {
		t.Fatal("classifier plane is not reachable through ordinary plane access")
	}
}

// TestCompileGeneration_AbsentOrDisabledSessionClassificationPublishesNoClassifierPlane
// pins the feature-flag protocol: without an enabled registration the standard
// distribution publishes no classifier and no classification lifecycle.
func TestCompileGeneration_AbsentOrDisabledSessionClassificationPublishesNoClassifierPlane(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		registrations []aipsdk.Registration
	}{
		{name: "absent registration", registrations: nil},
		{
			name: "disabled registration",
			registrations: []aipsdk.Registration{
				featureRegistration(t, featureclassification.ID, false, "mode: heuristic\n"),
			},
		},
		{
			name: "disabled registration with valid remote config",
			registrations: []aipsdk.Registration{
				featureRegistration(t, featureclassification.ID, false, "mode: hybrid\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n  timeout: 750ms\n  max_attempts_per_session: 1\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 0.90\n"),
			},
		},
		{
			name: "unrelated feature only",
			registrations: []aipsdk.Registration{
				featureRegistration(t, "keepwarm", true, "enabled: true\n"),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			baseline := newClassificationGenerationRuntime(t)
			baselineOut, err := baseline.CompileGeneration(context.Background(), featurehost.GenerationInput{})
			if err != nil {
				t.Fatalf("baseline CompileGeneration: %v", err)
			}
			if publishedClassifier(t, baselineOut.Planes) != nil {
				t.Fatal("the ordinary standard surface already publishes a classifier")
			}

			rt := newClassificationGenerationRuntime(t)
			out, err := rt.CompileGeneration(context.Background(), featurehost.GenerationInput{Registrations: tc.registrations})
			if err != nil {
				t.Fatalf("CompileGeneration: %v", err)
			}
			if got := publishedClassifier(t, out.Planes); got != nil {
				t.Fatalf("classifier plane = %v, want none when the feature is absent or disabled", got)
			}
			if got, want := len(out.Lifecycles), len(baselineOut.Lifecycles); got != want {
				t.Fatalf("lifecycles = %d, want the %d of a generation without classification", got, want)
			}
		})
	}
}

// TestCompileGeneration_InvalidSessionClassificationConfigRejectsCandidateAndKeepsLastGood
// pins requirement 8.4/8.6: invalid mode-specific configuration fails the
// candidate before publication while the last-good generation and the
// process-owned classification state stay untouched.
func TestCompileGeneration_InvalidSessionClassificationConfigRejectsCandidateAndKeepsLastGood(t *testing.T) {
	t.Parallel()

	invalid := []struct {
		name       string
		configYAML string
	}{
		{name: "unknown mode", configYAML: "mode: telepathy\n"},
		{name: "remote settings under heuristic mode", configYAML: "mode: heuristic\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n  timeout: 750ms\n  max_attempts_per_session: 1\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 0.90\n"},
		{name: "jev mode without remote settings", configYAML: "mode: jev\n"},
		{name: "impossible threshold", configYAML: "mode: hybrid\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n  timeout: 750ms\n  max_attempts_per_session: 1\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 1.5\n"},
		{name: "non-positive timeout", configYAML: "mode: jev\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n  timeout: 0s\n  max_attempts_per_session: 1\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 0.9\n"},
		{name: "lease shorter than timeout", configYAML: "mode: jev\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n  timeout: 750ms\n  max_attempts_per_session: 1\n  lease_ttl: 750ms\n  retry_backoff: 0s\n  positive_threshold: 0.9\n"},
		{name: "unbounded attempt budget", configYAML: "mode: jev\nremote:\n  provider: jev\n  api_key_env: TYPESAFE_API_KEY\n  timeout: 750ms\n  max_attempts_per_session: 99\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 0.9\n"},
		{name: "credential embedded instead of referenced", configYAML: "mode: jev\nremote:\n  provider: jev\n  api_key_env: \"sk-live-secret\"\n  timeout: 750ms\n  max_attempts_per_session: 1\n  lease_ttl: 2s\n  retry_backoff: 0s\n  positive_threshold: 0.9\n"},
		{name: "unbounded matcher data", configYAML: classificationPrefixList(17)},
		{name: "unknown config key", configYAML: "unknown_option: true\n"},
	}

	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := newClassificationGenerationRuntime(t)
			good, err := rt.CompileGeneration(context.Background(), featurehost.GenerationInput{
				Registrations: []aipsdk.Registration{featureRegistration(t, featureclassification.ID, true, "")},
			})
			if err != nil {
				t.Fatalf("last-good CompileGeneration: %v", err)
			}
			lastGood := publishedClassifier(t, good.Planes)
			if lastGood == nil {
				t.Fatal("last-good generation published no classifier")
			}
			for _, lifecycle := range good.Lifecycles {
				if err := lifecycle.Start(context.Background()); err != nil {
					t.Fatalf("last-good lifecycle Start: %v", err)
				}
			}

			out, err := rt.CompileGeneration(context.Background(), featurehost.GenerationInput{
				Registrations: []aipsdk.Registration{featureRegistration(t, featureclassification.ID, true, tc.configYAML)},
			})
			if err == nil {
				t.Fatalf("invalid candidate published a classifier: %v", out)
			}
			if !strings.Contains(err.Error(), featureclassification.ID) {
				t.Fatalf("candidate rejection error = %v, want it attributed to the feature", err)
			}
			if got := publishedClassifier(t, out.Planes); got != nil {
				t.Fatalf("rejected candidate published classifier %v", got)
			}
			if len(out.Lifecycles) != 0 {
				t.Fatalf("rejected candidate published %d lifecycles, want none", len(out.Lifecycles))
			}

			// The last-good generation stays usable and unchanged: its published
			// classifier keeps classifying over the state prepared before the
			// rejected candidate was compiled.
			if got := publishedClassifier(t, good.Planes); got != lastGood {
				t.Fatal("last-good generation classifier was replaced by a rejected candidate")
			}
			promoted, err := lastGood.Classify(context.Background(), sdkclassification.Input{
				Session:  session.SessionView{AuthoritativeSessionID: "sess-lastgood"},
				Evidence: sdkclassification.Evidence{ClientUserAgent: "codex_cli_rs/1.2.3"},
			})
			if err != nil {
				t.Fatalf("last-good classifier stopped working after a rejected candidate: %v", err)
			}
			if !promoted.IsCodingAgent() {
				t.Fatalf("last-good classification = %+v, want the coding_agent promotion", promoted)
			}
		})
	}
}

// TestCompileGeneration_DuplicateClassifierRejectsCandidateAndKeepsLastGood
// pins the exclusive-plane composition rule: a second effective classifier
// contributor must fail generation compilation rather than replace the
// published classification authority.
func TestCompileGeneration_DuplicateClassifierRejectsCandidateAndKeepsLastGood(t *testing.T) {
	t.Parallel()

	rt := newClassificationGenerationRuntime(t)
	occupied := aipfeature.NewContributionSet()
	if err := aipfeature.Contribute(occupied, aipfeature.PlaneSessionClassifier, "third-party-classifier", sdkclassification.Classifier(fixedClassificationClassifier{id: "third-party-classifier"})); err != nil {
		t.Fatalf("seed classifier contribution: %v", err)
	}
	seed := occupied.Freeze()

	good, err := rt.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Planes:        seed,
		Registrations: []aipsdk.Registration{featureRegistration(t, featureclassification.ID, true, "")},
	})
	if err == nil {
		t.Fatalf("duplicate classifier was accepted: %+v", publishedClassifier(t, good.Planes))
	}
	if !strings.Contains(err.Error(), aipfeature.PlaneSessionClassifier.ID) {
		t.Fatalf("duplicate classifier rejection error = %v, want it attributed to the exclusive plane", err)
	}
	if got := publishedClassifier(t, good.Planes); got != nil {
		t.Fatalf("rejected duplicate candidate published classifier %v", got)
	}
	if len(good.Lifecycles) != 0 {
		t.Fatalf("rejected duplicate candidate published %d lifecycles, want none", len(good.Lifecycles))
	}

	// A later candidate without the duplicate contributor still composes, and
	// the rejection above did not poison process-owned state.
	recovered, err := rt.CompileGeneration(context.Background(), featurehost.GenerationInput{
		Registrations: []aipsdk.Registration{featureRegistration(t, featureclassification.ID, true, "")},
	})
	if err != nil {
		t.Fatalf("CompileGeneration after rejected duplicate: %v", err)
	}
	if publishedClassifier(t, recovered.Planes) == nil {
		t.Fatal("recovered generation published no classifier")
	}
}

// TestCompileGeneration_ClassifiedTurnsUseThePublishedPlaneAndProcessState
// pins the composed behavior: the plane published by an enabled generation
// classifies a decisive coding-harness turn through the process-owned
// coordinator once the generation lifecycle prepared it, and the promotion is
// visible to the next turn of the same authoritative session.
func TestCompileGeneration_ClassifiedTurnsUseThePublishedPlaneAndProcessState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newClassificationGenerationRuntime(t)
	out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
		Registrations: []aipsdk.Registration{featureRegistration(t, featureclassification.ID, true, "")},
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	classifier := publishedClassifier(t, out.Planes)
	if classifier == nil {
		t.Fatal("enabled generation published no classifier")
	}
	for _, lifecycle := range out.Lifecycles {
		if err := lifecycle.Start(ctx); err != nil {
			t.Fatalf("generation lifecycle Start: %v", err)
		}
	}

	in := sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: "sess-generation"},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "codex_cli_rs/1.2.3"},
	}
	first, err := classifier.Classify(ctx, in)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !first.IsCodingAgent() || first.Evidence != "client_family.codex" || first.Revision != 1 {
		t.Fatalf("classification = %+v, want a single accepted coding_agent promotion", first)
	}

	weak := sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: "sess-generation"},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "Mozilla/5.0"},
	}
	second, err := classifier.Classify(ctx, weak)
	if err != nil {
		t.Fatalf("second Classify: %v", err)
	}
	if second != first {
		t.Fatalf("second classification = %+v, want the stable positive %+v", second, first)
	}
}

// TestCompileGeneration_ReloadRebindsPolicyAndDisableWithdrawsPlaneWithoutErasingState
// pins requirement 8.5/8.7/8.8: a reloaded generation publishes new immutable
// policy over shared process state, disabling withdraws the plane without
// deleting the persisted positive value, and re-enabling recovers it.
func TestCompileGeneration_ReloadRebindsPolicyAndDisableWithdrawsPlaneWithoutErasingState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newClassificationGenerationRuntime(t)
	decisive := sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: "sess-reload"},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "codex_cli_rs/1.2.3"},
	}
	compile := func(enabled bool, configYAML string) featurehost.GenerationOutput {
		t.Helper()
		out, err := rt.CompileGeneration(ctx, featurehost.GenerationInput{
			Registrations: []aipsdk.Registration{featureRegistration(t, featureclassification.ID, enabled, configYAML)},
		})
		if err != nil {
			t.Fatalf("CompileGeneration(enabled=%t): %v", enabled, err)
		}
		return out
	}

	baseline := compile(true, "")
	for _, lifecycle := range baseline.Lifecycles {
		if err := lifecycle.Start(ctx); err != nil {
			t.Fatalf("baseline lifecycle Start: %v", err)
		}
	}
	baselineClassifier := publishedClassifier(t, baseline.Planes)
	if baselineClassifier == nil {
		t.Fatal("baseline generation published no classifier")
	}
	if _, err := baselineClassifier.Classify(ctx, decisive); err != nil {
		t.Fatalf("baseline Classify: %v", err)
	}

	// Reloaded generation with a new exclusion list: new policy, same state.
	excluded := compile(true, "heuristic:\n  ignored_user_agent_prefixes: [codex_cli_rs]\n")
	excludedClassifier := publishedClassifier(t, excluded.Planes)
	if excludedClassifier == nil {
		t.Fatal("reloaded generation published no classifier")
	}
	if excludedClassifier == baselineClassifier {
		t.Fatal("reloaded generation reused the previous generation's classifier policy")
	}
	unknown, err := excludedClassifier.Classify(ctx, sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: "sess-reload-excluded"},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "codex_cli_rs/1.2.3"},
	})
	if err != nil {
		t.Fatalf("excluded Classify: %v", err)
	}
	if unknown != (session.Classification{}) {
		t.Fatalf("excluded classification = %+v, want unknown", unknown)
	}
	// The previous generation's policy is unchanged for in-flight turns.
	recovered, err := baselineClassifier.Classify(ctx, sdkclassification.Input{
		Session:  session.SessionView{AuthoritativeSessionID: "sess-reload-excluded"},
		Evidence: sdkclassification.Evidence{ClientUserAgent: "codex_cli_rs/1.2.3"},
	})
	if err != nil {
		t.Fatalf("in-flight baseline Classify: %v", err)
	}
	if !recovered.IsCodingAgent() {
		t.Fatalf("in-flight baseline classification = %+v, want the un-excluded promotion", recovered)
	}

	// Disable: the plane is withdrawn but the durable positive survives.
	disabled := compile(false, "")
	if got := publishedClassifier(t, disabled.Planes); got != nil {
		t.Fatalf("disabled generation published classifier %v", got)
	}

	// Re-enable: the persisted positive classification is recoverable.
	reEnabled := compile(true, "")
	reEnabledClassifier := publishedClassifier(t, reEnabled.Planes)
	if reEnabledClassifier == nil {
		t.Fatal("re-enabled generation published no classifier")
	}
	restored, err := reEnabledClassifier.Classify(ctx, sdkclassification.Input{
		Session: session.SessionView{AuthoritativeSessionID: "sess-reload"},
	})
	if err != nil {
		t.Fatalf("re-enabled Classify: %v", err)
	}
	if !restored.IsCodingAgent() || restored.Evidence != "client_family.codex" {
		t.Fatalf("re-enabled classification = %+v, want the retained durable positive", restored)
	}
}
