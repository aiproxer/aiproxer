package repair

import (
	"github.com/aiproxer/aiproxer/pkg/aipsdk/toolcall"
)

func NormalizeASCIIName(name string) string {
	return toolcall.NormalizeToolName(name)
}
