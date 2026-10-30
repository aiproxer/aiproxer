package openaichat

import (
	"fmt"
	"sync"
)

// RequestValidator inspects one backend-bound request body after the proxy transform.
// It must return content-safe structural errors only (no payload/reasoning text).
type RequestValidator func(body []byte) error

// OracleLedger is a concurrency-safe, precomputed per-request validator ledger.
// The HTTP handler must invoke Hook on a cloned body before the scripted response.
// It never calls testing.T and does not retain or log request payloads.
type OracleLedger struct {
	mu         sync.Mutex
	validators []RequestValidator
	err        error
	count      int
}

// NewOracleLedger builds a ledger with one validator per expected backend request, in order.
func NewOracleLedger(validators ...RequestValidator) *OracleLedger {
	cp := make([]RequestValidator, len(validators))
	copy(cp, validators)
	return &OracleLedger{validators: cp}
}

// Hook returns an OnRequestBody callback suitable for Config.OnRequestBody.
func (l *OracleLedger) Hook() func([]byte) {
	if l == nil {
		return func([]byte) {}
	}
	return l.Observe
}

// Observe validates the next request body. Safe for concurrent handler goroutines.
// Records only the first error. Excess requests beyond precomputed validators are errors.
func (l *OracleLedger) Observe(body []byte) {
	if l == nil {
		return
	}
	// Defensive clone so validators cannot alias caller-owned buffers across requests.
	cloned := append([]byte(nil), body...)

	l.mu.Lock()
	defer l.mu.Unlock()
	idx := l.count
	l.count++
	if idx >= len(l.validators) {
		if l.err == nil {
			l.err = fmt.Errorf("openaichat oracle: structural mismatch: unexpected_request index=%d", idx)
		}
		return
	}
	v := l.validators[idx]
	if v == nil {
		return
	}
	if err := v(cloned); err != nil && l.err == nil {
		l.err = err
	}
}

// Err returns the first content-safe validation error, if any.
// Intended to be read from the test goroutine after requests complete.
func (l *OracleLedger) Err() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Count returns how many backend requests were observed.
func (l *OracleLedger) Count() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}
