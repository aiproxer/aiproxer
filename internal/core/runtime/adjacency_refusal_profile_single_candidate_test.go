package runtime_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/providerprofiles"
	refopenairesponses "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Claim: on the profile-built path the #1136 cause-loss does not reproduce.
// The real deepseek-responses catalog profile (strict
// openai.responses.tool_output_adjacency quirk, static deepseek-v4-flash
// inventory) as the only candidate, pointed at the strict adjacency emulator,
// refuses the [callA, msg, callB, outA, outB] history locally and surfaces the
// refusal's capability root from the exhausted route — the error the
// openresponses frontend maps to the client-visible 502 backend_unavailable
// (#1150) — instead of the generic planning error. The recording it relies on
// is #1140's SwallowedCapabilityReject.
func TestAdjacencyRefusal_ProfileBuiltSingleCandidateKeepsCapabilityReject(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-test")

	// The strict emulator is the configured upstream, but the refusal must
	// precede any wire I/O: the history is never sent, rewritten or not.
	var wireHits atomic.Int32
	strict := refopenairesponses.NewHandler(refopenairesponses.Config{StrictToolOutputAdjacency: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wireHits.Add(1)
		strict.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	// The real catalog entry: quirks, family and capability ceiling stay as
	// shipped; only external I/O is replaced (endpoint and emulator client).
	profile, err := providerprofiles.EmbeddedProfile("deepseek-responses")
	if err != nil {
		t.Fatalf("embedded profile: %v", err)
	}
	profile.Endpoint.BaseURL = srv.URL + "/v1"
	if len(profile.Models.Static) == 0 {
		t.Fatal("deepseek-responses profile must ship a static model")
	}

	compiled, err := standardplugins.CompileProviderProfile(profile)
	if err != nil {
		t.Fatalf("compile profile: %v", err)
	}
	be, err := standardplugins.BuildProviderProfileBackend(compiled, profile.ID, srv.Client(), pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatalf("build profile backend: %v", err)
	}

	ex := algSecureExecutor(t, map[string]execbackend.Backend{profile.ID: be}, nil, extensions.SnapshotOptions{})
	call := refusingHistory()
	call.Route = aipapi.RouteIntent{Selector: profile.ID + ":" + profile.Models.Static[0].CanonicalID}
	_, err = ex.Execute(principalCtx("adjacency-profile-single"), call)

	if err == nil {
		t.Fatal("single refused candidate must surface an error")
	}
	// The refusal itself, not a negotiation or planning substitute.
	if !errors.Is(err, openaicompat.ErrResponsesAdjacencyRefused) {
		t.Fatalf("surfaced error must keep the refusal cause, got %T: %v", err, err)
	}
	// The 502 predicate: ErrCapabilityReject without being a 400-class reject.
	if !errors.Is(err, aipapi.ErrCapabilityReject) {
		t.Fatalf("surfaced error must match ErrCapabilityReject, got %T: %v", err, err)
	}
	if aipapi.IsReject(err) {
		t.Fatalf("surfaced error must not be a 400-class client reject: %v", err)
	}
	if errors.Is(err, routing.ErrNoEligibleCandidate) {
		t.Fatalf("surfaced error must not be the generic planning error: %v", err)
	}
	if got := wireHits.Load(); got != 0 {
		t.Fatalf("refusal must precede wire I/O, strict emulator requests = %d", got)
	}

	// The client-visible half: the surfaced error reaches the openresponses
	// wire as 502 backend_unavailable, not as the generic backend_error the
	// pre-#1140 planning error produced.
	rec := httptest.NewRecorder()
	if werr := (openresponses.WireErrors{}).WriteExecuteError(rec, execerr.ClassifyExecute(err)); werr != nil {
		t.Fatalf("write wire error: %v", werr)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("client status = %d, want %d (body %s)", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "backend_unavailable") {
		t.Fatalf("client body must carry backend_unavailable, got %s", rec.Body.String())
	}
}
