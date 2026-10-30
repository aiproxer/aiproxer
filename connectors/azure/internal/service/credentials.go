package service

import (
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

// decodeKeySlots validates host-issued ordered api_key Configure secrets
// through the shared slot decoder and returns slots in ascending order with
// their nonsecret slot identities. Sparse host numbering is preserved as
// ascending order; every rejection is fail-closed and secret-free: errors
// name only slot names, never key bytes.
func decodeKeySlots(bundle backendplugin.SecretBundle) ([]openaicompat.SlotSecret, error) {
	return openaicompat.DecodeAPIKeySlotSecrets(bundle)
}

// newKeyPool lifts ordered slots into pool credentials keyed by nonsecret
// slot name, so per-key wire clients stay bound to coordinator selection.
func newKeyPool(slots []openaicompat.SlotSecret) (*credpool.Pool, error) {
	creds := make([]credpool.Credential, 0, len(slots))
	for _, slot := range slots {
		if strings.TrimSpace(slot.Secret) == "" {
			continue
		}
		creds = append(creds, credpool.Credential{ID: slot.Name, Secret: slot.Secret})
	}
	return credpool.New(creds)
}
