package largebody_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type countCallOnlyCounter struct{}

func (countCallOnlyCounter) CountCall(ctx context.Context, in any) error {
	return nil
}

type stubWireCounter struct {
	countRes largebody.WireCountResult
	countErr error
	called   bool
	delay    time.Duration
}

func (s *stubWireCounter) CountWire(ctx context.Context, in largebody.WireCountInput) (largebody.WireCountResult, error) {
	s.called = true
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return largebody.WireCountResult{}, ctx.Err()
		}
	}
	if s.countErr != nil {
		return largebody.WireCountResult{}, s.countErr
	}
	return s.countRes, nil
}

type stubWireCounterProvider struct {
	counter *stubWireCounter
}

func (p stubWireCounterProvider) AsWireCounter() largebody.WireCounter {
	return p.counter
}

type memorySource struct {
	data []byte
}

func (m memorySource) Size() int64 {
	return int64(len(m.data))
}

func (m memorySource) Open() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(m.data))), nil
}

func (m memorySource) Close() error {
	return nil
}

func TestWireCounter_AsWireCounter(t *testing.T) {
	t.Parallel()

	if _, ok := largebody.AsWireCounter(nil); ok {
		t.Fatal("nil value must report ok=false")
	}
	if _, ok := largebody.AsWireCounter(countCallOnlyCounter{}); ok {
		t.Fatal("CountCall-only counter must report ok=false")
	}

	direct := &stubWireCounter{}
	if wc, ok := largebody.AsWireCounter(direct); !ok || wc != direct {
		t.Fatalf("direct WireCounter must report ok=true, got ok=%v", ok)
	}

	provider := stubWireCounterProvider{counter: direct}
	if wc, ok := largebody.AsWireCounter(provider); !ok || wc != direct {
		t.Fatalf("WireCounterProvider must report ok=true, got ok=%v", ok)
	}
}

func TestWireCounter_ExactTokenizerSemantics(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		semantics largebody.ExactTokenizerSemantics
		wantExact bool
	}{
		{
			name:      "zero value is not exact",
			semantics: largebody.ExactTokenizerSemantics{},
			wantExact: false,
		},
		{
			name:      "exact with empty tokenizer id is not exact",
			semantics: largebody.ExactTokenizerSemantics{TokenizerID: "", Exact: true},
			wantExact: false,
		},
		{
			name:      "tokenizer id with exact=false is not exact",
			semantics: largebody.ExactTokenizerSemantics{TokenizerID: "cl100k_base", Exact: false},
			wantExact: false,
		},
		{
			name:      "exact cl100k_base is exact",
			semantics: largebody.ExactTokenizerSemantics{TokenizerID: "cl100k_base", Exact: true},
			wantExact: true,
		},
		{
			name:      "exact o200k_base is exact",
			semantics: largebody.ExactTokenizerSemantics{TokenizerID: "o200k_base", Exact: true},
			wantExact: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.semantics.IsExact(); got != tc.wantExact {
				t.Fatalf("IsExact()=%v want %v", got, tc.wantExact)
			}
		})
	}
}

func TestWireCounter_EvaluateWireTokenCounting_AccountingDisabled(t *testing.T) {
	t.Parallel()

	// When accounting is disabled, wire proceeds immediately with no counting cost
	res := largebody.EvaluateWireTokenCounting(context.Background(), largebody.WireTokenCountGateInput{
		AccountingEnabled: false,
	})
	if !res.Proceed {
		t.Fatalf("disabled accounting must proceed, got false (reason=%v)", res.DeclineReason)
	}
	if res.DeclineReason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", res.DeclineReason)
	}
}

