package diag_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestAttemptsHandler_missingParam(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ah, err := diag.AttemptsHandler(st)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ah)
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestAttemptsHandler_notFound(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ah, err := diag.AttemptsHandler(st)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ah)
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"?a_leg_id=does-not-exist", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestAttemptsHandler_typedNilStoreRejected(t *testing.T) {
	t.Parallel()
	var st *b2bua.MemoryStore
	var loader diag.AttemptLoader = st
	_, err := diag.AttemptsHandler(loader)
	if err == nil {
		t.Fatal("expected error for typed-nil AttemptLoader")
	}
}

type fakeAttemptLoader struct {
	byALeg map[string][]aipapi.AttemptRecord
}

func (f *fakeAttemptLoader) LoadAttempts(_ context.Context, aLegID string) ([]aipapi.AttemptRecord, error) {
	rows, ok := f.byALeg[aLegID]
	if !ok {
		return nil, b2bua.ErrALegNotFound
	}
	return rows, nil
}

var errFailsWrite = errors.New("fail response write")

type failResponseWriter struct {
	hdr http.Header
}

func (f *failResponseWriter) Header() http.Header {
	if f.hdr == nil {
		f.hdr = make(http.Header)
	}
	return f.hdr
}

func (*failResponseWriter) WriteHeader(int) {}

func (*failResponseWriter) Write([]byte) (int, error) {
	return 0, errFailsWrite
}

//nolint:paralleltest // mutates slog default logger; not safe with other tests in parallel.
func TestAttemptsHandler_encodeError_logsOnce(t *testing.T) {
	// Uses slog.SetDefault — not parallel-safe with other tests touching the default logger.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError}))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	now := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	loader := &fakeAttemptLoader{byALeg: map[string][]aipapi.AttemptRecord{"a1": {{
		BLegID: "b1", ALegID: "a1", Seq: 1, BackendID: "be",
		EffectiveModel: "m", StartedAt: now, FinishedAt: now, Outcome: aipapi.AttemptSuccess,
	}}}}
	ah, err := diag.AttemptsHandler(loader)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/?a_leg_id=a1", nil)
	req = req.WithContext(context.Background())

	ah.ServeHTTP(&failResponseWriter{}, req)

	if !strings.Contains(logBuf.String(), "diag: attempts encode") {
		t.Fatalf("expected log line with diag: attempts encode, got %q", logBuf.String())
	}
}

func TestAttemptsHandler_fakeAttemptLoaderJSON(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	want := aipapi.AttemptRecord{
		BLegID: "b1", ALegID: "a1", Seq: 1, BackendID: "be",
		EffectiveModel: "m", StartedAt: now, FinishedAt: now, Outcome: aipapi.AttemptSuccess,
	}
	loader := &fakeAttemptLoader{byALeg: map[string][]aipapi.AttemptRecord{"a1": {want}}}
	ah, err := diag.AttemptsHandler(loader)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ah)
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"?a_leg_id=a1", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	var rows []aipapi.AttemptRecord
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].BLegID != want.BLegID || rows[0].ALegID != want.ALegID {
		t.Fatalf("got %+v", rows)
	}
}

func TestAttemptsHandler_ok(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateALeg(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.NextBLeg(ctx, a.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	rec := aipapi.AttemptRecord{
		BLegID:         b.BLegID,
		ALegID:         a.ALegID,
		Seq:            b.Seq,
		BackendID:      "openai",
		EffectiveModel: "gpt-4",
		StartedAt:      now,
		FinishedAt:     now,
		Outcome:        aipapi.AttemptSuccess,
		Reason:         "",
	}
	if err := st.RecordAttempt(ctx, rec); err != nil {
		t.Fatal(err)
	}
	ah, err := diag.AttemptsHandler(st)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ah)
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"?a_leg_id="+a.ALegID, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type: %q", ct)
	}
	var rows []aipapi.AttemptRecord
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows)=%d", len(rows))
	}
	if rows[0].BLegID != b.BLegID || rows[0].Seq != b.Seq {
		t.Fatalf("row: %+v", rows[0])
	}
}
