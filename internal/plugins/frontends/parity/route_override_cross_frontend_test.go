package parity_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

func TestRouteOverride_followsALegAcrossOpenAIResponsesAndLegacy(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens []routeOverrideOpen
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.MaxAttempts = 4
	ex.RouteOverrideReader = st
	ex.Backends = map[string]execbackend.Backend{
		"clientbe": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens = append(opens, routeOverrideOpen{backend: "clientbe", selector: call.Route.Selector, aLegID: call.Session.ALegID, model: cand.Primary.Model})
				return fixedOKStream(), nil
			},
		},
		"adminbe": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opens = append(opens, routeOverrideOpen{backend: "adminbe", selector: call.Route.Selector, aLegID: call.Session.ALegID, model: cand.Primary.Model})
				return fixedOKStream(), nil
			},
		},
	}
	testkit.WireConformanceExecutorSecureSession(t, ex)

	principal := execview.PrincipalView{ID: "ov-cross-fe"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", withPrincipal(&openairesponses.Handler{
		Exec:                 ex,
		DefaultRouteSelector: "clientbe:m",
	}, principal))
	mux.Handle("/v1/chat/completions", withPrincipal(&openailegacy.Handler{
		Exec:                 ex,
		DefaultRouteSelector: "clientbe:m",
	}, principal))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp1 := postJSON(t, srv.URL+"/v1/responses", `{"model":"m","input":"first"}`, map[string]string{
		routeselect.HeaderRouteSelector: "clientbe:m",
	})
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("responses create status=%d body=%s", resp1.StatusCode, readBody(t, resp1))
	}
	sid := resp1.Header.Get(sessionwire.HeaderAuthoritativeSessionID)
	tok := resp1.Header.Get(sessionwire.HeaderResumeToken)
	aLeg := resp1.Header.Get(sessionwire.HeaderALegID)
	_ = resp1.Body.Close()
	if sid == "" || tok == "" || aLeg == "" {
		t.Fatalf("missing session carriers sid=%q tok=%q aleg=%q", sid, tok, aLeg)
	}
	if len(opens) != 1 || opens[0].backend != "clientbe" || opens[0].aLegID != aLeg {
		t.Fatalf("first frontend open: %+v", opens)
	}

	if _, err := st.Replace(context.Background(), aLeg, "adminbe:m", time.Now().UTC()); err != nil {
		t.Fatalf("replace: %v", err)
	}

	resp2 := postJSON(t, srv.URL+"/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"next"}]}`, map[string]string{
		routeselect.HeaderRouteSelector:          "clientbe:m",
		sessionwire.HeaderAuthoritativeSessionID: sid,
		sessionwire.HeaderResumeToken:            tok,
	})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("legacy resume status=%d body=%s", resp2.StatusCode, readBody(t, resp2))
	}
	_ = resp2.Body.Close()
	if len(opens) != 2 {
		t.Fatalf("want 2 opens, got %+v", opens)
	}
	if opens[1].backend != "adminbe" || opens[1].selector != "adminbe:m" {
		t.Fatalf("override did not follow A-leg across frontends: %+v", opens)
	}
	if opens[1].aLegID != aLeg {
		t.Fatalf("A-leg changed across frontends: first=%q second=%q", aLeg, opens[1].aLegID)
	}
}

type routeOverrideOpen struct {
	backend  string
	selector string
	aLegID   string
	model    string
}

func withPrincipal(h http.Handler, p execview.PrincipalView) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(execview.WithPrincipal(r.Context(), p)))
	})
}

func postJSON(t *testing.T, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fixedOKStream() aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventResponseFinished},
	})
}
