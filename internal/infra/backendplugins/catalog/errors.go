package catalog

import "errors"

var (
	ErrEnabledUnresolved = errors.New("backendplugins/catalog: enabled kind unresolved")
	ErrStrictInvalid     = errors.New("backendplugins/catalog: strict mode rejects invalid artifacts")
)