func TestWireCounter_EvaluateWireTokenCounting_CountCallOnlyDeclinesUnderSamePermit(t *testing.T) {
	t.Parallel()

	src := memorySource{data: []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)}
	res := largebody.EvaluateWireTokenCounting(context.Background(), largebody.WireTokenCountGateInput{
		Source:            src,
		Model:             "gpt-4o",
		Counter:           countCallOnlyCounter{}, // CountCall only, no WireCounter
		Semantics:         largebody.ExactTokenizerSemantics{TokenizerID: "o200k_base", Exact: true},
		AccountingEnabled: true,
	})
	if res.Proceed {
		t.Fatal("CountCall-only counter must decline wire mode")
	}
	if res.DeclineReason != largebody.DeclineReasonCountingUnsupported {
		t.Fatalf("expected DeclineReasonCountingUnsupported, got %v", res.DeclineReason)
	}
}

func TestWireCounter_EvaluateWireTokenCounting_InexactTokenizerDeclinesUnderSamePermit(t *testing.T) {
	t.Parallel()

	src := memorySource{data: []byte(`{"model":"unsupported-model"}`)}
	wireCounter := &stubWireCounter{
		countRes: largebody.WireCountResult{InputTokens: 42},
	}

	testCases := []struct {
		name      string
		semantics largebody.ExactTokenizerSemantics
	}{
		{
			name:      "empty semantics",
			semantics: largebody.ExactTokenizerSemantics{},
		},
		{
			name:      "inexact estimator",
			semantics: largebody.ExactTokenizerSemantics{TokenizerID: "local_estimator", Exact: false},
		},
		{
			name:      "empty tokenizer id with exact=true",
			semantics: largebody.ExactTokenizerSemantics{TokenizerID: "  ", Exact: true},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := largebody.EvaluateWireTokenCounting(context.Background(), largebody.WireTokenCountGateInput{
				Source:            src,
				Model:             "unsupported-model",
				Counter:           wireCounter,
				Semantics:         tc.semantics,
				AccountingEnabled: true,
			})
			if res.Proceed {
				t.Fatal("inexact tokenizer semantics must decline wire mode")
			}
			if res.DeclineReason != largebody.DeclineReasonCountingUnsupported {
				t.Fatalf("expected DeclineReasonCountingUnsupported, got %v", res.DeclineReason)
			}
			if wireCounter.called {
				t.Fatal("WireCounter must NOT be called when tokenizer semantics are inexact")
			}
		})
	}
}

func TestWireCounter_EvaluateWireTokenCounting_UnboundedReplayDeclinesUnderSamePermit(t *testing.T) {
	t.Parallel()

	// Payload larger than max scan bytes
	bigData := make([]byte, 1024*1024)
	src := memorySource{data: bigData}
	wireCounter := &stubWireCounter{
		countRes: largebody.WireCountResult{InputTokens: 100},
	}

	res := largebody.EvaluateWireTokenCounting(context.Background(), largebody.WireTokenCountGateInput{
		Source:            src,
		Model:             "gpt-4o",
		Counter:           wireCounter,
		Semantics:         largebody.ExactTokenizerSemantics{TokenizerID: "o200k_base", Exact: true},
		MaxScanBytes:      64 * 1024, // 64 KiB ceiling
		AccountingEnabled: true,
	})
	if res.Proceed {
		t.Fatal("unbounded/expensive replay must decline wire mode")
	}
	if res.DeclineReason != largebody.DeclineReasonCountingUnsupported {
		t.Fatalf("expected DeclineReasonCountingUnsupported, got %v", res.DeclineReason)
	}
	if wireCounter.called {
		t.Fatal("WireCounter must NOT be called when replay exceeds scan budget")
	}
}

