//nolint:paralleltest // Tests share package-level fixtures and run serially.
package reasoningpreservation_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/features/reasoningpreservation"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBackground implements both BackgroundClient and BackgroundPoller for single-value pattern.
type fakeBackground struct {
	pollResult auxiliary.PollResult
}

func (f *fakeBackground) SubmitCollect(context.Context, auxiliary.Request, auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return auxiliary.JobID("job-1"), nil
}

func (f *fakeBackground) Await(context.Context, auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (f *fakeBackground) Forget(auxiliary.JobID) {}
func (f *fakeBackground) Poll(context.Context, auxiliary.JobID) (auxiliary.PollResult, error) {
	return f.pollResult, nil
}

// also separate fakes for missing-cap tests
type fakeClientOnly struct{}

func (fakeClientOnly) SubmitCollect(context.Context, auxiliary.Request, auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return auxiliary.JobID("job-1"), nil
}

func (fakeClientOnly) Await(context.Context, auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (fakeClientOnly) Forget(auxiliary.JobID) {}

type fakePollerOnly struct{}

func (fakePollerOnly) Poll(context.Context, auxiliary.JobID) (auxiliary.PollResult, error) {
	return auxiliary.PollResult{State: auxiliary.PollPending}, nil
}

type fakeEgress struct{}

func (fakeEgress) Decide(_ context.Context, _ reasoningpreservation.CompressionEgressInput) (reasoningpreservation.CompressionEgressDecision, error) {
	return reasoningpreservation.CompressionEgressDecision{Action: reasoningpreservation.EgressAllow, PolicyVersion: "v1"}, nil
}

type fakeSanitizer struct{}

func (fakeSanitizer) SanitizeText(_ context.Context, text string) (string, error) { return text, nil }

func TestConstructionMatrix_CompressionServices(t *testing.T) {
	t.Parallel()
	baseEnabled := decodeValidConfig(t, validCompressionYAML())
	baseDisabled := decodeValidConfig(t, validObserveYAML)

	fakeBG := &fakeBackground{}
	fakeEgr := fakeEgress{}
	fakeSan := fakeSanitizer{}

	// helper to build with services via new API if available, otherwise via FeatureBundle
	// RED expects: enabled without caps must fail; enabled with all caps must succeed with limits wired.

	// Disabled + no caps => OK via existing FeatureBundle
	t.Run("disabled_no_caps", func(t *testing.T) {
		_, b, err := reasoningpreservation.FeatureBundleWithParts(baseDisabled)
		require.NoError(t, err, "disabled+no-caps must succeed")
		require.NoError(t, b.Validate())
	})

	// Disabled via new services API with nil caps => OK (zero delta)
	t.Run("disabled_nil_services_explicit", func(t *testing.T) {
		// This tests the new explicit composition path; will fail to compile until implemented,
		// proving RED.
		svc := reasoningpreservation.CompressionServices{}
		_, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(baseDisabled, svc, reasoningpreservation.CompanionPolicy{})
		require.NoError(t, err, "disabled+nil services must succeed")
	})

	// Enabled + missing each cap => specific error
	t.Run("enabled_missing_client", func(t *testing.T) {
		svc := reasoningpreservation.CompressionServices{
			Client:       nil,
			Poller:       fakePollerOnly{},
			EgressPolicy: fakeEgr,
			Sanitizer:    fakeSan,
		}
		_, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(baseEnabled, svc, reasoningpreservation.CompanionPolicy{})
		require.Error(t, err, "enabled missing client must fail")
		assert.Contains(t, err.Error(), "BackgroundClient")
	})

	t.Run("enabled_missing_poller", func(t *testing.T) {
		svc := reasoningpreservation.CompressionServices{
			Client:       fakeClientOnly{},
			Poller:       nil,
			EgressPolicy: fakeEgr,
			Sanitizer:    fakeSan,
		}
		_, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(baseEnabled, svc, reasoningpreservation.CompanionPolicy{})
		require.Error(t, err, "enabled missing poller must fail")
		assert.Contains(t, err.Error(), "BackgroundPoller")
	})

	t.Run("enabled_missing_egress", func(t *testing.T) {
		svc := reasoningpreservation.CompressionServices{
			Client:       fakeClientOnly{},
			Poller:       fakePollerOnly{},
			EgressPolicy: nil,
			Sanitizer:    fakeSan,
		}
		_, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(baseEnabled, svc, reasoningpreservation.CompanionPolicy{})
		require.Error(t, err, "enabled missing egress must fail")
		assert.Contains(t, err.Error(), "EgressPolicy")
	})

	t.Run("enabled_missing_sanitizer", func(t *testing.T) {
		svc := reasoningpreservation.CompressionServices{
			Client:       fakeClientOnly{},
			Poller:       fakePollerOnly{},
			EgressPolicy: fakeEgr,
			Sanitizer:    nil,
		}
		_, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(baseEnabled, svc, reasoningpreservation.CompanionPolicy{})
		require.Error(t, err, "enabled missing sanitizer must fail")
		assert.Contains(t, err.Error(), "TrustedTextSanitizer")
	})

	// Enabled + all caps => parts wired, store limits from ToLimits, poller reachable
	t.Run("enabled_all_caps_wired", func(t *testing.T) {
		svc := reasoningpreservation.CompressionServices{
			Client:       fakeBG,
			Poller:       fakeBG,
			EgressPolicy: fakeEgr,
			Sanitizer:    fakeSan,
		}
		parts, bundle, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(baseEnabled, svc, reasoningpreservation.CompanionPolicy{})
		require.NoError(t, err, "enabled+all caps must succeed")
		require.NoError(t, bundle.Validate())
		require.NotNil(t, parts)
		// assert store limits populated from ToLimits
		limits := baseEnabled.Compression.ToLimits()
		assert.NotZero(t, limits.MaxPendingPerSession)
		// Verify generation-bound semantics: poller is the same instance we passed
		assert.Equal(t, fakeBG, svc.Poller)
		assert.Equal(t, fakeBG, svc.Client)
		// Check InstanceParts carries services
		assert.Equal(t, fakeBG, parts.CompressionServices.Client)
		assert.Equal(t, fakeBG, parts.CompressionServices.Poller)
		assert.NotNil(t, parts.CompressionServices.EgressPolicy)
		assert.NotNil(t, parts.CompressionServices.Sanitizer)
		// Check store limits via behavior: reserve up to limit then expect budget error
		cs, ok := parts.Store.(reasoningpreservation.CompressionStore)
		require.True(t, ok, "store must implement CompressionStore")
		p := reasoningpreservation.NewSessionPartition("sess-composition-wired")
		// Use fresh timestamps to avoid TTL expiry (store uses time.Now).
		for i := 0; i < limits.MaxPendingPerSession; i++ {
			id := assertID(i)
			art := sampleArtifactWithTime(id, "payload", 32, time.Now().UTC())
			_, err := cs.Append(context.Background(), p, art)
			require.NoError(t, err)
			d := digestFor(id)
			_, err = cs.ReserveCompression(context.Background(), p, id, d, "v1", semanticDigestFor("v1"), egressHashFor("v1"))
			require.NoError(t, err, "reserve within limit should succeed - limits must be wired from ToLimits")
		}
		// Next reserve should exceed per-session limit if wiring correct
		extra := sampleArtifactWithTime("extra", "payload", 32, time.Now().UTC())
		_, err = cs.Append(context.Background(), p, extra)
		require.NoError(t, err)
		_, err = cs.ReserveCompression(context.Background(), p, "extra", digestFor("extra"), "v1", semanticDigestFor("v1"), egressHashFor("v1"))
		require.Error(t, err, "should exceed per-session limit proving ToLimits wiring")
		assert.True(t, reasoningpreservation.IsBudgetError(err))
	})

	// Single value implementing both interfaces via same object
	t.Run("enabled_single_value_both_interfaces", func(t *testing.T) {
		both := &fakeBackground{}
		svc := reasoningpreservation.CompressionServices{
			Client:       both,
			Poller:       both,
			EgressPolicy: fakeEgr,
			Sanitizer:    fakeSan,
		}
		_, _, err := reasoningpreservation.FeatureBundleWithPartsAndCompression(baseEnabled, svc, reasoningpreservation.CompanionPolicy{})
		require.NoError(t, err, "single value implementing both must be accepted")
	})

	// FeatureBundle (legacy) with enabled config and no services must fail closed (prove global locator not used)
	t.Run("legacy_bundle_enabled_without_services_fails_closed", func(t *testing.T) {
		_, _, err := reasoningpreservation.FeatureBundleWithParts(baseEnabled)
		require.Error(t, err, "legacy bundle with enabled compression but no services must fail closed")
		assert.Contains(t, err.Error(), "compression")
	})
}

func assertID(i int) string { return "t-" + string(rune('0'+i)) }

func sampleArtifactWithTime(id, reasoningText string, bytes int, createdAt time.Time) reasoningpreservation.TurnArtifact {
	part := reasoningPart(aipapi.ReasoningDialectOpenAIChatTextV1, reasoningText, "", nil)
	return reasoningpreservation.TurnArtifact{
		ID:             id,
		Anchor:         [32]byte{1, 2, 3},
		SourceBackend:  "backend",
		SourceModel:    "model",
		Reasoning:      []reasoningpreservation.PlacedReasoning{placedReasoning(0, part)},
		CreatedAt:      createdAt,
		ReasoningBytes: bytes,
	}
}
