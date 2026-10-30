package openairesponses

import (
	"fmt"
	"sync"
)

type RequestValidator func(body []byte) error

type OracleLedger struct {
	mu         sync.Mutex
	validators []RequestValidator
	err        error
	count      int
}

func NewOracleLedger(validators ...RequestValidator) *OracleLedger {
	cp := make([]RequestValidator, len(validators))
	copy(cp, validators)
	return &OracleLedger{validators: cp}
}

func (l *OracleLedger) Hook() func([]byte) {
	if l == nil {
		return func([]byte) {}
	}
	return l.Observe
}

func (l *OracleLedger) Observe(body []byte) {
	if l == nil {
		return
	}
	cloned := append([]byte(nil), body...)
	l.mu.Lock()
	defer l.mu.Unlock()
	idx := l.count
	l.count++
	if idx >= len(l.validators) {
		if l.err == nil {
			l.err = fmt.Errorf("openairesponses oracle: structural mismatch: unexpected_request index=%d", idx)
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

func (l *OracleLedger) Err() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

func (l *OracleLedger) Count() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.count
}
