package main

import (
	"github.com/aiproxer/aiproxer/connectors/vllm/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-vllm", service.New())
}
