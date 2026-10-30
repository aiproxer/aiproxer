//go:build !unix

package main

import (
	"context"
)

// Task 1.5 contract reference: non-Unix API-only reload surface (req 1.8, 11.8).

// RefSIGHUPTrigger is an API-only stub; Notify is a no-op coalesce counter.
type RefSIGHUPTrigger struct {
	coalesced int64
}

func NewRefSIGHUPTrigger() *RefSIGHUPTrigger { return &RefSIGHUPTrigger{} }

func (t *RefSIGHUPTrigger) Coalesced() int64   { return t.coalesced }
func (t *RefSIGHUPTrigger) Delivered() int64   { return 0 }
func (t *RefSIGHUPTrigger) C() <-chan struct{} { return nil }
func (t *RefSIGHUPTrigger) Notify()            { t.coalesced++ }

func StartRefSIGHUPAdapter(ctx context.Context, trigger *RefSIGHUPTrigger) (stop func()) {
	return func() {}
}
