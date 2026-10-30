package main

import (
	"context"
	"errors"

	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

var errNilReloadSink = errors.New("aipstd: nil reload trigger sink")

// ReloadTriggerSink is the narrow coordinator surface used by OS signal and
// (later) management-API driving adapters. Callers never supply path or YAML.
type ReloadTriggerSink interface {
	Reload(ctx context.Context, trigger sdkreload.Trigger) sdkreload.Result
}
