package main

import (
	"github.com/aiproxer/aiproxer/connectors/databricks/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-databricks", service.New())
}
