package openresponses

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Observation captures a raw request or WebSocket turn envelope for test assertions.
// Sensitive headers and (optionally) bodies are redacted before storage.
type Observation struct {
	Path      string
	Method    string
	Headers   http.Header
	Body      []byte
	Timestamp time.Time
	Redacted  bool
}

// RedactedAuthorization is the placeholder written over credential headers.
const RedactedAuthorization = "[REDACTED]"

// defaultCaptureMax bounds the stored observation list.
const defaultCaptureMax = 128

// Capture is a thread-safe, bounded, redacting recorder of request observations
// plus atomic per-path counters used to prove zero-upstream rejection.
type Capture struct {
	total atomic.Int64

	counts sync.Map // map[string]*atomic.Int64

	mu           sync.Mutex
	maxBytes     int
	obs          []Observation
	overflow     int
	redactKeys   map[string]bool
	redactBodies bool
}

// NewCapture constructs a bounded capture. maxBytes <= 0 uses the default bound.
// redactHeaders lists header names (case-insensitive) that must be redacted;
// redactBodies controls whether bodies are replaced by a redaction marker.
func NewCapture(maxBytes int, redactHeaders []string, redactBodies bool) *Capture {
	if maxBytes <= 0 {
		maxBytes = defaultCaptureMax
	}
	keys := make(map[string]bool)
	for _, h := range redactHeaders {
		keys[strings.ToLower(strings.TrimSpace(h))] = true
	}
	return &Capture{
		maxBytes:     maxBytes,
		redactKeys:   keys,
		redactBodies: redactBodies,
	}
}

// Record captures one HTTP request and increments the total and per-path counters.
func (c *Capture) Record(r *http.Request, body []byte) Observation {
	obs := c.build(r.URL.Path, r.Method, r.Header, body)
	c.add(obs)
	return obs
}

// RecordHandshake captures a WebSocket upgrade handshake without counting it as
// a semantic create request (turn envelopes are counted by RecordFrame).
func (c *Capture) RecordHandshake(r *http.Request) Observation {
	obs := c.build(r.URL.Path, "WS_HANDSHAKE", r.Header, nil)
	c.store(obs)
	return obs
}

// RecordFrame captures a WebSocket turn envelope as an observation and counts it.
func (c *Capture) RecordFrame(path string, headers http.Header, body []byte) Observation {
	obs := c.build(path, "WS", headers, body)
	c.add(obs)
	return obs
}

func (c *Capture) build(path, method string, headers http.Header, body []byte) Observation {
	obs := Observation{
		Path:      path,
		Method:    method,
		Timestamp: time.Now(),
		Body:      append([]byte(nil), body...),
	}
	if headers != nil {
		obs.Headers = headers.Clone()
		redacted := false
		for k := range obs.Headers {
			if len(c.redactKeys) > 0 && c.redactKeys[strings.ToLower(k)] {
				obs.Headers.Set(k, RedactedAuthorization)
				redacted = true
			}
		}
		obs.Redacted = redacted
	}
	if c.redactBodies && len(obs.Body) > 0 {
		obs.Body = []byte(RedactedAuthorization)
		obs.Redacted = true
	}
	return obs
}

func (c *Capture) add(obs Observation) {
	c.total.Add(1)
	pc, _ := c.counts.LoadOrStore(obs.Path, new(atomic.Int64))
	if counter, ok := pc.(*atomic.Int64); ok {
		counter.Add(1)
	}
	c.store(obs)
}

func (c *Capture) store(obs Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.obs) >= c.maxBytes {
		c.overflow++
		return
	}
	c.obs = append(c.obs, obs)
}

// Total returns the atomic number of captured requests/frames.
func (c *Capture) Total() int64 { return c.total.Load() }

// Count returns the atomic per-path request count.
func (c *Capture) Count(path string) int64 {
	if pc, ok := c.counts.Load(path); ok {
		if counter, ok := pc.(*atomic.Int64); ok {
			return counter.Load()
		}
	}
	return 0
}

// Observations returns a defensive copy of the bounded capture.
func (c *Capture) Observations() []Observation {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Observation, len(c.obs))
	for i, o := range c.obs {
		cp := o
		if o.Body != nil {
			cp.Body = append([]byte(nil), o.Body...)
		}
		if o.Headers != nil {
			cp.Headers = o.Headers.Clone()
		}
		out[i] = cp
	}
	return out
}

// Last returns the most recent observation, if any.
func (c *Capture) Last() (Observation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.obs) == 0 {
		return Observation{}, false
	}
	last := c.obs[len(c.obs)-1]
	cp := last
	if last.Body != nil {
		cp.Body = append([]byte(nil), last.Body...)
	}
	if last.Headers != nil {
		cp.Headers = last.Headers.Clone()
	}
	return cp, true
}

// Len returns the number of stored observations.
func (c *Capture) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.obs)
}

// Overflow returns how many observations were dropped past the bound.
func (c *Capture) Overflow() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.overflow
}

// Reset clears the stored observations, overflow count, and all counters.
func (c *Capture) Reset() {
	c.total.Store(0)
	c.counts.Range(func(k, _ any) bool {
		c.counts.Delete(k)
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.obs = nil
	c.overflow = 0
}
