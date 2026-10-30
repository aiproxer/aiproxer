//go:build !unix

package main

import (
	"context"
	"errors"
)

// ErrSIGHUPUnavailable is returned by helpers that require Unix SIGHUP (req 1.8).
var ErrSIGHUPUnavailable = errors.New("aipstd: SIGHUP reload unavailable on this platform")

// SIGHUPAdapter is an API-only stub on platforms without SIGHUP (req 1.8, 11.8).
// It never registers signals and never invokes the sink.
type SIGHUPAdapter struct {
	sink ReloadTriggerSink
}

// NewSIGHUPAdapter constructs the non-Unix API-only adapter stub.
func NewSIGHUPAdapter(sink ReloadTriggerSink) *SIGHUPAdapter {
	return &SIGHUPAdapter{sink: sink}
}

// Start is a no-op on non-Unix platforms.
func (a *SIGHUPAdapter) Start(context.Context) error { return nil }

// Stop is a no-op on non-Unix platforms.
func (a *SIGHUPAdapter) Stop() {}
