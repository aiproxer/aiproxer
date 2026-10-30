package config

import (
	"fmt"
	"strings"
)

func validateMetering(cfg *Config) error {
	if cfg == nil || !cfg.Metering.Enabled {
		return nil
	}
	store := strings.ToLower(strings.TrimSpace(cfg.Metering.Journal.Store))
	switch store {
	case "", "memory":
		return nil
	case "sqlite":
		if strings.TrimSpace(cfg.Metering.Journal.SQLitePath) == "" {
			return fmt.Errorf("config: metering.journal.sqlite_path required when store=sqlite")
		}
		return nil
	case "postgres":
		if strings.TrimSpace(cfg.Metering.Journal.PostgresDSN) == "" {
			return fmt.Errorf("config: metering.journal.postgres_dsn required when store=postgres")
		}
		return nil
	default:
		return fmt.Errorf("config: metering.journal.store must be memory, sqlite, or postgres")
	}
}
