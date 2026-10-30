package bunstore

import "time"

// Options configures optional Bun secure-session store behavior.
// Zero value matches legacy uncached constructors.
type Options struct {
	SQLQueryCacheTTL        time.Duration
	SQLQueryCacheMaxEntries int
}
