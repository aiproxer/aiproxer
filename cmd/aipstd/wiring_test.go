package main

import (
	"path/filepath"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestReferenceConfigSatisfiesMandatoryStandardPlugins(t *testing.T) {
	t.Parallel()

	cfgPath := filepath.Join("..", "..", "config", "config.yaml")
	cfg, err := config.LoadFile(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if err := routing.ValidateModelAliasesConfig(cfg); err != nil {
		t.Fatalf("model_aliases: %v", err)
	}

	regs := config.RegistrationsFromConfig(cfg)
	if err := aipsdk.ValidateRegistrations(regs, mandatoryStandardPlugins()); err != nil {
		t.Fatalf("bootstrap validation: %v", err)
	}
}
