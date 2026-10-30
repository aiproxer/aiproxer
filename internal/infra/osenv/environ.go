package osenv

import (
	"os"
	"slices"
)

// Process implements secretguard.Environment using the process environment.
type Process struct{}

// Lookup implements secretguard.Environment.
func (Process) Lookup(name string) (string, bool) {
	return os.LookupEnv(name)
}

// Snapshot implements secretguard.Environment.
func (Process) Snapshot() []string {
	return slices.Clone(os.Environ())
}
