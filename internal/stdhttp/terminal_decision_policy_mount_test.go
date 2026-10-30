package stdhttp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionpolicy"
)

func TestComposeStandardHTTP_TerminalDecisionPolicyRoutesUseStoreAndOperatorProtection(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneMountConfig(false)
	store := sessionpolicy.NewStore(sessionpolicy.Config{})
	t.Cleanup(func() { _ = store.Close() })
	key := sessionpolicy.Key{
		SecureSessionIncarnation: "session-1",
		ALegID:                   "a-leg-1",
		FeatureID:                "terminal-decision",
	}
	authority := sessionpolicy.Authority{
		SecureSessionIncarnation: key.SecureSessionIncarnation,
		ALegID:                   key.ALegID,
		Authorized:               true,
	}
	ex := runtime.TestExecutor()
	in := StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, pluginreg.NewRegistry()),
		Operations: HTTPOperationsInput{TerminalDecisionPolicy: TerminalDecisionPolicyInput{
			Store: store,
			FeatureStatus: func(context.Context, string) (bool, bool, error) {
				return true, false, nil
			},
			ResolveClientScope: func(context.Context, *http.Request, string) (sessionpolicy.Key, sessionpolicy.Authority, error) {
				return key, authority, nil
			},
			AuthorizeOperatorTarget: func(context.Context, *http.Request, string, string) (sessionpolicy.Key, sessionpolicy.Authority, error) {
				return key, authority, nil
			},
		}},
	}
	h, err := ComposeStandardHTTP(context.Background(), cfg, slog.Default(), in)
	if err != nil {
		t.Fatalf("ComposeStandardHTTP: %v", err)
	}

	client := httptest.NewRecorder()
	h.ServeHTTP(client, httptest.NewRequest(http.MethodGet, "/v1/aip/session/features/terminal-decision", nil))
	if client.Code != http.StatusOK || !strings.Contains(client.Body.String(), `"available":false`) {
		t.Fatalf("client route status=%d body=%s", client.Code, client.Body.String())
	}

	operator := httptest.NewRecorder()
	operatorReq := httptest.NewRequest(http.MethodGet, "/admin/session-features/session-1/terminal-decision", nil)
	operatorReq.Header.Set("X-AIP-Diagnostics-Secret", "secretsecret")
	h.ServeHTTP(operator, operatorReq)
	if operator.Code != http.StatusOK {
		t.Fatalf("operator route status=%d body=%s", operator.Code, operator.Body.String())
	}

	denied := httptest.NewRecorder()
	deniedReq := httptest.NewRequest(http.MethodGet, "/admin/session-features/session-1/terminal-decision", nil)
	deniedReq.Header.Set("X-AIP-Diagnostics-Secret", "wrong")
	h.ServeHTTP(denied, deniedReq)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("operator wrong secret status=%d body=%s", denied.Code, denied.Body.String())
	}
}

func TestComposeStandardHTTP_TerminalDecisionPolicyUnavailableStaysMountedAndClosed(t *testing.T) {
	t.Parallel()
	cfg := controlPlaneMountConfig(false)
	ex := runtime.TestExecutor()
	in := StandardHTTPInput{
		Core:      HTTPCoreInput{Executor: ex},
		Frontends: frontendInputForTest(cfg, ex, pluginreg.NewRegistry()),
	}
	h, err := ComposeStandardHTTP(context.Background(), cfg, slog.Default(), in)
	if err != nil {
		t.Fatalf("ComposeStandardHTTP: %v", err)
	}
	client := httptest.NewRecorder()
	h.ServeHTTP(client, httptest.NewRequest(http.MethodGet, "/v1/aip/session/features/terminal-decision", nil))
	if client.Code != http.StatusServiceUnavailable || !strings.Contains(client.Body.String(), `"policy_unavailable"`) {
		t.Fatalf("client unavailable status=%d body=%s", client.Code, client.Body.String())
	}
	operator := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/session-features/session-1/terminal-decision", nil)
	req.Header.Set("X-AIP-Diagnostics-Secret", "secretsecret")
	h.ServeHTTP(operator, req)
	if operator.Code != http.StatusServiceUnavailable {
		t.Fatalf("operator unavailable status=%d body=%s", operator.Code, operator.Body.String())
	}
}
