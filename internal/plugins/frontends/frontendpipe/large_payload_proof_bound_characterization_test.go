package frontendpipe_test

import (
	"strings"
	"testing"
)

// Task 19.3 / Remediation Phase 0 Characterization Tests:
// Target Invariant: Proof-time transient allocation (B/op) must be bounded by
// memory_spool_bytes (64 KiB) + max_semantic_fact_bytes (256 KiB) + fixed buffers (128 KiB).
//
// These tests are designed to be RED against the current io.ReadAll implementation
// across all three lanes (OpenAI Responses, OpenAI Chat Legacy, OpenResponses),
// and will turn GREEN once Phase 1 streaming proof and Phase 2 lane rework are in place.

const (
	targetInvariantMemorySpoolBytes   = int64(64 << 10)                                                                                      // 64 KiB
	targetInvariantMaxSemanticFact    = int64(256 << 10)                                                                                     // 256 KiB
	targetInvariantFixedBuffersBudget = int64(128 << 10)                                                                                     // 128 KiB
	maxAllowedProofBytesPerOp         = targetInvariantMemorySpoolBytes + targetInvariantMaxSemanticFact + targetInvariantFixedBuffersBudget // 448 KiB (458,752 B)
)

func baselineOpenResponsesBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const prefix = `{"model":"stub:bench","store":false,"input":"`
	const suffix = `"}`
	pad := target - len(prefix) - len(suffix)
	if pad < 0 {
		tb.Fatalf("target %d smaller than envelope %d", target, len(prefix)+len(suffix))
	}
	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	b.WriteString(strings.Repeat("a", pad))
	b.WriteString(suffix)
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("openresponses body len=%d want %d", len(out), target)
	}
	return out
}

func bench20MiBChatBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const numParts = 4
	const prefix = `{"model":"stub:bench","messages":[`
	const msgPrefix = `{"role":"user","content":"`
	const msgSuffix = `"}`
	const suffix = `]}`
	fixedLen := len(prefix) + len(suffix) + numParts*(len(msgPrefix)+len(msgSuffix)) + (numParts - 1)
	totalPad := target - fixedLen
	if totalPad < 0 {
		tb.Fatalf("target %d too small", target)
	}
	padPerPart := totalPad / numParts
	remainder := totalPad % numParts

	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	for i := range numParts {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(msgPrefix)
		p := padPerPart
		if i == 0 {
			p += remainder
		}
		b.WriteString(strings.Repeat("a", p))
		b.WriteString(msgSuffix)
	}
	b.WriteString(suffix)
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("chat chunked body len=%d want %d", len(out), target)
	}
	return out
}

func bench20MiBOpenResponsesBody(tb testing.TB, target int) []byte {
	tb.Helper()
	const numParts = 4
	const prefix = `{"model":"stub:bench","store":false,"input":[`
	const msgPrefix = `{"role":"user","content":"`
	const msgSuffix = `"}`
	const suffix = `]}`
	fixedLen := len(prefix) + len(suffix) + numParts*(len(msgPrefix)+len(msgSuffix)) + (numParts - 1)
	totalPad := target - fixedLen
	if totalPad < 0 {
		tb.Fatalf("target %d too small", target)
	}
	padPerPart := totalPad / numParts
	remainder := totalPad % numParts

	var b strings.Builder
	b.Grow(target)
	b.WriteString(prefix)
	for i := range numParts {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(msgPrefix)
		p := padPerPart
		if i == 0 {
			p += remainder
		}
		b.WriteString(strings.Repeat("a", p))
		b.WriteString(msgSuffix)
	}
	b.WriteString(suffix)
	out := []byte(b.String())
	if len(out) != target {
		tb.Fatalf("openresponses chunked body len=%d want %d", len(out), target)
	}
	return out
}
