package standardplugins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const profileSnapDecisionResponse = `{"model":"prof-m","answers":{"q":{"type":"noul","noul":0.7}},"usage":{"input_tokens":1,"output_tokens":0}}`

// syntheticSystemOneProfile binds one embedded-style provider root to the
// emulator origin for snapshot-preference proofs. Auth carries only the
// nonsecret environment reference; no key value appears in configuration.
func syntheticSystemOneProfile(baseURL, root string) providerprofiles.Profile {
	return providerprofiles.Profile{
		APIVersion: providerprofiles.APIVersionV1,
		ID:         "red-snap-profile",
		Family:     providerprofiles.FamilySystemOne,
		Endpoint:   providerprofiles.Endpoint{BaseURL: baseURL, PathPolicy: providerprofiles.PathPolicyFamilyDefault},
		Auth:       providerprofiles.Auth{Mode: providerprofiles.AuthBearerEnv, EnvVar: root},
		Models: providerprofiles.ModelDiscovery{
			Policy:    providerprofiles.DiscoveryStatic,
			Namespace: providerprofiles.Namespace{Mode: providerprofiles.NamespacePreserve},
			Static:    []providerprofiles.Model{{CanonicalID: "prof-m", NativeID: "prof-m"}},
		},
	}
}

func profileSnapDecisionCall() aipapi.Call {
	req := aipapi.DecisionRequest{
		Evidence: json.RawMessage(`{"state":[1]}`),
		Questions: []aipapi.DecisionQuestion{
			{ID: "q", Kind: aipapi.DecisionKindNoul},
		},
	}
	return aipapi.Call{
		Decision:   &req,
		Invocation: aipapi.Invocation{Operation: aipapi.OperationDecisionEvaluate, DeliveryMode: aipapi.DeliveryModeNonStreaming},
	}
}

// TestProviderProfileSnapshot_embeddedRootPreferredOverProcessEnv proves
// requirement 1.4 for embedded provider-profile roots: the compiled profile's
// validated nonsecret environment reference resolves through the authorized
// candidate snapshot, so serving uses admitted keys while the process
// environment carries divergent material.
func TestProviderProfileSnapshot_embeddedRootPreferredOverProcessEnv(t *testing.T) {
	root := "PROF_SNAP_RED_API_KEY"
	t.Setenv(root, "env-key")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if gotAuth != "Bearer snap-k1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, profileSnapDecisionResponse)
	}))
	t.Cleanup(srv.Close)

	compiled, err := CompileProviderProfile(syntheticSystemOneProfile(srv.URL, root))
	if err != nil {
		t.Fatalf("CompileProviderProfile: %v", err)
	}
	snap := pluginreg.HostCredentialSnapshot{
		ByRoot:      map[string][]string{root: {"snap-k1"}},
		MaxEnvIndex: 20,
	}
	be, err := BuildProviderProfileBackend(compiled, "red-prof", srv.Client(), pluginreg.BackendFactoryDeps{Credentials: snap})
	if err != nil {
		t.Fatalf("BuildProviderProfileBackend: %v", err)
	}
	es, err := be.Open(context.Background(), profileSnapDecisionCall(), routing.AttemptCandidate{Primary: routing.Primary{Model: "prof-m"}})
	if err != nil {
		t.Fatalf("Open with snapshot keys: %v", err)
	}
	defer func() { _ = es.Close() }()
	for {
		_, rerr := es.Recv(context.Background())
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			t.Fatalf("Recv: %v", rerr)
		}
	}
	if gotAuth != "Bearer snap-k1" {
		t.Fatalf("serving auth = %q, want admitted snapshot key", gotAuth)
	}
}

// TestProviderProfileSnapshot_literalSecretValueRejected proves no source
// file or literal configuration key is accepted for profile credentials:
// an embedded-style profile carrying a key value fails compilation.
func TestProviderProfileSnapshot_literalSecretValueRejected(t *testing.T) {
	profile := syntheticSystemOneProfile("http://127.0.0.1:9", "PROF_SNAP_RED_API_KEY")
	profile.Auth.Secret = "sk-literal-profile-secret"
	if _, err := CompileProviderProfile(profile); err == nil {
		t.Fatal("expected literal profile secret rejection")
	}
}
