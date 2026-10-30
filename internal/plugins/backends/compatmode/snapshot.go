package compatmode

import (
	"github.com/aiproxer/aiproxer/internal/pluginreg"
)

// ResolveCustomKeys returns the admitted ordered secrets for one custom
// compatible credential root (requirement 1.4). When the composition root
// captured an authorized candidate snapshot, its values are authoritative: a
// root the snapshot does not carry resolves to no keys rather than falling
// back to ambient material, so serving keeps admitted credentials across
// process-environment rotation. Without a snapshot (legacy build path) the
// candidate-build process-environment lookup applies.
func ResolveCustomKeys(apiKeyEnvVarRoot string, snap pluginreg.HostCredentialSnapshot) []string {
	if snap.Resolved() {
		return snap.Keys(apiKeyEnvVarRoot)
	}
	return ResolveEnvAPIKeys(apiKeyEnvVarRoot)
}
