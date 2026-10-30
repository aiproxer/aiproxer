package openresponses

import "errors"

// Sentinel errors for the independent reference backend emulator.
var (
	// ErrNoScriptSelected is returned when a request arrives while no script is active.
	ErrNoScriptSelected = errors.New("refbackend/openresponses: no script selected")
	// ErrUnknownScript is returned when Select names an unregistered script ID.
	ErrUnknownScript = errors.New("refbackend/openresponses: unknown script id")
)
