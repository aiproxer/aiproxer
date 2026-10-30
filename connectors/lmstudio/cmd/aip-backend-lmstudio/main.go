package main

import (
	"github.com/aiproxer/aiproxer/connectors/lmstudio/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-lmstudio", service.New())
}
