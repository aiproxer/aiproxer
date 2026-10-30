package config_test

import (
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestInterleaved_DefaultsDisabled(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if cfg.Interleaved.Enabled {
		t.Fatal("interleaved thinking must be disabled by default")
	}
}

func TestInterleavedConfig_MovedSymbolsAbsent(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[config.InterleavedConfig]()
	for _, field := range []string{"StreamToClient", "RegularTurnsRemaining", "MaxMemoBytes", "InstructionsFile"} {
		if _, ok := typ.FieldByName(field); ok {
			t.Fatalf("config.InterleavedConfig must not contain moved field %q", field)
		}
	}
}
