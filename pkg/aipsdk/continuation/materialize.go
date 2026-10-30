package continuation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// MaterializerProvenanceExtension is the proxy-owned canonical extension that
// records that an ordered item trajectory was assembled by the continuation
// materializer from a complete parent chain.
//
// The key lives in the reserved "aip." namespace. Only proxy code populates
// that namespace; frontends never copy client input into it, so the marker is
// provenance, not a client assertion.
const MaterializerProvenanceExtension = "aip.continuation.materialized.v1"

// MarkMaterialized records that the call's ordered items are a complete
// materialized trajectory. Every materializer that produces a provider-facing
// call marks its result; a consumer that must not rely on
// previous_response_id requires the marker instead of inferring completeness
// from a nonempty item slice.
func MarkMaterialized(call *aipapi.Call) {
	if call == nil {
		return
	}
	if call.Extensions == nil {
		call.Extensions = make(map[string]json.RawMessage, 1)
	}
	call.Extensions[MaterializerProvenanceExtension] = json.RawMessage(`true`)
}

// Materialized reports whether the call's ordered items were assembled by a
// continuation materializer from the complete parent trajectory.
func Materialized(call aipapi.Call) bool {
	raw, ok := call.Extensions[MaterializerProvenanceExtension]
	if !ok {
		return false
	}
	return bytes.Equal(bytes.TrimSpace(raw), []byte(`true`))
}

// MaterializeInput describes one bounded continuation materialization request.
type MaterializeInput struct {
	Store      Store
	Scope      Scope
	StartID    ResponseID
	NewInput   []aipapi.Item
	Bounds     Bounds
	Now        func() int64
	EstimateFn func(ContinuationRecord) int64
}

// Resolver resolves a client-visible previous response ID into a canonical call.
type Resolver interface {
	ResolveParent(ctx context.Context, scope Scope, parentID string, baseCall aipapi.Call) (aipapi.Call, ContinuationRecord, error)
}

// NewResolver constructs a resolver backed by a protocol-neutral continuation store.
func NewResolver(store Store, bounds Bounds) Resolver {
	return storeResolver{store: store, bounds: bounds}
}

type storeResolver struct {
	store  Store
	bounds Bounds
}

func (r storeResolver) ResolveParent(ctx context.Context, scope Scope, parentID string, baseCall aipapi.Call) (aipapi.Call, ContinuationRecord, error) {
	if r.store == nil || parentID == "" {
		return aipapi.Call{}, ContinuationRecord{}, ErrPreviousResponseNotFound
	}
	id := ResponseID(parentID)
	record, err := Lookup(ctx, r.store, scope, id)
	if err != nil {
		if errors.Is(err, ErrStorageFailure) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return aipapi.Call{}, ContinuationRecord{}, err
		}
		return aipapi.Call{}, ContinuationRecord{}, ErrPreviousResponseNotFound
	}
	trajectory, err := Materialize(ctx, MaterializeInput{Store: r.store, Scope: scope, StartID: id, NewInput: baseCall.Items, Bounds: r.bounds})
	if err != nil {
		if errors.Is(err, ErrStorageFailure) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return aipapi.Call{}, ContinuationRecord{}, err
		}
		return aipapi.Call{}, ContinuationRecord{}, ErrPreviousResponseNotFound
	}
	call := aipapi.CloneCall(baseCall)
	call.Items = CloneItems(trajectory.Items)
	call.Messages = nil
	call.Instructions = nil
	call.Session.ClientSessionID = ""
	call.Session.ContinuityKey = ""
	call.Session.AuthoritativeSessionID = ""
	call.Session.ResumeToken = ""
	MarkMaterialized(&call)
	return call, record, nil
}

// MaterializedTrajectory is prior input, prior output, then new input in order.
type MaterializedTrajectory struct {
	Items              []aipapi.Item
	InputItems         []aipapi.Item
	OutputItems        []aipapi.Item
	NewInput           []aipapi.Item
	ChainDepth         int
	TotalBytes         int64
	Lineage            Lineage
	Requirements       aipapi.ProtocolRequirements
	NativeRequirements []NativeRequirement
}

