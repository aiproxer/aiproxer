package main

import (
	"github.com/aiproxer/aiproxer/connectors/nvidia/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-nvidia", service.New())
}
