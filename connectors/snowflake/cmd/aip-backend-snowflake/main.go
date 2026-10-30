package main

import (
	"github.com/aiproxer/aiproxer/connectors/snowflake/internal/service"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func main() {
	backendplugin.Main("aip-backend-snowflake", service.New())
}
