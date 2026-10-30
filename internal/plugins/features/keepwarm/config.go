package keepwarm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidConfig  = errors.New("keepwarm: invalid configuration")
	ErrQuiescing      = errors.New("keepwarm: generation is quiescing")
	ErrDisabled       = errors.New("keepwarm: disabled")
	ErrPolicyCapacity = errors.New("keepwarm: session policy capacity exhausted")
	ErrPolicyNotFound = errors.New("keepwarm: session policy not found")
	ErrNoSchedule     = errors.New("keepwarm: target has no safe schedule")
	ErrStale          = errors.New("keepwarm: stale renewal result")
)

// ID is the canonical feature identifier.
const ID = "keepwarm"

const (
	DefaultMaxRefreshesPerIdleEpoch = 6
	DefaultMaxIdleDuration          = time.Hour
	DefaultMaxActiveTargets         = 1024
	DefaultMaxConcurrentRenewals    = 4
	DefaultRenewTimeout             = 15 * time.Second
	DefaultMaxPolicyEntries         = 4096
)

type HeuristicOverride struct {
	BackendInstance string
	CanonicalModel  string
	Interval        time.Duration
}

func (h HeuristicOverride) Validate() error {
	if strings.TrimSpace(h.BackendInstance) == "" || h.Interval <= 0 || !finiteDuration(h.Interval) {
		return fmt.Errorf("%w: heuristic override", ErrInvalidConfig)
	}
	return nil
}

type Config struct {
	Enabled                       bool
	MaxRefreshesPerIdleEpoch      int
	MaxIdleDuration               time.Duration
	MaxActiveTargets              int
	MaxConcurrentRenewals         int
	RenewTimeout                  time.Duration
	ContinueAfterColdRecreate     bool
	MaxColdRecreatesPerIdleEpoch  int
	MaxProviderTokensPerIdleEpoch *int64
	HeuristicOverrides            []HeuristicOverride
}

func DefaultConfig() Config {
	return Config{
		Enabled:                      true,
		MaxRefreshesPerIdleEpoch:     DefaultMaxRefreshesPerIdleEpoch,
		MaxIdleDuration:              DefaultMaxIdleDuration,
		MaxActiveTargets:             DefaultMaxActiveTargets,
		MaxConcurrentRenewals:        DefaultMaxConcurrentRenewals,
		RenewTimeout:                 DefaultRenewTimeout,
		MaxColdRecreatesPerIdleEpoch: 0,
	}
}

func (c Config) Validate() error {
	if c.MaxRefreshesPerIdleEpoch <= 0 || c.MaxIdleDuration <= 0 || !finiteDuration(c.MaxIdleDuration) || c.MaxActiveTargets <= 0 || c.MaxConcurrentRenewals <= 0 || c.RenewTimeout <= 0 || !finiteDuration(c.RenewTimeout) {
		return fmt.Errorf("%w: positive finite bounds required", ErrInvalidConfig)
	}
	if c.MaxColdRecreatesPerIdleEpoch < 0 || (!c.ContinueAfterColdRecreate && c.MaxColdRecreatesPerIdleEpoch != 0) {
		return fmt.Errorf("%w: cold recreation continuation is contradictory", ErrInvalidConfig)
	}
	if c.MaxProviderTokensPerIdleEpoch != nil && *c.MaxProviderTokensPerIdleEpoch <= 0 {
		return fmt.Errorf("%w: provider token budget", ErrInvalidConfig)
	}
	seenHeuristics := make(map[string]struct{}, len(c.HeuristicOverrides))
	for _, override := range c.HeuristicOverrides {
		if err := override.Validate(); err != nil {
			return err
		}
		key := strings.TrimSpace(override.BackendInstance) + "\x00" + strings.TrimSpace(override.CanonicalModel)
		if _, exists := seenHeuristics[key]; exists {
			return fmt.Errorf("%w: duplicate heuristic override", ErrInvalidConfig)
		}
		seenHeuristics[key] = struct{}{}
	}
	return nil
}

func finiteDuration(d time.Duration) bool {
	return d > 0 && d < 1<<63-1
}

func (c Config) heuristic(backend, model string) (HeuristicOverride, bool) {
	for _, override := range c.HeuristicOverrides {
		if override.BackendInstance == backend && (override.CanonicalModel == "" || override.CanonicalModel == model) {
			return override, true
		}
	}
	return HeuristicOverride{}, false
}
