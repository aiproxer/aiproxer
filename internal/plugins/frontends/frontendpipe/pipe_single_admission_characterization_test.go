package frontendpipe_test

// Task 1.3 characterization: freeze the shared-pipe admission seams that a
// future large-payload candidate lane must preserve (requirements 1, 2, 6, 13;
// design 1, 2, 8, 16).
//
// Reuses pipe_ordering_test.go for header-wins, guarded RouteFromBodyModel,
// resolver-before-preflight, and success-path single-decision ordering; this
// file adds only the gaps:
//   - the current canonical path applies at most one TryAdmit decision per
//     considered request, including terminal Spec.Decode failure; true
//     proof/assessment-decline same-permit fallback is owned by Tasks 7.6/11.9
//     and is explicitly out of scope here),
//   - overweight weight-mapping end to end with a real Limiter,
//   - canceled-request preflight ownership (503, no admission),
//   - gzip canonical parity (decompressed bytes drive preflight/admission/decode).
//
// No production fast path exists yet; test-only, no feature flag.

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
)

func TestPipeSingleAdmission_DecodeFailureStillSingleTryAdmit(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)
	decodeErr := errors.New("protocol decode failed")
	spec.Decode = func(frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
		log.add("decode")
		return nil, decodeErr
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"model":"stub:body-model"}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s want 400 decode failure", rec.Code, rec.Body.String())
	}
	if adm.calls != 1 {
		t.Fatalf("TryAdmit calls=%d want exactly 1 (no second decision on post-admission failure)", adm.calls)
	}
	if adm.releases != 1 {
		t.Fatalf("releases=%d want exactly 1 (Guard releases the one held permit)", adm.releases)
	}
	if exec.called {
		t.Fatal("executor ran after decode failure")
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After=%q want absent (decode failure is not an admission reject)", got)
	}
}

func TestPipeSingleAdmission_OverweightBodyMapsTo429WithRetryAfter(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	obs := &orderingObserver{log: log}
	// Real limiter: weight domain is exact decoded bytes. Body is 13 decoded
	// bytes, budget is 12, so TryAcquire fails overweight even though the
	// request-body ceiling (default 8 MiB) passes.
	body := `{"a":"12345"}`
	if len(body) != 13 {
		t.Fatalf("fixture body len=%d want 13", len(body))
	}
	spec := newOrderingSpec(log, exec, &orderingAdmission{log: &orderingLog{}}, obs)
	spec.DecodeAdmission = decodeqos.New(10, 12)
	decoded := false
	origDecode := spec.Decode
	_ = origDecode
	spec.Decode = func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
		decoded = true
		return nil, errors.New("must not decode after overweight reject")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s want 429 overweight", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != decodeqos.RetryAfterSeconds {
		t.Fatalf("Retry-After=%q want %q", got, decodeqos.RetryAfterSeconds)
	}
	if decoded {
		t.Fatal("Decode ran after overweight admission reject")
	}
	if exec.called {
		t.Fatal("executor ran after overweight admission reject")
	}
}

func TestPipeSingleAdmission_CanceledPreflightSkipsAdmission(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"a":1}`))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s want 503 preflight canceled", rec.Code, rec.Body.String())
	}
	if adm.calls != 0 {
		t.Fatalf("TryAdmit calls=%d want 0 (canceled preflight precedes admission)", adm.calls)
	}
	if exec.called {
		t.Fatal("executor ran after canceled preflight")
	}
}

func TestPipeSingleAdmission_GzipCanonicalParity(t *testing.T) {
	t.Parallel()
	plain := `{"model":"stub:body-model","x":1}`
	var compressed bytes.Buffer
	gw := gzip.NewWriter(&compressed)
	if _, err := gw.Write([]byte(plain)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	for _, tc := range []struct {
		name    string
		encode  bool
		wantSel string
	}{
		{name: "identity", encode: false, wantSel: "decode:stub:body-model"},
		{name: "gzip", encode: true, wantSel: "decode:stub:body-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log := &orderingLog{}
			exec := &orderingExec{log: log}
			adm := &orderingAdmission{log: log}
			obs := &orderingObserver{log: log}
			spec := newOrderingSpec(log, exec, adm, obs)

			var req *http.Request
			if tc.encode {
				req = httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(compressed.Bytes()))
				req.Header.Set("Content-Encoding", "gzip")
			} else {
				req = httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(plain))
			}
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(&spec, rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			found := false
			for _, ev := range log.snapshot() {
				if ev == tc.wantSel {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %q in %q", tc.wantSel, log.snapshot())
			}
			if len(adm.weights) != 1 || adm.weights[0] != int64(len(plain)) {
				t.Fatalf("admit weights=%v want [%d] (decoded bytes govern, never compressed length)", adm.weights, len(plain))
			}
			if adm.calls != 1 || adm.releases != 1 {
				t.Fatalf("admit=%d releases=%d want 1/1", adm.calls, adm.releases)
			}
		})
	}
}
