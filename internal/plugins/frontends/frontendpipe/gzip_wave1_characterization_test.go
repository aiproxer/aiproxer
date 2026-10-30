package frontendpipe_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Task 18.1: Prove wave-1 gzip always bypasses candidate capture/profile.
// - Existing decoded-limit/error behavior unchanged.
// - Compressed Content-Length never used as decoded threshold/reservation fact.
// - Requirements: 2, 11; Design: section 14 (Gzip).

type recordingGzipExecutor struct {
	mu                    sync.Mutex
	canonicalExecuteCalls int
	lastCall              *aipapi.Call
	assessCalls           int
	executeLargeBodyCalls int
	staticCalls           int
}

func (e *recordingGzipExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.canonicalExecuteCalls++
	e.lastCall = call
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *recordingGzipExecutor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.assessCalls++
	return largebody.Assessment{}, nil
}

func (e *recordingGzipExecutor) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executeLargeBodyCalls++
	return largebody.ExecutionResult{}, nil
}

func (e *recordingGzipExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error {
	return nil
}
func (e *recordingGzipExecutor) WallClock() func() time.Time { return nil }

func (e *recordingGzipExecutor) LargeBodyStaticDisposition(profileID string) (largebody.StaticWireDisposition, largebody.StaticWireReason) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.staticCalls++
	return largebody.StaticWireNeedsRequestAssessment, largebody.StaticWireReasonNone
}

var (
	_ frontendpipe.StaticDispositionProvider = (*recordingGzipExecutor)(nil)
	_ largebody.LargeBodyExecutor            = (*recordingGzipExecutor)(nil)
)

type recordingGzipProfile struct {
	mu         sync.Mutex
	proofCalls int
}

func (p *recordingGzipProfile) ProfileID() string {
	return "profile_gzip_bypass_test"
}

func (p *recordingGzipProfile) CompileProof(ctx context.Context, in frontendpipe.ProofInput) (frontendpipe.ProofOutput, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.proofCalls++
	return frontendpipe.ProofOutput{}, nil
}

var _ frontendpipe.FrontendProfile = (*recordingGzipProfile)(nil)

type recordingAdmission struct {
	mu       sync.Mutex
	weights  []int64
	releases int
}

func (a *recordingAdmission) TryAcquire(ctx context.Context, weight int64) (func(), bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.weights = append(a.weights, weight)
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.releases++
	}, true, nil
}

func gzipCompressBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func newGzipTestSpec(
	exec *recordingGzipExecutor,
	prof *recordingGzipProfile,
	adm *recordingAdmission,
	spoolDir string,
	ledger *largebody.SpoolLedger,
	onGate func(r *http.Request, res frontendpipe.PreCaptureResult),
	onCap func(r *http.Request, res frontendpipe.CandidateCaptureResult),
	maxBodyBytes int64,
) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:                exec,
			FrontendID:          "gzip_bypass_test",
			MaxRequestBodyBytes: maxBodyBytes,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:          true,
				ThresholdBytes:   1 << 20, // 1 MiB
				MemorySpoolBytes: 64 << 10,
				SpoolDir:         spoolDir,
				SpoolLedger:      ledger,
			},
			DecodeAdmission: adm,
		},
		Wire:    frontendpipe.OpenAIWire{},
		Profile: prof,
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if path == "/v1/create" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return &frontendpipe.Decoded{
				Call: &aipapi.Call{
					ID: "call_gzip_test",
					Messages: []aipapi.Message{{
						Role:  aipapi.RoleUser,
						Parts: []aipapi.Part{aipapi.TextPart("decoded: " + string(dctx.Body))},
					}},
				},
			}, nil
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) struct{} {
			return struct{}{}
		},
		WriteNonStream: func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts struct{}) error {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":"ok"}`))
			return nil
		},
		OnPreCaptureGate:   onGate,
		OnCandidateCapture: onCap,
	}
}

