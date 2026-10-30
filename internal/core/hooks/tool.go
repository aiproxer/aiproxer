package hooks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// ToolApplyResult is the outcome of running the tool-reactor chain on one tool event.
type ToolApplyResult struct {
	// Emit is false when a reactor swallowed the event.
	Emit bool
	// Event is the canonical tool event to surface when Emit is true.
	Event aipapi.ToolEvent
	// Err is set when ToolReactorErrorsFailClosed is configured and a reactor returned an error.
	Err error
}

// ApplyToolReactors runs tool reactors in order. Reactor errors follow Config.ToolReactorErrorPolicy
// (default fail-open). Swallow stops the chain and returns Emit=false.
func (b *Bus) ApplyToolReactors(ctx context.Context, te aipapi.ToolEvent, meta sdk.ToolMeta) ToolApplyResult {
	if ctx == nil {
		return ToolApplyResult{Err: fmt.Errorf("hooks: %w", aipapi.ErrNilContext)}
	}
	if v, ok := execctx.FromContext(ctx); ok {
		meta.Principal = v.Principal
		meta.Scope = v.Scope
		meta.Session = v.Session
		meta.Workspace = v.Workspace
	}
	cur := te
	var tools []sdk.ToolReactor
	var pol sdk.ToolReactorErrorPolicy
	if b != nil {
		tools = b.tools
		pol = b.toolErrPol
	}
	reactorEvidence := ToolReactorEvidenceFromContext(ctx)
	emit := func(providerID string, dec sdk.ToolDecision, err error, validationErr error) {
		if reactorEvidence != nil {
			reactorEvidence(ctx, providerID, dec, err, validationErr)
		}
	}
	for _, r := range tools {
		dec, next, err := callToolReactor(ctx, r, cur, meta)
		if err != nil {
			emit(r.ID(), dec, err, nil)
			switch pol {
			case sdk.ToolReactorErrorsFailClosed:
				return ToolApplyResult{Err: fmt.Errorf("tool reactor %s: %w", r.ID(), err)}
			case sdk.ToolReactorErrorsSwallowEvent:
				return ToolApplyResult{Emit: false, Event: aipapi.ToolEvent{}}
			default:
				if _, ok := errors.AsType[*safety.PanicError](err); ok {
					logFailOpenHookPanic(ctx, "tool_reactor", r.ID(), err)
				}
				continue
			}
		}
		switch dec {
		case sdk.ToolPass:
			emit(r.ID(), dec, nil, nil)
		case sdk.ToolRewrite, sdk.ToolReplace:
			if vErr := ValidateToolEventAfterPolicy(r.ID(), &next); vErr != nil {
				emit(r.ID(), dec, nil, vErr)
				switch pol {
				case sdk.ToolReactorErrorsFailClosed:
					return ToolApplyResult{Err: fmt.Errorf("tool reactor %s: %w", r.ID(), vErr)}
				default:
					// Fail-open (default): reject invalid mutation and continue with the current event.
					continue
				}
			}
			emit(r.ID(), dec, nil, nil)
			reconcileDerivedToolMetadata(&next, cur)
			cur = next
		case sdk.ToolSwallow:
			emit(r.ID(), dec, nil, nil)
			return ToolApplyResult{Emit: false, Event: aipapi.ToolEvent{}}
		default:
			emit(r.ID(), dec, nil, nil)
			continue
		}
	}
	return ToolApplyResult{Emit: true, Event: cur}
}

// reconcileDerivedToolMetadata keeps Category and MayMutateLocalFS authoritative
// from the effective tool name after a reactor rewrite/replace. A non-empty name is
// reclassified; a same-ID name-less mutation preserves the current classification
// when cur is already classified (so existing reactors that build fresh same-ID
// argument-delta events do not need to copy unchanged metadata); an unclassified
// cur or a changed-ID name-less replacement falls back to the conservative
// unknown/true. Reactor-authored category/bool values never override
// classification derived from a non-empty effective name.
func reconcileDerivedToolMetadata(next *aipapi.ToolEvent, cur aipapi.ToolEvent) {
	if next == nil {
		return
	}
	// Trim first (Requirement 1.1): whitespace-only is an omitted name, not a
	// present name that would classify as unknown.
	if strings.TrimSpace(next.ToolName) != "" {
		next.Category, next.MayMutateLocalFS = aipapi.ClassifyToolName(next.ToolName)
		return
	}
	if next.ToolCallID == cur.ToolCallID && cur.Category != "" {
		next.Category = cur.Category
		next.MayMutateLocalFS = cur.MayMutateLocalFS
		return
	}
	next.Category = aipapi.ToolCategoryUnknown
	next.MayMutateLocalFS = true
}

type toolReactorResult struct {
	dec  sdk.ToolDecision
	next aipapi.ToolEvent
}

// callToolReactor invokes HandleToolEvent and maps a panic to *safety.PanicError like a returned error.
func callToolReactor(
	ctx context.Context,
	r sdk.ToolReactor,
	cur aipapi.ToolEvent,
	meta sdk.ToolMeta,
) (dec sdk.ToolDecision, next aipapi.ToolEvent, err error) {
	res, err := safety.CallValue(safety.BoundaryExtension, "tool_reactor", func() (toolReactorResult, error) {
		d, n, e := r.HandleToolEvent(ctx, cur, meta)
		return toolReactorResult{dec: d, next: n}, e
	})
	return res.dec, res.next, err
}
