package largebody

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

// WireReplayFactory produces fresh offset-zero readers over the exact wire
// bytes for adapter-local credential retries
// (api-key-backend-credential-contract task 8, requirements 3.4, 3.5, 4.3).
//
// The factory is owned by the largebody wire boundary: adapters must never
// assume a consumed request reader is seekable and must never buffer
// unbounded payloads to enable rotation. Each NewReader call returns an
// independent reader the caller must close; closing releases the underlying
// source reader. The zero value is invalid and fails closed.
type WireReplayFactory struct {
	newReader func() (io.ReadCloser, int64, error)
}

// NewWireReplayFactory binds a replay constructor. A nil constructor yields
// an invalid factory that fails closed on NewReader.
func NewWireReplayFactory(newReader func() (io.ReadCloser, int64, error)) WireReplayFactory {
	return WireReplayFactory{newReader: newReader}
}

// Valid reports whether the factory can produce replay readers.
func (f WireReplayFactory) Valid() bool {
	return f.newReader != nil
}

// NewReader returns a fresh offset-zero reader over the exact wire bytes and
// its exact length. Every successful call must be paired with Close.
func (f WireReplayFactory) NewReader() (io.ReadCloser, int64, error) {
	if f.newReader == nil {
		return nil, 0, fmt.Errorf("largebody: wire replay is not available for this request")
	}
	return f.newReader()
}

// ProveWireReplay binds an exact-bytes replay factory over an immutable
// completed source when safe credential rotation can be proven:
//
//   - src is a completed immutable capture supporting independent readers;
//   - accepted is an accepted assessment carrying a bound stamp;
//   - a certified model-token rewrite requires a non-empty candidate model.
//
// It returns ok=false when replay cannot be proven. Callers must then make a
// single provider attempt: mark future credential state from the outcome and
// return the classified error without another HTTP attempt, extra buffering,
// or duplicate provider execution.
func ProveWireReplay(src Source, accepted Assessment, candidateModel string) (WireReplayFactory, bool) {
	if src == nil {
		return WireReplayFactory{}, false
	}
	if !accepted.Accepted() || accepted.Stamp.IsZero() {
		return WireReplayFactory{}, false
	}
	stamp := accepted.Stamp
	if stamp.BodyBytes() <= 0 {
		return WireReplayFactory{}, false
	}
	if err := stamp.BodyMode().Validate(); err != nil {
		return WireReplayFactory{}, false
	}
	rewrite := stamp.Rewrite()
	if rewrite.NeedsModelRewrite() && strings.TrimSpace(candidateModel) == "" {
		return WireReplayFactory{}, false
	}
	return NewWireReplayFactory(func() (io.ReadCloser, int64, error) {
		return openWireReplayReader(src, rewrite, candidateModel)
	}), true
}

// openWireReplayReader opens one fresh offset-zero reader over the exact wire
// bytes: a certified model-token splice when the stamp requires it, otherwise
// a direct source reader. It mirrors the wire execution body contract so the
// first attempt and every replay carry identical bytes.
func openWireReplayReader(src Source, rewrite RewriteSemantics, candidateModel string) (io.ReadCloser, int64, error) {
	if rewrite.NeedsModelRewrite() {
		splice, err := SpliceModelToken(src, rewrite.Span(), candidateModel)
		if err != nil {
			return nil, 0, err
		}
		return splice, splice.RewrittenLength(), nil
	}
	rc, err := src.Open()
	if err != nil {
		return nil, 0, err
	}
	return rc, src.Size(), nil
}

// NewWireReplayFromBytes snapshots a bounded in-memory payload for replay.
// Bodies larger than maxBytes are rejected so rotation never causes runaway
// buffering; the payload is defensively copied so later caller mutations
// cannot change replayed bytes. A non-positive maxBytes fails closed.
func NewWireReplayFromBytes(body []byte, maxBytes int64) (WireReplayFactory, error) {
	if maxBytes <= 0 {
		return WireReplayFactory{}, fmt.Errorf("largebody: wire replay snapshot budget must be > 0, got %d", maxBytes)
	}
	if int64(len(body)) > maxBytes {
		return WireReplayFactory{}, fmt.Errorf("largebody: wire replay snapshot of %d bytes exceeds %d-byte budget", len(body), maxBytes)
	}
	snapshot := append([]byte(nil), body...)
	return NewWireReplayFactory(func() (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(snapshot)), int64(len(snapshot)), nil
	}), nil
}
