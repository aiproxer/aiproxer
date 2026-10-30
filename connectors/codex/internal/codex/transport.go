package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// wsTransportError marks a WebSocket failure that occurs before the first
// canonical event: dial/handshake, send of response.create, or first-frame
// read/close/timeout. Only these errors trigger auto HTTPS fallback.
// wsWrappedError is the shared base for WebSocket error sentinels: it formats a
// backend-prefixed message and unwraps to the underlying cause. Concrete types
// embed it so errors.As can still discriminate between transport and read errors.
type wsWrappedError struct {
	prefix string
	cause  error
}

func (e *wsWrappedError) Error() string {
	return fmt.Sprintf("%s: %s: %v", ID, e.prefix, e.cause)
}

func (e *wsWrappedError) Unwrap() error {
	return e.cause
}

type wsTransportError struct {
	wsWrappedError
}

func newWSTransportError(cause error) error {
	if cause == nil {
		return nil
	}
	return &wsTransportError{wsWrappedError{prefix: "websocket transport", cause: cause}}
}

func isWSTransportFailure(err error) bool {
	var e *wsTransportError
	return errors.As(err, &e)
}

type wsStreamReadError struct {
	wsWrappedError
}

func newWSStreamReadError(cause error) error {
	if cause == nil {
		return nil
	}
	return &wsStreamReadError{wsWrappedError{prefix: "read websocket", cause: cause}}
}

func isWSStreamReadError(err error) bool {
	var e *wsStreamReadError
	return errors.As(err, &e)
}

func wsPreFirstEventFailure(err error) error {
	if err == nil || isWSTransportFailure(err) {
		return err
	}
	if errors.Is(err, io.EOF) || isWSStreamReadError(err) {
		return newWSTransportError(err)
	}
	return err
}

// transportCooldown is a negative cache for WebSocket attempts. When auto mode
// records a fallback-eligible WS failure, markFailed pushes the cooldown window
// forward so subsequent auto attempts skip WS and go straight to HTTPS until the
// window expires.
type transportCooldown struct {
	mu       sync.Mutex
	until    time.Time
	cooldown time.Duration
	now      func() time.Time
}

func newTransportCooldown(cooldown time.Duration) *transportCooldown {
	if cooldown <= 0 {
		cooldown = DefaultWebSocketFallbackCooldown
	}
	return &transportCooldown{cooldown: cooldown, now: time.Now}
}

func (c *transportCooldown) active() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now().Before(c.until)
}

func (c *transportCooldown) markFailed() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.until = c.now().Add(c.cooldown)
}

// isWSFallbackError reports whether a WebSocket open failure should trigger auto
// fallback to HTTPS and record the cooldown. Context cancellation must never
// trigger fallback. Only typed pre-first-event transport failures qualify.
func isWSFallbackError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	return isWSTransportFailure(err)
}
