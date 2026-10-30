package continuation

import (
	"encoding/json"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Lineage captures model/route context required for portable reroute or pinning.
type Lineage struct {
	ProfileID     string
	Model         string
	RouteSelector string
	// ProviderBound prevents a record containing provider-specific semantics from
	// silently moving to a different candidate.
	ProviderBound bool
	ProviderID    string
	CandidateKey  string
}

// NativeRequirement describes private provider evidence that is valid only for
// one exact backend/model lineage. It is never a provider request field.
type NativeRequirement struct {
	BackendID   string
	Model       string
	Kind        string
	Dialect     string
	Implementor string
}

// RecordStatus describes whether a terminal record can be used as a parent.
type RecordStatus string

const (
	RecordStatusCompleted  RecordStatus = "completed"
	RecordStatusIncomplete RecordStatus = "incomplete"
	RecordStatusFailed     RecordStatus = "failed"
)

// NativeReference is private provider evidence retained for exact-lineage optimizations.
// It is never a proxy ID and must not be forwarded as client continuation state.
type NativeReference struct {
	Provider string
	Kind     string
	ID       string
	Opaque   []byte
}

// String returns a redacted string representation to prevent sensitive native state leakage.
func (r NativeReference) String() string {
	return "[REDACTED_NATIVE_REF]"
}

// GoString returns a redacted representation for fmt %#v formatting.
func (r NativeReference) GoString() string {
	return "[REDACTED_NATIVE_REF]"
}

// ContinuationRecord is the protocol-neutral persisted continuation payload.
type ContinuationRecord struct {
	ID                 ResponseID
	Scope              Scope
	PreviousID         ResponseID
	ProfileID          string
	Lineage            Lineage
	InputItems         []aipapi.Item
	OutputItems        []aipapi.Item
	Requirements       aipapi.ProtocolRequirements
	Policy             StoragePolicy
	ExpiresAt          time.Time
	Terminal           bool
	Status             RecordStatus
	NativeRefs         []NativeReference
	NativeRequirements []NativeRequirement
	MaterializedBytes  int64
	ChainDepth         int
}

// CloneItems returns defensive copies of trajectory slices, including nested JSON.
func CloneItems(in []aipapi.Item) []aipapi.Item {
	if in == nil {
		return nil
	}
	data, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out []aipapi.Item
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

// CloneRequirements returns an independent requirements value.
func CloneRequirements(in aipapi.ProtocolRequirements) aipapi.ProtocolRequirements {
	return aipapi.NormalizeProtocolRequirements(aipapi.ProtocolRequirements{
		Capabilities:       append([]aipapi.Capability(nil), in.Capabilities...),
		ItemDialects:       append([]aipapi.DialectRequirement(nil), in.ItemDialects...),
		ReasoningDialects:  append([]aipapi.DialectRequirement(nil), in.ReasoningDialects...),
		CompactionDialects: append([]aipapi.DialectRequirement(nil), in.CompactionDialects...),
		ExtensionTypes:     append([]aipapi.ExtensionRequirement(nil), in.ExtensionTypes...),
	})
}

// CloneRecord returns a deep copy suitable for crossing the store boundary.
func CloneRecord(in ContinuationRecord) ContinuationRecord {
	out := in
	out.InputItems = CloneItems(in.InputItems)
	out.OutputItems = CloneItems(in.OutputItems)
	out.Requirements = CloneRequirements(in.Requirements)
	if in.NativeRefs != nil {
		out.NativeRefs = make([]NativeReference, len(in.NativeRefs))
		for i, ref := range in.NativeRefs {
			out.NativeRefs[i] = ref
			out.NativeRefs[i].Opaque = append([]byte(nil), ref.Opaque...)
		}
	}
	out.NativeRequirements = append([]NativeRequirement(nil), in.NativeRequirements...)
	return out
}

// EffectiveStatus treats records written by the Phase 1 contract as completed.
func EffectiveStatus(record ContinuationRecord) RecordStatus {
	if record.Status == "" {
		return RecordStatusCompleted
	}
	return record.Status
}

// RecordSize returns a conservative serialized size for storage accounting.
func RecordSize(record ContinuationRecord) int64 {
	data, err := json.Marshal(record)
	if err != nil {
		return 1<<63 - 1
	}
	if n := int64(len(data)); n > record.MaterializedBytes {
		return n
	}
	return record.MaterializedBytes
}
