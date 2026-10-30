package runtimebundle

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	infraGeoIP "github.com/aiproxer/aiproxer/internal/infra/geoip"
)

func configureGeoIPProcessService(parent context.Context, ps *ProcessServices, cfg *config.Config, register func(func() error)) error {
	if ps == nil || cfg == nil {
		return fmt.Errorf("runtimebundle: GeoIP process service requires config")
	}
	compiled, err := config.CompileGeoIP(cfg.Access.GeoIP)
	if err != nil {
		return err
	}
	requiresCountry := compiled.Enabled() && compiled.Policy() != nil && compiled.Policy().NeedsCountryLookup()
	switch compiled.DatabaseSource() {
	case config.GeoIPDatabaseSourceLocal:
		service, err := infraGeoIP.OpenLocal(cfg.Access.GeoIP.Database.LocalPath)
		if err != nil {
			if !requiresCountry {
				return nil
			}
			return err
		}
		ps.GeoIP = service
		register(service.Close)
	case config.GeoIPDatabaseSourceManaged:
		directory := strings.TrimSpace(cfg.Access.GeoIP.Database.Directory)
		if directory == "" {
			if !requiresCountry {
				return nil
			}
			return fmt.Errorf("runtimebundle: access.geoip.database.directory is required for managed source")
		}
		service, err := infraGeoIP.OpenManaged(directory, cfg.Access.GeoIP.Database.Edition)
		if err != nil {
			return err
		}
		ps.GeoIP = service
		register(service.Close)
		var client *http.Client
		if ps.opts != nil {
			client = ps.opts.Infra.HTTPClient
		}
		updater, err := infraGeoIP.NewManagedUpdater(service, infraGeoIP.ManagedConfig{
			Directory:  directory,
			Edition:    cfg.Access.GeoIP.Database.Edition,
			Interval:   compiled.UpdateInterval(),
			HTTPClient: client,
			Observe: func(result string) {
				if ps.Metrics != nil && ps.Metrics.GeoIP != nil {
					ps.Metrics.GeoIP.Update(result)
				}
			},
			Logger: ps.Logger,
		})
		if err != nil {
			return err
		}
		if requiresCountry {
			if _, err := updater.Update(parent); err != nil {
				return fmt.Errorf("runtimebundle: managed GeoIP startup acquisition: %w", err)
			}
		}
		if cfg.Access.GeoIP.Database.Update.Enabled {
			if err := service.StartManagedUpdater(updater, compiled.UpdateInterval()); err != nil {
				return err
			}
		}
	}
	if requiresCountry && (ps.GeoIP == nil || !ps.GeoIP.Ready()) {
		return fmt.Errorf("runtimebundle: GeoIP country lookup is not ready")
	}
	return nil
}
