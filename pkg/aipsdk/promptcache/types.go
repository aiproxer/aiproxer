// Package promptcache defines the provider-neutral host/plugin contract for
// prompt-cache residency. It deliberately contains no provider wire types,
// cache-key schema, request replay shape, or scheduling policy.
package promptcache

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	MaxTargetIDBytes      = 256
	MaxGenerationIDBytes  = 256
	MaxHandleBytes        = 256
	MaxOperationIDBytes   = 256
	MaxLegIDBytes         = 256
	MaxObservationsPerLeg = 16
	MaxLifecycleKinds     = 5
)

var (
	ErrInvalid            = errors.New("promptcache: invalid contract value")
	ErrOversized          = errors.New("promptcache: value exceeds bound")
	ErrUnknownLifecycle   = errors.New("promptcache: unknown lifecycle")
	ErrHandleRequired     = errors.New("promptcache: renewable observation requires a handle")
	ErrControlUnsupported = errors.New("promptcache: control unsupported")
	ErrStaleHandle        = errors.New("promptcache: stale handle")
)

type LifecycleKind string

const (
	LifecycleUnknown          LifecycleKind = "unknown"
	LifecycleSlidingExpiry    LifecycleKind = "sliding_expiry"
	LifecycleFixedExpiry      LifecycleKind = "fixed_expiry"
	LifecycleMinimumResidency LifecycleKind = "minimum_residency"
	LifecycleBestEffort       LifecycleKind = "best_effort"
)

func (k LifecycleKind) Validate() error {
	switch k {
	case LifecycleUnknown, LifecycleSlidingExpiry, LifecycleFixedExpiry, LifecycleMinimumResidency, LifecycleBestEffort:
		return nil
	default:
		return ErrUnknownLifecycle
	}
}

type Profile struct {
	ObservationSupported bool
	RenewalSupported     bool
	LifecycleKinds       []LifecycleKind
}

func (p Profile) Validate() error {
	if len(p.LifecycleKinds) > MaxLifecycleKinds {
		return ErrOversized
	}
	seen := make(map[LifecycleKind]struct{}, len(p.LifecycleKinds))
	for _, kind := range p.LifecycleKinds {
		if err := kind.Validate(); err != nil {
			return err
		}
		if _, ok := seen[kind]; ok {
			return fmt.Errorf("%w: duplicate lifecycle %q", ErrInvalid, kind)
		}
		seen[kind] = struct{}{}
	}
	if p.RenewalSupported && !p.ObservationSupported {
		return fmt.Errorf("%w: renewal requires observation", ErrInvalid)
	}
	return nil
}

func (p Profile) Normalize() (Profile, error) {
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	p.LifecycleKinds = slices.Clone(p.LifecycleKinds)
	slices.Sort(p.LifecycleKinds)
	return p, nil
}

type (
	TargetID     string
	GenerationID string
	Handle       []byte
)

// ObservationLineage carries only bounded routing/session attribution needed
// by a provider adapter to associate a foreground observation with its B-leg.
// It is not a cache identity or a scheduling key.
type ObservationLineage struct {
	ALegID            string
	BLegID            string
	BackendInstanceID string
	CanonicalModelID  string
}

type observationLineageContextKey struct{}

func WithObservationLineage(ctx context.Context, lineage ObservationLineage) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, observationLineageContextKey{}, lineage)
}

func ObservationLineageFromContext(ctx context.Context) (ObservationLineage, bool) {
	if ctx == nil {
		return ObservationLineage{}, false
	}
	lineage, ok := ctx.Value(observationLineageContextKey{}).(ObservationLineage)
	return lineage, ok
}

func validateBoundedString(value string, max int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return ErrInvalid
	}
	if len(value) > max {
		return ErrOversized
	}
	return nil
}

func (id TargetID) Validate(required bool) error {
	return validateBoundedString(string(id), MaxTargetIDBytes, required)
}

func (id GenerationID) Validate(required bool) error {
	return validateBoundedString(string(id), MaxGenerationIDBytes, required)
}

func (h Handle) Validate(required bool) error {
	if required && len(h) == 0 {
		return ErrHandleRequired
	}
	if len(h) > MaxHandleBytes {
		return ErrOversized
	}
	return nil
}

type Timing struct {
	ObservedAt           time.Time
	ExpiresAt            *time.Time
	MinimumResidentUntil *time.Time
}

