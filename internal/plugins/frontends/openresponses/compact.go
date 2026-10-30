package openresponses

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	proto "github.com/aiproxer/aiproxer/internal/plugins/protocols/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// CompactResourceIDSource issues identifiers for the standalone compaction
// resource. These are deliberately separate from proxy response IDs because a
// compact result is not a continuation record.
type CompactResourceIDSource interface {
	NewCompactResourceID() string
}

type systemCompactResourceIDSource struct{}

var _ CompactResourceIDSource = systemCompactResourceIDSource{}

func (systemCompactResourceIDSource) NewCompactResourceID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "comp_" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("comp_%d", time.Now().UnixNano())
}

const (
	maxCompactEvents    = 100_000
	maxCompactItems     = 10_000
	maxCompactTextBytes = 64 << 20
	maxCompactToolBytes = 128 << 20
)

func compactEnvelope(decoded *DecodedCompact, responseID string, now time.Time) proto.EnvelopeMetadata {
	return proto.EnvelopeMetadata{
		ResponseID:  responseID,
		CreatedAt:   now,
		CompletedAt: &now,
		Model:       decoded.Model,
	}
}

func collectCompact(ctx context.Context, stream aipapi.EventStream, envelope proto.EnvelopeMetadata, options aipapi.GenerationOptions, limits proto.Limits) (resource []byte, err error) {
	if stream == nil {
		return nil, errDriveNilStream
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil && err == nil {
			err = errDriveStreamCloseFailed
			resource = nil
		}
	}()

	sm := proto.NewStateMachine(envelope, options, effectiveProtocolLimits(limits))
	var usage proto.UsageStats
	_, driveErr := driveStateMachine(ctx, stream, sm, driveOptions{
		limits:              compactDriveLimits(),
		stopOnTerminal:      false,
		errorEventIsFailure: true,
		checkContext:        true,
	}, func(ev aipapi.Event, _ []proto.StreamEvent) error {
		if ev.Kind == aipapi.EventUsageDelta {
			usage.InputTokens += ev.InputTokens
			usage.OutputTokens += ev.OutputTokens
			usage.TotalTokens += ev.TotalTokens
			usage.CachedTokens += ev.CacheReadTokens
			usage.ReasoningTokens += ev.ReasoningTokens
		}
		return nil
	})
	if driveErr != nil {
		return nil, driveErr
	}
	if sm.State() != proto.StateTerminal {
		return nil, errDriveNoTerminal
	}

	env := envelope
	env.Status = sm.Status()
	_, resourceJSON, buildErr := proto.BuildCompactResource(env, sm.Trajectory(), usage)
	if buildErr != nil {
		return nil, errDriveBuildCompact
	}
	return resourceJSON, nil
}
