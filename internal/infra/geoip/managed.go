package geoip

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	coregeoip "github.com/aiproxer/aiproxer/internal/core/geoip"
)

const dbIPEdition = "dbip-country-lite"

// UpdateResult is a finite updater outcome.
type UpdateResult string

const (
	UpdateUnchanged UpdateResult = "unchanged"
	UpdateUpdated   UpdateResult = "updated"
)

// ManagedConfig contains process-startup-fixed updater settings.
type ManagedConfig struct {
	Directory  string
	Edition    string
	Interval   time.Duration
	HTTPClient *http.Client
	Now        func() time.Time
	Observe    func(result string)
	Logger     *slog.Logger
}

// ManagedUpdater performs one bounded DB-IP Lite update operation. Scheduling
// ownership remains with the process service/composition root.
type ManagedUpdater struct {
	service *Service
	config  ManagedConfig
}

func NewManagedUpdater(service *Service, cfg ManagedConfig) (*ManagedUpdater, error) {
	if service == nil {
		return nil, fmt.Errorf("geoip: nil service")
	}
	if strings.TrimSpace(cfg.Directory) == "" {
		return nil, fmt.Errorf("geoip: managed directory is required")
	}
	if cfg.Edition == "" {
		cfg.Edition = dbIPEdition
	}
	if cfg.Edition != dbIPEdition {
		return nil, fmt.Errorf("geoip: unsupported managed edition %q", cfg.Edition)
	}
	if cfg.Interval == 0 {
		cfg.Interval = coregeoip.DefaultUpdateInterval
	}
	if cfg.Interval < coregeoip.MinUpdateInterval || cfg.Interval > coregeoip.MaxUpdateInterval {
		return nil, fmt.Errorf("geoip: managed interval must be between %s and %s", coregeoip.MinUpdateInterval, coregeoip.MaxUpdateInterval)
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &ManagedUpdater{service: service, config: cfg}, nil
}

// Update registers the complete acquisition/publication operation with the
// owning service. Direct callers therefore receive the same Close fence as the
// periodic scheduler and startup acquisition.
func (u *ManagedUpdater) Update(ctx context.Context) (UpdateResult, error) {
	if u == nil || u.service == nil {
		return "", fmt.Errorf("geoip: nil managed updater")
	}
	var result UpdateResult
	err := u.service.RunOwnedUpdate(ctx, func(operation context.Context) error {
		var err error
		result, err = u.update(operation)
		return err
	})
	return result, err
}