// TestGzipWave1_BypassesCandidateCaptureAndProfile_EndToEnd proves that gzip requests
// always bypass candidate capture, shared scanner, profile proof compilation,
// assessment, and wire execution, flowing directly through canonical reqbody.ReadAll.
func TestGzipWave1_BypassesCandidateCaptureAndProfile_EndToEnd(t *testing.T) {
	t.Parallel()

	spoolDir := t.TempDir()
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      64 << 10,
		MaxInflightSpoolBytes: 10 << 20,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}

	exec := &recordingGzipExecutor{}
	prof := &recordingGzipProfile{}
	adm := &recordingAdmission{}

	var gateRecords []frontendpipe.PreCaptureResult
	var capRecords []frontendpipe.CandidateCaptureResult

	spec := newGzipTestSpec(
		exec, prof, adm, spoolDir, ledger,
		func(r *http.Request, res frontendpipe.PreCaptureResult) {
			gateRecords = append(gateRecords, res)
		},
		func(r *http.Request, res frontendpipe.CandidateCaptureResult) {
			capRecords = append(capRecords, res)
		},
		10<<20, // 10 MiB limit
	)

	plain := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello gzip"}]}`)
	compressed := gzipCompressBytes(t, plain)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(compressed))
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ServeHTTP status = %d, want 200 OK. Body: %s", rec.Code, rec.Body.String())
	}

	// 1. Gate declined at PreCaptureGateGzipWave1 with StaticWireReasonGzipCompressed.
	if len(gateRecords) != 1 {
		t.Fatalf("gateRecords len = %d, want 1", len(gateRecords))
	}
	if gateRecords[0].Candidate {
		t.Fatal("expected candidate to be false for gzip request")
	}
	if gateRecords[0].Gate != frontendpipe.PreCaptureGateGzipWave1 {
		t.Fatalf("gate = %v, want PreCaptureGateGzipWave1", gateRecords[0].Gate)
	}
	if gateRecords[0].Reason != largebody.StaticWireReasonGzipCompressed {
		t.Fatalf("reason = %v, want StaticWireReasonGzipCompressed", gateRecords[0].Reason)
	}

	// 2. Candidate capture was never invoked.
	if len(capRecords) != 0 {
		t.Fatalf("expected 0 capture callbacks, got %d", len(capRecords))
	}

	// 3. No spill files were written to SpoolDir.
	entries, err := os.ReadDir(spoolDir)
	if err != nil {
		t.Fatalf("ReadDir(spoolDir): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 spool files, found %d", len(entries))
	}

	// 4. Logical spool ledger has 0 inflight bytes and 0 active reservations.
	if ledger.InflightBytes() != 0 {
		t.Fatalf("InflightBytes = %d, want 0", ledger.InflightBytes())
	}
	if ledger.ActiveReservations() != 0 {
		t.Fatalf("ActiveReservations = %d, want 0", ledger.ActiveReservations())
	}

	// 5. Fast-path profile proof compilation was never called.
	if prof.proofCalls != 0 {
		t.Fatalf("proofCalls = %d, want 0", prof.proofCalls)
	}

	// 6. Fast-path executor AssessLargeBody and ExecuteLargeBody were never called.
	if exec.assessCalls != 0 {
		t.Fatalf("assessCalls = %d, want 0", exec.assessCalls)
	}
	if exec.executeLargeBodyCalls != 0 {
		t.Fatalf("executeLargeBodyCalls = %d, want 0", exec.executeLargeBodyCalls)
	}

	// 7. Canonical Execute was called exactly once with the decompressed payload.
	if exec.canonicalExecuteCalls != 1 {
		t.Fatalf("canonicalExecuteCalls = %d, want 1", exec.canonicalExecuteCalls)
	}
	if exec.lastCall == nil || len(exec.lastCall.Messages) == 0 {
		t.Fatal("canonical Execute did not receive messages")
	}
	gotText := exec.lastCall.Messages[0].Parts[0].Text
	if !strings.Contains(gotText, "hello gzip") {
		t.Fatalf("expected canonical execute to receive decompressed text, got: %s", gotText)
	}

	// 8. Decode admission weight reflects exact decompressed length, not compressed Content-Length.
	if len(adm.weights) != 1 {
		t.Fatalf("adm.weights len = %d, want 1", len(adm.weights))
	}
	if adm.weights[0] != int64(len(plain)) {
		t.Fatalf("admission weight = %d, want decompressed size %d (compressed was %d)",
			adm.weights[0], len(plain), len(compressed))
	}
	if adm.releases != 1 {
		t.Fatalf("admission releases = %d, want 1", adm.releases)
	}
}

