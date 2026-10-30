// Package gitscope provides environment hygiene for commands that must resolve
// a git repository from an explicit directory rather than from ambient state.
package gitscope

import (
	"os"
	"slices"
	"strings"
)

// repoEnvVars are the repository-location variables git honours when discovering
// the repository a command operates on.
//
// Git exports GIT_DIR (and friends) to every hook it runs, so any process started
// from a pre-commit or pre-push hook inherits them. A helper that means to build
// or inspect a throwaway fixture repository must therefore clear them: otherwise
// `git init` initialises the ambient repository instead of the fixture, and the
// fixture's `git add`/`git commit` rewrite the real repository's index and refs.
var repoEnvVars = [...]string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_COMMON_DIR",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_NAMESPACE",
	"GIT_CEILING_DIRECTORIES",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",
}

// WithoutRepoEnv returns env with git's repository-location variables removed so
// that a command rooted at dir discovers the repository at dir instead of an
// ambient one. Every other variable is preserved.
func WithoutRepoEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if slices.Contains(repoEnvVars[:], name) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// Environ returns the current process environment with git's repository-location
// variables removed. Use it as the Env of a command whose Dir selects the
// repository it is meant to operate on.
func Environ() []string {
	return WithoutRepoEnv(os.Environ())
}