func TestWireCounter_EvaluateWireTokenCounting_CPUTimeoutDeclinesUnderSamePermit(t *testing.T) {
	t.Parallel()

	src := memorySource{data: []byte(`{"model":"gpt-4o"}`)}
	wireCounter := &stubWireCounter{
		countRes: largebody.WireCountResult{InputTokens: 10},
		delay:    500 * time.Millisecond,
	}

	res := largebody.EvaluateWireTokenCounting(context.Background(), largebody.WireTokenCountGateInput{
		Source:            src,
		Model:             "gpt-4o",
		Counter:           wireCounter,
		Semantics:         largebody.ExactTokenizerSemantics{TokenizerID: "o200k_base", Exact: true},
		MaxPermitHoldCPU:  20 * time.Millisecond, // tight CPU timeout under permit
		AccountingEnabled: true,
	})
	if res.Proceed {
		t.Fatal("exceeded CPU budget must decline wire mode")
	}
	if res.DeclineReason != largebody.DeclineReasonCountingUnsupported {
		t.Fatalf("expected DeclineReasonCountingUnsupported, got %v", res.DeclineReason)
	}
}

func TestWireCounter_EvaluateWireTokenCounting_ExactCounterProceedsWithMeasuredCPU(t *testing.T) {
	t.Parallel()

	bodyBytes := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"count this wire"}]}`)
	src := memorySource{data: bodyBytes}
	wireCounter := &stubWireCounter{
		countRes: largebody.WireCountResult{
			InputTokens: 15,
			TotalTokens: 15,
			Accounting: aipapi.UsageAccountingMetadata{
				Source:    aipapi.UsageSourceLocalTokenizer,
				Authority: aipapi.UsageAuthorityEstimated,
			},
		},
	}

	res := largebody.EvaluateWireTokenCounting(context.Background(), largebody.WireTokenCountGateInput{
		Source:            src,
		Model:             "gpt-4o",
		Counter:           wireCounter,
		Semantics:         largebody.ExactTokenizerSemantics{TokenizerID: "o200k_base", Exact: true},
		MaxScanBytes:      1024 * 1024,
		MaxPermitHoldCPU:  500 * time.Millisecond,
		AccountingEnabled: true,
	})

	if !res.Proceed {
		t.Fatalf("exact wire counting must proceed, got false: %v (err=%v)", res.DeclineReason, res.Err)
	}
	if res.DeclineReason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", res.DeclineReason)
	}
	if !wireCounter.called {
		t.Fatal("WireCounter must be called")
	}
	if res.Count.InputTokens != 15 {
		t.Fatalf("InputTokens=%d want 15 (bytes must NOT be substituted for tokens)", res.Count.InputTokens)
	}
	if int64(res.Count.InputTokens) == src.Size() {
		t.Fatal("byte count must not equal token count")
	}
	if !res.MeasuredCPU {
		t.Fatal("permit-hold CPU must be measured")
	}
	if res.Duration < 0 {
		t.Fatal("permit-hold CPU duration must be non-negative")
	}
}

func TestWireCounter_ScanningWireCounter_ScansReplayWithoutCallMaterialization(t *testing.T) {
	t.Parallel()

	data := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello world from wire"}]}`
	src := memorySource{data: []byte(data)}

	scanner := &largebody.ScanningWireCounter{
		CountFunc: func(ctx context.Context, model string, r io.Reader) (int, error) {
			// Counts words as tokens for test double; verifies reader is read from replay
			buf, err := io.ReadAll(r)
			if err != nil {
				return 0, err
			}
			if string(buf) != data {
				return 0, errors.New("mismatched stream bytes")
			}
			return 5, nil
		},
	}

	res, err := scanner.CountWire(context.Background(), largebody.WireCountInput{
		Source:      src,
		Model:       "gpt-4o",
		TokenizerID: "o200k_base",
	})
	if err != nil {
		t.Fatalf("CountWire failed: %v", err)
	}
	if res.InputTokens != 5 || res.TotalTokens != 5 {
		t.Fatalf("expected 5 tokens, got input=%d total=%d", res.InputTokens, res.TotalTokens)
	}
	if res.Accounting.Source != aipapi.UsageSourceLocalTokenizer {
		t.Fatalf("expected UsageSourceLocalTokenizer, got %v", res.Accounting.Source)
	}
}
