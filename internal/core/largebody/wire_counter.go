package largebody

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Default ceilings for permit-hold CPU bounding (Requirements 6.8, 15.5, 21.4; Task 10.4).
const (
	// DefaultMaxWireScanBytes bounds the replay size evaluated by a wire token counter
	// under the held decode permit (5 MiB). Replays exceeding this bound decline to
	// canonical processing to avoid unbounded permit-hold latency.
	DefaultMaxWireScanBytes int64 = 5 * 1024 * 1024

	// DefaultMaxPermitHoldCPU bounds the wall-clock CPU duration a wire token counter
	// is permitted to hold before declining to canonical processing.
	DefaultMaxPermitHoldCPU time.Duration = 250 * time.Millisecond
)

var (
	// ErrCountCallOnly is returned when the counter only supports CountCall and lacks wire counting.
	ErrCountCallOnly = errors.New("largebody: counter does not support wire replay counting (CountCall only)")

	// ErrInexactTokenizerSemantics is returned when exact tokenizer semantics are not declared.
	ErrInexactTokenizerSemantics = errors.New("largebody: exact tokenizer semantics not declared")

	// ErrUnboundedReplayScan is returned when replay source size exceeds the wire scan budget.
	ErrUnboundedReplayScan = errors.New("largebody: replay source size exceeds wire count budget")

	// ErrPermitHoldCPUExceeded is returned when wire counting times out or exceeds CPU budget.
	ErrPermitHoldCPUExceeded = errors.New("largebody: wire counting timed out or exceeded permit-hold CPU budget")
)

// WireCountInput supplies bounded facts and the immutable replay source to a WireCounter
// (Requirement 15.5; Task 10.4). Prompt text or a full aipapi.Call is never passed here.
type WireCountInput struct {
	Source      Source
	Model       string
	Backend     string
	CallID      string
	ProfileID   string
	TokenizerID string
}

// WireCountResult is the bounded result of exact wire token counting.
// Raw body bytes are NEVER substituted for tokens (Requirement 15.5).
type WireCountResult struct {
	InputTokens int
	TotalTokens int
	Accounting  aipapi.UsageAccountingMetadata
}

// WireCounter is the optional contract for counting tokens directly from an
// immutable replay source without constructing a full aipapi.Call or materializing
// prompt text (Requirements 6.2, 15.5, 21; Task 10.4).
type WireCounter interface {
	CountWire(ctx context.Context, in WireCountInput) (WireCountResult, error)
}

// WireCounterProvider is an optional interface for types that adapt into a WireCounter.
type WireCounterProvider interface {
	AsWireCounter() WireCounter
}

// AsWireCounter probes a value for the internal WireCounter capability.
func AsWireCounter(v any) (WireCounter, bool) {
	if v == nil {
		return nil, false
	}
	if wc, ok := v.(WireCounter); ok && wc != nil {
		return wc, true
	}
	if p, ok := v.(WireCounterProvider); ok && p != nil {
		wc := p.AsWireCounter()
		if wc != nil {
			return wc, true
		}
	}
	return nil, false
}

// ExactTokenizerSemantics declares whether exact tokenizer semantics are proven
// for an effective model under a profile/backend (Requirement 15.5; Task 10.4).
type ExactTokenizerSemantics struct {
	TokenizerID string
	Exact       bool
}

// IsExact reports whether exact tokenizer semantics are proven.
func (s ExactTokenizerSemantics) IsExact() bool {
	return s.Exact && strings.TrimSpace(s.TokenizerID) != ""
}

// ExactTokenizerDeclarer is an optional interface implemented by profiles, backends,
// or model catalogs to declare exact tokenizer semantics for an effective model (Task 10.4).
type ExactTokenizerDeclarer interface {
	ExactTokenizerSemantics(model string) (ExactTokenizerSemantics, bool)
}

// WireTokenCountGateInput supplies the parameters evaluated by the exact wire counter gate.
type WireTokenCountGateInput struct {
	Source            Source
	Model             string
	Backend           string
	CallID            string
	ProfileID         string
	Counter           any // can be WireCounter, preflight.Counter, app.LocalCounter, etc.
	Semantics         ExactTokenizerSemantics
	MaxScanBytes      int64         // Maximum bytes allowed to scan; <= 0 uses DefaultMaxWireScanBytes
	MaxPermitHoldCPU  time.Duration // CPU time budget; <= 0 uses DefaultMaxPermitHoldCPU
	AccountingEnabled bool
}

// WireTokenCountGateResult is the bounded outcome of the exact wire counter gate.
type WireTokenCountGateResult struct {
	Proceed       bool
	DeclineReason DeclineReason
	Count         WireCountResult
	Duration      time.Duration
	MeasuredCPU   bool
	Err           error
}

