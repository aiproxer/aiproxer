package configreload_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	mgmtreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	"github.com/stretchr/testify/assert"
)

func newManagementHarness(t *testing.T, reloadFn func(context.Context, sdkreload.Trigger) sdkreload.Result) (*httptest.Server, *fakeCoordinator, *mgmtreload.Handler) {
	t.Helper()
	coord := newFakeCoordinator("/fixed/startup/config.yaml", reloadFn)
	h, err := mgmtreload.NewHandler(mgmtreload.Options{
		Address:     "127.0.0.1:0",
		AuthMode:    mgmtreload.AuthModeBearer,
		BearerToken: "test-management-secret",
	}, coord)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Mux())
	t.Cleanup(srv.Close)
	return srv, coord, h
}

func authReq(method, url, token string, body io.Reader) *http.Request {
	req, _ := http.NewRequest(method, url, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestManagement_AuthOriginMethodBodyBusyDisconnectStatus(t *testing.T) {
	t.Parallel()

	t.Run("auth_required", func(t *testing.T) {
		t.Parallel()
		srv, _, _ := newManagementHarness(t, func(context.Context, sdkreload.Trigger) sdkreload.Result {
			return sdkreload.Result{Category: sdkreload.ResultPublished}
		})
		res, err := http.Post(srv.URL+mgmtreload.ReloadPath, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		assert.NoError(t, res.Body.Close())
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", res.StatusCode)
		}
		if res.Header.Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("must not emit CORS")
		}
	})

	t.Run("cookie_does_not_authorize", func(t *testing.T) {
		t.Parallel()
		srv, coord, _ := newManagementHarness(t, func(context.Context, sdkreload.Trigger) sdkreload.Result {
			return sdkreload.Result{Category: sdkreload.ResultPublished}
		})
		before := coord.Status().LastResult.AttemptID
		req := authReq(http.MethodPost, srv.URL+mgmtreload.ReloadPath, "", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "session", Value: "admin"})
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.NoError(t, res.Body.Close())
		if res.StatusCode != http.StatusUnauthorized || coord.Status().LastResult.AttemptID != before {
			t.Fatalf("cookie auth status=%d attempt=%d", res.StatusCode, coord.Status().LastResult.AttemptID)
		}
	})

	t.Run("browser_guard", func(t *testing.T) {
		t.Parallel()
		srv, coord, _ := newManagementHarness(t, func(context.Context, sdkreload.Trigger) sdkreload.Result {
			return sdkreload.Result{Category: sdkreload.ResultPublished}
		})
		before := coord.Status().LastResult.AttemptID
		for _, hdr := range []map[string]string{
			{"Origin": "https://evil.example"},
			{"Sec-Fetch-Site": "cross-site"},
			{"Sec-Fetch-Site": "same-site"},
		} {
			req := authReq(http.MethodPost, srv.URL+mgmtreload.ReloadPath, "test-management-secret", strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			assert.NoError(t, res.Body.Close())
			if res.StatusCode != http.StatusForbidden || res.Header.Get("Access-Control-Allow-Origin") != "" {
				t.Fatalf("guard status=%d cors=%q", res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
			}
		}
		opt, _ := http.NewRequest(http.MethodOptions, srv.URL+mgmtreload.ReloadPath, nil)
		ores, err := http.DefaultClient.Do(opt)
		if err != nil {
			t.Fatal(err)
		}
		assert.NoError(t, ores.Body.Close())
		if ores.StatusCode != http.StatusForbidden || coord.Status().LastResult.AttemptID != before {
			t.Fatal("OPTIONS must not trigger reload")
		}
		coord2 := newFakeCoordinator("/fixed/startup/config.yaml", func(context.Context, sdkreload.Trigger) sdkreload.Result {
			return sdkreload.Result{Category: sdkreload.ResultPublished}
		})
		h2, err := mgmtreload.NewHandler(mgmtreload.Options{
			Address:      "127.0.0.1:0",
			AuthMode:     mgmtreload.AuthModeBearer,
			BearerToken:  "test-management-secret",
			AllowOrigins: map[string]struct{}{"https://allowed.example": {}},
		}, coord2)
		if err != nil {
			t.Fatal(err)
		}
		srv2 := httptest.NewServer(h2.Mux())
		t.Cleanup(srv2.Close)
		req := authReq(http.MethodPost, srv2.URL+mgmtreload.ReloadPath, "test-management-secret", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://allowed.example")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.NoError(t, res.Body.Close())
		if res.StatusCode != http.StatusOK {
			t.Fatalf("allowlisted status=%d", res.StatusCode)
		}
	})
}

func TestFixedSource_MethodBodyGuards(t *testing.T) {
	t.Parallel()
	srv, coord, _ := newManagementHarness(t, func(context.Context, sdkreload.Trigger) sdkreload.Result {
		return sdkreload.Result{Category: sdkreload.ResultPublished}
	})
	req := authReq(http.MethodGet, srv.URL+mgmtreload.ReloadPath, "test-management-secret", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assert.NoError(t, res.Body.Close())
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d", res.StatusCode)
	}
	for _, tc := range []struct{ ct, body string }{
		{"application/json", `{"yaml":"x:1"}`},
		{"application/json", `{"path":"/etc/passwd"}`},
		{"application/json", `{"url":"https://evil"}`},
		{"application/json", `{"command":"rm -rf /"}`},
		{"application/json", `{"plugin":"install-me"}`},
		{"application/json", `{"foo":1}`},
		{"text/yaml", "x: 1"},
	} {
		req := authReq(http.MethodPost, srv.URL+mgmtreload.ReloadPath, "test-management-secret", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.ct)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.NoError(t, res.Body.Close())
		if res.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("body %q status=%d", tc.body, res.StatusCode)
		}
	}
	oversized := strings.Repeat("a", 100)
	req = authReq(http.MethodPost, srv.URL+mgmtreload.ReloadPath, "test-management-secret", strings.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assert.NoError(t, res.Body.Close())
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("oversized status=%d", res.StatusCode)
	}
	req = authReq(http.MethodPost, srv.URL+mgmtreload.ReloadPath, "test-management-secret", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assert.NoError(t, res.Body.Close())
	if res.StatusCode != http.StatusOK || coord.FixedSourcePath() != "/fixed/startup/config.yaml" {
		t.Fatalf("ok status=%d path=%s", res.StatusCode, coord.FixedSourcePath())
	}
}

func TestManagement_BusyConflict(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	srv, _, _ := newManagementHarness(t, func(context.Context, sdkreload.Trigger) sdkreload.Result {
		close(entered)
		<-release
		return sdkreload.Result{Category: sdkreload.ResultPublished}
	})
	errCh := make(chan int, 1)
	go func() {
		req := authReq(http.MethodPost, srv.URL+mgmtreload.ReloadPath, "test-management-secret", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			errCh <- -1
			return
		}
		errCh <- res.StatusCode
		assert.NoError(t, res.Body.Close())
	}()
	<-entered
	req := authReq(http.MethodPost, srv.URL+mgmtreload.ReloadPath, "test-management-secret", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var body mgmtreload.ResultDTO
	_ = json.NewDecoder(res.Body).Decode(&body)
	assert.NoError(t, res.Body.Close())
	if res.StatusCode != http.StatusConflict || body.Category != string(sdkreload.ResultBusy) {
		t.Fatalf("busy status=%d cat=%s", res.StatusCode, body.Category)
	}
	close(release)
	if <-errCh != http.StatusOK {
		t.Fatal("first reload")
	}
}

func TestDisconnect_HostOwnedContext(t *testing.T) {
	t.Parallel()
	started, finish, completed := make(chan struct{}), make(chan struct{}), make(chan sdkreload.Result, 1)
	var mu sync.Mutex
	var sawCancel bool
	srv, coord, _ := newManagementHarness(t, func(ctx context.Context, _ sdkreload.Trigger) sdkreload.Result {
		close(started)
		<-finish
		mu.Lock()
		sawCancel = ctx.Err() != nil
		mu.Unlock()
		return sdkreload.Result{Category: sdkreload.ResultNoop}
	})
	coord.SetOnComplete(func(res sdkreload.Result) { completed <- res })
	ctx, cancel := context.WithCancel(context.Background())
	req := authReq(http.MethodPost, srv.URL+mgmtreload.ReloadPath, "test-management-secret", strings.NewReader("{}"))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	errCh := make(chan error, 1)
	go func() { _, err := http.DefaultClient.Do(req); errCh <- err }()
	<-started
	cancel()
	<-errCh
	close(finish)
	if (<-completed).Category != sdkreload.ResultNoop || coord.Status().LastResult.Category != sdkreload.ResultNoop {
		t.Fatal("hosted result lost")
	}
	mu.Lock()
	defer mu.Unlock()
	if sawCancel {
		t.Fatal("client cancel must not cancel host ctx")
	}
}

func TestReloadAPI_StatusGoldensAndShutdown(t *testing.T) {
	t.Parallel()
	srv, coord, _ := newManagementHarness(t, func(context.Context, sdkreload.Trigger) sdkreload.Result {
		return sdkreload.Result{
			Category:          sdkreload.ResultRestartRequired,
			RestartFields:     []string{"server.address", "management.auth"},
			RestartFieldCount: 2,
			ReasonCategory:    "startup-only-fields",
		}
	})
	req := authReq(http.MethodPost, srv.URL+mgmtreload.ReloadPath, "test-management-secret", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var body mgmtreload.ResultDTO
	_ = json.NewDecoder(res.Body).Decode(&body)
	assert.NoError(t, res.Body.Close())
	if res.StatusCode != 409 || body.RestartFieldCount != 2 {
		t.Fatalf("%d %+v", res.StatusCode, body)
	}
	stReq := authReq(http.MethodGet, srv.URL+mgmtreload.StatusPath, "test-management-secret", nil)
	stRes, err := http.DefaultClient.Do(stReq)
	if err != nil {
		t.Fatal(err)
	}
	var st mgmtreload.StatusDTO
	_ = json.NewDecoder(stRes.Body).Decode(&st)
	assert.NoError(t, stRes.Body.Close())
	if st.FixedSourcePath != coord.FixedSourcePath() || st.LastResult.Category != string(sdkreload.ResultRestartRequired) {
		t.Fatalf("%+v", st)
	}
	if stRes.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("status must not emit CORS")
	}

	coord.MarkShutdown()
	req = authReq(http.MethodPost, srv.URL+mgmtreload.ReloadPath, "test-management-secret", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assert.NoError(t, res.Body.Close())
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", res.StatusCode)
	}
}

func TestManagement_LocalTrustLoopback(t *testing.T) {
	t.Parallel()
	coord := newFakeCoordinator("/fixed/startup/config.yaml", func(context.Context, sdkreload.Trigger) sdkreload.Result {
		return sdkreload.Result{Category: sdkreload.ResultNoop}
	})
	h, err := mgmtreload.NewHandler(mgmtreload.Options{
		Address:  "127.0.0.1:0",
		AuthMode: mgmtreload.AuthModeLocalTrust,
	}, coord)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Mux())
	t.Cleanup(srv.Close)
	res, err := http.Post(srv.URL+mgmtreload.ReloadPath, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	assert.NoError(t, res.Body.Close())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("local trust status=%d", res.StatusCode)
	}
}
