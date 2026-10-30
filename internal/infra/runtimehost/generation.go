package runtimehost

import (
	"sync"
	"sync/atomic"
	"time"
)

type GenLifecycle uint32

const (
	GenUnspecified GenLifecycle = iota
	GenPreparing
	GenPrepared
	GenActive
	GenRetiring
	GenQuiescing
	GenQuiesced
	GenDrained
	GenClosing
	GenClosed
	GenFailed
)

type PinKind uint8

const (
	PinUnknown PinKind = iota
	PinHTTP
	PinSSE
	PinAsync
	PinProvider
)

type OwnedCloser interface {
	Close() error
}

type MetaHints struct {
	PublicFingerprint string
	TriggerKind       string
	LoadedAt          time.Time
}

type GenerationMeta struct {
	ID                int64
	InstanceID        string
	PreviousID        int64
	Label             string
	PublicFingerprint string
	TriggerKind       string
	LoadedAt          time.Time
	PublishedAt       time.Time
}

type Status struct {
	Meta      GenerationMeta
	Lifecycle GenLifecycle
	Refs      uint32
}

func packLease(state GenLifecycle, refs uint32) uint64 {
	return (uint64(state) << 32) | uint64(refs)
}

func unpackLease(w uint64) (GenLifecycle, uint32) {
	return GenLifecycle(w >> 32), uint32(w)
}

type Generation struct {
	id             atomic.Int64
	label          string
	word           atomic.Uint64
	drainMu        sync.Mutex
	drainCh        chan struct{}
	drainClosed    bool
	postDrainClose func()
	retireAdmit    retireAdmission
	closeCount     atomic.Int32
	closeMu        sync.Mutex
	metaMu         sync.RWMutex
	meta           GenerationMeta
	payloadMu      sync.Mutex
	owned          OwnedCloser
	requestPlane   PublishedRequestPlane
}

func newGeneration(label string, state GenLifecycle, owned OwnedCloser) *Generation {
	g := &Generation{
		label:       label,
		drainCh:     make(chan struct{}),
		owned:       owned,
		meta:        GenerationMeta{Label: label},
		retireAdmit: newRetireAdmission(),
	}
	g.word.Store(packLease(state, 0))
	return g
}

func newGenerationWithRequestPlane(label string, state GenLifecycle, plane PublishedRequestPlane) *Generation {
	g := newGeneration(label, state, plane)
	g.requestPlane = plane
	return g
}

func (g *Generation) ID() int64 {
	if g == nil {
		return 0
	}
	return g.id.Load()
}

func (g *Generation) Label() string {
	if g == nil {
		return ""
	}
	return g.label
}

func (g *Generation) Lifecycle() GenLifecycle {
	if g == nil {
		return GenFailed
	}
	st, _ := unpackLease(g.word.Load())
	return st
}

func (g *Generation) Refs() uint32 {
	if g == nil {
		return 0
	}
	_, refs := unpackLease(g.word.Load())
	return refs
}

func (g *Generation) Drained() <-chan struct{} {
	if g == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return g.drainCh
}

func (g *Generation) CloseCount() int32 {
	if g == nil {
		return 0
	}
	return g.closeCount.Load()
}

func (g *Generation) SetMetaHints(h MetaHints) {
	if g == nil {
		return
	}
	g.metaMu.Lock()
	defer g.metaMu.Unlock()
	g.meta.PublicFingerprint = h.PublicFingerprint
	g.meta.TriggerKind = h.TriggerKind
	g.meta.LoadedAt = h.LoadedAt
}

func (g *Generation) Status() Status {
	if g == nil {
		return Status{}
	}
	g.metaMu.RLock()
	meta := g.meta
	g.metaMu.RUnlock()
	st, refs := unpackLease(g.word.Load())
	meta.ID = g.id.Load()
	meta.Label = g.label
	return Status{Meta: meta, Lifecycle: st, Refs: refs}
}

func (g *Generation) MarkPrepared() error {
	return g.casLifecycle(GenPreparing, GenPrepared)
}

func (g *Generation) BeginQuiesce() error {
	return g.casLifecycle(GenRetiring, GenQuiescing)
}

func (g *Generation) MarkQuiesced() error {
	if err := g.casLifecycle(GenQuiescing, GenQuiesced); err != nil {
		return err
	}
	st, refs := unpackLease(g.word.Load())
	if st == GenQuiesced && refs == 0 {
		g.signalDrained()
	}
	return nil
}

func (g *Generation) BeginClose() error {
	return g.casLifecycle(GenDrained, GenClosing)
}

func (g *Generation) Transition(to GenLifecycle) error {
	if g == nil {
		return ErrNotPrepared
	}
	for {
		cur := g.word.Load()
		from, refs := unpackLease(cur)
		if !legalExplicitTransition(from, to) {
			return ErrIllegalTransition
		}
		if g.word.CompareAndSwap(cur, packLease(to, refs)) {
			return nil
		}
	}
}

func legalExplicitTransition(from, to GenLifecycle) bool {
	switch from {
	case GenPreparing:
		return to == GenPrepared || to == GenFailed
	case GenPrepared:
		return to == GenFailed
	default:
		return false
	}
}

func (g *Generation) casLifecycle(from, to GenLifecycle) error {
	if g == nil {
		return ErrNotPrepared
	}
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if st != from {
			return ErrIllegalTransition
		}
		if g.word.CompareAndSwap(cur, packLease(to, refs)) {
			return nil
		}
	}
}

func (g *Generation) assignPublish(id, prev int64, publishedAt time.Time) error {
	g.payloadMu.Lock()
	defer g.payloadMu.Unlock()
	for {
		cur := g.word.Load()
		st, refs := unpackLease(cur)
		if st != GenPrepared {
			if st == GenActive || st == GenRetiring || st == GenQuiescing ||
				st == GenQuiesced || st == GenDrained || st == GenClosing || st == GenClosed {
				return ErrAlreadyPublished
			}
			return ErrNotPrepared
		}
		if !g.word.CompareAndSwap(cur, packLease(GenActive, refs)) {
			continue
		}
		break
	}
	g.id.Store(id)
	g.metaMu.Lock()
	g.meta.ID = id
	g.meta.PreviousID = prev
	g.meta.PublishedAt = publishedAt
	g.meta.Label = g.label
	g.metaMu.Unlock()
	return nil
}

func (g *Generation) assignPublishWithInstance(id, prev int64, instanceID string, publishedAt time.Time) error {
	if err := g.assignPublish(id, prev, publishedAt); err != nil {
		return err
	}
	g.metaMu.Lock()
	g.meta.InstanceID = instanceID
	g.metaMu.Unlock()
	return nil
}