// EvaluateWireTokenCounting executes the exact wire counter gate (Task 10.4):
//  1. If accounting is not enabled, wire proceeds immediately with zero counting cost (Requirement 15.4).
//  2. If accounting is enabled and only CountCall exists (no WireCounter), dynamic assessment
//     declines under the same permit with DeclineReasonCountingUnsupported (Requirement 15.5).
//  3. Only when exact tokenizer semantics exist for the effective model does wire proceed,
//     else same-permit canonical decline with DeclineReasonCountingUnsupported (Requirement 15.5).
//  4. Permit-hold CPU is bounded and measured; if exact counting is expensive/unbounded
//     (e.g. source size exceeds MaxScanBytes or duration exceeds budget), dynamic assessment
//     declines under the same permit with DeclineReasonCountingUnsupported (Requirements 6.8, 21.13).
//  5. Prompt text or aipapi.Call is NEVER materialized for CountCall; raw bytes are NEVER substituted for tokens.
func EvaluateWireTokenCounting(ctx context.Context, in WireTokenCountGateInput) WireTokenCountGateResult {
	if !in.AccountingEnabled {
		return WireTokenCountGateResult{
			Proceed:       true,
			DeclineReason: DeclineReasonNone,
		}
	}

	if in.Source == nil {
		return WireTokenCountGateResult{
			Proceed:       false,
			DeclineReason: DeclineReasonCountingUnsupported,
			Err:           errors.New("largebody: replay source is nil"),
		}
	}

	wc, ok := AsWireCounter(in.Counter)
	if !ok || wc == nil {
		// Only CountCall exists (or nil counter) -> decline under same permit
		return WireTokenCountGateResult{
			Proceed:       false,
			DeclineReason: DeclineReasonCountingUnsupported,
			Err:           ErrCountCallOnly,
		}
	}

	// Probe exact tokenizer semantics
	semantics := in.Semantics
	if !semantics.IsExact() {
		if declarer, ok := in.Counter.(ExactTokenizerDeclarer); ok && declarer != nil {
			if s, found := declarer.ExactTokenizerSemantics(in.Model); found {
				semantics = s
			}
		}
	}
	if !semantics.IsExact() {
		// Profile / backend does not declare exact tokenizer semantics for the effective model
		return WireTokenCountGateResult{
			Proceed:       false,
			DeclineReason: DeclineReasonCountingUnsupported,
			Err:           fmt.Errorf("%w for model %q", ErrInexactTokenizerSemantics, in.Model),
		}
	}

	// Enforce bounded permit-hold scan bytes
	maxScanBytes := in.MaxScanBytes
	if maxScanBytes <= 0 {
		maxScanBytes = DefaultMaxWireScanBytes
	}
	if in.Source.Size() > maxScanBytes {
		// Expensive / unbounded -> decline under same permit
		return WireTokenCountGateResult{
			Proceed:       false,
			DeclineReason: DeclineReasonCountingUnsupported,
			Err:           fmt.Errorf("%w: replay source size %d exceeds wire count budget %d", ErrUnboundedReplayScan, in.Source.Size(), maxScanBytes),
		}
	}

	maxCPU := in.MaxPermitHoldCPU
	if maxCPU <= 0 {
		maxCPU = DefaultMaxPermitHoldCPU
	}

	countCtx := ctx
	var cancel context.CancelFunc
	if countCtx == nil {
		countCtx = context.Background()
	}
	countCtx, cancel = context.WithTimeout(countCtx, maxCPU)
	defer cancel()

	start := time.Now()
	res, err := wc.CountWire(countCtx, WireCountInput{
		Source:      in.Source,
		Model:       in.Model,
		Backend:     in.Backend,
		CallID:      in.CallID,
		ProfileID:   in.ProfileID,
		TokenizerID: semantics.TokenizerID,
	})
	elapsed := time.Since(start)

	if err != nil {
		countErr := fmt.Errorf("largebody: wire counting failed: %w", err)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			countErr = fmt.Errorf("%w: %v", ErrPermitHoldCPUExceeded, err)
		}
		return WireTokenCountGateResult{
			Proceed:       false,
			DeclineReason: DeclineReasonCountingUnsupported,
			Duration:      elapsed,
			MeasuredCPU:   true,
			Err:           countErr,
		}
	}

	if res.InputTokens < 0 {
		return WireTokenCountGateResult{
			Proceed:       false,
			DeclineReason: DeclineReasonCountingUnsupported,
			Duration:      elapsed,
			MeasuredCPU:   true,
			Err:           fmt.Errorf("largebody: wire count returned negative input tokens: %d", res.InputTokens),
		}
	}

	return WireTokenCountGateResult{
		Proceed:       true,
		DeclineReason: DeclineReasonNone,
		Count:         res,
		Duration:      elapsed,
		MeasuredCPU:   true,
	}
}

// NewScanningWireCounter constructs a ScanningWireCounter with the given count function.
func NewScanningWireCounter(fn func(ctx context.Context, model string, r io.Reader) (int, error)) *ScanningWireCounter {
	return &ScanningWireCounter{CountFunc: fn}
}

// ScanningWireCounter implements WireCounter by streaming replay bytes through
// a token counting function without constructing a full aipapi.Call.
type ScanningWireCounter struct {
	CountFunc func(ctx context.Context, model string, r io.Reader) (int, error)
}

// CountWire scans replay bytes from the source and emits exact token counts.
func (s *ScanningWireCounter) CountWire(ctx context.Context, in WireCountInput) (WireCountResult, error) {
	if s == nil || s.CountFunc == nil {
		return WireCountResult{}, errors.New("largebody: scanning wire counter function is nil")
	}
	if in.Source == nil {
		return WireCountResult{}, errors.New("largebody: replay source is nil")
	}
	rc, err := in.Source.Open()
	if err != nil {
		return WireCountResult{}, fmt.Errorf("largebody: open replay source: %w", err)
	}
	defer func() {
		_ = rc.Close()
	}()

	tokens, err := s.CountFunc(ctx, in.Model, rc)
	if err != nil {
		return WireCountResult{}, err
	}
	return WireCountResult{
		InputTokens: tokens,
		TotalTokens: tokens,
		Accounting: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceLocalTokenizer,
			Authority: aipapi.UsageAuthorityEstimated,
			Tokenizer: aipapi.TokenizerRef{
				Type: "local_tokenizer",
				ID:   in.TokenizerID,
			},
		},
	}, nil
}
