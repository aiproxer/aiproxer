package acp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Resolver resolves an executable name to a path.
type Resolver func(string) (string, error)

type executableCacheGeneration struct {
	m sync.Map // map[string]func() lookPathResult
}

// ExecutableCache is an instance-owned LookPath cache. Callers (connector
// instances) own a cache so PATH mutations in tests do not race other instances.
type ExecutableCache struct {
	mu         sync.Mutex
	generation *executableCacheGeneration
	resolver   Resolver
}

type lookPathResult struct {
	path string
	err  error
}

// NewExecutableCache creates an instance-owned executable cache. A nil
// resolver uses exec.LookPath.
func NewExecutableCache(resolver Resolver) *ExecutableCache {
	return &ExecutableCache{resolver: resolver}
}

func (c *ExecutableCache) LookPath(file string) (string, error) {
	if c == nil {
		return exec.LookPath(file)
	}
	c.mu.Lock()
	if c.generation == nil {
		c.generation = &executableCacheGeneration{}
	}
	generation := c.generation
	resolver := c.resolver
	c.mu.Unlock()
	if resolver == nil {
		resolver = exec.LookPath
	}
	v, _ := generation.m.LoadOrStore(file, sync.OnceValue(func() lookPathResult {
		p, e := resolver(file)
		return lookPathResult{path: p, err: e}
	}))
	r, ok := v.(func() lookPathResult)
	if !ok {
		return "", fmt.Errorf("acp: lookPath cache value of unexpected type %T for %q", v, file)
	}
	got := r()
	return got.path, got.err
}

func (c *ExecutableCache) Reset() {
	if c == nil {
		return
	}
	// Publish a new generation instead of clearing the old map. Lookups that
	// already captured the old generation may finish without blocking Reset.
	c.mu.Lock()
	c.generation = &executableCacheGeneration{}
	c.mu.Unlock()
}

func (c *ExecutableCache) CheckExecutable(candidate string) (string, bool) {
	cand := strings.TrimSpace(candidate)
	if cand == "" {
		return "", false
	}
	if filepath.IsAbs(cand) {
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand, true
		}
		return "", false
	}
	if resolved, err := c.LookPath(cand); err == nil {
		return resolved, true
	}
	return "", false
}

// defaultExeCache backs package-level helpers used by support-module tests.
// Production connectors must construct their own [ExecutableCache].
var defaultExeCache = &ExecutableCache{}
