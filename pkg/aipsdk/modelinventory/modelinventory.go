// Package modelinventory defines the backend-owned model inventory contract.
package modelinventory

import (
	"context"
	"errors"
	"time"
)

var ErrNilContext = errors.New("modelinventory: nil context")

type Source string

const (
	SourceUnknown       Source = ""
	SourceRemote        Source = "remote"
	SourceStaticFile    Source = "static_file"
	SourceStaticInline  Source = "static_inline"
	SourceStaticBuiltin Source = "static_builtin"
)

// DiscoveryStatus is protocol-neutral inventory discovery outcome for one backend instance.
type DiscoveryStatus string

const (
	DiscoveryStatusOK          DiscoveryStatus = "ok"
	DiscoveryStatusEmpty       DiscoveryStatus = "empty"
	DiscoveryStatusUnavailable DiscoveryStatus = "unavailable"
	DiscoveryStatusCached      DiscoveryStatus = "cached"
)

// Stable machine-readable inventory error codes. Never carry raw provider error text.
const (
	ErrorCodeNone             = ""
	ErrorCodeUnavailable      = "unavailable"
	ErrorCodeTimeout          = "timeout"
	ErrorCodeCanceled         = "canceled"
	ErrorCodeEmpty            = "empty"
	ErrorCodeInvalidInventory = "invalid_inventory"
)

type Model struct {
	CanonicalID string
	NativeID    string
	DisplayName string
	// IsDefault marks the model the provider designates as the instance's
	// default: the model a request that names no model should use. The
	// provider owns that choice, because only the provider knows the model's
	// role (a current model, a served default, a capability class).
	//
	// At most one model of a snapshot may carry it. A provider that cannot
	// name a default leaves every model unmarked, and the proxy derives no
	// default route rather than picking one arbitrarily.
	IsDefault bool
}

type Snapshot struct {
	Source   Source
	LoadedAt time.Time
	Models   []Model
	Warnings []string
}

// Discovery is protocol-neutral per-inventory discovery metadata.
type Discovery struct {
	Status     DiscoveryStatus
	Source     Source
	ModelCount int
	ErrorCode  string
}

// OperationalError marks a fail-soft inventory load failure.
// Aggregators omit the backend without aborting unrelated successful inventories.
type OperationalError struct {
	Code string
	Err  error
}

func (e *OperationalError) Error() string {
	if e == nil {
		return "modelinventory: unavailable"
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Code != "" {
		return "modelinventory: " + e.Code
	}
	return "modelinventory: unavailable"
}

func (e *OperationalError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Operational reports that this error is safe to fail soft during inventory aggregation.
func (e *OperationalError) Operational() bool { return true }

// IsOperational reports whether err is an operational (fail-soft) inventory failure.
func IsOperational(err error) bool {
	if err == nil {
		return false
	}
	var op interface{ Operational() bool }
	if errors.As(err, &op) && op.Operational() {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// DiscoveryFromSnapshot builds discovery metadata for a successful LoadModels snapshot.
func DiscoveryFromSnapshot(snap Snapshot) Discovery {
	if len(snap.Models) == 0 {
		return Discovery{
			Status:     DiscoveryStatusEmpty,
			Source:     snap.Source,
			ModelCount: 0,
			ErrorCode:  ErrorCodeEmpty,
		}
	}
	return Discovery{
		Status:     DiscoveryStatusOK,
		Source:     snap.Source,
		ModelCount: len(snap.Models),
		ErrorCode:  ErrorCodeNone,
	}
}

// DiscoveryFromLoadError builds discovery metadata for a LoadModels failure.
// Raw error text is not copied; only a stable ErrorCode is retained.
func DiscoveryFromLoadError(err error) Discovery {
	code := ErrorCodeUnavailable
	var op *OperationalError
	if errors.As(err, &op) && op != nil && op.Code != "" {
		code = op.Code
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = ErrorCodeTimeout
	} else if errors.Is(err, context.Canceled) {
		code = ErrorCodeCanceled
	}
	return Discovery{
		Status:     DiscoveryStatusUnavailable,
		ModelCount: 0,
		ErrorCode:  code,
	}
}

// Provider loads model inventory for one configured backend instance.
type Provider interface {
	LoadModels(ctx context.Context) (Snapshot, error)
}

// StaticInventory marks providers whose inventory is local and does not require periodic remote refresh.
type StaticInventory interface {
	StaticInventory() bool
}

// AcceptedInventory is optionally implemented by Provider values that maintain a
// local allowlist aligned with inventory accepted into the aggregate registry.
// Core calls AcceptInventory after publishing a registry snapshot: accepted
// backends receive their published models, and omitted backends receive a nil
// or empty slice (both must clear). An empty/nil slice must clear the allowlist.
type AcceptedInventory interface {
	AcceptInventory(models []Model)
}

// emptyModels / emptyWarnings are shared immutable empty slices for StaticProvider.
var (
	emptyModels   = []Model{}
	emptyWarnings = []string{}
)

// StaticProvider returns a fixed inventory snapshot. Models and Warnings slices
// are treated as immutable after construction; LoadModels returns them without
// cloning. Callers must not mutate the returned Snapshot slices.
type StaticProvider struct {
	Source   Source
	LoadedAt time.Time
	Models   []Model
	Warnings []string
}

func (p StaticProvider) StaticInventory() bool {
	return true
}

func (p StaticProvider) LoadModels(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	source := p.Source
	if source == "" {
		source = SourceStaticBuiltin
	}
	loadedAt := p.LoadedAt
	if loadedAt.IsZero() {
		loadedAt = time.Now()
	}
	models := p.Models
	if models == nil {
		models = emptyModels
	}
	warnings := p.Warnings
	if warnings == nil {
		warnings = emptyWarnings
	}
	return Snapshot{
		Source:   source,
		LoadedAt: loadedAt,
		Models:   models,
		Warnings: warnings,
	}, nil
}

type ErrorProvider struct {
	Err error
}

// StaticInventory marks ErrorProvider as non-refreshable so a cached snapshot
// loaded at startup is not repeatedly overwritten by the same construction error.
func (p ErrorProvider) StaticInventory() bool { return true }

func (p ErrorProvider) LoadModels(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	underlying := p.Err
	if underlying == nil {
		underlying = errors.New("modelinventory: unavailable")
	}
	return Snapshot{}, &OperationalError{Code: ErrorCodeUnavailable, Err: underlying}
}
