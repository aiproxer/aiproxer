package metering

import (
	"fmt"
	"strings"
)

// FactRef is a durable journal identity handle for rating and authority binding
// (design Deterministic Identity / D6). It excludes raw call content.
type FactRef struct {
	StreamID string `json:"stream_id"`
	FactID   string `json:"fact_id"`
}

// Validate requires stream and fact identity.
func (r FactRef) Validate() error {
	if strings.TrimSpace(r.StreamID) == "" {
		return fmt.Errorf("%w: fact_ref stream_id required", ErrInvalidFact)
	}
	if strings.TrimSpace(r.FactID) == "" {
		return fmt.Errorf("%w: fact_ref fact_id required", ErrInvalidFact)
	}
	return nil
}

// FactRef returns the durable identity handle for this fact.
func (f Fact) FactRef() FactRef {
	return FactRef{
		StreamID: strings.TrimSpace(f.StreamID),
		FactID:   strings.TrimSpace(f.FactID),
	}
}

// FactRefsFactIDs returns FactID values in order.
func FactRefsFactIDs(refs []FactRef) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		id := strings.TrimSpace(r.FactID)
		if id == "" {
			continue
		}
		out = append(out, id)
	}
	return out
}
