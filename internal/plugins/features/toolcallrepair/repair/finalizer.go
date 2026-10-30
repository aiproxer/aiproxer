package repair

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

const (
	DefaultFinalizerID    = "tool-call-repair"
	DefaultFinalizerOrder = 40

	OnUnrepairablePassThrough = "pass_through"
	OnUnrepairableError       = "error"
)

// FinalizerPolicy configures the SDK Finalizer adapter over Engine.
// Order is taken as given (including 0); callers that want the default should
// pass DefaultFinalizerOrder.
type FinalizerPolicy struct {
	ID             string
	MaxArgsBytes   int
	OnUnrepairable string
	Order          int
	Schema         SchemaLimits
}

// Finalizer is the core-owned toolcall.Finalizer adapter for Engine.Repair.
type Finalizer struct {
	policy FinalizerPolicy
	eng    *Engine
	order  int
	id     string
}

var _ toolcall.Finalizer = (*Finalizer)(nil)

func NewFinalizer(policy FinalizerPolicy) *Finalizer {
	id := strings.TrimSpace(policy.ID)
	if id == "" {
		id = DefaultFinalizerID
	}
	if policy.MaxArgsBytes <= 0 {
		policy.MaxArgsBytes = DefaultMaxArgsBytes
	}
	on := strings.TrimSpace(policy.OnUnrepairable)
	if on == "" {
		on = OnUnrepairablePassThrough
	}
	policy.OnUnrepairable = on
	policy.Schema = policy.Schema.normalized()
	return &Finalizer{
		policy: policy,
		eng:    NewEngineWithCache(NewSchemaCache(policy.Schema)),
		order:  policy.Order,
		id:     id,
	}
}

func (f *Finalizer) ID() string {
	if f == nil {
		return DefaultFinalizerID
	}
	return f.id
}

func (f *Finalizer) Order() int {
	if f == nil {
		return DefaultFinalizerOrder
	}
	return f.order
}

func (f *Finalizer) Finalize(ctx context.Context, call toolcall.CompletedCall, tool aipapi.ToolDef, catalog []aipapi.ToolDef, meta toolcall.Meta) (toolcall.Result, error) {
	_ = meta
	if f == nil || f.eng == nil {
		return toolcall.Result{Action: toolcall.ActionPass, ReasonCode: toolcall.ReasonValidPassThrough}, nil
	}
	out, err := f.eng.RepairWithContext(ctx, Input{
		ToolCallID:   call.ToolCallID,
		ToolName:     call.ToolName,
		ArgsJSON:     call.ArgsJSON,
		Tool:         tool,
		Catalog:      catalog,
		MaxArgsBytes: f.policy.MaxArgsBytes,
	})
	if err != nil {
		return f.mapUnrepairable(toolcall.ReasonUnrepairable), nil
	}
	switch out.Kind {
	case OutcomePass:
		return toolcall.Result{
			Action:     toolcall.ActionPass,
			ReasonCode: out.ReasonCode,
		}, nil
	case OutcomeRewrite:
		name := strings.TrimSpace(out.ToolName)
		if name == "" {
			name = call.ToolName
		}
		return toolcall.Result{
			Action:     toolcall.ActionRewrite,
			ToolName:   name,
			ArgsJSON:   out.ArgsJSON,
			ReasonCode: out.ReasonCode,
		}, nil
	default:
		return f.mapUnrepairable(out.ReasonCode), nil
	}
}

func (f *Finalizer) mapUnrepairable(reason string) toolcall.Result {
	if reason == "" {
		reason = toolcall.ReasonUnrepairable
	}
	if f != nil && f.policy.OnUnrepairable == OnUnrepairableError {
		return toolcall.Result{
			Action:     toolcall.ActionReject,
			ReasonCode: reason,
		}
	}
	return toolcall.Result{
		Action:     toolcall.ActionPass,
		ReasonCode: reason,
	}
}