// TestGzipWave1_CompressedContentLengthNeverUsedAsThresholdFact proves:
//  1. A small compressed Content-Length (< threshold) with a large decompressed body (> threshold)
//     is NOT rejected by Gate 2 (known length below threshold) — Gate 3 catches it.
//  2. A large compressed Content-Length (>= threshold) with a small decompressed body (< threshold)
//     is caught by Gate 3 and never reserves the compressed size in SpoolLedger.
//  3. Admission QoS permit weight is always the decompressed byte count.
func TestGzipWave1_CompressedContentLengthNeverUsedAsThresholdFact(t *testing.T) {
	t.Parallel()

	t.Run("compressed length below threshold does not trigger gate 2", func(t *testing.T) {
		t.Parallel()

		// Highly compressible: 2 MiB decompressed > 1 MiB threshold, but ~2 KiB compressed < 1 MiB threshold.
		decompressed := bytes.Repeat([]byte(`{"k":"v"} `), 200000)
		compressed := gzipCompressBytes(t, decompressed)

		const threshold = 1 << 20 // 1 MiB
		if int64(len(compressed)) >= threshold {
			t.Fatalf("fixture error: compressed len %d must be < threshold %d", len(compressed), threshold)
		}
		if int64(len(decompressed)) <= threshold {
			t.Fatalf("fixture error: decompressed len %d must be > threshold %d", len(decompressed), threshold)
		}

		exec := &recordingGzipExecutor{}
		prof := &recordingGzipProfile{}
		spec := newGzipTestSpec(exec, prof, nil, t.TempDir(), nil, nil, nil, 10<<20)

		req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(compressed))
		req.Header.Set("Content-Encoding", "gzip")

		res := frontendpipe.EvaluatePreCaptureGates(&spec, req)

		// MUST be Gate 3 (GzipWave1), NOT Gate 2 (KnownLengthThreshold)!
		if res.Gate != frontendpipe.PreCaptureGateGzipWave1 {
			t.Fatalf("gate = %v, want PreCaptureGateGzipWave1 (compressed Content-Length must not trigger gate 2)", res.Gate)
		}
		if res.Reason != largebody.StaticWireReasonGzipCompressed {
			t.Fatalf("reason = %v, want StaticWireReasonGzipCompressed", res.Reason)
		}
	})

	t.Run("compressed length above threshold never reserves compressed size in ledger", func(t *testing.T) {
		t.Parallel()

		spoolDir := t.TempDir()
		// SpoolLedger with 10 MiB limit
		ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
			MemorySpoolBytes:      64 << 10,
			MaxInflightSpoolBytes: 10 << 20,
		})
		if err != nil {
			t.Fatalf("NewSpoolLedger: %v", err)
		}

		exec := &recordingGzipExecutor{}
		prof := &recordingGzipProfile{}
		adm := &recordingAdmission{}

		spec := newGzipTestSpec(exec, prof, adm, spoolDir, ledger, nil, nil, 10<<20)

		// Small decompressed payload (100 B), but pretend ContentLength is 2 MiB (simulating compressed length).
		plain := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"small"}]}`)
		compressed := gzipCompressBytes(t, plain)

		req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(compressed))
		req.Header.Set("Content-Encoding", "gzip")
		req.Header.Set("Content-Type", "application/json")
		// Explicitly set ContentLength >= 1 MiB threshold to simulate large compressed stream
		req.ContentLength = 2 << 20

		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("ServeHTTP status = %d, want 200 OK. Body: %s", rec.Code, rec.Body.String())
		}

		// Ledger was never charged for 2 MiB
		if ledger.InflightBytes() != 0 {
			t.Fatalf("ledger inflight bytes = %d, want 0", ledger.InflightBytes())
		}

		// Admission weight was for 100 bytes (decompressed), NOT 2 MiB
		if len(adm.weights) != 1 || adm.weights[0] != int64(len(plain)) {
			t.Fatalf("admission weight = %v, want [%d] (never 2 MiB compressed)", adm.weights, len(plain))
		}
	})
}

// TestGzipWave1_SpoolBudgetExhaustionDoesNotBlockGzip proves that because gzip requests
// bypass candidate capture before spool reservation, an exhausted spool budget
// does not cause an ErrSpoolBudgetExhausted decline or error.
func TestGzipWave1_SpoolBudgetExhaustionDoesNotBlockGzip(t *testing.T) {
	t.Parallel()

	// Ledger with 100 byte budget, 100% exhausted
	ledger, err := largebody.NewSpoolLedger(largebody.SpoolBudgetConfig{
		MemorySpoolBytes:      50,
		MaxInflightSpoolBytes: 100,
	})
	if err != nil {
		t.Fatalf("NewSpoolLedger: %v", err)
	}

	res, err := ledger.Reserve(100)
	if err != nil {
		t.Fatalf("Reserve(100): %v", err)
	}
	defer func() { _ = res.Release() }()

	exec := &recordingGzipExecutor{}
	prof := &recordingGzipProfile{}
	adm := &recordingAdmission{}

	spec := newGzipTestSpec(exec, prof, adm, t.TempDir(), ledger, nil, nil, 10<<20)

	plain := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"passes despite spool exhaustion"}]}`)
	compressed := gzipCompressBytes(t, plain)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(compressed))
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(compressed))

	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ServeHTTP status = %d, want 200 OK. Body: %s", rec.Code, rec.Body.String())
	}
	if exec.canonicalExecuteCalls != 1 {
		t.Fatalf("canonicalExecuteCalls = %d, want 1", exec.canonicalExecuteCalls)
	}
}

