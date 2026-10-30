package openresponses

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

var ErrCompactDecode = errors.New("openresponses: compact decode failed")

// DecodeCompactRequest is the compact endpoint's decode seam.
//
// The protocol package owns strict JSON bounds, duplicate-key detection, and
// wire-to-item conversion. The frontend adds authentication, route/model
// authority, compact-only field validation, and the canonical invocation
// metadata through AuthenticateAndDecodeCompact.
func DecodeCompactRequest(ctx context.Context, body []byte, opts DecodeCompactOptions) (*DecodedCompact, error) {
	return AuthenticateAndDecodeCompact(ctx, body, opts)
}

// CompactOperation is the narrow value handed to the compact executor seam.
// Keeping the call and its complete admission requirements together prevents a
// later execution slice from reconstructing requirements from wire data.
type CompactOperation struct {
	Call         *aipapi.Call
	Requirements aipapi.ProtocolRequirements
}

func CompactOperationFromDecoded(decoded *DecodedCompact) (CompactOperation, error) {
	if err := validateCompactOperation(decoded); err != nil {
		return CompactOperation{}, err
	}
	return CompactOperation{
		Call:         decoded.Call,
		Requirements: decoded.Requirements,
	}, nil
}

func validateCompactOperation(decoded *DecodedCompact) error {
	if decoded == nil || decoded.Call == nil {
		return ErrCompactDecode
	}
	if decoded.Call.Invocation.Operation != aipapi.OperationContextCompaction ||
		decoded.Call.Invocation.TransportMode != aipapi.TransportModeNonStreaming {
		return ErrCompactDecode
	}
	return nil
}
