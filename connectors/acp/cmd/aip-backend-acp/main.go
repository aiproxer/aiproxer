package main

import (
	"github.com/aiproxer/aiproxer/connectors/acp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-acp", service.New())
}