// TestGzipWave1_ContentEncodingVariationsBypass proves that all forms of gzip
// and compressed Content-Encoding (case-insensitive, whitespace, comma-separated lists,
// and multi-line headers) are caught by Gate 3 and bypass candidate capture.
func TestGzipWave1_ContentEncodingVariationsBypass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setHeaders func(h http.Header)
	}{
		{
			name: "standard lowercase gzip",
			setHeaders: func(h http.Header) {
				h.Set("Content-Encoding", "gzip")
			},
		},
		{
			name: "uppercase GZIP",
			setHeaders: func(h http.Header) {
				h.Set("Content-Encoding", "GZIP")
			},
		},
		{
			name: "gzip with whitespace",
			setHeaders: func(h http.Header) {
				h.Set("Content-Encoding", "  gzip  ")
			},
		},
		{
			name: "comma separated identity then gzip",
			setHeaders: func(h http.Header) {
				h.Set("Content-Encoding", "identity, gzip")
			},
		},
		{
			name: "comma separated gzip then identity",
			setHeaders: func(h http.Header) {
				h.Set("Content-Encoding", "gzip, identity")
			},
		},
		{
			name: "non-gzip compression deflate",
			setHeaders: func(h http.Header) {
				h.Set("Content-Encoding", "deflate")
			},
		},
		{
			name: "multi-line headers identity then gzip",
			setHeaders: func(h http.Header) {
				h.Add("Content-Encoding", "identity")
				h.Add("Content-Encoding", "gzip")
			},
		},
		{
			name: "multi-line headers gzip then custom",
			setHeaders: func(h http.Header) {
				h.Add("Content-Encoding", "gzip")
				h.Add("Content-Encoding", "custom")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			exec := &recordingGzipExecutor{}
			prof := &recordingGzipProfile{}
			spec := newGzipTestSpec(exec, prof, nil, t.TempDir(), nil, nil, nil, 10<<20)

			req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"dummy":1}`))
			req.ContentLength = 2 << 20 // 2 MiB (> threshold)
			tc.setHeaders(req.Header)

			res := frontendpipe.EvaluatePreCaptureGates(&spec, req)

			if res.Candidate {
				t.Fatalf("[%s] expected Candidate=false, got Candidate=true", tc.name)
			}
			if res.Gate != frontendpipe.PreCaptureGateGzipWave1 {
				t.Fatalf("[%s] gate = %v, want PreCaptureGateGzipWave1", tc.name, res.Gate)
			}
			if res.Reason != largebody.StaticWireReasonGzipCompressed {
				t.Fatalf("[%s] reason = %v, want StaticWireReasonGzipCompressed", tc.name, res.Reason)
			}
		})
	}
}

// TestGzipWave1_ExistingDecodedLimitAndErrorBehaviorUnchanged proves:
//  1. Decompression bomb / limit exceeded returns HTTP 413 (WriteBodyTooLarge),
//     with no executor calls and no spool creation.
//  2. Corrupt gzip bytes return HTTP 400 (WriteReadBodyFailed),
//     with no executor calls and no spool creation.
//  3. Decompressed invalid JSON returns HTTP 400 (WriteInvalidJSON).
func TestGzipWave1_ExistingDecodedLimitAndErrorBehaviorUnchanged(t *testing.T) {
	t.Parallel()

	t.Run("decompression bomb exceeding MaxRequestBodyBytes returns 413", func(t *testing.T) {
		t.Parallel()

		spoolDir := t.TempDir()
		exec := &recordingGzipExecutor{}
		prof := &recordingGzipProfile{}

		// Set tight limit: 500 bytes.
		spec := newGzipTestSpec(exec, prof, nil, spoolDir, nil, nil, nil, 500)

		// 2000 bytes decompressed > 500 byte limit, but compresses to ~50 bytes.
		largePayload := bytes.Repeat([]byte("x"), 2000)
		compressed := gzipCompressBytes(t, largePayload)

		req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(compressed))
		req.Header.Set("Content-Encoding", "gzip")
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413 RequestEntityTooLarge. Body: %s", rec.Code, rec.Body.String())
		}
		if exec.canonicalExecuteCalls != 0 {
			t.Fatalf("canonicalExecuteCalls = %d, want 0 on 413", exec.canonicalExecuteCalls)
		}
		if exec.assessCalls != 0 || exec.executeLargeBodyCalls != 0 {
			t.Fatal("fast-path executor called on 413")
		}

		// Ensure no spool artifacts created
		entries, _ := os.ReadDir(spoolDir)
		if len(entries) != 0 {
			t.Fatalf("expected 0 spool files, found %d", len(entries))
		}
	})

	t.Run("corrupted gzip stream returns 400 read failed", func(t *testing.T) {
		t.Parallel()

		spoolDir := t.TempDir()
		exec := &recordingGzipExecutor{}
		prof := &recordingGzipProfile{}

		spec := newGzipTestSpec(exec, prof, nil, spoolDir, nil, nil, nil, 10<<20)

		// Malformed gzip payload (random bytes instead of gzip header)
		corrupt := []byte("this is definitely not a valid gzip stream")

		req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(corrupt))
		req.Header.Set("Content-Encoding", "gzip")
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 BadRequest. Body: %s", rec.Code, rec.Body.String())
		}
		if exec.canonicalExecuteCalls != 0 {
			t.Fatalf("canonicalExecuteCalls = %d, want 0 on corrupt gzip", exec.canonicalExecuteCalls)
		}

		entries, _ := os.ReadDir(spoolDir)
		if len(entries) != 0 {
			t.Fatalf("expected 0 spool files, found %d", len(entries))
		}
	})

	t.Run("invalid JSON after decompression returns 400 invalid JSON", func(t *testing.T) {
		t.Parallel()

		spoolDir := t.TempDir()
		exec := &recordingGzipExecutor{}
		prof := &recordingGzipProfile{}

		spec := newGzipTestSpec(exec, prof, nil, spoolDir, nil, nil, nil, 10<<20)

		// Valid gzip containing invalid JSON
		notJSON := []byte(`{this is not valid json`)
		compressed := gzipCompressBytes(t, notJSON)

		req := httptest.NewRequest(http.MethodPost, "/v1/create", bytes.NewReader(compressed))
		req.Header.Set("Content-Encoding", "gzip")
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 BadRequest. Body: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "invalid request JSON") {
			t.Fatalf("body = %s, want 'invalid request JSON'", rec.Body.String())
		}
		if exec.canonicalExecuteCalls != 0 {
			t.Fatalf("canonicalExecuteCalls = %d, want 0 on invalid JSON", exec.canonicalExecuteCalls)
		}

		entries, _ := os.ReadDir(spoolDir)
		if len(entries) != 0 {
			t.Fatalf("expected 0 spool files, found %d", len(entries))
		}
	})
}
