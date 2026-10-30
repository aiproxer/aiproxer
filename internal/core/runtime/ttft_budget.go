package runtime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type ttftTimeoutScope string

const (
	ttftTimeoutNone   ttftTimeoutScope = ""
	ttftTimeoutGlobal ttftTimeoutScope = "global"
	ttftTimeoutLeaf   ttftTimeoutScope = "leaf"
)

type ttftBudget struct {
	mu            sync.Mutex
	start         time.Time
	global        time.Duration
	done          bool
	leafDeadlines map[string]time.Time
}

func newTTFTBudget(start time.Time, sel *routing.Selector) *ttftBudget {
	if sel == nil || sel.GlobalTTFTTimeout == nil {
		return &ttftBudget{start: start}
	}
	return &ttftBudget{start: start, global: *sel.GlobalTTFTTimeout}
}

func (b *ttftBudget) markCommitted() {
	if b != nil {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.done = true
	}
}

func (b *ttftBudget) deadline(now time.Time, candidateKey string, leaf *time.Duration) (time.Time, ttftTimeoutScope, bool) {
	if b == nil {
		return time.Time{}, ttftTimeoutNone, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return time.Time{}, ttftTimeoutNone, false
	}
	var dl time.Time
	scope := ttftTimeoutNone
	if b.global > 0 {
		dl = b.start.Add(b.global)
		scope = ttftTimeoutGlobal
	}
	if leaf != nil && *leaf > 0 {
		if b.leafDeadlines == nil {
			b.leafDeadlines = map[string]time.Time{}
		}
		leafDL, ok := b.leafDeadlines[candidateKey]
		if !ok {
			leafDL = now.Add(*leaf)
			b.leafDeadlines[candidateKey] = leafDL
		}
		if scope == ttftTimeoutNone || leafDL.Before(dl) {
			dl = leafDL
			scope = ttftTimeoutLeaf
		}
	}
	return dl, scope, scope != ttftTimeoutNone
}

type ttftContextDeadline struct {
	scope    ttftTimeoutScope
	deadline time.Time
	parent   context.Context
}

func (d ttftContextDeadline) expired(ctx context.Context, err error) bool {
	if d.scope == ttftTimeoutNone || d.parent == nil || d.parent.Err() != nil {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func (b *ttftBudget) scopedContext(parent context.Context, now time.Time, candidateKey string, leaf *time.Duration) (context.Context, context.CancelFunc, ttftContextDeadline) {
	if parent == nil {
		return parent, func() {}, ttftContextDeadline{}
	}
	dl, scope, ok := b.deadline(now, candidateKey, leaf)
	if !ok {
		return parent, func() {}, ttftContextDeadline{}
	}
	ctx, cancel := context.WithDeadline(parent, dl)
	return ctx, cancel, ttftContextDeadline{scope: scope, deadline: dl, parent: parent}
}

func ttftFailure(scope ttftTimeoutScope, candidateKey string) error {
	return &aipapi.UpstreamFailureError{
		Phase:        aipapi.PhasePreOutput,
		Recoverable:  scope == ttftTimeoutLeaf,
		Reason:       ttftAttemptReason(scope),
		CandidateKey: candidateKey,
	}
}

func ttftAttemptReason(scope ttftTimeoutScope) string {
	switch scope {
	case ttftTimeoutLeaf:
		return "leaf ttft timeout"
	case ttftTimeoutGlobal:
		return "global ttft timeout"
	default:
		return "ttft timeout"
	}
}