func mergeBounds(b Bounds) Bounds {
	defaults := DefaultBounds()
	if b.MaxChainDepth <= 0 {
		b.MaxChainDepth = defaults.MaxChainDepth
	}
	if b.MaxMaterializedItems <= 0 {
		b.MaxMaterializedItems = defaults.MaxMaterializedItems
	}
	if b.MaxMaterializedBytes <= 0 {
		b.MaxMaterializedBytes = defaults.MaxMaterializedBytes
	}
	return b
}

// MaterializeCall resolves proxy-owned continuation state into a fresh provider-facing call.
func MaterializeCall(ctx context.Context, in MaterializeInput, base aipapi.Call) (aipapi.Call, MaterializedTrajectory, error) {
	trajectory, err := Materialize(ctx, in)
	if err != nil {
		return aipapi.Call{}, MaterializedTrajectory{}, err
	}
	out := aipapi.CloneCall(base)
	out.Items = CloneItems(trajectory.Items)
	out.Messages = nil
	out.Instructions = nil
	out.Session.ClientSessionID = ""
	out.Session.ContinuityKey = ""
	out.Session.AuthoritativeSessionID = ""
	out.Session.ResumeToken = ""
	MarkMaterialized(&out)
	return out, trajectory, nil
}

// Materialize walks the previous_response_id chain, enforces depth/cycle/byte bounds,
// and returns the concatenated semantic order without invoking backends.
func Materialize(ctx context.Context, in MaterializeInput) (MaterializedTrajectory, error) {
	if ctx == nil || in.Store == nil || in.StartID.IsZero() {
		return MaterializedTrajectory{}, ErrPreviousResponseNotFound
	}
	bounds := mergeBounds(in.Bounds)
	estimate := in.EstimateFn
	if estimate == nil {
		estimate = EstimateRecordBytes
	}
	newInputBytes := EstimateItemsBytes(in.NewInput)
	if len(in.NewInput) > bounds.MaxMaterializedItems {
		return MaterializedTrajectory{}, ErrMaterializedItemsExceeded
	}
	if newInputBytes > bounds.MaxMaterializedBytes {
		return MaterializedTrajectory{}, ErrMaterializedSizeExceeded
	}

	visited := make(map[ResponseID]struct{})
	var records []ContinuationRecord
	var lineage Lineage
	var reqs aipapi.ProtocolRequirements
	var native []NativeRequirement
	var depth int
	var total int64
	var items int
	lineageSet := false
	cur := in.StartID
	for {
		if err := ctx.Err(); err != nil {
			return MaterializedTrajectory{}, err
		}
		if _, ok := visited[cur]; ok {
			return MaterializedTrajectory{}, ErrCycleDetected
		}
		visited[cur] = struct{}{}
		depth++
		if depth > bounds.MaxChainDepth {
			return MaterializedTrajectory{}, ErrChainDepthExceeded
		}
		rec, err := Lookup(ctx, in.Store, in.Scope, cur)
		if err != nil {
			return MaterializedTrajectory{}, err
		}
		if !rec.Terminal || EffectiveStatus(rec) == RecordStatusFailed || (EffectiveStatus(rec) == RecordStatusIncomplete && !rec.Policy.AllowIncomplete) {
			return MaterializedTrajectory{}, ErrPreviousResponseNotFound
		}
		size := estimate(rec)
		if size < 0 || size > (1<<63-1)-total {
			return MaterializedTrajectory{}, ErrMaterializedSizeExceeded
		}
		total += size
		if total > bounds.MaxMaterializedBytes {
			return MaterializedTrajectory{}, ErrMaterializedSizeExceeded
		}
		recordItems, err := checkedAddItemCounts(len(rec.InputItems), len(rec.OutputItems))
		if err != nil {
			return MaterializedTrajectory{}, err
		}
		if recordItems > bounds.MaxMaterializedItems-items {
			return MaterializedTrajectory{}, ErrMaterializedItemsExceeded
		}
		items += recordItems
		records = append(records, CloneRecord(rec))
		if !lineageSet {
			lineage = rec.Lineage
			lineageSet = true
		} else if err := mergeLineage(&lineage, rec.Lineage); err != nil {
			return MaterializedTrajectory{}, err
		}
		reqs = aipapi.UnionProtocolRequirements(reqs, rec.Requirements)
		recReqs, err := deriveRecordRequirements(rec)
		if err != nil {
			return MaterializedTrajectory{}, err
		}
		reqs = aipapi.UnionProtocolRequirements(reqs, recReqs)
		native = append(native, rec.NativeRequirements...)
		if rec.PreviousID.IsZero() {
			break
		}
		cur = rec.PreviousID
	}
	if len(in.NewInput) > bounds.MaxMaterializedItems-items {
		return MaterializedTrajectory{}, ErrMaterializedItemsExceeded
	}
	if newInputBytes > bounds.MaxMaterializedBytes-total {
		return MaterializedTrajectory{}, ErrMaterializedSizeExceeded
	}
	newInput := CloneItems(in.NewInput)
	items += len(newInput)
	total += newInputBytes
	reqs = aipapi.UnionProtocolRequirements(reqs, aipapi.DeriveProtocolRequirements(aipapi.Call{Items: in.NewInput}))
	ordered := make([]aipapi.Item, 0, items)
	for _, record := range slices.Backward(records) {
		ordered = append(ordered, CloneItems(record.InputItems)...)
		ordered = append(ordered, CloneItems(record.OutputItems)...)
	}
	ordered = append(ordered, CloneItems(newInput)...)
	return MaterializedTrajectory{Items: ordered, InputItems: materializedInputs(records), OutputItems: materializedOutputs(records), NewInput: newInput, ChainDepth: depth, TotalBytes: total, Lineage: lineage, Requirements: reqs, NativeRequirements: native}, nil
}

