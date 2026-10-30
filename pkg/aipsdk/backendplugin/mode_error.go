package backendplugin

import "fmt"

// ModeError is a typed conformance/fake diagnostic with a stable Code.
type ModeError struct {
	Code string
}

func (e ModeError) Error() string {
	if e.Code == "" {
		return "backendplugin: mode error"
	}
	return e.Code
}

// ModeErrorf builds a ModeError with a formatted Code.
func ModeErrorf(code string, args ...any) ModeError {
	if len(args) == 0 {
		return ModeError{Code: code}
	}
	return ModeError{Code: fmt.Sprintf(code, args...)}
}
