package aipruntime

import (
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

// Ensure the public facade reload seam matches the production coordinator (req 16.1).
var (
	_ reloadQuery = (*runtimehost.Coordinator)(nil)
	_ reloadQuery = bundleHost{}
	_ hostAPI     = bundleHost{}
)