func materializedInputs(records []ContinuationRecord) []aipapi.Item {
	var out []aipapi.Item
	for _, record := range slices.Backward(records) {
		out = append(out, CloneItems(record.InputItems)...)
	}
	return out
}

func materializedOutputs(records []ContinuationRecord) []aipapi.Item {
	var out []aipapi.Item
	for _, record := range slices.Backward(records) {
		out = append(out, CloneItems(record.OutputItems)...)
	}
	return out
}

func EstimateRecordBytes(rec ContinuationRecord) int64 {
	serialized := RecordSize(rec)
	if rec.MaterializedBytes > serialized {
		return rec.MaterializedBytes
	}
	return serialized
}

func EstimateItemsBytes(items []aipapi.Item) int64 {
	var n int64
	for _, it := range items {
		data, err := json.Marshal(it)
		if err != nil || int64(len(data)) > (1<<63-1)-n {
			return 1<<63 - 1
		}
		n += int64(len(data))
	}
	return n
}

func mergeLineage(dst *Lineage, next Lineage) error {
	if dst == nil {
		return ErrLineageMismatch
	}
	if !dst.ProviderBound && !next.ProviderBound {
		return nil
	}
	if !dst.ProviderBound {
		*dst = next
		return nil
	}
	if !next.ProviderBound {
		return nil
	}
	if dst.ProviderBound != next.ProviderBound || dst.ProviderID != next.ProviderID || dst.Model != next.Model || dst.CandidateKey != next.CandidateKey {
		return ErrLineageMismatch
	}
	return nil
}

// checkedAddItemCounts returns a+b when it fits in an int, otherwise it reports
// the item-bound error instead of wrapping to a negative capacity.
func checkedAddItemCounts(a, b int) (int, error) {
	if a > math.MaxInt-b {
		return 0, ErrMaterializedItemsExceeded
	}
	return a + b, nil
}

func deriveRecordRequirements(rec ContinuationRecord) (aipapi.ProtocolRequirements, error) {
	count, err := checkedAddItemCounts(len(rec.InputItems), len(rec.OutputItems))
	if err != nil {
		return aipapi.ProtocolRequirements{}, err
	}
	items := make([]aipapi.Item, 0, count)
	items = append(items, rec.InputItems...)
	items = append(items, rec.OutputItems...)
	if len(items) == 0 {
		return aipapi.ProtocolRequirements{}, nil
	}
	return aipapi.DeriveProtocolRequirements(aipapi.Call{Items: items}), nil
}
