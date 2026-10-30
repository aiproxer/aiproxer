package modelcatalog

import (
	"maps"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// CloneBackendCaps returns a shallow copy of caps for safe in-place mutation, or nil when caps is nil.
func CloneBackendCaps(b aipapi.BackendCaps) aipapi.BackendCaps {
	if b == nil {
		return nil
	}
	return maps.Clone(b)
}
