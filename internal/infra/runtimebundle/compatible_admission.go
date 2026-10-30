package runtimebundle

import (
	"fmt"
	"strings"

	compatibleadmission "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/compatible"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
)

func attachCompatibleAdmission(prod *ProductionOptions, cfg *config.Config) error {
	if prod == nil || cfg == nil {
		return nil
	}
	limits, err := standardplugins.CollectCompatibleAdmissionLimits(cfg.Plugins.Backends)
	if err != nil {
		return fmt.Errorf("runtimebundle: compatible admission limits: %w", err)
	}
	if len(limits) == 0 {
		return nil
	}
	store := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "compatible-admission"})
	reg, _, err := compatibleadmission.AttemptRegistration(limits, store)
	if err != nil {
		return err
	}
	for _, existing := range prod.AttemptRegistrations {
		if strings.TrimSpace(existing.Descriptor.ID) == compatibleadmission.ProviderID {
			return nil
		}
	}
	prod.AttemptRegistrations = append(prod.AttemptRegistrations, reg)
	return nil
}
