package service

import (
	"strings"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func providerHooks() openaicompat.RequestHooks {
	return openaicompat.RequestHooks{}
}

func resolveModel(kind string, inv backendplugin.Invocation, _ aipapi.Call) string {
	return strings.TrimPrefix(strings.TrimSpace(inv.CanonicalModelID), kind+"/")
}

func resolveFlavor(aipapi.Call) openaicompat.Flavor {
	return openaicompat.FlavorChat
}
