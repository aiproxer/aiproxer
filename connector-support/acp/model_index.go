package acp

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// noCopy signals go vet's copylocks analyzer to reject accidental copies of
// mutex-backed types ([ModelIndex], [TrackingInventory]).
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// CanonicalFallback builds a CanonicalID from a NativeID when the inventory
// snapshot omits CanonicalID. Each connector injects its own rule:
//   - Codex: "openai/" + native
//   - Cursor: strip "cursor-" prefix, then "cursor/" + stripped
//   - AGY: conversion-table lookup, may return "" (no canonical)
//
// The function value must be immutable after NewModelIndex: Replace reads it
// without holding the index lock while building the next map.
type CanonicalFallback func(native string) string

// ModelIndex is a concurrent-safe allowlist from canonical/native identities
// to the exact NativeID required by the connector's wire protocol.
// Successful LoadModels does not mutate the allowlist; AcceptInventory does.
// Do not copy a ModelIndex value after first use (embeds a mutex).
type ModelIndex struct {
	_                 noCopy
	mu                sync.RWMutex
	byCanonical       map[string]string // CanonicalID → NativeID
	byNative          map[string]struct{}
	canonicalFallback CanonicalFallback // immutable after NewModelIndex
}

// NewModelIndex creates a ModelIndex with the given canonical-fallback rule.
// If fallback is nil, models without a CanonicalID are indexed by NativeID only.
func NewModelIndex(fallback CanonicalFallback) *ModelIndex {
	return &ModelIndex{
		byCanonical:       make(map[string]string),
		byNative:          make(map[string]struct{}),
		canonicalFallback: fallback,
	}
}

// Replace atomically swaps the allowlist with the models from a snapshot.
func (idx *ModelIndex) Replace(models []modelinventory.Model) {
	if idx == nil {
		return
	}
	byCanonical := make(map[string]string, len(models))
	byNative := make(map[string]struct{}, len(models))
	for _, m := range models {
		native := strings.TrimSpace(m.NativeID)
		if native == "" {
			continue
		}
		byNative[native] = struct{}{}
		canonical := strings.TrimSpace(m.CanonicalID)
		if canonical == "" && idx.canonicalFallback != nil {
			canonical = idx.canonicalFallback(native)
		}
		if canonical != "" {
			byCanonical[canonical] = native
		}
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.byCanonical = byCanonical
	idx.byNative = byNative
}

// IsKnownNative reports whether native is in the current allowlist.
// Callers must pass already-trimmed IDs; keys are normalized at Replace.
func (idx *ModelIndex) IsKnownNative(native string) bool {
	if idx == nil {
		return false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	_, ok := idx.byNative[native]
	return ok
}

// NativeForCanonical returns the NativeID mapped to canonical, if any.
// Callers must pass already-trimmed IDs; keys are normalized at Replace.
func (idx *ModelIndex) NativeForCanonical(canonical string) (string, bool) {
	if idx == nil {
		return "", false
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	native, ok := idx.byCanonical[canonical]
	return native, ok
}

// TrackingInventory wraps an inner Provider. LoadModels only fetches; it does
// not mutate the ModelIndex. Core updates the allowlist via AcceptInventory
// after publishing a validated registry snapshot (including cache startup
// hydration and clearing when a backend is omitted). SetInner swaps
// the inner provider without discarding the index object.
// Do not copy a TrackingInventory value after first use (embeds a mutex).
type TrackingInventory struct {
	_     noCopy
	mu    sync.Mutex
	inner modelinventory.Provider
	index *ModelIndex
	label string // e.g. "codexappserver" for error messages
}

// NewTrackingInventory creates a tracking wrapper around inner. label identifies
// the connector in error messages (e.g. "codexappserver"). The index is updated
// only by AcceptInventory, not by LoadModels.
func NewTrackingInventory(inner modelinventory.Provider, index *ModelIndex, label string) *TrackingInventory {
	return &TrackingInventory{inner: inner, index: index, label: label}
}

// Index returns the ModelIndex updated by AcceptInventory.
func (t *TrackingInventory) Index() *ModelIndex {
	if t == nil {
		return nil
	}
	return t.index
}

// SetInner swaps the underlying Provider. The next successful LoadModels returns
// the new snapshot; AcceptInventory (from core publish) replaces the allowlist.
func (t *TrackingInventory) SetInner(p modelinventory.Provider) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inner = p
}

// AcceptInventory replaces the model index allowlist from a core-validated
// registry snapshot (or clears it when models is nil/empty).
func (t *TrackingInventory) AcceptInventory(models []modelinventory.Model) {
	if t == nil {
		return
	}
	t.index.Replace(models)
}

var _ modelinventory.AcceptedInventory = (*TrackingInventory)(nil)

// LoadModels delegates to the inner provider without mutating the ModelIndex.
// Allowlist updates happen only via AcceptInventory after registry publish so
// Open cannot race ahead of GET /v1/models during refresh.
func (t *TrackingInventory) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	if t == nil {
		return modelinventory.Snapshot{}, &modelinventory.OperationalError{
			Code: modelinventory.ErrorCodeUnavailable,
			Err:  errors.New("nil tracking inventory"),
		}
	}
	if ctx == nil {
		return modelinventory.Snapshot{}, modelinventory.ErrNilContext
	}
	t.mu.Lock()
	inner := t.inner
	t.mu.Unlock()
	if inner == nil {
		return modelinventory.Snapshot{}, &modelinventory.OperationalError{
			Code: modelinventory.ErrorCodeUnavailable,
			Err:  errors.New(t.label + ": nil inventory provider"),
		}
	}
	return inner.LoadModels(ctx)
}

// StaticInventory delegates to the inner provider.
func (t *TrackingInventory) StaticInventory() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.inner.(modelinventory.StaticInventory); ok {
		return s.StaticInventory()
	}
	return false
}
