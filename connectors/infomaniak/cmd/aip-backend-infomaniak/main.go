package main

import (
	"github.com/aiproxer/aiproxer/connectors/infomaniak/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-infomaniak", service.New())
}
