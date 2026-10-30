//go:build integration

package conformance

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

func TestBoundedSentinelCasesAreExplicitAndProfileIndependent(t *testing.T) {
	cases := BoundedSentinelCases()
	if len(cases) == 0 || len(cases) > maxBoundedSentinelCases {
		t.Fatalf("sentinel count=%d, want 1..%d", len(cases), maxBoundedSentinelCases)
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		if tc.ID == "" || seen[tc.ID] || strings.TrimSpace(tc.Protects) == "" {
			t.Fatalf("invalid or duplicate sentinel case: %+v", tc)
		}
		if tc.Backend == BackendCompatibleOpenAI && tc.ProfileID == "" {
			t.Fatalf("compatible-family sentinel must bind an explicit provider profile: %+v", tc)
		}
		seen[tc.ID] = true
		if tc.Backend == BackendOpenRouter && tc.Frontend != FrontendOpenResponses {
			t.Fatalf("connector sentinel must use the supported Responses frontend: %+v", tc)
		}
	}

	template, err := providerprofiles.EmbeddedProfile("groq")
	if err != nil {
		t.Fatalf("EmbeddedProfile(groq): %v", err)
	}
	profiles := make([]providerprofiles.Profile, 1000)
	for i := range profiles {
		p := template
		p.ID = fmt.Sprintf("profile-%04d", i)
		// The profile catalog owns provider population; sentinel policy does not.
		profiles[i] = p
	}
	catalog, err := providerprofiles.NewCatalog(profiles)
	if err != nil {
		t.Fatal(err)
	}
	if compiled, err := catalog.CompileAll(); err != nil || len(compiled) != 1000 {
		t.Fatalf("profile compilation count=%d err=%v", len(compiled), err)
	}
	if len(BoundedSentinelCases()) != len(cases) {
		t.Fatal("sentinel count changed after synthetic profile population")
	}
}

func TestBoundedSentinelCatchesBrokenCompositionWiring(t *testing.T) {
	d := Deploy(t, DeploymentSpec{Frontend: FrontendOpenResponses, Backend: BackendOpenResponses, Transport: TransportJSON})
	if d == nil {
		t.Fatal("deployment failed")
	}
	d.Exec.DefaultBackend = "missing-backend"
	if err := d.RoundTripModel(context.Background(), "gpt-4o-mini", "broken wiring"); err == nil {
		t.Fatal("broken composition wiring was not caught")
	}
	if got := d.RequestCount(BackendOpenResponses); got != 0 {
		t.Fatalf("broken wiring reached upstream: %d", got)
	}
}

func TestBoundedSentinelComposition(t *testing.T) {
	for _, tc := range BoundedSentinelCases() {
		t.Run(tc.ID, func(t *testing.T) {
			var d *Deployment
			switch tc.Backend {
			case BackendOpenRouter:
				d = DeployConnectorColumnFor(t, tc.Frontend, tc.Backend, tc.Transport)
			case BackendCompatibleOpenAI:
				d = Deploy(t, DeploymentSpec{Frontend: tc.Frontend, Backend: tc.Backend, ProfileID: tc.ProfileID, ProviderOrigin: "", Transport: tc.Transport})
			default:
				d = Deploy(t, DeploymentSpec{Frontend: tc.Frontend, Backend: tc.Backend, ProfileID: tc.ProfileID, Transport: tc.Transport})
			}
			if d == nil {
				t.Fatal("sentinel deployment failed")
			}
			defer func() { _ = d.Close() }()
			if tc.Negative {
				var err error
				switch tc.ID {
				case "negative-openresponses-decode":
					err = d.SendRawCreate(context.Background(), `{"stream":false,"store":false}`)
				case "negative-openresponses-websocket-store":
					err = d.SendRawWSTurn(context.Background(), `{"type":"response.create","model":"m","input":"x","store":true}`)
				}
				if err == nil || d.RequestCount(BackendOpenResponses) != 0 {
					t.Fatalf("negative sentinel admitted upstream work: err=%v requests=%d", err, d.RequestCount(BackendOpenResponses))
				}
				return
			}
			res, err := d.Client.RoundTrip(context.Background(), "sentinel")
			if err != nil {
				t.Logf("origin=%+v capture=%+v", d.OriginFor(tc.Backend), d.OriginFor(tc.Backend).Capture())
				t.Fatal(err)
			}
			if strings.TrimSpace(res.Text) == "" {
				t.Fatalf("empty sentinel response: %+v", res)
			}
			if tc.Transport == TransportWebSocket {
				if !strings.HasPrefix(res.ResponseID, continuation.ResponseIDPrefix) {
					t.Fatalf("stateful sentinel response ID=%q, want exact proxy prefix %q", res.ResponseID, continuation.ResponseIDPrefix)
				}
			}
		})
	}
}
