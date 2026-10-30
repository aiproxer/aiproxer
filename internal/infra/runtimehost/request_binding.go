package runtimehost

import (
	"context"
	"strconv"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
)

type requestBindingKey struct{}

// RequestBinding is the safe, immutable generation binding attached to a
// request context. It exposes metadata snapshots and controlled pin transfer
// only — never mutable Generation internals, config, credentials, Built, App,
// or ProcessServices handles (req 4.5-4.8).
type RequestBinding struct {
	lease  *Lease
	status Status // frozen at dispatch bind
}

func newRequestBinding(lease *Lease) *RequestBinding {
	b := &RequestBinding{lease: lease}
	if lease != nil && lease.gen != nil {
		b.status = lease.gen.Status()
	}
	return b
}

func withRequestBinding(ctx context.Context, b *RequestBinding) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, requestBindingKey{}, b)
	if b != nil {
		ctx = genpin.WithRetainer(ctx, b)
	}
	return ctx
}

// BindingFromContext returns the request generation binding when present.
func BindingFromContext(ctx context.Context) (*RequestBinding, bool) {
	if ctx == nil {
		return nil, false
	}
	b, ok := ctx.Value(requestBindingKey{}).(*RequestBinding)
	if !ok || b == nil || b.lease == nil {
		return nil, false
	}
	return b, true
}

// Meta returns a defensive copy of the generation metadata frozen at bind time.
func (b *RequestBinding) Meta() GenerationMeta {
	if b == nil {
		return GenerationMeta{}
	}
	return b.status.Meta
}

// Status returns a defensive lifecycle/metadata snapshot frozen at bind time.
func (b *RequestBinding) Status() Status {
	if b == nil {
		return Status{}
	}
	return b.status
}

// RuntimeInstanceID implements genpin.Retainer using the bind-time manager instance.
func (b *RequestBinding) RuntimeInstanceID() string {
	if b == nil {
		return ""
	}
	return b.status.Meta.InstanceID
}

// RuntimeGenerationID implements genpin.Retainer using the bind-time generation id.
func (b *RequestBinding) RuntimeGenerationID() string {
	if b == nil {
		return ""
	}
	id := b.status.Meta.ID
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// Retain implements genpin.Retainer by acquiring an independent child pin.
func (b *RequestBinding) Retain(kind genpin.Kind) (genpin.Pin, bool) {
	if b == nil {
		return nil, false
	}
	p, ok := b.RetainPin(pinKindFromGenpin(kind))
	if !ok || p == nil {
		return nil, false
	}
	return genpinPin{p: p}, true
}

// TransferPin converts the request lease retain into an async/SSE/provider pin.
// Exactly one successful transfer is allowed; subsequent calls fail. After a
// successful transfer, the dispatcher's deferred Release becomes a no-op.
func (b *RequestBinding) TransferPin(kind PinKind) (*Pin, bool) {
	if b == nil || b.lease == nil {
		return nil, false
	}
	return b.lease.TransferPin(kind)
}

// RetainPin acquires an additional independent generation pin while the request
// lease still holds spawn rights (req 5.3, 5.7, 10.3).
func (b *RequestBinding) RetainPin(kind PinKind) (*Pin, bool) {
	if b == nil || b.lease == nil {
		return nil, false
	}
	return b.lease.RetainPin(kind)
}

// Meta returns safe generation metadata for this lease.
func (l *Lease) Meta() GenerationMeta {
	if l == nil || l.gen == nil {
		return GenerationMeta{}
	}
	return l.gen.Status().Meta
}

// Status returns a defensive lifecycle/metadata snapshot for this lease.
func (l *Lease) Status() Status {
	if l == nil || l.gen == nil {
		return Status{}
	}
	return l.gen.Status()
}

func pinKindFromGenpin(kind genpin.Kind) PinKind {
	switch kind {
	case genpin.KindSSE:
		return PinSSE
	case genpin.KindAsync:
		return PinAsync
	case genpin.KindProvider:
		return PinProvider
	default:
		return PinHTTP
	}
}

// genpinPin adapts *Pin to genpin.Pin (Kind returns genpin.Kind).
type genpinPin struct {
	p *Pin
}

func (w genpinPin) Kind() genpin.Kind {
	if w.p == nil {
		return 0
	}
	switch w.p.kind {
	case PinSSE:
		return genpin.KindSSE
	case PinAsync:
		return genpin.KindAsync
	case PinProvider:
		return genpin.KindProvider
	default:
		return 0
	}
}

func (w genpinPin) Release() {
	if w.p != nil {
		w.p.Release()
	}
}

var (
	_ genpin.Retainer = (*RequestBinding)(nil)
	_ genpin.Pin      = genpinPin{}
)
