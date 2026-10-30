package gemini

import (
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendconfig"

	"gopkg.in/yaml.v3"
)

type Config = frontendconfig.Config

func DecodeConfig(n yaml.Node) (Config, error) {
	return frontendconfig.Decode(n, ID)
}
