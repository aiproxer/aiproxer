package main

import (
	"github.com/aiproxer/aiproxer/connectors/llamacpp/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-llamacpp", service.New())
}
