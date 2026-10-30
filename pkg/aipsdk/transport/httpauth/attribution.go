package httpauth

import "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"

// IngressAttribution is a transport alias for [secretguard.IngressAttribution]
// so auth middleware and core share one type without core importing this package.
type IngressAttribution = secretguard.IngressAttribution
