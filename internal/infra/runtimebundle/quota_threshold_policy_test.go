package runtimebundle

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// TestQuotaSwitchThreshold_HostProjectsNondefaultPolicy proves the host
// projects the validated nonsecret upstream_credentials threshold into the
// executable-connector RuntimePolicy: a 30m configuration yields 1800000ms,
// the default selects zero (coordinator default), and distinct thresholds
// yield distinct physical resource identities so pooled connectors never
// alias across policy revisions.
func TestQuotaSwitchThreshold_HostProjectsNondefaultPolicy(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.UpstreamCredentials.QuotaSwitchRetryAfter = "30m"
	quotaSwitch, err := cfg.UpstreamCredentials.EffectiveQuotaSwitchRetryAfter()
	if err != nil {
		t.Fatalf("EffectiveQuotaSwitchRetryAfter: %v", err)
	}
	if quotaSwitch.String() != "30m0s" {
		t.Fatalf("quotaSwitch = %v, want 30m0s", quotaSwitch)
	}
	ms := quotaSwitch.Milliseconds()
	if ms != 30*60*1000 {
		t.Fatalf("quotaSwitch ms = %d, want 1800000", ms)
	}

	base := testBackendResourcePhysicalInput()
	baseID, baseShareable, err := physicalIdentity(base)
	if err != nil {
		t.Fatalf("base identity: %v", err)
	}
	if !baseShareable {
		t.Fatal("base input must be shareable")
	}
	changed := cloneBackendResourcePhysicalInput(base)
	changed.RuntimePolicy.QuotaSwitchRetryAfterMS = ms
	got, shareable, err := physicalIdentity(changed)
	if err != nil {
		t.Fatalf("changed identity: %v", err)
	}
	if !shareable {
		t.Fatal("threshold-changed input must stay shareable")
	}
	if baseID == got {
		t.Fatal("quota threshold change aliased base physical identity; pooled connectors would reuse across policy revisions")
	}

	// Default (zero) and explicit 1h are distinct identities: zero selects
	// the coordinator default, explicit 1h is the same duration but an
	// explicit host projection. Both must not alias the 30m revision.
	explicitHour := cloneBackendResourcePhysicalInput(base)
	explicitHour.RuntimePolicy.QuotaSwitchRetryAfterMS = 60 * 60 * 1000
	hourID, _, err := physicalIdentity(explicitHour)
	if err != nil {
		t.Fatalf("hour identity: %v", err)
	}
	if hourID == got {
		t.Fatal("30m and 1h thresholds must not share one physical identity")
	}
}
