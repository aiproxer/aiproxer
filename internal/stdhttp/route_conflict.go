package stdhttp

import (
	"errors"
	"fmt"
	"strings"
)

// ErrRouteConflict is returned when request-plane route registration collides
// (frontend/diagnostics/models mounts) during generation composition.
var ErrRouteConflict = errors.New("stdhttp: route conflict")

// callMount runs fn and converts ServeMux route-registration panics into
// ErrRouteConflict. Non-conflict panics are re-raised.
func callMount(fn func() error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			msg := fmt.Sprint(p)
			if strings.Contains(msg, "conflicts with pattern") || strings.Contains(msg, "conflicts with") {
				err = fmt.Errorf("%w: %v", ErrRouteConflict, p)
				return
			}
			panic(p)
		}
	}()
	return fn()
}
