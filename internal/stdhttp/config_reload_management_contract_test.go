package stdhttp

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

// Task 1.5 management goldens (req 1.7, 11.6, 12.1-12.11).

func newManagementHarness(t *testing.T, reloadFn func(context.Context, ReloadTrigger) ReloadResult) (*httptest.Server, *RefReloadCoordinator, *RefConfigReloadManagement) {
	t.Helper()
	coord := NewRefReloadCoordinator("/fixed/startup/config.yaml", reloadFn)
	h := NewRefConfigReloadManagement(coord)
	h.BearerToken = "test-secret"
	srv := httptest.NewServer(h.Handler())
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
		srv, _, _ := newManagementHarness(t, func(context.Context, ReloadTrigger) ReloadResult {
			return ReloadResult{Category: ReloadCategoryPublished}
		})
		res, err := http.Post(srv.URL+ConfigReloadPath, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		assert.NoError(t, res.Body.Close())
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", res.StatusCode)
		}
	})

	t.Run("browser_guard", func(t *testing.T) {
		t.Parallel()
		srv, coord, h := newManagementHarness(t, func(context.Context, ReloadTrigger) ReloadResult {
			return ReloadResult{Category: ReloadCategoryPublished}
		})
		before := coord.Status().LastResult.AttemptID
		for _, hdr := range []map[string]string{
			{"Origin": "https://evil.example"},
			{"Sec-Fetch-Site": "cross-site"},
			{"Sec-Fetch-Site": "same-site"},
		} {
			req := authReq(http.MethodPost, srv.URL+ConfigReloadPath, "test-secret", strings.NewReader("{}"))
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
		opt, _ := http.NewRequest(http.MethodOptions, srv.URL+ConfigReloadPath, nil)
		ores, err := http.DefaultClient.Do(opt)
		if err != nil {
			t.Fatal(err)
		}
		assert.NoError(t, ores.Body.Close())
		if ores.StatusCode != http.StatusForbidden || coord.Status().LastResult.AttemptID != before {
			t.Fatal("OPTIONS must not trigger reload")
		}
		h.AllowOrigins["https://allowed.example"] = struct{}{}
		req := authReq(http.MethodPost, srv.URL+ConfigReloadPath, "test-secret", strings.NewReader("{}"))
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

	t.Run("fixed_source_method_body", func(t *testing.T) {
		t.Parallel()
		srv, coord, _ := newManagementHarness(t, func(context.Context, ReloadTrigger) ReloadResult {
			return ReloadResult{Category: ReloadCategoryPublished}
		})
		req := authReq(http.MethodGet, srv.URL+ConfigReloadPath, "test-secret", nil)
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
			{"application/json", `{"foo":1}`},
			{"text/yaml", "x: 1"},
		} {
			req := authReq(http.MethodPost, srv.URL+ConfigReloadPath, "test-secret", strings.NewReader(tc.body))
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
		req = authReq(http.MethodPost, srv.URL+ConfigReloadPath, "test-secret", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		res, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.NoError(t, res.Body.Close())
		if res.StatusCode != http.StatusOK || coord.FixedSourcePath() != "/fixed/startup/config.yaml" {
			t.Fatalf("ok status=%d path=%s", res.StatusCode, coord.FixedSourcePath())
		}
	})

	t.Run("busy_conflict", func(t *testing.T) {
		t.Parallel()
		entered, release := make(chan struct{}), make(chan struct{})
		srv, _, _ := newManagementHarness(t, func(context.Context, ReloadTrigger) ReloadResult {
			close(entered)
			<-release
			return ReloadResult{Category: ReloadCategoryPublished}
		})
		errCh := make(chan int, 1)
		go func() {
			req := authReq(http.MethodPost, srv.URL+ConfigReloadPath, "test-secret", strings.NewReader("{}"))
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
		req := authReq(http.MethodPost, srv.URL+ConfigReloadPath, "test-secret", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body ReloadResult
		_ = json.NewDecoder(res.Body).Decode(&body)
		assert.NoError(t, res.Body.Close())
		if res.StatusCode != http.StatusConflict || body.Category != ReloadCategoryBusy {
			t.Fatalf("busy status=%d cat=%s", res.StatusCode, body.Category)
		}
		close(release)
		if <-errCh != http.StatusOK {
			t.Fatal("first reload")
		}
	})

	t.Run("disconnect_hosted", func(t *testing.T) {
		t.Parallel()
		started, finish, completed := make(chan struct{}), make(chan struct{}), make(chan ReloadResult, 1)
		var mu sync.Mutex
		var sawCancel bool
		srv, coord, _ := newManagementHarness(t, func(ctx context.Context, _ ReloadTrigger) ReloadResult {
			close(started)
			<-finish
			mu.Lock()
			sawCancel = ctx.Err() != nil
			mu.Unlock()
			return ReloadResult{Category: ReloadCategoryNoop}
		})
		coord.SetOnComplete(func(res ReloadResult) { completed <- res })
		ctx, cancel := context.WithCancel(context.Background())
		req := authReq(http.MethodPost, srv.URL+ConfigReloadPath, "test-secret", strings.NewReader("{}"))
		req = req.WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		errCh := make(chan error, 1)
		go func() { _, err := http.DefaultClient.Do(req); errCh <- err }()
		<-started
		cancel()
		<-errCh
		close(finish)
		if (<-completed).Category != ReloadCategoryNoop || coord.Status().LastResult.Category != ReloadCategoryNoop {
			t.Fatal("hosted result lost")
		}
		mu.Lock()
		defer mu.Unlock()
		if sawCancel {
			t.Fatal("client cancel must not cancel host ctx")
		}
	})

	t.Run("status_goldens", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			cat  string
			want int
		}{
			{ReloadCategoryPublished, 200},
			{ReloadCategoryNoop, 200},
			{ReloadCategoryBusy, 409},
			{ReloadCategoryRestartRequired, 409},
			{ReloadCategoryRetentionBlocked, 409},
			{ReloadCategoryInvalid, 422},
			{ReloadCategorySourceIntegrity, 422},
			{ReloadCategoryCanceled, 503},
			{ReloadCategoryPreparationFailed, 503},
			{ReloadCategoryInternalFailed, 503},
		} {
			if httpStatusForReload(tc.cat) != tc.want {
				t.Fatalf("%s => %d", tc.cat, httpStatusForReload(tc.cat))
			}
		}
		srv, coord, _ := newManagementHarness(t, func(context.Context, ReloadTrigger) ReloadResult {
			return ReloadResult{Category: ReloadCategoryRestartRequired, RestartFields: []string{"server.address", "management.auth"}, RestartFieldCount: 2, ReasonCategory: "startup-only-fields"}
		})
		req := authReq(http.MethodPost, srv.URL+ConfigReloadPath, "test-secret", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body ReloadResult
		_ = json.NewDecoder(res.Body).Decode(&body)
		assert.NoError(t, res.Body.Close())
		if res.StatusCode != 409 || body.RestartFieldCount != 2 {
			t.Fatalf("%d %+v", res.StatusCode, body)
		}
		stReq := authReq(http.MethodGet, srv.URL+ConfigStatusPath, "test-secret", nil)
		stRes, err := http.DefaultClient.Do(stReq)
		if err != nil {
			t.Fatal(err)
		}
		var st ReloadStatus
		_ = json.NewDecoder(stRes.Body).Decode(&st)
		assert.NoError(t, stRes.Body.Close())
		if st.FixedSourcePath != coord.FixedSourcePath() || st.LastResult.Category != ReloadCategoryRestartRequired {
			t.Fatalf("%+v", st)
		}
	})

	t.Run("shutdown_rejects", func(t *testing.T) {
		t.Parallel()
		srv, coord, _ := newManagementHarness(t, func(context.Context, ReloadTrigger) ReloadResult {
			return ReloadResult{Category: ReloadCategoryPublished}
		})
		coord.MarkShutdown()
		req := authReq(http.MethodPost, srv.URL+ConfigReloadPath, "test-secret", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		assert.NoError(t, res.Body.Close())
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status=%d", res.StatusCode)
		}
	})
}

func TestProductionManagementConfigReload_Integration(t *testing.T) {
	t.Parallel()
	coord := NewRefReloadCoordinator("/fixed/startup/config.yaml", func(context.Context, ReloadTrigger) ReloadResult {
		return ReloadResult{Category: ReloadCategoryPublished, ActiveGeneration: 2}
	})
	// Production adapter from internal/stdhttp/admin/configreload (task 5.3).
	prod, err := mgmtreload.New(mgmtreload.Options{
		Address:     "127.0.0.1:0",
		AuthMode:    mgmtreload.AuthModeBearer,
		BearerToken: "test-management-secret",
	}, &prodCoordAdapter{coord: coord})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(prod.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+mgmtreload.ReloadPath, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-management-secret")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { assert.NoError(t, res.Body.Close()) }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("production reload status=%d", res.StatusCode)
	}
	stReq, err := http.NewRequest(http.MethodGet, srv.URL+mgmtreload.StatusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	stReq.Header.Set("Authorization", "Bearer test-management-secret")
	stRes, err := http.DefaultClient.Do(stReq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { assert.NoError(t, stRes.Body.Close()) }()
	if stRes.StatusCode != http.StatusOK {
		t.Fatalf("production status=%d", stRes.StatusCode)
	}
}

// prodCoordAdapter adapts the stdhttp ref coordinator to the production
// ReloadCoordinator seam without duplicating compile/publish logic.
type prodCoordAdapter struct {
	coord *RefReloadCoordinator
}

func (a *prodCoordAdapter) Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result {
	res := a.coord.Reload(ctx, ReloadTrigger{
		Kind:       ReloadTriggerKind(trigger.Kind),
		AcceptedAt: trigger.AcceptedAt,
		SafeActor:  trigger.SafeActor,
	})
	return sdkreload.Result{
		Category:           sdkreload.ResultCategory(res.Category),
		AttemptID:          res.AttemptID,
		ActiveGeneration:   res.ActiveGeneration,
		PreviousGeneration: res.PreviousGeneration,
		RestartFields:      res.RestartFields,
		RestartFieldCount:  res.RestartFieldCount,
		ReasonCategory:     res.ReasonCategory,
		CoalescedSignals:   res.CoalescedSignals,
	}
}

func (a *prodCoordAdapter) FixedSourcePath() string {
	return a.coord.FixedSourcePath()
}

func (a *prodCoordAdapter) Status() sdkreload.Status {
	st := a.coord.Status()
	return sdkreload.Status{
		ActiveGeneration: st.ActiveGeneration,
		LastResult: sdkreload.Result{
			Category:           sdkreload.ResultCategory(st.LastResult.Category),
			AttemptID:          st.LastResult.AttemptID,
			ActiveGeneration:   st.LastResult.ActiveGeneration,
			PreviousGeneration: st.LastResult.PreviousGeneration,
			RestartFields:      st.LastResult.RestartFields,
			RestartFieldCount:  st.LastResult.RestartFieldCount,
			ReasonCategory:     st.LastResult.ReasonCategory,
			CoalescedSignals:   st.LastResult.CoalescedSignals,
		},
		Busy: st.Busy,
	}
}