func (t Timing) Validate() error {
	if t.ObservedAt.IsZero() {
		return ErrInvalid
	}
	if t.ExpiresAt != nil && t.ExpiresAt.Before(t.ObservedAt) {
		return fmt.Errorf("%w: expiry precedes observation", ErrInvalid)
	}
	if t.MinimumResidentUntil != nil && t.MinimumResidentUntil.Before(t.ObservedAt) {
		return fmt.Errorf("%w: minimum residency precedes observation", ErrInvalid)
	}
	return nil
}

type CacheEvidence struct {
	InputTokens      *int64
	OutputTokens     *int64
	CacheReadTokens  *int64
	CacheWriteTokens *int64
	TotalTokens      *int64
}

func (e CacheEvidence) Validate() error {
	for _, value := range []*int64{e.InputTokens, e.OutputTokens, e.CacheReadTokens, e.CacheWriteTokens, e.TotalTokens} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%w: negative usage", ErrInvalid)
		}
	}
	return nil
}

type Observation struct {
	ALegID            string
	BLegID            string
	BackendInstanceID string
	TargetID          TargetID
	GenerationID      GenerationID
	Lifecycle         LifecycleKind
	Timing            Timing
	Renewable         bool
	Handle            Handle
	Evidence          CacheEvidence
}

func (o Observation) Validate() error {
	if err := validateBoundedString(o.ALegID, MaxLegIDBytes, true); err != nil {
		return err
	}
	if err := validateBoundedString(o.BLegID, MaxLegIDBytes, true); err != nil {
		return err
	}
	if err := validateBoundedString(o.BackendInstanceID, MaxLegIDBytes, true); err != nil {
		return err
	}
	if err := o.TargetID.Validate(true); err != nil {
		return err
	}
	if err := o.GenerationID.Validate(true); err != nil {
		return err
	}
	if err := o.Lifecycle.Validate(); err != nil {
		return err
	}
	if err := o.Timing.Validate(); err != nil {
		return err
	}
	if err := o.Evidence.Validate(); err != nil {
		return err
	}
	if err := o.Handle.Validate(o.Renewable); err != nil {
		return err
	}
	return nil
}

type RenewStatus string

const (
	Renewed       RenewStatus = "renewed"
	StillResident RenewStatus = "still_resident"
	ColdRecreated RenewStatus = "cold_recreated"
	Stale         RenewStatus = "stale"
	Unsupported   RenewStatus = "unsupported"
	ControlFailed RenewStatus = "control_failed"
)

func (s RenewStatus) Validate() error {
	switch s {
	case Renewed, StillResident, ColdRecreated, Stale, Unsupported, ControlFailed:
		return nil
	default:
		return ErrInvalid
	}
}

type RenewRequest struct {
	Handle      Handle
	OperationID string
}

func (r RenewRequest) Validate() error {
	if err := r.Handle.Validate(true); err != nil {
		return err
	}
	return validateBoundedString(r.OperationID, MaxOperationIDBytes, true)
}

type RenewResult struct {
	Status      RenewStatus
	Observation *Observation
	Evidence    CacheEvidence
}

func (r RenewResult) Validate() error {
	if err := r.Status.Validate(); err != nil {
		return err
	}
	if r.Observation != nil {
		if err := r.Observation.Validate(); err != nil {
			return err
		}
	}
	return r.Evidence.Validate()
}

type ReleaseRequest struct{ Handle Handle }

func (r ReleaseRequest) Validate() error { return r.Handle.Validate(true) }

type RenewResponse struct {
	Result     RenewResult
	Accounting *AccountingEvidence
}

type Controller interface {
	Renew(context.Context, RenewRequest) (RenewResponse, error)
	Release(context.Context, ReleaseRequest) error
}

type ObservationSource interface{ DrainPromptCacheObservations() []Observation }

type ObservationBuffer struct {
	mu           sync.Mutex
	observations []Observation
	committed    bool
	drained      bool
}

func (b *ObservationBuffer) Add(o Observation) error {
	if b == nil {
		return ErrInvalid
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.committed || b.drained {
		return ErrInvalid
	}
	if err := o.Validate(); err != nil {
		return err
	}
	if len(b.observations) >= MaxObservationsPerLeg {
		return ErrOversized
	}
	b.observations = append(b.observations, o)
	return nil
}

func (b *ObservationBuffer) Commit() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.drained {
		b.committed = true
	}
}

func (b *ObservationBuffer) Discard() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observations = nil
	b.committed = false
	b.drained = true
}

func (b *ObservationBuffer) DrainPromptCacheObservations() []Observation {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.committed || b.drained {
		return nil
	}
	b.drained = true
	out := append([]Observation(nil), b.observations...)
	b.observations = nil
	return out
}
