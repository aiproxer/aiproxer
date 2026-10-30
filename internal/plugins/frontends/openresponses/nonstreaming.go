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

// ResponseIDSource issues proxy-owned response identifiers.
type ResponseIDSource interface {
	NewResponseID() string
}

// ResponseClock supplies proxy-owned response timestamps.
type ResponseClock interface {
	Now() time.Time
}

type systemResponseIDSource struct{}

var _ ResponseIDSource = systemResponseIDSource{}

func (systemResponseIDSource) NewResponseID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "resp_" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("resp_%d", time.Now().UnixNano())
}

type systemResponseClock struct{}

var _ ResponseClock = systemResponseClock{}

func (systemResponseClock) Now() time.Time { return time.Now() }

const (
	maxNonStreamingEvents    = 100_000
	maxNonStreamingItems     = 10_000
	maxNonStreamingTextBytes = 64 << 20
	maxNonStreamingToolBytes = 128 << 20
)

func collectNonStreaming(ctx context.Context, stream aipapi.EventStream, envelope proto.EnvelopeMetadata, options aipapi.GenerationOptions, limits proto.Limits) (resource []byte, err error) {
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
	_, driveErr := driveStateMachine(ctx, stream, sm, driveOptions{
		limits:              collectDriveLimits(),
		stopOnTerminal:      true,
		errorEventIsFailure: true,
	}, nil)
	if driveErr != nil {
		return nil, driveErr
	}
	_, resourceJSON, buildErr := sm.AccumulateResource()
	if buildErr != nil {
		return nil, errDriveBuildResource
	}
	return resourceJSON, nil
}

func nonStreamingEnvelope(decoded *DecodedCreate, responseID string, now time.Time) proto.EnvelopeMetadata {
	return proto.EnvelopeMetadata{
		ResponseID:         responseID,
		PreviousResponseID: decoded.PreviousResponseID,
		CreatedAt:          now,
		CompletedAt:        &now,
		Model:              decoded.Model,
		Store:              &decoded.Store,
	}
}
