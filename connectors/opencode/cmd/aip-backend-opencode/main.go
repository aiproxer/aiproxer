package main

import (
	"github.com/aiproxer/aiproxer/connectors/opencode/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-opencode", service.New())
}
