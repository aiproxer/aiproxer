package main

import (
	"github.com/aiproxer/aiproxer/connectors/geminicliacp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-geminicliacp", service.New())
}
