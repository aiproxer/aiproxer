package httpauth

import "github.com/aiproxer/aiproxer/pkg/aipsdk"

// Transport-local aliases for auth error rendering contracts defined on [github.com/aiproxer/aiproxer/pkg/aipsdk]
// so stdhttp and plugins can refer to this subpackage without duplicating types, while
// internal/core avoids a transitive dependency on pkg/aipsdk/transport (see internal/archtest).
type (
	AuthErrorRenderInput  = aipsdk.AuthErrorRenderInput
	AuthErrorRenderResult = aipsdk.AuthErrorRenderResult
	AuthErrorRenderer     = aipsdk.AuthErrorRenderer
)
