// Package scope holds the authoritative, protocol-neutral principal/scope
// attribution snapshot for an accepted AIProxer request.
//
// Values in this package are safe-by-construction: raw credentials, raw
// transport headers, bearer/API/OAuth/resume tokens, and unvetted claim
// values are never fields on [PrincipalScopeView]. Only non-secret
// identifiers, display labels, roles, operator-safe claims, and policy
// labels are carried.
//
// The snapshot is immutable request lifecycle evidence. Consumers receive
// copies produced by [PrincipalScopeView.Clone] so roles, claims, and labels
// cannot be mutated through returned views.
package scope
