package runtimebundle

import (
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	cpadmin "github.com/aiproxer/aiproxer/internal/stdhttp/admin/controlplane"
)

// adaptAccountingAuthorityQueries projects the injected usage-limit port onto
// the narrow HTTP query contract owned by stdhttp/admin/controlplane.
//
// The identity projection keeps the concrete port type out of the HTTP layer:
// a port that implements the composed status/query surface passes through
// unchanged, and a nil or non-query port becomes a true nil interface, which
// leaves the optional authority routes disabled (404).
func adaptAccountingAuthorityQueries(port usagelimit.Authority) cpadmin.AccountingAuthorityQueries {
	queries, _ := port.(cpadmin.AccountingAuthorityQueries)
	return queries
}
