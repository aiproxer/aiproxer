package repair

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"

	"golang.org/x/sync/singleflight"
)

type cacheEntry struct {
	key    string
	schema *CompiledSchema
	bytes  int
	prev   *cacheEntry
	next   *cacheEntry
}

type SchemaCache struct {
	mu      sync.Mutex
	limits  SchemaLimits
	entries map[string]*cacheEntry
	head    *cacheEntry
	tail    *cacheEntry
	bytes   int
	group   singleflight.Group
	// compile is the schema compiler seam. Production constructs it with
	// compileSchema; tests replace it to hold one shared flight open.
	compile func(context.Context, json.RawMessage, SchemaLimits) (*CompiledSchema, error)
}

func NewSchemaCache(limits SchemaLimits) *SchemaCache {
	return &SchemaCache{
		limits:  limits.normalized(),
		entries: make(map[string]*cacheEntry),
		compile: compileSchema,
	}
}

// Limits returns a snapshot of the cache's normalized schema limits.
func (c *SchemaCache) Limits() SchemaLimits {
	if c == nil {
		return DefaultSchemaLimits()
	}
	return c.limits
}

func (c *SchemaCache) GetOrCompile(schema json.RawMessage) (*CompiledSchema, error) {
	return c.GetOrCompileWithContext(context.Background(), schema)
}

func (c *SchemaCache) GetOrCompileWithContext(ctx context.Context, schema json.RawMessage) (cs *CompiledSchema, err error) {
	defer func() {
		if r := recover(); r != nil {
			cs = nil
			err = schemaErr(SchemaKindInvalid, ReasonCompilePanic, "")
		}
	}()
	if c == nil {
		return compileSchema(ctx, schema, DefaultSchemaLimits())
	}
	if err := ctx.Err(); err != nil {
		return nil, schemaErr(SchemaKindInvalid, ReasonCanceled, "")
	}
	digest := schemaDigest(schema)

	c.mu.Lock()
	if e, ok := c.entries[digest]; ok {
		c.moveToFrontLocked(e)
		out := e.schema
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()

	// One shared compile serves every concurrent caller, so it must not
	// inherit any single caller's cancellation: a request that disconnects
	// would otherwise fail the compile for every peer waiting on the same
	// schema. SchemaLimits bound the work; each caller still stops waiting on
	// its own context below. DoChan, rather than Do, makes that wait
	// cancelable instead of blocking until the shared compile finishes.
	compileCtx := context.WithoutCancel(ctx)
	resultCh := c.group.DoChan(digest, func() (v any, err error) {
		// DoChan runs the shared function on its own goroutine, where a raw
		// panic would terminate the process instead of reaching the
		// caller-facing recovery. Keep that boundary here.
		defer func() {
			if recover() != nil {
				v = nil
				err = schemaErr(SchemaKindInvalid, ReasonCompilePanic, "")
			}
		}()
		c.mu.Lock()
		if e, ok := c.entries[digest]; ok {
			c.moveToFrontLocked(e)
			out := e.schema
			c.mu.Unlock()
			return out, nil
		}
		c.mu.Unlock()

		compiled, err := c.compileShared(compileCtx, schema)
		if err != nil {
			return nil, err
		}
		compiled.digest = digest
		compiled.bytes = len(schema)

		c.mu.Lock()
		defer c.mu.Unlock()
		if e, ok := c.entries[digest]; ok {
			c.moveToFrontLocked(e)
			return e.schema, nil
		}
		c.addLocked(digest, compiled)
		return compiled, nil
	})

	var v any
	select {
	case <-ctx.Done():
		return nil, schemaErr(SchemaKindInvalid, ReasonCanceled, "")
	case result := <-resultCh:
		if result.Err != nil {
			return nil, result.Err
		}
		v = result.Val
	}
	out, _ := v.(*CompiledSchema)
	if out == nil {
		return nil, schemaErr(SchemaKindInvalid, ReasonInvalidSchema, "")
	}
	return out, nil
}

// compileShared runs one compile through the production compiler or the test
// seam. Its caller owns the panic boundary because it runs on the singleflight
// goroutine.
func (c *SchemaCache) compileShared(ctx context.Context, schema json.RawMessage) (*CompiledSchema, error) {
	if c.compile != nil {
		return c.compile(ctx, schema, c.limits)
	}
	return compileSchema(ctx, schema, c.limits)
}

func (c *SchemaCache) addLocked(digest string, compiled *CompiledSchema) {
	approx := compiled.bytes
	for (len(c.entries) >= c.limits.MaxCacheEntries || c.bytes+approx > c.limits.MaxCacheBytes) && c.tail != nil {
		c.evictTailLocked()
	}
	if approx > c.limits.MaxCacheBytes {
		return
	}
	e := &cacheEntry{key: digest, schema: compiled, bytes: approx}
	c.entries[digest] = e
	c.bytes += approx
	c.pushFrontLocked(e)
}

func (c *SchemaCache) evictTailLocked() {
	e := c.tail
	if e == nil {
		return
	}
	delete(c.entries, e.key)
	c.bytes -= e.bytes
	if e.prev != nil {
		e.prev.next = nil
	} else {
		c.head = nil
	}
	c.tail = e.prev
	e.prev = nil
	e.next = nil
}

func (c *SchemaCache) pushFrontLocked(e *cacheEntry) {
	e.prev = nil
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	} else {
		c.tail = e
	}
	c.head = e
}

func (c *SchemaCache) moveToFrontLocked(e *cacheEntry) {
	if c.head == e {
		return
	}
	if e.prev != nil {
		e.prev.next = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	}
	if c.tail == e {
		c.tail = e.prev
	}
	e.prev = nil
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

func sha256Sum(b []byte) [sha256.Size]byte {
	return sha256.Sum256(b)
}

var packageSchemaCache = sync.OnceValue(func() *SchemaCache {
	return NewSchemaCache(DefaultSchemaLimits())
})
