package modelregistry

import (
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

const (
	CapabilitySourceStaticConfig    = "static_config"
	CapabilitySourceRemoteDiscovery = "remote_discovery"
	CapabilitySourceBuiltin         = "builtin"
)

// MaxInventoryModelSample is the maximum number of model rows exposed in operator diagnostics.
const MaxInventoryModelSample = 3

func capabilitySourceFromInventorySource(src modelinventory.Source) string {
	switch src {
	case modelinventory.SourceStaticInline, modelinventory.SourceStaticFile:
		return CapabilitySourceStaticConfig
	case modelinventory.SourceRemote:
		return CapabilitySourceRemoteDiscovery
	case modelinventory.SourceStaticBuiltin:
		return CapabilitySourceBuiltin
	default:
		return ""
	}
}

func primaryPrefix(prefixes []string) string {
	if len(prefixes) == 0 {
		return ""
	}
	return strings.TrimSpace(prefixes[0])
}

func enrichBackendModel(row BackendModel, inv BackendInventory) BackendModel {
	row.Prefix = primaryPrefix(inv.BackendPrefixes)
	row.CapabilitySource = capabilitySourceFromInventorySource(row.Source)
	return row
}

func enrichBackendModels(rows []BackendModel, inv BackendInventory) []BackendModel {
	if len(rows) == 0 {
		return rows
	}
	out := make([]BackendModel, len(rows))
	for i, row := range rows {
		out[i] = enrichBackendModel(row, inv)
	}
	return out
}
