package config_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestValidate_MeteringJournalDefaultsOff(t *testing.T) {
	t.Parallel()
	cfg := validTokenAccountingConfig()
	cfg.Metering = config.MeteringConfig{}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_MeteringJournalStore(t *testing.T) {
	t.Parallel()
	cfg := validTokenAccountingConfig()
	cfg.Metering.Enabled = true
	cfg.Metering.Journal.Store = "sqlite"
	if err := config.Validate(cfg); err == nil || !strings.Contains(err.Error(), "sqlite_path") {
		t.Fatalf("got %v", err)
	}
	cfg.Metering.Journal.SQLitePath = "metering.db"
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}
}
